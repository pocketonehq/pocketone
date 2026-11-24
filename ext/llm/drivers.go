package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	llmc "pocketone.dev/ext/contracts/llm"
	alignv1 "pocketone.dev/ext/contracts/llm/align/v1"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	transcribev1 "pocketone.dev/ext/contracts/llm/transcribe/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/llm/providers/oai"
)

// driver is one provider type's way of doing each kind, over its client in providers/. Models are the provider's own
// ids here; the router adds the provider's name. A kind the type can't do answers llmc.ErrNotSupported (unsupported).
type driver interface {
	models(ctx context.Context) ([]llmc.ModelInfo, error)
	// complete answers; onDelta, when set, gets the text and reasoning as they're written (false stops it).
	complete(ctx context.Context, model string, in textv1.CompleteInput, onDelta func(text, reasoning string) bool) (textv1.CompleteResult, error)
	image(ctx context.Context, model string, in imagev1.GenerateInput) (imagev1.Result, error)
	speak(ctx context.Context, model string, in speechv1.SpeakInput) (speechv1.Result, error)
	voices(ctx context.Context, model string) ([]speechv1.Voice, error)
	transcribe(ctx context.Context, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error)
	embed(ctx context.Context, model string, in embedv1.EmbedInput) (embedv1.Result, error)
	align(ctx context.Context, model string, in alignv1.AlignInput) (alignv1.Result, error)
	// startJob starts music or video (kind); the job's id is the provider's own.
	startJob(ctx context.Context, kind, model string, in jobInput) (llmc.Job, error)
	getJob(ctx context.Context, kind, id string) (llmc.Job, error)
	jobContent(ctx context.Context, kind, id string, index int) (llmc.File, error)
	pollInterval() time.Duration
	// checkKey checks the key with a call that needs one and costs nothing, for a provider whose model list answers
	// without a key (nil: listing the models already checks it).
	checkKey(ctx context.Context) error
}

// jobInput is music's and video's create input, merged.
type jobInput struct {
	Prompt      string       `json:"prompt"`
	Lyrics      string       `json:"lyrics,omitempty"`
	Seconds     float64      `json:"seconds,omitempty"`
	Size        string       `json:"size,omitempty"`
	AspectRatio string       `json:"aspectRatio,omitempty"`
	Image       *llmc.File   `json:"image,omitempty"`
	Options     llmc.Options `json:"options,omitempty"`
}

func newDriver(p Provider) (driver, error) {
	switch p.Type {
	case TypeVultr:
		return newVultr(p), nil
	case TypeCloudflare:
		return newCloudflare(p), nil
	case TypeOpenRouter:
		return newOpenRouter(p), nil
	case TypeOpenAI:
		return newOpenAI(p), nil
	}
	return nil, fmt.Errorf("provider %s: unknown type %q", p.Name, p.Type)
}

// unsupported answers ErrNotSupported for every kind; drivers embed it and override what they do.
type unsupported struct{ name string }

func (u unsupported) no(what string) error {
	return &extkit.Error{Code: llmc.ErrNotSupported.Code, Status: 400, Message: u.name + " doesn't do " + what}
}

func (u unsupported) complete(context.Context, string, textv1.CompleteInput, func(string, string) bool) (textv1.CompleteResult, error) {
	return textv1.CompleteResult{}, u.no("text")
}
func (u unsupported) image(context.Context, string, imagev1.GenerateInput) (imagev1.Result, error) {
	return imagev1.Result{}, u.no("images")
}
func (u unsupported) speak(context.Context, string, speechv1.SpeakInput) (speechv1.Result, error) {
	return speechv1.Result{}, u.no("speech")
}
func (u unsupported) voices(context.Context, string) ([]speechv1.Voice, error) {
	return []speechv1.Voice{}, nil
}
func (u unsupported) transcribe(context.Context, string, transcribev1.TranscribeInput) (transcribev1.Result, error) {
	return transcribev1.Result{}, u.no("transcription")
}
func (u unsupported) embed(context.Context, string, embedv1.EmbedInput) (embedv1.Result, error) {
	return embedv1.Result{}, u.no("embeddings")
}
func (u unsupported) align(context.Context, string, alignv1.AlignInput) (alignv1.Result, error) {
	return alignv1.Result{}, u.no("alignment")
}
func (u unsupported) startJob(_ context.Context, kind, _ string, _ jobInput) (llmc.Job, error) {
	return llmc.Job{}, u.no(kind)
}
func (u unsupported) getJob(_ context.Context, kind, _ string) (llmc.Job, error) {
	return llmc.Job{}, u.no(kind)
}
func (u unsupported) jobContent(_ context.Context, kind, _ string, _ int) (llmc.File, error) {
	return llmc.File{}, u.no(kind)
}
func (u unsupported) pollInterval() time.Duration    { return 2 * time.Second }
func (u unsupported) checkKey(context.Context) error { return nil }

