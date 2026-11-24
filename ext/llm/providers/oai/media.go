package oai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// File is generated media: its bytes and type, or only a URL when the provider hosts the result and nothing was
// downloaded.
type File struct {
	Data        []byte
	ContentType string
	URL         string
	// Seconds and Cost are what the provider billed for it, from its X-Usage-Seconds and X-Usage-Cost headers (Vultr's
	// speech), when it sends them.
	Seconds float64
	Cost    *float64
}

// ImageRequest is POST /images/generations. Extra holds provider fields (aspect_ratio, seed, steps, ...).
type ImageRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	N              int            `json:"n,omitempty"`
	Size           string         `json:"size,omitempty"`
	ResponseFormat string         `json:"response_format,omitempty"` // b64_json or url
	OutputFormat   string         `json:"output_format,omitempty"`   // png, jpeg, webp
	Extra          map[string]any `json:"-"`
}

// ImageResponse is the generated images, decoded (b64_json) or as URLs. Raw is the whole answer.
type ImageResponse struct {
	Images []File
	Usage  json.RawMessage
	Raw    json.RawMessage
}

// Images generates images. A b64_json answer is decoded into each File's Data; a url answer leaves Data empty.
func (c *Client) Images(ctx context.Context, req ImageRequest) (ImageResponse, error) {
	return c.ImagesAt(ctx, "images/generations", req)
}

// ImagesAt is Images on another path with the same shape (OpenRouter's /images).
func (c *Client) ImagesAt(ctx context.Context, path string, req ImageRequest) (ImageResponse, error) {
	body, err := withExtra(req, req.Extra)
	if err != nil {
		return ImageResponse{}, err
	}
	var raw json.RawMessage
	if err := c.JSON(ctx, "POST", path, json.RawMessage(body), &raw); err != nil {
		return ImageResponse{}, err
	}
	var v struct {
		Data []struct {
			B64JSON   string `json:"b64_json"`
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
		} `json:"data"`
		OutputFormat string          `json:"output_format"`
		Usage        json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ImageResponse{}, &Error{Message: "the answer isn't an image list", Body: clip(string(raw), 300)}
	}
	out := ImageResponse{Usage: v.Usage, Raw: raw}
	format := v.OutputFormat
	if format == "" {
		format = req.OutputFormat
	}
	for i, d := range v.Data {
		f := File{URL: d.URL}
		if d.B64JSON != "" {
			b, err := base64.StdEncoding.DecodeString(d.B64JSON)
			if err != nil {
				return out, &Error{Message: fmt.Sprintf("image %d isn't base64: %v", i, err)}
			}
			f.Data, f.ContentType = b, sniffImage(b, format)
			if d.MediaType != "" {
				f.ContentType = d.MediaType
			}
		}
		out.Images = append(out.Images, f)
	}
	return out, nil
}

func sniffImage(b []byte, format string) string {
	if t := http.DetectContentType(b); strings.HasPrefix(t, "image/") {
		return t
	}
	if format != "" {
		return "image/" + format
	}
	return "application/octet-stream"
}

// SpeechRequest is POST /audio/speech. Voice is usually a name; Extra holds provider fields.
type SpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          any     `json:"voice,omitempty"`
	Instructions   string  `json:"instructions,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"` // mp3, opus, flac, wav, pcm
	Speed          float64 `json:"speed,omitempty"`
	// InputReferences clone a voice (OpenRouter's shape, which Vultr takes too): one input_audio part with the clip
	// and, optionally, a text part with what it says.
	InputReferences []SpeechReference `json:"input_references,omitempty"`
	Extra           map[string]any    `json:"-"`
}

// SpeechReference is one part of a voice-cloning reference.
type SpeechReference struct {
	Type       string      `json:"type"` // "input_audio" or "text"
	InputAudio *InputAudio `json:"input_audio,omitempty"`
	Text       string      `json:"text,omitempty"`
}

// InputAudio is a clip, base64-encoded, with its format (wav, mp3, flac, ogg, m4a, webm).
type InputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

// Speech turns text into audio and returns it whole.
func (c *Client) Speech(ctx context.Context, req SpeechRequest) (File, error) {
	body, err := withExtra(req, req.Extra)
	if err != nil {
		return File{}, err
	}
	res, err := c.Request(ctx, "POST", "audio/speech", bytes.NewReader(body), "application/json")
	if err != nil {
		return File{}, err
	}
	defer res.Body.Close()
	return ReadFile(res)
}

