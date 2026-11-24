package llm

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"

	llmc "pocketone.dev/ext/contracts/llm"
	alignv1 "pocketone.dev/ext/contracts/llm/align/v1"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	transcribev1 "pocketone.dev/ext/contracts/llm/transcribe/v1"
	"pocketone.dev/ext/llm/providers/oai"
	"pocketone.dev/ext/llm/providers/vultr"
)

// vultrDriver is Vultr Serverless Inference: every kind, music and video as jobs.
type vultrDriver struct {
	unsupported
	c *vultr.Client
}

func newVultr(p Provider) *vultrDriver {
	return &vultrDriver{unsupported{p.Name}, vultr.New(p.endpoint(), p.Key)}
}

func (d *vultrDriver) models(ctx context.Context) ([]llmc.ModelInfo, error) {
	ms, err := d.c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]llmc.ModelInfo, 0, len(ms))
	for _, m := range ms {
		info := llmc.ModelInfo{ID: m.ID, Name: m.Name, Description: m.Description, Reasoning: m.Reasoning != nil,
			Size: sizeOf(m.HuggingFace, m.ID)}
		video := slices.ContainsFunc(m.Outputs, func(o vultr.Output) bool { return o.Type == "video" })
		references := slices.ContainsFunc(m.Inputs, func(in vultr.Input) bool { return in.Type == "image" && in.Supported.References != nil })
		for _, in := range m.Inputs {
			info.Inputs = append(info.Inputs, in.Type)
			if in.Supported.MaxContext != nil && in.Type == "text" {
				info.ContextTokens = in.Supported.MaxContext.Value
			}
		}
		for _, o := range m.Outputs {
			k := kindOfOutput(o.Type)
			if o.Type == "audio" {
				k = llmc.KindMusic
			}
			// a model that makes video is a video model, though it can make a still too; it's an image model as well
			// when it draws stills from reference images (minimax-h3)
			if k == "" || (k == llmc.KindImage && video && !references) {
				continue
			}
			if k == llmc.KindImage && references {
				addFeature(&info, llmc.FeatureReferenceImages)
			}
			info.Kinds = append(info.Kinds, k)
			for _, pr := range o.Pricing {
				usd, _ := strconv.ParseFloat(pr.USD, 64)
				switch pr.Unit {
				case "token":
					if k == llmc.KindEmbed { // an embedding's tokens are the text it reads
						addPrice(&info, llmc.UnitInputTokens, usd*1e6)
					} else {
						addPrice(&info, llmc.UnitOutputTokens, usd*1e6)
					}
				case "second":
					addPrice(&info, llmc.UnitSecond, usd)
				case "megapixel":
					addPrice(&info, llmc.UnitMegapixel, usd)
				}
			}
			vultrFeatures(&info, k, o.Params)
		}
		if len(info.Kinds) == 0 {
			continue
		}
		// what it costs to send: prompt tokens for text, seconds of audio for a transcriber
		for _, in := range m.Inputs {
			for _, pr := range in.Pricing {
				usd, _ := strconv.ParseFloat(pr.USD, 64)
				switch {
				case in.Type == "text" && pr.Unit == "token":
					addPrice(&info, llmc.UnitInputTokens, usd*1e6)
				case in.Type == "audio" && pr.Unit == "second" && slices.Contains(info.Kinds, llmc.KindTranscribe):
					addPrice(&info, llmc.UnitSecond, usd)
				}
			}
			switch in.Type {
			case "image":
				if slices.Contains(info.Kinds, llmc.KindText) {
					addFeature(&info, llmc.FeatureVision)
				}
			case "audio":
				if slices.Contains(info.Kinds, llmc.KindText) {
					addFeature(&info, llmc.FeatureAudioInput)
				}
			}
		}
		out = append(out, info)
	}
	// the aligner is only in the authenticated catalog (GET /models/all), under its feature
	if d.c.Key != "" {
		if cat, err := d.c.Catalog(ctx); err == nil {
			for _, m := range cat {
				if m.Has("AudioAlignment") {
					out = append(out, llmc.ModelInfo{ID: m.ID, Kinds: []string{llmc.KindAlign}, Inputs: []string{"audio", "text"},
						Description: "Forced alignment: when each line and word of a known text is said in its audio."})
				}
			}
		}
	}
	return out, nil
}

