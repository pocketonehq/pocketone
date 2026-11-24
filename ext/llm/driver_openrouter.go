package llm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	llmc "pocketone.dev/ext/contracts/llm"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	transcribev1 "pocketone.dev/ext/contracts/llm/transcribe/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/llm/providers/oai"
	"pocketone.dev/ext/llm/providers/openrouter"
)

// openRouterDriver is OpenRouter: text, images (its /images, or chat for models that draw in chat), speech,
// transcription, embeddings and video jobs. No music (its audio models answer only in streamed chat).
type openRouterDriver struct {
	unsupported
	c *openrouter.Client
}

func newOpenRouter(p Provider) *openRouterDriver {
	return &openRouterDriver{unsupported{p.Name}, openrouter.New(p.endpoint(), p.Key)}
}

// openRouterPrices are its per-token price keys in the contract's units (per million tokens). Its other keys are
// left out: charges for extras a call may not use (web search, cache writes, images or audio sent in), and
// internal_reasoning, which the contract counts as output.
var openRouterPrices = map[string]string{"prompt": llmc.UnitInputTokens, "completion": llmc.UnitOutputTokens,
	"input_cache_read": llmc.UnitCachedTokens}

func (d *openRouterDriver) models(ctx context.Context) ([]llmc.ModelInfo, error) {
	ms, err := d.c.Models(ctx, "all")
	if err != nil {
		return nil, err
	}
	out := make([]llmc.ModelInfo, 0, len(ms))
	for _, m := range ms {
		info := llmc.ModelInfo{ID: m.ID, Name: m.Name, Description: clip(m.Description, 300), Inputs: m.Architecture.Inputs,
			Reasoning: m.Reasoning != nil, ContextTokens: m.ContextLength, Size: sizeOf(m.ID, m.Name)}
		for _, o := range m.Architecture.Outputs {
			if k := kindOfOutput(o); k != "" {
				info.Kinds = append(info.Kinds, k)
			}
		}
		if len(info.Kinds) == 0 {
			continue
		}
		// an image model that reads images draws from them: they go to it in a chat request
		if slices.Contains(info.Kinds, llmc.KindImage) && slices.Contains(m.Architecture.Inputs, "image") {
			addFeature(&info, llmc.FeatureReferenceImages)
		}
		if slices.Contains(info.Kinds, llmc.KindText) {
			for _, in := range m.Architecture.Inputs {
				switch in {
				case "image":
					addFeature(&info, llmc.FeatureVision)
				case "audio":
					addFeature(&info, llmc.FeatureAudioInput)
				}
			}
		}
		for _, p := range m.Params {
			switch p {
			case "tools":
				addFeature(&info, llmc.FeatureTools)
			case "structured_outputs":
				addFeature(&info, llmc.FeatureStructured)
			}
		}
		if len(m.Voices) > 0 {
			addFeature(&info, llmc.FeatureVoices)
		}
		for key, v := range m.Pricing {
			s, _ := v.(string)
			usd, err := strconv.ParseFloat(s, 64)
			if err != nil || usd == 0 {
				continue
			}
			if unit, ok := openRouterPrices[key]; ok {
				addPrice(&info, unit, usd*1e6)
			} else if key == "request" {
				addPrice(&info, llmc.UnitRequest, usd)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func openRouterReasoning(level string) map[string]any {
	if level == "off" {
		return map[string]any{"reasoning": map[string]any{"enabled": false}}
	}
	return map[string]any{"reasoning": map[string]any{"effort": level}}
}

func (d *openRouterDriver) complete(ctx context.Context, model string, in textv1.CompleteInput, onDelta func(string, string) bool) (textv1.CompleteResult, error) {
	req := chatRequest(model, in, openRouterReasoning)
	if req.ResponseFormat != nil {
		req.ResponseFormat.JSONSchema.Strict = true
	}
	return chatComplete(ctx, d.c, req, idleText(TypeOpenRouter), onDelta)
}

func (d *openRouterDriver) image(ctx context.Context, model string, in imagev1.GenerateInput) (imagev1.Result, error) {
	if len(in.References) == 0 {
		req := oai.ImageRequest{Model: model, Prompt: in.Prompt, N: in.N, Size: in.Size, Extra: in.Options}
		if in.AspectRatio != "" {
			req.Extra = merge(map[string]any{"aspect_ratio": in.AspectRatio}, in.Options)
		}
		r, err := d.c.Images(ctx, req)
		var oe *oai.Error
		if err == nil {
			out := imagev1.Result{Usage: usageJSON(r.Usage)}
			for i, f := range r.Images {
				out.Files = append(out.Files, fileOf(f, "image-"+strconv.Itoa(i+1)))
			}
			return out, nil
		}
		if !errors.As(err, &oe) || (oe.Status != 400 && oe.Status != 404) {
			return imagev1.Result{}, err
		}
		// a model that draws only in chat (Gemini's image models): ask it there
	}
	content := []any{map[string]any{"type": "text", "text": in.Prompt}}
	for _, f := range in.References {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(f)}})
	}
	extra := in.Options
	if in.AspectRatio != "" {
		extra = merge(map[string]any{"image_config": map[string]any{"aspect_ratio": in.AspectRatio}}, in.Options)
	}
	r, files, err := d.c.ChatImages(ctx, oai.ChatRequest{Model: model, Messages: []oai.Message{{Role: "user", Content: content}}, Extra: extra})
	if err != nil {
		return imagev1.Result{}, err
	}
	if len(files) == 0 {
		return imagev1.Result{}, &extkit.Error{Code: "bad_answer", Status: 502, Message: d.name + ": the model answered without a picture: " + clip(r.Text(), 200)}
	}
	out := imagev1.Result{Usage: usageOf(r.Usage)}
	for i, f := range files {
		out.Files = append(out.Files, fileOf(f, "image-"+strconv.Itoa(i+1)))
	}
	return out, nil
}

func (d *openRouterDriver) speak(ctx context.Context, model string, in speechv1.SpeakInput) (speechv1.Result, error) {
	f, err := d.c.Speech(ctx, oai.SpeechRequest{Model: model, Input: in.Text, Voice: voiceOrNil(in.Voice), Instructions: in.Instructions,
		ResponseFormat: in.Format, Speed: in.Speed, InputReferences: speechReferences(in.Reference), Extra: in.Options})
	if err != nil {
		return speechv1.Result{}, err
	}
	return speechv1.Result{File: fileOf(f, "speech")}, nil
}

// voices are on the model's catalog entry.
func (d *openRouterDriver) voices(ctx context.Context, model string) ([]speechv1.Voice, error) {
	ms, err := d.c.Models(ctx, "speech")
	if err != nil {
		return nil, err
	}
	out := []speechv1.Voice{}
	for _, m := range ms {
		if m.ID == model {
			for _, v := range m.Voices {
				out = append(out, speechv1.Voice{ID: v, Name: v})
			}
		}
	}
	return out, nil
}

func (d *openRouterDriver) transcribe(ctx context.Context, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error) {
	return oaiTranscribe(ctx, d.c.Client, model, in)
}

func (d *openRouterDriver) embed(ctx context.Context, model string, in embedv1.EmbedInput) (embedv1.Result, error) {
	return oaiEmbed(ctx, d.c.Client, model, in)
}

func (d *openRouterDriver) startJob(ctx context.Context, kind, model string, in jobInput) (llmc.Job, error) {
	if kind != llmc.KindVideo {
		return llmc.Job{}, d.no(kind)
	}
	extra := map[string]any{}
	if in.Image != nil {
		extra["frame_images"] = []any{map[string]any{"image_url": map[string]any{"url": dataURL(*in.Image)}, "frame_type": "first_frame"}}
	}
	j, err := d.c.Video(ctx, openrouter.VideoRequest{Model: model, Prompt: in.Prompt, Duration: int(in.Seconds), Resolution: in.Size,
		AspectRatio: in.AspectRatio, Extra: merge(extra, in.Options)})
	if err != nil {
		return llmc.Job{}, err
	}
	return jobOf(j), nil
}

func (d *openRouterDriver) getJob(ctx context.Context, kind, id string) (llmc.Job, error) {
	if kind != llmc.KindVideo {
		return llmc.Job{}, d.no(kind)
	}
	j, err := d.c.GetJob(ctx, "videos", id)
	if err != nil {
		return llmc.Job{}, err
	}
	return jobOf(j), nil
}

func (d *openRouterDriver) jobContent(ctx context.Context, kind, id string, index int) (llmc.File, error) {
	if kind != llmc.KindVideo {
		return llmc.File{}, d.no(kind)
	}
	f, err := d.c.Content(ctx, id, index)
	if err != nil {
		return llmc.File{}, err
	}
	return fileOf(f, "video-"+strconv.Itoa(index+1)), nil
}

// checkKey reads the key's own details, which needs the key (the model list doesn't).
func (d *openRouterDriver) checkKey(ctx context.Context) error {
	var v json.RawMessage
	return d.c.JSON(ctx, "GET", "key", nil, &v)
}

// OpenRouter suggests polling videos every 10–30 s.
func (d *openRouterDriver) pollInterval() time.Duration { return 10 * time.Second }
