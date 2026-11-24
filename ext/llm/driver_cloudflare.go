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
		info := llmc.ModelInfo{ID: m.ID, Description: m.Description, Kinds: []string{k}, Size: sizeOf(m.ID)}
		info.Prices = cloudflarePrices(m.Prices())
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
	if req.ResponseFormat != nil {
		// Workers AI's JSON mode doesn't stream: answer whole, then send it as one piece
		r, err := chatWhole(ctx, d.c, req)
		if err == nil && onDelta != nil {
			onDelta(r.Text, r.Reasoning)
		}
		return r, err
	}
	return chatComplete(ctx, d.c, req, idleText(TypeCloudflare), onDelta)
}

func (d *cloudflareDriver) image(ctx context.Context, model string, in imagev1.GenerateInput) (imagev1.Result, error) {
	if len(in.References) > 0 {
		return imagev1.Result{}, d.no("reference images")
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

// cloudflarePrices are a model's prices in the contract's units. Live-stream prices ("(websocket)") are left out; a
// price that can't be put in those units ("per step") leaves the model with none, since the others alone would make
// it look cheaper than it is.
func cloudflarePrices(ps []cloudflare.Price) []llmc.Price {
	info := llmc.ModelInfo{}
	for _, p := range ps {
		unit := strings.ToLower(strings.TrimSpace(p.Unit))
		switch {
		case strings.Contains(unit, "websocket"):
		case unit == "per m input tokens":
			addPrice(&info, llmc.UnitInputTokens, p.Price)
		case unit == "per m cached input tokens":
			addPrice(&info, llmc.UnitCachedTokens, p.Price)
		case unit == "per m output tokens":
			addPrice(&info, llmc.UnitOutputTokens, p.Price)
		case unit == "per 1k characters":
			addPrice(&info, llmc.UnitCharacters, p.Price)
		case unit == "per audio minute":
			addPrice(&info, llmc.UnitSecond, p.Price/60)
		case unit == "per 512 by 512 tile":
			addPrice(&info, llmc.UnitMegapixel, p.Price/(512*512/1e6))
		default:
			return nil
		}
	}
	return info.Prices
}