// ReadFile reads a binary answer (audio, video, an image) and its type; a JSON or text answer is an error.
func ReadFile(res *http.Response) (File, error) {
	b, err := io.ReadAll(io.LimitReader(res.Body, 512<<20))
	if err != nil {
		return File{}, &Error{Status: res.StatusCode, Message: "reading the answer: " + err.Error(), Err: err}
	}
	ct := res.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	}
	if ct == "" || ct == "application/octet-stream" {
		ct = http.DetectContentType(b)
	}
	if strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") {
		return File{}, &Error{Status: res.StatusCode, Message: "expected a file, got " + ct, Body: clip(string(b), 300)}
	}
	f := File{Data: b, ContentType: ct}
	if s, err := strconv.ParseFloat(res.Header.Get("X-Usage-Seconds"), 64); err == nil {
		f.Seconds = s
	}
	if c, err := strconv.ParseFloat(res.Header.Get("X-Usage-Cost"), 64); err == nil {
		f.Cost = &c
	}
	return f, nil
}

// Download fetches a URL (a provider-hosted result) with this client's key when it's on the same host.
func (c *Client) Download(ctx context.Context, url string) (File, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return File{}, err
	}
	if c.Key != "" && strings.HasPrefix(url, strings.TrimRight(c.BaseURL, "/")) {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	res, err := hc.Do(req)
	if err != nil {
		return File{}, &Error{Message: "download: " + err.Error(), Err: err}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return File{}, readError(res)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<30))
	if err != nil {
		return File{}, &Error{Message: "download: " + err.Error(), Err: err}
	}
	ct := res.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		ct = mt
	}
	return File{Data: b, ContentType: ct, URL: url}, nil
}

// TranscribeRequest is POST /audio/transcriptions, sent as multipart form data.
type TranscribeRequest struct {
	Model    string
	Audio    []byte
	Filename string // with an extension the provider can read the format from, like story.mp3
	Language string
	Prompt   string
	Extra    map[string]string
}

// Transcript is the recognized text. Raw is the whole answer.
type Transcript struct {
	Text string
	Raw  json.RawMessage
}

// Transcribe turns audio into text.
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (Transcript, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fields := map[string]string{"model": req.Model, "response_format": "json", "language": req.Language, "prompt": req.Prompt}
	for k, v := range req.Extra {
		fields[k] = v
	}
	for k, v := range fields {
		if v != "" {
			_ = w.WriteField(k, v)
		}
	}
	name := req.Filename
	if name == "" {
		name = "audio.mp3"
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return Transcript{}, err
	}
	_, _ = fw.Write(req.Audio)
	if err := w.Close(); err != nil {
		return Transcript{}, err
	}
	res, err := c.Request(ctx, "POST", "audio/transcriptions", &buf, w.FormDataContentType())
	if err != nil {
		return Transcript{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return Transcript{}, &Error{Status: res.StatusCode, Message: "reading the answer: " + err.Error(), Err: err}
	}
	var v struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return Transcript{Text: strings.TrimSpace(string(raw)), Raw: nil}, nil // response_format text
	}
	return Transcript{Text: v.Text, Raw: raw}, nil
}

// EmbedRequest is POST /embeddings. Input is a string or a list of strings.
type EmbedRequest struct {
	Model      string         `json:"model"`
	Input      any            `json:"input"`
	Dimensions int            `json:"dimensions,omitempty"`
	Extra      map[string]any `json:"-"`
}

// EmbedResponse holds one vector per input, in order.
type EmbedResponse struct {
	Model   string
	Vectors [][]float64
	Usage   Usage
	Raw     json.RawMessage
}

// Embed returns embedding vectors.
func (c *Client) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	body, err := withExtra(req, req.Extra)
	if err != nil {
		return EmbedResponse{}, err
	}
	var raw json.RawMessage
	if err := c.JSON(ctx, "POST", "embeddings", json.RawMessage(body), &raw); err != nil {
		return EmbedResponse{}, err
	}
	var v struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return EmbedResponse{}, &Error{Message: "the answer isn't an embedding list", Body: clip(string(raw), 300)}
	}
	out := EmbedResponse{Model: v.Model, Usage: v.Usage, Raw: raw, Vectors: make([][]float64, len(v.Data))}
	for i, d := range v.Data {
		at := d.Index
		if at < 0 || at >= len(out.Vectors) {
			at = i
		}
		out.Vectors[at] = d.Embedding
	}
	return out, nil
}

// Model is one entry of GET /models: OpenAI's fields, plus the raw entry for provider fields.
type Model struct {
	ID      string          `json:"id"`
	OwnedBy string          `json:"owned_by"`
	Created int64           `json:"created"`
	Raw     json.RawMessage `json:"-"`
}

// Models lists GET /models.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var v struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := c.JSON(ctx, "GET", "models", nil, &v); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(v.Data))
	for _, raw := range v.Data {
		var m Model
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			continue
		}
		m.Raw = raw
		out = append(out, m)
	}
	return out, nil
}
