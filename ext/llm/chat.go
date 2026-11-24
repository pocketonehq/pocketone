package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	chatv1 "pocketone.dev/ext/contracts/llm/chat/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
)

// errNoChat: no extension provides llm.chat/v1 on the app.
var errNoChat = errors.New("$llm.chat isn't available: add the llm-chat extension (llm.chat/v1)")

// bindChatJS adds the natives under $llm.chat. They look llm.chat/v1 up on each call, so they work with whichever
// extension provides it, and say what's missing when none does. ext/llm imports only the contract, never ext/llm-chat.
func bindChatJS(app core.App, rt js.Runtime, n js.Object) {
	get := func() (chatv1.Chats, error) {
		c, ok := chatv1.Contract.Of(app)
		if !ok {
			return nil, errNoChat
		}
		return c, nil
	}
	ctx := context.Background()
	call := func(what string, fn func(c chatv1.Chats, raw []byte) (any, error)) func(string) (string, error) {
		return func(opts string) (string, error) {
			c, err := get()
			if err != nil {
				return "", err
			}
			v, err := fn(c, []byte(opts))
			if err != nil {
				return "", fmt.Errorf("%s: %w", what, err)
			}
			b, err := json.Marshal(v)
			return string(b), err
		}
	}
	_ = n.Set("chatNew", call("$llm.chat.new", func(c chatv1.Chats, raw []byte) (any, error) {
		var in chatv1.NewInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return c.New(ctx, extkit.Internal, in)
	}))
	// chatSend(ctx, opts): the prelude calls it with .asyncWith, so the caller's AbortSignal cancels ctx
	_ = n.Set("chatSend", func(ctx context.Context, opts string) (string, error) {
		return call("chat.send", func(c chatv1.Chats, raw []byte) (any, error) {
			var in chatv1.SendInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			return c.Send(subjectCtx(ctx, raw), extkit.Internal, in)
		})(opts)
	})
	_ = n.Set("chatGet", call("chat.get", func(c chatv1.Chats, raw []byte) (any, error) {
		return c.Get(ctx, extkit.Internal, chatv1.Ref{ID: string(raw)})
	}))
	_ = n.Set("chatHistory", call("chat.history", func(c chatv1.Chats, raw []byte) (any, error) {
		var in chatv1.HistoryInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return c.History(ctx, extkit.Internal, in)
	}))
	_ = n.Set("chatUpdate", call("chat", func(c chatv1.Chats, raw []byte) (any, error) {
		var in chatv1.UpdateInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return c.Update(ctx, extkit.Internal, in)
	}))
	_ = n.Set("chatForget", call("chat.forget", func(c chatv1.Chats, raw []byte) (any, error) {
		return c.Forget(ctx, extkit.Internal, chatv1.Ref{ID: string(raw)})
	}))
	_ = n.Set("chatsList", call("$llm.chat.list", func(c chatv1.Chats, raw []byte) (any, error) {
		var in chatv1.ListInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return c.List(ctx, extkit.Internal, in)
	}))
	_ = n.Set("chatsForget", call("$llm.chat.forget", func(c chatv1.Chats, raw []byte) (any, error) {
		var in chatv1.ListInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Meta) == 0 {
			return nil, errors.New("give the meta to match")
		}
		return c.ForgetWhere(ctx, extkit.Internal, in)
	}))
}

// chatPrelude builds $llm.chat; jsPrelude includes it. Sending is async only (sendAsync): it reaches a provider. The rest
// reads or writes the chat's records and is plain. A chat's context option may be a function of the question; it runs
// on the VM before the send.
const chatPrelude = `
  const contextOf = (c, q) => typeof c === "function" ? String(c(q) ?? "") : (c ?? "");
  const sendInput = (id, o, text, s) => ({ id, text, system: o.system, window: o.window, summarizeEvery: o.summarizeEvery,
    model: s.model ?? o.model, reasoning: s.reasoning ?? o.reasoning,
    maxTokens: s.maxTokens ?? o.maxTokens, temperature: s.temperature ?? o.temperature,
    schema: typeof s.schema === "string" ? JSON.parse(s.schema) : s.schema, context: contextOf(s.context ?? o.context, text),
    enrich: enrichOf(s.enrich ?? o.enrich) });
  const openChat = (id, o = {}) => {
    if (!id) throw new Error("$llm.chat.open: give the chat's id ($llm.chat.new() returns one)");
    return Object.freeze({
      id,
      sendAsync: async (text, s = {}) => {
        const i = sendInput(id, o, text, s);
        if (s.onText || s.onReasoning) return readStream("chat", { ...i, signal: s.signal }, s.onText, s.onReasoning);
        try { return J(await n.chatSend.asyncWith({ signal: s.signal }, S(i))).answer; } catch (err) { throw clean(err); }
      },
      get: () => J(n.chatGet(id)),
      history: (h = {}) => J(n.chatHistory(S({ id, limit: h.limit, before: h.before }))).messages,
      summary: () => J(n.chatGet(id)).summary,
      title: (t) => t === undefined ? J(n.chatGet(id)).title : J(n.chatUpdate(S({ id, title: String(t) }))).title,
      meta: (m) => m === undefined ? J(n.chatGet(id)).meta : J(n.chatUpdate(S({ id, meta: m }))).meta,
      forget: () => { n.chatForget(id) },
    });
  };
  const chat = Object.freeze({
    new: (o = {}) => J(n.chatNew(S(o))).id,
    open: openChat,
    list: (o = {}) => J(n.chatsList(S(o))).chats,
    forget: (o = {}) => J(n.chatsForget(S(o))).count,
  });
`
