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
	"pocketone.dev/ext/llm/providers/oai"
)

// openAIDriver is any OpenAI-compatible API (OpenAI, Groq, Together, Ollama, llama-server...): text, images, speech,
// transcription and embeddings at OpenAI's paths. Its model list names only ids, so a model's kind is guessed from its
// name, and text is the guess when nothing else fits.
type openAIDriver struct {
	unsupported
	c *oai.Client
}

func newOpenAI(p Provider) *openAIDriver {
	return &openAIDriver{unsupported{p.Name}, &oai.Client{BaseURL: p.endpoint(), Key: p.Key}}
}

// guessKind guesses a model's kind from its id.
func guessKind(id string) string {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "embed"):
		return llmc.KindEmbed
	case strings.Contains(s, "whisper") || strings.Contains(s, "transcribe"):
		return llmc.KindTranscribe
	case strings.Contains(s, "tts") || strings.Contains(s, "speech"):
		return llmc.KindSpeech
	case strings.Contains(s, "dall-e") || strings.Contains(s, "gpt-image") || strings.Contains(s, "flux") ||
		strings.Contains(s, "diffusion") || strings.Contains(s, "imagen"):
		return llmc.KindImage
	case strings.Contains(s, "sora") || strings.Contains(s, "veo"):
		return "" // video lives elsewhere on these APIs
	case strings.Contains(s, "moderation") || strings.Contains(s, "rerank") || strings.Contains(s, "guard"):
		return ""
	}
	return llmc.KindText
}

func (d *openAIDriver) models(ctx context.Context) ([]llmc.ModelInfo, error) {
	ms, err := d.c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]llmc.ModelInfo, 0, len(ms))
	for _, m := range ms {
		if k := guessKind(m.ID); k != "" {
			out = append(out, llmc.ModelInfo{ID: m.ID, Kinds: []string{k}})
		}
	}
	return out, nil
}

func (d *openAIDriver) complete(ctx context.Context, model string, in textv1.CompleteInput, onDelta func(string, string) bool) (textv1.CompleteResult, error) {
	return chatComplete(ctx, d.c, chatRequest(model, in, reasoningEffort), onDelta)
}

func (d *openAIDriver) image(ctx context.Context, model string, in imagev1.GenerateInput) (imagev1.Result, error) {
	if len(in.References) > 0 {
		return imagev1.Result{}, d.no("reference images")
	}
	r, err := d.c.Images(ctx, oai.ImageRequest{Model: model, Prompt: in.Prompt, N: in.N, Size: in.Size, Extra: in.Options})
	if err != nil {
		return imagev1.Result{}, err
	}
	out := imagev1.Result{Usage: usageJSON(r.Usage)}
	for i, f := range r.Images {
		out.Files = append(out.Files, fileOf(f, "image-"+strconv.Itoa(i+1)))
	}
	return out, nil
}

func (d *openAIDriver) speak(ctx context.Context, model string, in speechv1.SpeakInput) (speechv1.Result, error) {
	// the OpenAI speech API has no clip-based cloning; a compatible server that has it can take input_references in Options
	if in.Reference != nil {
		return speechv1.Result{}, d.no("voice cloning")
	}
	f, err := d.c.Speech(ctx, oai.SpeechRequest{Model: model, Input: in.Text, Voice: voiceOrNil(in.Voice), Instructions: in.Instructions,
		ResponseFormat: orDefault(in.Format, "mp3"), Speed: in.Speed, Extra: in.Options})
	if err != nil {
		return speechv1.Result{}, err
	}
	return speechv1.Result{File: fileOf(f, "speech")}, nil
}

func (d *openAIDriver) transcribe(ctx context.Context, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error) {
	return oaiTranscribe(ctx, d.c, model, in)
}

func (d *openAIDriver) embed(ctx context.Context, model string, in embedv1.EmbedInput) (embedv1.Result, error) {
	return oaiEmbed(ctx, d.c, model, in)
}
