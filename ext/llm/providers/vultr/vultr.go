// Package vultr drives Vultr Serverless Inference (https://api.vultrinference.com, OpenAPI spec at /docs/openapi.json).
// Chat, images, speech, transcription and embeddings are OpenAI-shaped (package oai); the model list is richer than
// OpenAI's, and music and video are async jobs: create, poll, download.
package vultr

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"pocketone.dev/ext/llm/providers/oai"
)

// DefaultBaseURL is Vultr's inference API.
const DefaultBaseURL = "https://api.vultrinference.com/v1"

// Client is a Vultr inference client. The embedded oai.Client gives Chat, ChatStream, Images, Speech, Transcribe and
// Embed.
type Client struct {
	*oai.Client
}

// New returns a client for key. baseURL "" is DefaultBaseURL.
func New(baseURL, key string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{&oai.Client{BaseURL: baseURL, Key: key}}
}

// Model is one entry of Vultr's model list.
type Model struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	HuggingFace string          `json:"hugging_face_id"`
	Reasoning   any             `json:"reasoning"` // Vultr's field, shape varies by model
	Inputs      []Input         `json:"input_modalities"`
	Outputs     []Output        `json:"output_modalities"`
	Datacenters []Region        `json:"datacenters"`
	Raw         json.RawMessage `json:"-"`
}

// Input is a kind of input a model takes: text, image, file, audio, video, with what it costs to send (prompt tokens,
// seconds of audio) and its limits.
type Input struct {
	Type      string  `json:"type"`
	Pricing   []Price `json:"pricing"`
	Supported struct {
		MaxContext *struct {
			Value int `json:"value"`
		} `json:"max_context_length"`
		// References is how many reference images an image input takes, on a model that draws from them.
		References *struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"references"`
	} `json:"supported_inputs"`
}

// Output is a kind of output a model makes: text, image, speech, audio (music), video, transcription, embeddings,
// rerank, decisions. Params are its supported parameters, by name.
type Output struct {
	Type      string                     `json:"type"`
	Streaming bool                       `json:"streaming"`
	Params    map[string]json.RawMessage `json:"supported_parameters"`
	Pricing   []Price                    `json:"pricing"`
}

// Price is a cost per unit (token, megapixel, second), in US dollars as a decimal string.
type Price struct {
	Type string `json:"type"`
	Unit string `json:"unit"`
	USD  string `json:"cost_usd"`
}

// Region is a datacenter a model runs in.
type Region struct {
	Country string `json:"country_code"`
	Region  string `json:"region"`
}

// OutputTypes are the model's output kinds.
func (m Model) OutputTypes() []string {
	out := make([]string, len(m.Outputs))
	for i, o := range m.Outputs {
		out[i] = o.Type
	}
	return out
}

// Makes reports whether the model makes outputs of kind t.
func (m Model) Makes(t string) bool {
	for _, o := range m.Outputs {
		if o.Type == t {
			return true
		}
	}
	return false
}

// Models lists Vultr's models with their kinds, parameters and prices. It works without a key.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ms, err := c.Client.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(ms))
	for _, m := range ms {
		var v Model
		if err := json.Unmarshal(m.Raw, &v); err != nil {
			continue
		}
		v.Raw = m.Raw
		out = append(out, v)
	}
	return out, nil
}

// Voice is a voice a speech model offers.
type Voice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Language    string `json:"native_language"`
	Description string `json:"description"`
}

// Voices lists a speech model's voices.
func (c *Client) Voices(ctx context.Context, model string) ([]Voice, error) {
	var v struct {
		Data []Voice `json:"data"`
	}
	err := c.JSON(ctx, "GET", "audio/voices?model="+url.QueryEscape(model), nil, &v)
	return v.Data, err
}

// MusicRequest is POST /music. Extra holds the other fields (task, bpm, key, quality, sample_rate, ...).
type MusicRequest struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt,omitempty"`
	Lyrics         string         `json:"lyrics,omitempty"`
	Instrumental   bool           `json:"instrumental,omitempty"`
	Duration       int            `json:"duration,omitempty"` // seconds
	ResponseFormat string         `json:"response_format,omitempty"`
	Extra          map[string]any `json:"-"`
}

// VideoRequest is POST /videos. Extra holds the other fields (resolution, size, frame_images, quality, ...).
type VideoRequest struct {
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt"`
	Duration      int            `json:"duration,omitempty"` // seconds
	AspectRatio   string         `json:"aspect_ratio,omitempty"`
	GenerateAudio bool           `json:"generate_audio,omitempty"`
	Extra         map[string]any `json:"-"`
}

// Music starts a music job (oai.Job; poll with Wait("music", ...), download with Content("music", ...)).
func (c *Client) Music(ctx context.Context, req MusicRequest) (oai.Job, error) {
	return c.StartJob(ctx, "music", req, req.Extra)
}

// Video starts a video job.
func (c *Client) Video(ctx context.Context, req VideoRequest) (oai.Job, error) {
	return c.StartJob(ctx, "videos", req, req.Extra)
}

// Wait polls a job until it stops. kind is "music" or "video".
func (c *Client) Wait(ctx context.Context, kind string, j oai.Job, interval time.Duration) (oai.Job, error) {
	return c.WaitJob(ctx, jobPath(kind), j, interval)
}

// Content downloads output index of a finished job. kind is "music" or "video".
func (c *Client) Content(ctx context.Context, kind, id string, index int) (oai.File, error) {
	return c.JobContent(ctx, jobPath(kind), id, index)
}

func jobPath(kind string) string {
	if kind == "video" {
		return "videos"
	}
	return "music"
}
