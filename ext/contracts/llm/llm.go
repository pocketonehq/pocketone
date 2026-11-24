// Package llm holds what the llm.* contracts share: a model reference, generated files, usage, model info, jobs and
// errors. It isn't a contract itself; llm.text/v1, llm.image/v1 and the others import it.
//
// A model is named "provider/model": the name an app's admins gave a provider in Settings → LLM, a slash, and that
// provider's own id for the model ("vultr/glm-5.2", "or/z-ai/glm-5.2", "cf/@cf/black-forest-labs/flux-1-schnell").
// It's split on the first slash. Every call names one: there are no defaults.
package llm

import (
	"encoding/json"
	"math"
	"strings"

	"pocketone.dev/ext/extkit"
)

// Kinds are what a model makes, one contract each.
const (
	KindText       = "text"
	KindImage      = "image"
	KindSpeech     = "speech"
	KindTranscribe = "transcribe"
	KindMusic      = "music"
	KindVideo      = "video"
	KindEmbed      = "embed"
	KindAlign      = "align"
)

// AllKinds lists every kind, in Settings → LLM's order.
var AllKinds = []string{KindText, KindImage, KindSpeech, KindTranscribe, KindAlign, KindMusic, KindVideo, KindEmbed}

// SplitModel splits "provider/model" on the first slash. ok is false without a slash or with an empty part.
func SplitModel(ref string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(strings.TrimSpace(ref), "/")
	return provider, model, ok && provider != "" && model != ""
}

// File is generated media, or media sent in (audio to transcribe, an image to edit). Data is the bytes (base64 over
// HTTP); URL is the provider's hosted copy when it gave one, which may expire. Name is a suggested file name.
type File struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"` // a media type: image/png, audio/mpeg, video/mp4
	Data []byte `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Usage is what a call used. The token and seconds fields appear only when known; CostMicroUSD is always there.
// Every llm.* contract's reply carries one (chat's in its answer, music's and video's on the job).
type Usage struct {
	InputTokens     int     `json:"inputTokens,omitempty"`
	OutputTokens    int     `json:"outputTokens,omitempty"`
	CachedTokens    int     `json:"cachedTokens,omitempty"`    // input tokens that came from the provider's prompt cache
	ReasoningTokens int     `json:"reasoningTokens,omitempty"` // output tokens the model spent thinking
	Seconds         float64 `json:"seconds,omitempty"`         // audio or video length billed
	// CostMicroUSD is what the call cost, in millionths of a US dollar (1,000,000 is $1; 1,000 is a tenth of a cent),
	// as a whole number so costs add up exactly. It's what the provider charged when it says (OpenRouter on every call,
	// Vultr for media), else an estimate from the model's listed prices and what the call used (CostEstimated), else 0:
	// 0 means free or not known.
	CostMicroUSD int64 `json:"costMicroUSD"`
	// CostEstimated is true when CostMicroUSD was worked out from listed prices rather than reported by the provider.
	CostEstimated bool `json:"costEstimated,omitempty"`
}

// MicroUSD is a cost in US dollars as millionths of a dollar, rounded to the nearest; nil is 0.
func MicroUSD(usd *float64) int64 {
	if usd == nil {
		return 0
	}
	return int64(math.Round(*usd * 1e6))
}

// Price is one of a model's prices: USD per Unit, one of the Unit constants. A price is a rate on one meter of a call;
// a model may list several (input and output tokens), and a call's cost is each meter's use times its rate, added up.
type Price struct {
	Unit string  `json:"unit"`
	USD  float64 `json:"usd"`
	// Source is where the price comes from: "listed" (the provider's own model list; also when empty) or "measured"
	// (what this app's calls to the model were billed, from the ledger: cost divided by what they used).
	Source string `json:"source,omitempty"`
	// Calls is, for a measured price, how many billed calls it was measured over.
	Calls int `json:"calls,omitempty"`
}

// The units a Price is in. Every provider's prices are given in these, so prices compare across providers.
const (
	UnitInputTokens  = "1M input tokens"
	UnitCachedTokens = "1M cached input tokens" // input tokens served from the provider's prompt cache
	UnitOutputTokens = "1M output tokens"       // reasoning tokens count as output
	// UnitTokens is input and output tokens together: the unit of a text model's measured price, since a call's bill
	// doesn't say how much was for input and how much for output.
	UnitTokens     = "1M tokens"
	UnitSecond     = "second" // of audio or video made (speech, music, video) or read (transcribe, align)
	UnitMegapixel  = "megapixel"
	UnitImage      = "image"
	UnitCharacters = "1K characters" // of text read aloud
	UnitRequest    = "request"       // one call, whatever it used
)

// Price sources.
const (
	SourceListed   = "listed"
	SourceMeasured = "measured"
)

// Features a model has, as ModelInfo.Features lists them (a model that thinks before answering is Reasoning instead).
const (
	FeatureTools        = "tools"             // calls functions the caller defines
	FeatureStructured   = "structured output" // answers in a JSON schema
	FeatureVision       = "vision"            // reads images
	FeatureAudioInput   = "audio input"       // reads audio
	FeatureVoiceCloning = "voice cloning"     // speaks in the voice of a reference clip
	FeatureVoiceDesign  = "voice design"      // speaks in a voice described in words
	FeatureVoices       = "built-in voices"   // has named voices to pick from
	FeatureLyrics       = "lyrics"            // sings given lyrics
	// FeatureReferenceImages is an image model that draws from pictures it's given (llm.image/v1's References).
	FeatureReferenceImages = "reference images"
)

// ModelInfo is one model, as each contract's models operation lists it. Every field past Kinds is optional: a provider
// fills what it says about the model, and nothing is guessed.
type ModelInfo struct {
	ID       string   `json:"id"`       // "provider/model", what a call names
	Provider string   `json:"provider"` // the provider's name in Settings → LLM
	Type     string   `json:"type"`     // the provider's type: vultr, cloudflare, openrouter, openai
	Name     string   `json:"name,omitempty"`
	Kinds    []string `json:"kinds"`
	Inputs   []string `json:"inputs,omitempty"` // what it reads: text, image, audio...
	// Reasoning is true for a model that thinks before answering (its thinking counts toward maxTokens).
	Reasoning     bool `json:"reasoning,omitempty"`
	ContextTokens int  `json:"contextTokens,omitempty"`
	// Prices are its listed prices, then the prices measured from this app's own calls, each marked by Source. None
	// means its price isn't known.
	Prices      []Price `json:"prices,omitempty"`
	Description string  `json:"description,omitempty"`
	// Size is how many parameters it has, as its maker names it: "1.7B", "27B", "30B (3B active)" for a
	// mixture of experts.
	Size string `json:"size,omitempty"`
	// Features are what it can do beyond its kind, from the Feature constants.
	Features []string `json:"features,omitempty"`
	// Languages are the languages it speaks, writes or hears (ISO 639-1 codes), when the provider lists them.
	Languages []string `json:"languages,omitempty"`
}

// Models is a models operation's answer.
type Models struct {
	Models []ModelInfo `json:"models"`
}

// ModelsInput narrows a models operation to one provider (its name in Settings → LLM).
type ModelsInput struct {
	Provider string `json:"provider,omitempty"`
}

// Options are a provider's own fields, passed through unchanged (seed, steps, aspect_ratio, OpenRouter's provider
// routing...). They override what the contract's fields set.
type Options = map[string]any

// Job is a long generation (music, video): started by create, followed by get, its files fetched by content. The id
// carries the provider ("vultr:music_02a2..."), so get and content find it again. Files are filled in once it's done
// when the provider returns them inline or create was given wait.
type Job struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"` // queued, running, done or failed
	Progress float64 `json:"progress,omitempty"`
	Files    []File  `json:"files,omitempty"`
	// Outputs is how many files the job made, for content's index (0..Outputs-1), once it's done.
	Outputs int    `json:"outputs,omitempty"`
	Error   string `json:"error,omitempty"`
	Model   string `json:"model"`
	Usage   Usage  `json:"usage"`
}

