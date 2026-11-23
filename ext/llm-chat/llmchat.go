// Package llmchat provides llm.chat/v1: stored conversations with the app's language model, on top of llm.text/v1.
// In pb_hooks it's $llm.chat.new(), $llm.chat.open(id).send(text) and $llm.chat.list() (bound by ext/llm).
//
// A chat is a record in _llm_chat_chats; its messages are in _llm_chat_messages. Each send passes llm.text/v1 the
// system text, the chat's summary, the messages since the last fold, and the question with its context after it.
// Once SummarizeEvery messages have piled up past the window, the oldest of them are folded into the summary with a
// second call, and the next send starts a new stretch.
package llmchat

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/contracts/llm"
	chatv1 "pocketone.dev/ext/contracts/llm/chat/v1"
	searchv1 "pocketone.dev/ext/contracts/llm/search/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/extkit"
)

// Name is the extension's key.
const Name = "llm-chat"

// Defaults for a send that doesn't say.
const (
	DefaultWindow         = 12
	DefaultSummarizeEvery = 6
)

// Extension provides llm.chat/v1 on every app it's registered on.
type Extension struct{}

var (
	_ extkit.Extension = (*Extension)(nil)
	_ extkit.Migrator  = (*Extension)(nil)
)

// New returns the extension.
func New() *Extension { return &Extension{} }

// Name implements extkit.Extension.
func (*Extension) Name() string { return Name }

// Contracts implements extkit.Declarer: it answers with llm.text/v1, so it needs a provider of it (ext/llm).
func (*Extension) Contracts() extkit.Contracts {
	return extkit.Contracts{Implements: extkit.IDs("llm.chat/v1"), Requires: extkit.IDs("llm.text/v1"), WorksWith: extkit.IDs("llm.search/v1")}
}

// Register implements extkit.Extension.
func (*Extension) Register(app core.App, _ extkit.Config) error {
	chatv1.Contract.Provide(app, &chats{app: app})
	return nil
}

// chats is llm.chat/v1 on one app.
type chats struct{ app core.App }

// text is llm.text/v1, which the extension Requires.
func (c *chats) text() textv1.Text { return textv1.Contract.Must(c.app) }

func (c *chats) find(id string) (*core.Record, error) {
	if id == "" {
		return nil, chatv1.ErrNoChat
	}
	r, err := c.app.FindRecordById(ChatsCollection, id)
	if err != nil {
		return nil, chatv1.ErrNoChat
	}
	return r, nil
}

func (c *chats) count(chatID string) int {
	var n int
	_ = c.app.DB().Select("count(*)").From(MessagesCollection).Where(dbx.HashExp{"chat": chatID}).Row(&n)
	return n
}

func (c *chats) chatOf(r *core.Record) chatv1.Chat {
	meta := json.RawMessage(r.GetString("meta"))
	if len(meta) == 0 || string(meta) == "null" || string(meta) == `""` {
		meta = json.RawMessage("{}")
	}
	return chatv1.Chat{ID: r.Id, Title: r.GetString("title"), Summary: r.GetString("summary"), Meta: meta,
		Messages: c.count(r.Id), Created: r.GetDateTime("created").Time(), Updated: r.GetDateTime("updated").Time()}
}

func badRequest(msg string) error {
	return &extkit.Error{Code: "bad_request", Status: 400, Message: msg}
}

func checkMeta(meta json.RawMessage) error {
	if len(meta) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		return badRequest("meta must be a JSON object")
	}
	return nil
}

// New implements llm.chat/v1.
func (c *chats) New(_ context.Context, _ extkit.Auth, in chatv1.NewInput) (chatv1.Chat, error) {
	if err := checkMeta(in.Meta); err != nil {
		return chatv1.Chat{}, err
	}
	col, err := c.app.FindCachedCollectionByNameOrId(ChatsCollection)
	if err != nil {
		return chatv1.Chat{}, err
	}
	r := core.NewRecord(col)
	r.Set("title", in.Title)
	if len(in.Meta) > 0 {
		r.Set("meta", in.Meta)
	} else {
		r.Set("meta", json.RawMessage("{}"))
	}
	if err := c.app.Save(r); err != nil {
		return chatv1.Chat{}, err
	}
	return c.chatOf(r), nil
}