// b64 is standard base64.
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Text over an OpenAI-shaped chat API, shared by every type.

type chatClient interface {
	Chat(ctx context.Context, req oai.ChatRequest) (oai.ChatResponse, error)
	ChatStream(ctx context.Context, req oai.ChatRequest, onDelta func(oai.Delta) bool) (oai.ChatResponse, error)
}

// chatRequest builds an OpenAI chat request from the contract's input. reasoning maps the contract's reasoning level
// onto the provider's field (nil leaves it out).
func chatRequest(model string, in textv1.CompleteInput, reasoning func(level string) map[string]any) oai.ChatRequest {
	req := oai.ChatRequest{Model: model, MaxTokens: in.MaxTokens, Temperature: in.Temperature, Stop: in.Stop}
	if in.System != "" {
		req.Messages = append(req.Messages, oai.Message{Role: "system", Content: in.System})
	}
	for _, m := range in.Messages {
		req.Messages = append(req.Messages, oai.Message{Role: m.Role, Content: m.Content})
	}
	if strings.TrimSpace(in.Prompt) != "" {
		req.Messages = append(req.Messages, oai.Message{Role: "user", Content: in.Prompt})
	}
	if len(in.Schema) > 0 {
		req.ResponseFormat = &oai.ResponseFormat{Type: "json_schema", JSONSchema: &oai.JSONSchema{Name: "answer", Schema: in.Schema}}
	}
	extra := map[string]any{}
	if in.Reasoning != "" && reasoning != nil {
		for k, v := range reasoning(in.Reasoning) {
			extra[k] = v
		}
	}
	for k, v := range in.Options {
		extra[k] = v
	}
	if len(extra) > 0 {
		req.Extra = extra
	}
	return req
}

// reasoningEffort is the common reasoning_effort field: none, low or high.
func reasoningEffort(level string) map[string]any {
	if level == "off" {
		level = "none"
	}
	return map[string]any{"reasoning_effort": level}
}

// chatComplete answers over c, streaming when onDelta is set.
func chatComplete(ctx context.Context, c chatClient, req oai.ChatRequest, onDelta func(text, reasoning string) bool) (textv1.CompleteResult, error) {
	var r oai.ChatResponse
	var err error
	if onDelta != nil {
		req.Stream, req.StreamOptions = true, &oai.StreamOptions{IncludeUsage: true}
		r, err = c.ChatStream(ctx, req, func(d oai.Delta) bool { return onDelta(d.Text, d.Reasoning) })
	} else {
		r, err = c.Chat(ctx, req)
	}
	if err != nil {
		return textv1.CompleteResult{}, err
	}
	return chatResult(r, req.ResponseFormat != nil)
}

