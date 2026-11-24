// Package speechv1 is the llm.speech/v1 contract: a model reads text aloud.
//
// A wire contract at /api/_c/llm.speech/v1. In pb_hooks it's $llm.speech.
package speechv1

//go:generate go run pocketone.dev/ext/contracts/cmd/contractgen

import (
	"context"

	"pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/extkit"
)

// Speech turns text into audio.
//
//contract:id llm.speech/v1
//contract:kind wire
//contract:scope llm
//contract:js hooks
type Speech interface {
	// Speak reads the text aloud and returns the audio.
	//
	//op: POST speak
	Speak(ctx context.Context, a extkit.Auth, in SpeakInput) (Result, error)
	// Voices lists a model's voices, when the provider says.
	//
	//op: POST voices scope=llm:read
	Voices(ctx context.Context, a extkit.Auth, in VoicesInput) (VoiceList, error)
	// Models lists the speech models of every provider in Settings → LLM.
	//
	//op: GET models scope=llm:read
	Models(ctx context.Context, a extkit.Auth, in llm.ModelsInput) (llm.Models, error)
}

// SpeakInput is what to say and how.
type SpeakInput struct {
	Model string `json:"model"` // "provider/model", required
	Text  string `json:"text"`
	Voice string `json:"voice,omitempty"`
	// Format is the audio format: mp3 (default), wav, opus, flac, pcm, as the provider allows.
	Format string  `json:"format,omitempty"`
	Speed  float64 `json:"speed,omitempty"`
	// Instructions describe the delivery ("slow and calm, a bedtime voice"), for models that take them. Not with a
	// Reference: a cloned voice takes its delivery from the clip.
	Instructions string `json:"instructions,omitempty"`
	// Reference clones a voice: the model reads Text in the voice of this clip. Leave Voice empty (or the model's
	// clone voice, Voices' Mode "clone"). Models that clone list a voice with that mode; a provider that doesn't clone
	// refuses the call with llm.ErrNotSupported. Nothing is saved at the provider: send the clip with every call.
	Reference *VoiceReference `json:"reference,omitempty"`
	Options   llm.Options     `json:"options,omitempty"`
}

// VoiceReference is a clip of the voice to imitate.
type VoiceReference struct {
	// Audio is the clip, with its data: a short, clean recording (10 to 30 seconds) read the way the model should
	// speak. Its Name's extension (me.wav) or Type tells the provider the format.
	Audio llm.File `json:"audio"`
	// Text is what the clip says, word for word. Optional, but most models match the voice more closely with it.
	Text string `json:"text,omitempty"`
}

// Result is the audio.
type Result struct {
	File  llm.File  `json:"file"`
	Model string    `json:"model"`
	Usage llm.Usage `json:"usage"`
}

// VoicesInput names the model whose voices to list.
type VoicesInput struct {
	Model string `json:"model"` // "provider/model", required
}

// VoiceList is a model's voices.
type VoiceList struct {
	Voices []Voice `json:"voices"`
}

// Voice is one voice.
type Voice struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Gender   string `json:"gender,omitempty"`
	// Mode is how the voice is made, when the provider says: "builtin" (a speaker the model has), "design" (a new
	// voice described in Instructions) or "clone" (the voice of a Reference clip). A model with a "clone" voice
	// clones.
	Mode        string `json:"mode,omitempty"`
	Description string `json:"description,omitempty"`
}

// Voice modes, in Voice.Mode.
const (
	ModeBuiltin = "builtin"
	ModeDesign  = "design"
	ModeClone   = "clone"
)

// Clones reports whether a model's voices include a clone voice, so a Reference works with it.
func (l VoiceList) Clones() bool {
	for _, v := range l.Voices {
		if v.Mode == ModeClone {
			return true
		}
	}
	return false
}