// Job statuses.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// JobRef names a job, for get.
type JobRef struct {
	ID string `json:"id"`
}

// ContentInput names one of a done job's files.
type ContentInput struct {
	ID    string `json:"id"`
	Index int    `json:"index,omitempty"`
}

// Errors every llm.* contract may answer. A provider's own error comes back as ErrProvider's code with the cloud's
// status and message (never the key).
var (
	ErrNotConfigured = &extkit.Error{Code: "not_configured", Status: 503,
		Message: "no providers yet: add one in Settings → LLM, then name a model as provider/model"}
	ErrUnknownModel = &extkit.Error{Code: "unknown_model", Status: 400,
		Message: "no such model: name one as provider/model from this kind's models list"}
	ErrNotSupported = &extkit.Error{Code: "not_supported", Status: 400, Message: "that provider doesn't do this"}
	ErrProvider     = &extkit.Error{Code: "provider", Status: 502, Message: "the provider answered with an error"}
	// ErrLimit: a limit in Settings → LLM refused the call before it reached the provider. Each refusal says which
	// limit and when its window resets ("try again after 00:00 UTC").
	ErrLimit = &extkit.Error{Code: "limit", Status: 429, Message: "a limit in Settings → LLM refused this call"}
	// ErrTimeout: the provider didn't answer in time and the request was cancelled. Its message names the model and
	// what ran out: a streamed answer that went quiet ("the provider sent nothing for 10s"), or the call's time limit
	// ("no answer in 3m0s"). Worth a retry, unlike a bad answer. A caller's own deadline or cancellation (a Go
	// context, an AbortSignal in page code) ends the call as that caller's error instead.
	ErrTimeout = &extkit.Error{Code: "timeout", Status: 504, Message: "the provider didn't answer in time"}
)

// Enrich adds passages from the app's search buckets (search/v1) to a call's prompt: the call searches Buckets with
// Query (default: the prompt, or the last user message) and sends the best Limit passages after it. In JSON it's an
// object, or just the list of buckets (["storytime"]).
type Enrich struct {
	Buckets []string `json:"buckets"`
	Limit   int      `json:"limit,omitempty"` // passages to add (default 5, at most 20)
	Query   string   `json:"query,omitempty"`
	// Viewer is who the answer is for: passages from records they can't view (the collection's view rule) are left
	// out. Nil sees everything, as the app's own server code.
	Viewer *Viewer `json:"viewer,omitempty"`
}

// Viewer is a signed-in auth record (Collection and ID), or a guest (both empty).
type Viewer struct {
	Collection string `json:"collection,omitempty"`
	ID         string `json:"id,omitempty"`
}

// UnmarshalJSON takes the object form or a list of bucket names.
func (e *Enrich) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err == nil {
		*e = Enrich{Buckets: names}
		return nil
	}
	type plain Enrich
	return json.Unmarshal(b, (*plain)(e))
}

// Passage is a piece of a search bucket that was added to a prompt, and where it came from.
type Passage struct {
	Bucket   string  `json:"bucket"`
	Source   string  `json:"source,omitempty"`
	Document string  `json:"document"`
	Heading  string  `json:"heading,omitempty"`
	Text     string  `json:"text"`
	Score    float64 `json:"score"`
}