// messages are a chat's messages from seq from on, in order.
func (c *chats) messages(chatID string, from int) ([]*core.Record, error) {
	return c.app.FindRecordsByFilter(MessagesCollection, "chat = {:chat} && seq >= {:from}", "seq", 0, 0,
		dbx.Params{"chat": chatID, "from": from})
}

// prepared is a send's llm.text/v1 input and what it needs to save afterwards.
type prepared struct {
	chat     *core.Record
	input    textv1.CompleteInput
	next     int // the question's seq
	window   int
	every    int
	pending  []*core.Record // the messages since the last fold
	sent     string         // the question as sent, with its context
	passages []llm.Passage  // what enrich added, for the answer
}

func (c *chats) prepare(ctx context.Context, in chatv1.SendInput) (*prepared, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, badRequest("give the question's text")
	}
	chat, err := c.find(in.ID)
	if err != nil {
		return nil, err
	}
	p := &prepared{chat: chat, window: in.Window, every: in.SummarizeEvery}
	if p.window <= 0 {
		p.window = DefaultWindow
	}
	if p.every <= 0 {
		p.every = DefaultSummarizeEvery
	}
	folded := chat.GetInt("folded")
	p.pending, err = c.messages(chat.Id, folded)
	if err != nil {
		return nil, err
	}
	p.next = folded + len(p.pending)
	var msgs []textv1.Message
	if s := chat.GetString("summary"); s != "" {
		// the summary stays the same between folds, so the start of the prompt does too (the provider's prompt
		// cache reuses it)
		msgs = append(msgs, textv1.Message{Role: "system", Content: "Summary of the conversation so far: " + s})
	}
	for _, m := range p.pending {
		content := m.GetString("content")
		if sent := m.GetString("sent"); sent != "" {
			content = sent // exactly as sent last time, so the start of the prompt stays the same
		}
		msgs = append(msgs, textv1.Message{Role: m.GetString("role"), Content: content})
	}
	question := in.Text
	if strings.TrimSpace(in.Context) != "" {
		question += "\n\n" + in.Context // after the question: the cached start stays the same
	}
	if in.Enrich != nil {
		// the passages go with the question and are saved with it, so later turns send the same start
		text, passages, err := searchv1.Enrich(ctx, c.app, in.Enrich, in.Text)
		if err != nil {
			return nil, err
		}
		if text != "" {
			question += "\n\n" + text
		}
		p.passages = passages
	}
	msgs = append(msgs, textv1.Message{Role: "user", Content: question})
	p.sent = question
	p.input = textv1.CompleteInput{System: in.System, Messages: msgs, Schema: in.Schema, MaxTokens: in.MaxTokens,
		Temperature: in.Temperature, Model: in.Model, Reasoning: in.Reasoning}
	return p, nil
}

// save stores both turns, touches the chat and folds the oldest messages into the summary when it's due.
func (c *chats) save(ctx context.Context, p *prepared, question, answer string) (bool, error) {
	col, err := c.app.FindCachedCollectionByNameOrId(MessagesCollection)
	if err != nil {
		return false, err
	}
	err = c.app.RunInTransaction(func(tx core.App) error {
		for i, t := range []struct{ role, content, sent string }{{"user", question, p.sent}, {"assistant", answer, ""}} {
			m := core.NewRecord(col)
			m.Set("chat", p.chat.Id)
			m.Set("role", t.role)
			m.Set("content", t.content)
			if t.sent != t.content {
				m.Set("sent", t.sent)
			}
			m.Set("seq", p.next+i)
			if err := tx.Save(m); err != nil {
				return err
			}
		}
		return tx.Save(p.chat) // updated
	})
	if err != nil {
		return false, err
	}
	pending := len(p.pending) + 2
	if pending-p.window < p.every {
		return false, nil
	}
	// a failed summary doesn't fail the send: the answer is saved, and the next send tries the fold again
	if err := c.fold(ctx, p.chat, pending-p.window, p.input.Model); err != nil {
		c.app.Logger().Warn("llm-chat: summary", "chat", p.chat.Id, "error", err)
		return false, nil
	}
	return true, nil
}