func (d *vultrDriver) align(ctx context.Context, model string, in alignv1.AlignInput) (alignv1.Result, error) {
	extra := map[string]string{}
	for k, v := range in.Options {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	name := in.Audio.Name
	if name == "" {
		name = "audio" + orDefault(extFor(in.Audio.Type), ".mp3")
	}
	a, err := d.c.Align(ctx, vultr.AlignRequest{Model: model, Audio: in.Audio.Data, Filename: name, Text: in.Text, Mode: in.Mode,
		Language: in.Language, Extra: extra})
	if err != nil {
		return alignv1.Result{}, err
	}
	out := alignv1.Result{Duration: a.Duration, Lines: []alignv1.Line{}, Words: []alignv1.Word{},
		Usage: llmc.Usage{Seconds: a.Usage.Seconds, CostMicroUSD: llmc.MicroUSD(a.Usage.Cost)}}
	for _, l := range a.Lines {
		line := alignv1.Line{Index: l.Index, Text: l.Text, Start: l.Start, End: l.End, Score: l.Score, Certain: l.Certain}
		if l.Tag != nil {
			line.Tag = *l.Tag
		}
		out.Lines = append(out.Lines, line)
	}
	for _, w := range a.Words {
		out.Words = append(out.Words, alignv1.Word{Line: w.Line, Text: w.Text, Start: w.Start, End: w.End, Score: w.Score})
	}
	for _, u := range a.Unaligned {
		out.Unaligned = append(out.Unaligned, alignv1.Unaligned{Line: u.Line, Text: u.Text, Reason: u.Reason})
	}
	return out, nil
}

func (d *vultrDriver) complete(ctx context.Context, model string, in textv1.CompleteInput, onDelta func(string, string) bool) (textv1.CompleteResult, error) {
	return chatComplete(ctx, d.c, chatRequest(model, in, reasoningEffort), idleText(TypeVultr), onDelta)
}

func (d *vultrDriver) image(ctx context.Context, model string, in imagev1.GenerateInput) (imagev1.Result, error) {
	extra := map[string]any{}
	if in.AspectRatio != "" {
		extra["aspect_ratio"] = in.AspectRatio
	}
	// references go as OpenAI's JSON edit shape on /images/edits, each a data: URL (or its https URL). Only a model
	// with reference editing takes them (its image input lists references); any other answers a 400 naming "images".
	path := "images/generations"
	if len(in.References) > 0 {
		path = "images/edits"
		images := make([]any, 0, len(in.References))
		for _, f := range in.References {
			images = append(images, map[string]any{"image_url": dataURL(f)})
		}
		extra["images"] = images
	}
	req := oai.ImageRequest{Model: model, Prompt: in.Prompt, N: in.N, Size: in.Size, ResponseFormat: "b64_json",
		Extra: merge(extra, in.Options)}
	r, err := d.c.ImagesAt(ctx, path, req)
	if err != nil {
		return imagev1.Result{}, err
	}
	out := imagev1.Result{Usage: usageJSON(r.Usage)}
	for i, f := range r.Images {
		out.Files = append(out.Files, fileOf(f, "image-"+strconv.Itoa(i+1)))
	}
	return out, nil
}

func (d *vultrDriver) speak(ctx context.Context, model string, in speechv1.SpeakInput) (speechv1.Result, error) {
	voice := in.Voice
	if in.Reference != nil && voice == "" {
		voice = speechv1.ModeClone // Vultr's clone voice is named "clone" on every model that clones
	}
	f, err := d.c.Speech(ctx, oai.SpeechRequest{Model: model, Input: in.Text, Voice: voiceOrNil(voice), Instructions: in.Instructions,
		ResponseFormat: orDefault(in.Format, "mp3"), Speed: in.Speed, InputReferences: speechReferences(in.Reference),
		Extra: in.Options})
	if err != nil {
		return speechv1.Result{}, err
	}
	// Vultr bills speech per second of audio and says so in headers
	return speechv1.Result{File: fileOf(f, "speech"), Usage: llmc.Usage{Seconds: f.Seconds, CostMicroUSD: llmc.MicroUSD(f.Cost)}}, nil
}

func (d *vultrDriver) voices(ctx context.Context, model string) ([]speechv1.Voice, error) {
	vs, err := d.c.Voices(ctx, model)
	if err != nil {
		return nil, err
	}
	out := make([]speechv1.Voice, 0, len(vs))
	for _, v := range vs {
		out = append(out, speechv1.Voice{ID: v.ID, Name: v.Name, Language: v.Language, Mode: v.Mode, Description: v.Description})
	}
	return out, nil
}

func (d *vultrDriver) transcribe(ctx context.Context, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error) {
	return oaiTranscribe(ctx, d.c.Client, model, in)
}

func (d *vultrDriver) embed(ctx context.Context, model string, in embedv1.EmbedInput) (embedv1.Result, error) {
	return oaiEmbed(ctx, d.c.Client, model, in)
}

func (d *vultrDriver) startJob(ctx context.Context, kind, model string, in jobInput) (llmc.Job, error) {
	var j oai.Job
	var err error
	switch kind {
	case llmc.KindMusic:
		j, err = d.c.Music(ctx, vultr.MusicRequest{Model: model, Prompt: in.Prompt, Lyrics: in.Lyrics, Duration: int(in.Seconds),
			Instrumental: in.Lyrics == "", Extra: in.Options})
	case llmc.KindVideo:
		extra := map[string]any{}
		if in.Size != "" {
			extra["size"] = in.Size
		}
		if in.Image != nil {
			extra["frame_images"] = []any{map[string]any{"image_url": dataURL(*in.Image), "frame_type": "first_frame"}}
		}
		j, err = d.c.Video(ctx, vultr.VideoRequest{Model: model, Prompt: in.Prompt, Duration: int(in.Seconds), AspectRatio: in.AspectRatio,
			Extra: merge(extra, in.Options)})
	default:
		return llmc.Job{}, d.no(kind)
	}
	if err != nil {
		return llmc.Job{}, err
	}
	return jobOf(j), nil
}

func (d *vultrDriver) getJob(ctx context.Context, kind, id string) (llmc.Job, error) {
	j, err := d.c.GetJob(ctx, vultrJobPath(kind), id)
	if err != nil {
		return llmc.Job{}, err
	}
	return jobOf(j), nil
}

func (d *vultrDriver) jobContent(ctx context.Context, kind, id string, index int) (llmc.File, error) {
	f, err := d.c.Content(ctx, kind, id, index)
	if err != nil {
		return llmc.File{}, err
	}
	return fileOf(f, kind+"-"+strconv.Itoa(index+1)), nil
}

// checkKey reads the account's usage, which needs the key (the model list doesn't).
func (d *vultrDriver) checkKey(ctx context.Context) error {
	var v json.RawMessage
	return d.c.JSON(ctx, "GET", "usage", nil, &v)
}

func vultrJobPath(kind string) string {
	if kind == llmc.KindVideo {
		return "videos"
	}
	return "music"
}

// Shared OpenAI-shaped helpers for transcription and embeddings.

func oaiTranscribe(ctx context.Context, c *oai.Client, model string, in transcribev1.TranscribeInput) (transcribev1.Result, error) {
	extra := map[string]string{}
	for k, v := range in.Options {
		if s, ok := v.(string); ok {
			extra[k] = s
		} else if b, err := json.Marshal(v); err == nil {
			extra[k] = string(b)
		}
	}
	name := in.Audio.Name
	if name == "" {
		name = "audio" + orDefault(extFor(in.Audio.Type), ".mp3")
	}
	t, err := c.Transcribe(ctx, oai.TranscribeRequest{Model: model, Audio: in.Audio.Data, Filename: name, Language: in.Language,
		Prompt: in.Prompt, Extra: extra})
	if err != nil {
		return transcribev1.Result{}, err
	}
	out := transcribev1.Result{Text: strings.TrimSpace(t.Text)}
	var v struct {
		Language string          `json:"language"`
		Duration float64         `json:"duration"`
		Usage    json.RawMessage `json:"usage"`
	}
	if len(t.Raw) > 0 && json.Unmarshal(t.Raw, &v) == nil {
		out.Language, out.Usage = v.Language, usageJSON(v.Usage)
		if out.Usage.Seconds == 0 {
			out.Usage.Seconds = v.Duration
		}
	}
	return out, nil
}

func oaiEmbed(ctx context.Context, c *oai.Client, model string, in embedv1.EmbedInput) (embedv1.Result, error) {
	r, err := c.Embed(ctx, oai.EmbedRequest{Model: model, Input: in.Input, Dimensions: in.Dimensions, Extra: in.Options})
	if err != nil {
		return embedv1.Result{}, err
	}
	return embedv1.Result{Vectors: r.Vectors, Usage: usageOf(r.Usage)}, nil
}

// Small helpers.

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// speechReferences is a voice-cloning clip as OpenRouter's input_references (which Vultr takes too), or nil.
func speechReferences(r *speechv1.VoiceReference) []oai.SpeechReference {
	if r == nil {
		return nil
	}
	format := strings.TrimPrefix(path.Ext(r.Audio.Name), ".")
	if format == "" {
		format = strings.TrimPrefix(extFor(r.Audio.Type), ".")
	}
	refs := []oai.SpeechReference{{Type: "input_audio", InputAudio: &oai.InputAudio{Data: b64(r.Audio.Data), Format: strings.ToLower(format)}}}
	if t := strings.TrimSpace(r.Text); t != "" {
		refs = append(refs, oai.SpeechReference{Type: "text", Text: t})
	}
	return refs
}

func voiceOrNil(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// dataURL is a file as a data: URL (or its URL when it has no bytes).
func dataURL(f llmc.File) string {
	if len(f.Data) == 0 {
		return f.URL
	}
	return "data:" + orDefault(f.Type, "application/octet-stream") + ";base64," + b64(f.Data)
}

// vultrFeatures reads what a model can do from the parameters its output takes.
func vultrFeatures(info *llmc.ModelInfo, kind string, params map[string]json.RawMessage) {
	if _, ok := params["tools"]; ok {
		addFeature(info, llmc.FeatureTools)
	}
	if _, ok := params["structured_outputs"]; ok {
		addFeature(info, llmc.FeatureStructured)
	}
	if _, ok := params["lyrics"]; ok && kind == llmc.KindMusic {
		addFeature(info, llmc.FeatureLyrics)
	}
	var enum struct {
		Values []string `json:"values"`
	}
	if v, ok := params["voice"]; ok && json.Unmarshal(v, &enum) == nil {
		for _, voice := range enum.Values {
			switch voice {
			case "clone":
				addFeature(info, llmc.FeatureVoiceCloning)
			case "design":
				addFeature(info, llmc.FeatureVoiceDesign)
			case "default":
			default:
				addFeature(info, llmc.FeatureVoices)
			}
		}
	}
	if _, ok := params["input_references"]; ok && kind == llmc.KindSpeech {
		addFeature(info, llmc.FeatureVoiceCloning)
	}
	enum.Values = nil
	if v, ok := params["language"]; ok && json.Unmarshal(v, &enum) == nil {
		for _, l := range enum.Values {
			if len(l) == 2 && !slices.Contains(info.Languages, l) {
				info.Languages = append(info.Languages, l)
			}
		}
	}
}
