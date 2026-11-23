package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	kvv1 "pocketone.dev/ext/contracts/kv/v1"
	settingsv1 "pocketone.dev/ext/contracts/settings/v1"
	"pocketone.dev/ext/extkit"
)

// Provider types: the three clouds with their own handling, and any OpenAI-compatible API.
const (
	TypeVultr      = "vultr"
	TypeCloudflare = "cloudflare"
	TypeOpenRouter = "openrouter"
	TypeOpenAI     = "openai"
)

// Types lists the provider types, with what Settings → LLM shows for each.
var Types = []TypeInfo{
	{Type: TypeVultr, Title: "Vultr", Endpoint: "https://api.vultrinference.com/v1", Name: "vultr",
		About: "Vultr Serverless Inference: text, images, speech, transcription, music, video and embeddings."},
	{Type: TypeCloudflare, Title: "Cloudflare Workers AI", Name: "cf", Account: true,
		About: "Workers AI on your Cloudflare account: text, images, speech, transcription and embeddings. Needs the account ID and a token with Workers AI permission."},
	{Type: TypeOpenRouter, Title: "OpenRouter", Endpoint: "https://openrouter.ai/api/v1", Name: "or",
		About: "One key for hundreds of models: text, images, speech, transcription, video and embeddings."},
	{Type: TypeOpenAI, Title: "OpenAI-compatible API", Endpoint: "https://api.openai.com/v1", Name: "openai",
		About: "Any API shaped like OpenAI's: OpenAI, Groq, Together, Ollama or llama-server on localhost. The endpoint includes its version path (/v1)."},
}

// TypeInfo describes a provider type in Settings → LLM.
type TypeInfo struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Endpoint string `json:"endpoint,omitempty"` // the default endpoint ("" for Cloudflare, built from the account)
	Name     string `json:"name"`               // a suggested provider name
	Account  bool   `json:"account,omitempty"`  // needs an account id (Cloudflare)
	About    string `json:"about"`
}

func typeInfo(t string) (TypeInfo, bool) {
	for _, ti := range Types {
		if ti.Type == t {
			return ti, true
		}
	}
	return TypeInfo{}, false
}

// Provider is one connection the app's admins added in Settings → LLM.
type Provider struct {
	Name     string `json:"name"` // what calls name before the slash: "vultr" in "vultr/glm-5.3"
	Type     string `json:"type"`
	Endpoint string `json:"endpoint,omitempty"` // "" uses the type's default
	Account  string `json:"account,omitempty"`  // Cloudflare's account id
	Key      string `json:"key,omitempty"`
}

// endpoint is the provider's base URL: its own, or its type's default.
func (p Provider) endpoint() string {
	if p.Endpoint != "" {
		return strings.TrimRight(p.Endpoint, "/")
	}
	ti, _ := typeInfo(p.Type)
	return ti.Endpoint
}

// Settings are Settings → LLM: the providers calls name, and the limits every call passes first (limits.go). Stored
// in the app's key-value store (kv/v1, private).
type Settings struct {
	Providers []Provider `json:"providers"`
	Limits    []Limit    `json:"limits,omitempty"`
}