// fold summarizes the chat's n oldest unfolded messages into its summary, with the model the send used and
// reasoning off, so the thinking can't spend the summary's tokens.
func (c *chats) fold(ctx context.Context, chat *core.Record, n int, model string) error {
	folded := chat.GetInt("folded")
	msgs, err := c.messages(chat.Id, folded)
	if err != nil || len(msgs) < n {
		return err
	}
	a := c.text()
	var b strings.Builder
	for _, m := range msgs[:n] {
		fmt.Fprintf(&b, "%s: %s\n", m.GetString("role"), m.GetString("content"))
	}
	prev := chat.GetString("summary")
	if prev == "" {
		prev = "(none)"
	}
	temp := 0.2
	res, err := a.Complete(ctx, extkit.Internal, textv1.CompleteInput{
		Model:       model,
		Reasoning:   "off",
		System:      "Update the summary of a conversation. Keep names, numbers, decisions and open questions. At most 120 words. Answer with the summary only.",
		Prompt:      "Summary so far: " + prev + "\n\nNew turns:\n" + b.String(),
		MaxTokens:   220,
		Temperature: &temp,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Text) == "" {
		return fmt.Errorf("llm-chat: %s wrote an empty summary (stop reason %q); the messages stay unfolded until the next send", res.Model, res.StopReason)
	}
	chat.Set("summary", strings.TrimSpace(res.Text))
	chat.Set("folded", folded+n)
	return c.app.Save(chat)
}

// Send implements llm.chat/v1.
func (c *chats) Send(ctx context.Context, _ extkit.Auth, in chatv1.SendInput) (chatv1.SendResult, error) {
	p, err := c.prepare(ctx, in)
	if err != nil {
		return chatv1.SendResult{}, err
	}
	a := c.text()
	res, err := a.Complete(ctx, extkit.Internal, p.input)
	if err != nil {
		return chatv1.SendResult{}, err
	}
	res.Passages = p.passages
	folded, err := c.save(ctx, p, in.Text, res.Text)
	if err != nil {
		return chatv1.SendResult{Answer: res}, err
	}
	return chatv1.SendResult{Answer: res, Summarized: folded}, nil
}

// SendStream implements llm.chat/v1: llm.text/v1's stream, with both turns saved before the last chunk.
func (c *chats) SendStream(ctx context.Context, _ extkit.Auth, in chatv1.SendInput) (extkit.Stream, error) {
	p, err := c.prepare(ctx, in)
	if err != nil {
		return extkit.Stream{}, err
	}
	a := c.text()
	s, err := a.Stream(context.Background(), extkit.Internal, p.input)
	if err != nil {
		return extkit.Stream{}, err
	}
	return extkit.Stream{Close: s.Close, Run: func(ctx context.Context, send func(any) error) error {
		var res *textv1.CompleteResult
		err := s.Run(ctx, func(e any) error {
			ch, _ := e.(textv1.Chunk)
			if ch.Done {
				res = ch.Result
				return nil
			}
			return send(ch)
		})
		if err != nil {
			return err
		}
		if res == nil {
			return fmt.Errorf("llm-chat: the answer ended without a result")
		}
		if _, err := c.save(ctx, p, in.Text, res.Text); err != nil {
			return err
		}
		res.Passages = p.passages
		return send(textv1.Chunk{Done: true, Result: res})
	}}, nil
}

// Get implements llm.chat/v1.
func (c *chats) Get(_ context.Context, _ extkit.Auth, in chatv1.Ref) (chatv1.Chat, error) {
	r, err := c.find(in.ID)
	if err != nil {
		return chatv1.Chat{}, err
	}
	return c.chatOf(r), nil
}

// History implements llm.chat/v1.
func (c *chats) History(_ context.Context, _ extkit.Auth, in chatv1.HistoryInput) (chatv1.History, error) {
	if _, err := c.find(in.ID); err != nil {
		return chatv1.History{}, err
	}
	filter, params := "chat = {:chat}", dbx.Params{"chat": in.ID}
	if in.Before != "" {
		b, err := c.app.FindRecordById(MessagesCollection, in.Before)
		if err != nil || b.GetString("chat") != in.ID {
			return chatv1.History{}, badRequest("before: no such message in this chat")
		}
		filter += " && seq < {:seq}"
		params["seq"] = b.GetInt("seq")
	}
	recs, err := c.app.FindRecordsByFilter(MessagesCollection, filter, "-seq", in.Limit, 0, params)
	if err != nil {
		return chatv1.History{}, err
	}
	out := chatv1.History{Messages: make([]chatv1.Message, 0, len(recs))}
	for i := len(recs) - 1; i >= 0; i-- {
		m := recs[i]
		out.Messages = append(out.Messages, chatv1.Message{ID: m.Id, Role: m.GetString("role"), Content: m.GetString("content"),
			Created: m.GetDateTime("created").Time()})
	}
	return out, nil
}

// Update implements llm.chat/v1.
func (c *chats) Update(_ context.Context, _ extkit.Auth, in chatv1.UpdateInput) (chatv1.Chat, error) {
	r, err := c.find(in.ID)
	if err != nil {
		return chatv1.Chat{}, err
	}
	if err := checkMeta(in.Meta); err != nil {
		return chatv1.Chat{}, err
	}
	if in.Title != nil {
		r.Set("title", *in.Title)
	}
	if len(in.Meta) > 0 {
		r.Set("meta", in.Meta)
	}
	if err := c.app.Save(r); err != nil {
		return chatv1.Chat{}, err
	}
	return c.chatOf(r), nil
}

var metaKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// metaFilter is a records filter matching chats whose meta has each key of meta with that value.
func metaFilter(meta map[string]any) (string, dbx.Params, error) {
	parts := []string{"id != ''"}
	params := dbx.Params{}
	i := 0
	for k, v := range meta {
		if !metaKey.MatchString(k) {
			return "", nil, badRequest(fmt.Sprintf("meta key %q: letters, digits and _ only", k))
		}
		switch v.(type) {
		case string, float64, bool, nil:
		default:
			return "", nil, badRequest(fmt.Sprintf("meta %q: only strings, numbers, true/false and null match", k))
		}
		p := fmt.Sprintf("m%d", i)
		parts = append(parts, fmt.Sprintf("meta.%s = {:%s}", k, p))
		params[p] = v
		i++
	}
	return strings.Join(parts, " && "), params, nil
}

// List implements llm.chat/v1.
func (c *chats) List(_ context.Context, _ extkit.Auth, in chatv1.ListInput) (chatv1.ChatList, error) {
	filter, params, err := metaFilter(in.Meta)
	if err != nil {
		return chatv1.ChatList{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	recs, err := c.app.FindRecordsByFilter(ChatsCollection, filter, "-updated", limit, max(0, in.Offset), params)
	if err != nil {
		return chatv1.ChatList{}, err
	}
	out := chatv1.ChatList{Chats: make([]chatv1.Chat, 0, len(recs))}
	for _, r := range recs {
		out.Chats = append(out.Chats, c.chatOf(r))
	}
	return out, nil
}

// forget deletes a chat (its messages go with it) and the model's state for it.
func (c *chats) forget(ctx context.Context, r *core.Record) error {
	if err := c.app.Delete(r); err != nil {
		return err
	}
	return nil
}

// Forget implements llm.chat/v1.
func (c *chats) Forget(ctx context.Context, _ extkit.Auth, in chatv1.Ref) (chatv1.Forgotten, error) {
	r, err := c.find(in.ID)
	if err != nil {
		return chatv1.Forgotten{}, err
	}
	if err := c.forget(ctx, r); err != nil {
		return chatv1.Forgotten{}, err
	}
	return chatv1.Forgotten{Count: 1}, nil
}

// ForgetWhere implements llm.chat/v1.
func (c *chats) ForgetWhere(ctx context.Context, _ extkit.Auth, in chatv1.ListInput) (chatv1.Forgotten, error) {
	if len(in.Meta) == 0 {
		return chatv1.Forgotten{}, badRequest("give the meta to match")
	}
	filter, params, err := metaFilter(in.Meta)
	if err != nil {
		return chatv1.Forgotten{}, err
	}
	recs, err := c.app.FindRecordsByFilter(ChatsCollection, filter, "", 0, 0, params)
	if err != nil {
		return chatv1.Forgotten{}, err
	}
	n := 0
	for _, r := range recs {
		if err := c.forget(ctx, r); err != nil {
			return chatv1.Forgotten{Count: n}, err
		}
		n++
	}
	return chatv1.Forgotten{Count: n}, nil
}
