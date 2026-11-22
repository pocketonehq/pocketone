package llm

import (
	"context"
	"strconv"
	"strings"

	llmc "pocketone.dev/ext/contracts/llm"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	transcribev1 "pocketone.dev/ext/contracts/llm/transcribe/v1"
	"pocketone.dev/ext/llm/providers/cloudflare"
)

// cloudflareDriver is Workers AI: text and embeddings over its OpenAI-shaped API, the rest through /ai/run. No music
// or video.
type cloudflareDriver struct {
	unsupported
	c *cloudflare.Client
}

func newCloudflare(p Provider) *cloudflareDriver {
	var c *cloudflare.Client
	if p.Endpoint != "" {
		c = cloudflare.NewAt(p.Endpoint, p.Key) // an AI Gateway or a test server: the .../ai base
	} else {
		c = cloudflare.New(p.Account, p.Key)
	}
	return &cloudflareDriver{unsupported{p.Name}, c}
}

func (d *cloudflareDriver) models(ctx context.Context) ([]llmc.ModelInfo, error) {
	ms, err := d.c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]llmc.ModelInfo, 0, len(ms))
	for _, m := range ms {
		k := kindOfOutput(m.Kind())
		if k == "" {
			continue
		}
		info := llmc.ModelInfo{ID: m.ID, Description: m.Description, Kinds: []string{k}}
		for _, p := range m.Prices() {
			info.Prices = append(info.Prices, llmc.Price{Unit: p.Unit, USD: p.Price})
		}
		if v, ok := m.Properties["context_window"]; ok {
			info.ContextTokens, _ = strconv.Atoi(strings.Trim(string(v), `"`))
		}
		if _, ok := m.Properties["reasoning"]; ok {
			info.Reasoning = true
		}
		out = append(out, info)
	}
	return out, nil
}

func (d *cloudflareDriver) complete(ctx context.Context, model string, in textv1.CompleteInput, onDelta func(string, string) bool) (textv1.CompleteResult, error) {
	req := chatRequest(model, in, reasoningEffort)
	if onDelta != nil && req.ResponseFormat != nil {
		// Workers AI's JSON mode doesn't stream: answer whole, then send it as one piece
		r, err := chatComplete(ctx, d.c, req, nil)
		if err == nil {
			onDelta(r.Text, r.Reasoning)
		}
		return r, err
	}
	return chatComplete(ctx, d.c, req, onDelta)
}

func (d *cloudflareDriver) image(ctx context.Context, model string, in imagev1.GenerateInput, edit []llmc.File) (imagev1.Result, error) {
	if len(edit) > 0 {
		return imagev1.Result{}, d.no("image edits yet")
	}
	w, h := sizeWH(in.Size)
	out := imagev1.Result{}
	for i := range max(in.N, 1) {
		f, err := d.c.Image(ctx, cloudflare.ImageRequest{Model: model, Prompt: in.Prompt, Width: w, Height: h, Extra: in.Options})
		if err != nil {
			return out, err
		}
		out.Files = append(out.Files, fileOf(f, "image-"+strconv.Itoa(i+1)))
	}
	return out, nil
}

func (d *cloudflareDriver) speak(ctx context.Context, model string, in speechv1.SpeakInput) (speechv1.Result, error) {
	if in.Reference != nil {
		return speechv1.Result{}, d.no("voice cloning")
	}
	f, err := d.c.Speech(ctx, cloudflare.SpeechRequest{Model: model, Text: in.Text, Voice: in.Voice, Encoding: in.Format, Extra: in.Options})
	if err != nil {
		return speechv1.Result{}, err
	}
	return speechv1.Result{File: fileOf(f, "speech")}, nil
}

func (d *cloudflareDriver) transcribe(ctx context.Context, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error) {
	t, err := d.c.Transcribe(ctx, model, in.Audio.Data, in.Language)
	if err != nil {
		return transcribev1.Result{}, err
	}
	return transcribev1.Result{Text: strings.TrimSpace(t.Text)}, nil
}

func (d *cloudflareDriver) embed(ctx context.Context, model string, in embedv1.EmbedInput) (embedv1.Result, error) {
	return oaiEmbed(ctx, d.c.Client, model, in)
}