func (s Settings) provider(name string) (Provider, bool) {
	for _, p := range s.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// settingsName is where the settings live in the app's key-value store (kv/v1), private to Go since they hold API keys.
const settingsName = "_llm/settings"

// legacyParam is the _params row the settings had before kv/v1; the first load moves it to settingsName.
// Remove once every app has started on a release with kv/v1.
const legacyParam = "llm"

// LoadSettings returns the app's LLM settings (empty when its admins never saved any).
func LoadSettings(app core.App) (Settings, error) {
	if err := kvv1.MoveParamOnce(app, legacyParam, settingsName); err != nil {
		return Settings{}, fmt.Errorf("llm: settings: %w", err)
	}
	var s Settings
	if _, err := kvv1.Get(app, settingsName, &s); err != nil {
		return Settings{}, fmt.Errorf("llm: settings: %w", err)
	}
	return s, nil
}

// SaveSettings stores the app's LLM settings.
func SaveSettings(app core.App, s Settings) error {
	if err := kvv1.MoveParamOnce(app, legacyParam, settingsName); err != nil {
		return err
	}
	return kvv1.Set(app, settingsName, s)
}

// settingsSection is Settings → LLM, over settings/v1.
type settingsSection struct {
	r *router
}

func (s settingsSection) Info() settingsv1.Info {
	return settingsv1.Info{Extension: Name, Title: "LLM", Testable: true,
		Description: "The providers $llm calls name as provider/model (Vultr, Cloudflare, OpenRouter, any OpenAI-compatible API), and the limits every call passes first."}
}

// providerView is a provider as Get returns it: the key never comes back, only whether one is set and its start.
type providerView struct {
	Provider
	KeySet  bool   `json:"keySet"`
	KeyHint string `json:"keyHint,omitempty"` // the first few characters, then dots: sk-or-v1••••••••

}

// settingsView is the section's values.
type settingsView struct {
	Providers []providerView `json:"providers"`
	Limits    []Limit        `json:"limits"`
	Types     []TypeInfo     `json:"types"`
	Kinds     []string       `json:"kinds"`
	Spend     spend          `json:"spend"` // from the ledger: shown, never saved
}

func (s settingsSection) Get(context.Context) (json.RawMessage, error) {
	cur, err := LoadSettings(s.r.app)
	if err != nil {
		return nil, err
	}
	sp, err := s.r.spend(context.Background())
	if err != nil {
		return nil, err
	}
	v := settingsView{Types: Types, Kinds: kinds, Providers: []providerView{}, Limits: cur.Limits, Spend: sp}
	if v.Limits == nil {
		v.Limits = []Limit{}
	}
	for _, p := range cur.Providers {
		pv := providerView{Provider: p, KeySet: p.Key != "", KeyHint: keyHint(p.Key)}
		pv.Key = ""
		v.Providers = append(v.Providers, pv)
	}
	return json.Marshal(v)
}

func (s settingsSection) Save(ctx context.Context, values json.RawMessage) (json.RawMessage, error) {
	next, err := s.parse(values)
	if err != nil {
		return nil, err
	}
	if err := SaveSettings(s.r.app, next); err != nil {
		return nil, err
	}
	s.r.forget() // the model lists are listed again with the new settings
	return s.Get(ctx)
}

// Test checks each provider by listing its models with its key (the cheapest call every cloud has). Vultr's and
// OpenRouter's lists are open without a key, so for them it also checks the key with the key-only account call.
func (s settingsSection) Test(ctx context.Context, values json.RawMessage) error {
	next, err := s.parse(values)
	if err != nil {
		return err
	}
	if len(next.Providers) == 0 {
		return badRequest("add a provider first")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var problems []string
	for _, p := range next.Providers {
		if _, err := s.r.catalog(ctx, p, true); err != nil {
			problems = append(problems, message(providerError(p, err)))
			continue
		}
		if d, err := newDriver(p); err == nil {
			if err := d.checkKey(ctx); err != nil {
				problems = append(problems, message(providerError(p, err)))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; ")) // shown as is: each starts with its provider's name
	}
	return nil
}

func badRequest(msg string) error {
	return &extkit.Error{Code: "bad_request", Message: msg, Status: 400}
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// parse checks submitted settings. A provider sent without a key keeps its saved key, but only when its type,
// endpoint and account are unchanged: a key is never sent somewhere it wasn't entered for.
func (s settingsSection) parse(values json.RawMessage) (Settings, error) {
	var in Settings
	if err := json.Unmarshal(values, &in); err != nil {
		return Settings{}, badRequest(`expected {"providers": [...]}`)
	}
	cur, err := LoadSettings(s.r.app)
	if err != nil {
		return Settings{}, err
	}
	next := Settings{Providers: []Provider{}, Limits: []Limit{}}
	for i, l := range in.Limits {
		if err := checkLimit(&l, i); err != nil {
			return Settings{}, err
		}
		next.Limits = append(next.Limits, l)
	}
	seen := map[string]bool{}
	for _, p := range in.Providers {
		p.Name, p.Type = strings.TrimSpace(p.Name), strings.TrimSpace(p.Type)
		p.Endpoint, p.Account = strings.TrimRight(strings.TrimSpace(p.Endpoint), "/"), strings.TrimSpace(p.Account)
		if !nameRE.MatchString(p.Name) {
			return Settings{}, badRequest(fmt.Sprintf("provider name %q: lowercase letters, digits, '.', '_' and '-', no slash", p.Name))
		}
		if seen[p.Name] {
			return Settings{}, badRequest(fmt.Sprintf("two providers are named %q", p.Name))
		}
		seen[p.Name] = true
		ti, ok := typeInfo(p.Type)
		if !ok {
			return Settings{}, badRequest(fmt.Sprintf("provider %s: type must be vultr, cloudflare, openrouter or openai", p.Name))
		}
		if p.Endpoint == ti.Endpoint {
			p.Endpoint = ""
		}
		if p.Endpoint != "" && !strings.HasPrefix(p.Endpoint, "http://") && !strings.HasPrefix(p.Endpoint, "https://") {
			return Settings{}, badRequest(fmt.Sprintf("provider %s: the endpoint must start with http:// or https://", p.Name))
		}
		if ti.Account && p.Account == "" && p.Endpoint == "" {
			return Settings{}, badRequest(fmt.Sprintf("provider %s: Cloudflare needs the account ID", p.Name))
		}
		if p.Key == "" {
			if old, ok := cur.provider(p.Name); ok && old.Type == p.Type && old.Endpoint == p.Endpoint && old.Account == p.Account {
				p.Key = old.Key
			}
		}
		// every cloud needs a key; an OpenAI-compatible server on your own machine (Ollama, llama-server) may not
		if p.Key == "" && p.Type != TypeOpenAI {
			return Settings{}, badRequest(fmt.Sprintf("provider %s: enter its %s", p.Name, keyWord(ti)))
		}
		next.Providers = append(next.Providers, p)
	}
	return next, nil
}

// message is an error's text without its code: a contract error's message ("vultr: 401: Invalid API key").
func message(err error) string {
	var xe *extkit.Error
	if errors.As(err, &xe) {
		return xe.Message
	}
	return err.Error()
}

// keyHint is a saved key's first characters followed by dots, so an admin can tell which key is set: 8 characters of a
// long key, 4 of a medium one, none of a short one (it would give too much of it away).
func keyHint(key string) string {
	switch n := len(key); {
	case n == 0:
		return ""
	case n >= 32:
		return key[:8] + "••••••••"
	case n >= 16:
		return key[:4] + "••••••••"
	}
	return "••••••••"
}

// keyWord is what the provider type calls its key in Settings → LLM.
func keyWord(ti TypeInfo) string {
	if ti.Account {
		return "API token"
	}
	return "API key"
}