// chatResult is the contract's answer from an OpenAI chat response; with a schema, Data is the parsed answer.
func chatResult(r oai.ChatResponse, schema bool) (textv1.CompleteResult, error) {
	out := textv1.CompleteResult{Text: r.Text(), Usage: usageOf(r.Usage)}
	if len(r.Choices) > 0 {
		out.Reasoning = r.Choices[0].Message.Reasoning
		out.StopReason = r.Choices[0].FinishReason
	}
	if schema {
		data := strings.TrimSpace(out.Text)
		data = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(data, "```json"), "```"), "```")
		if json.Valid([]byte(strings.TrimSpace(data))) {
			out.Data = json.RawMessage(strings.TrimSpace(data))
		} else if data == "" {
			msg := "the model wrote no answer"
			if out.StopReason != "" {
				msg += " (stopped: " + out.StopReason + ")"
			}
			if out.StopReason == "length" {
				msg += "; raise maxTokens, or set reasoning off if the model was thinking"
			}
			return out, &extkit.Error{Code: "bad_answer", Status: 502, Message: msg}
		} else {
			msg := "the answer isn't valid JSON"
			if out.StopReason != "" && out.StopReason != "stop" {
				msg += " (stopped: " + out.StopReason + ")"
			}
			// the end shows where it broke off
			t := []rune(strings.TrimSpace(out.Text))
			if len(t) > 300 {
				msg += ": …" + string(t[len(t)-300:])
			} else {
				msg += ": " + string(t)
			}
			return out, &extkit.Error{Code: "bad_answer", Status: 502, Message: msg}
		}
	}
	return out, nil
}

func usageOf(u oai.Usage) llmc.Usage {
	return llmc.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CostMicroUSD: llmc.MicroUSD(u.Cost),
		CachedTokens: u.PromptTokensDetails.CachedTokens, ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens}
}

// usageJSON reads a media answer's usage object (cost, seconds), as Vultr and OpenRouter send it.
func usageJSON(raw json.RawMessage) llmc.Usage {
	var v struct {
		Cost            *float64 `json:"cost"`
		DurationSeconds float64  `json:"duration_seconds"`
		BilledSeconds   float64  `json:"billed_seconds"`
		InputTokens     int      `json:"input_tokens"`
		PromptTokens    int      `json:"prompt_tokens"`
		OutputTokens    int      `json:"output_tokens"`
		Completion      int      `json:"completion_tokens"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return llmc.Usage{}
	}
	u := llmc.Usage{CostMicroUSD: llmc.MicroUSD(v.Cost), Seconds: v.BilledSeconds, InputTokens: max(v.InputTokens, v.PromptTokens),
		OutputTokens: max(v.OutputTokens, v.Completion)}
	if u.Seconds == 0 {
		u.Seconds = v.DurationSeconds
	}
	return u
}

// fileOf names a driver's file for the contract: base plus the extension its type has.
func fileOf(f oai.File, base string) llmc.File {
	ct := f.ContentType
	if ct == "" && len(f.Data) > 0 {
		ct = http.DetectContentType(f.Data)
	}
	return llmc.File{Name: base + extFor(ct), Type: ct, Data: f.Data, URL: f.URL}
}

func extFor(ct string) string {
	switch ct {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "image/jpeg":
		return ".jpg"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	}
	if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// jobOf is the contract's job from an OpenAI-style job (Vultr's and OpenRouter's), with the provider's id.
func jobOf(j oai.Job) llmc.Job {
	out := llmc.Job{ID: j.ID, Progress: float64(j.Progress) / 100, Usage: usageJSON(j.Usage), Outputs: len(j.Outputs)}
	if out.Outputs == 0 {
		out.Outputs = len(j.URLs)
	}
	switch j.Status {
	case "completed", "succeeded":
		out.Status, out.Progress = llmc.JobDone, 1
	case "failed", "cancelled", "canceled", "expired":
		out.Status = llmc.JobFailed
		if err := j.Err(); err != nil {
			out.Error = err.Error()
		}
	case "in_progress", "running", "processing", "generating":
		out.Status = llmc.JobRunning
	default:
		out.Status = llmc.JobQueued
	}
	return out
}

// sizeWH splits "1024x768".
func sizeWH(s string) (w, h int) {
	_, _ = fmt.Sscanf(strings.ToLower(s), "%dx%d", &w, &h)
	return w, h
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// kindOfOutput maps a provider's output name to a kind ("" for one PocketOne doesn't use: rerank, decisions...).
func kindOfOutput(o string) string {
	switch o {
	case "text":
		return llmc.KindText
	case "image":
		return llmc.KindImage
	case "speech":
		return llmc.KindSpeech
	case "transcription":
		return llmc.KindTranscribe
	case "embeddings":
		return llmc.KindEmbed
	case "video":
		return llmc.KindVideo
	}
	return ""
}
