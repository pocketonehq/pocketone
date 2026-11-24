// Package imagev1 is the llm.image/v1 contract: a model makes pictures from a prompt, and from reference pictures.
//
// A wire contract at /api/_c/llm.image/v1. In pb_hooks it's $llm.image.
package imagev1

//go:generate go run pocketone.dev/ext/contracts/cmd/contractgen

import (
	"context"

	"pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/extkit"
)

// Image makes pictures.
//
//contract:id llm.image/v1
//contract:kind wire
//contract:scope llm
//contract:js hooks
type Image interface {
	// Generate makes pictures from a prompt, and from the reference pictures it's given.
	//
	//op: POST generate
	Generate(ctx context.Context, a extkit.Auth, in GenerateInput) (Result, error)
	// Models lists the image models of every provider in Settings → LLM.
	//
	//op: GET models scope=llm:read
	Models(ctx context.Context, a extkit.Auth, in llm.ModelsInput) (llm.Models, error)
}

// GenerateInput is what to draw.
type GenerateInput struct {
	Model  string `json:"model"` // "provider/model", required
	Prompt string `json:"prompt"`
	N      int    `json:"n,omitempty"`    // how many (default 1)
	Size   string `json:"size,omitempty"` // "1024x1024"
	// AspectRatio is "16:9", "1:1"... for models that take one instead of a size.
	AspectRatio string `json:"aspectRatio,omitempty"`
	// References are pictures the model draws from: one to change as the prompt says, or one whose style and
	// characters a new scene keeps ("same style and characters; show them ..."). Only a model whose ModelInfo lists
	// llm.FeatureReferenceImages takes them; any other answers not_supported or the provider's own error.
	References []llm.File  `json:"references,omitempty"`
	Options    llm.Options `json:"options,omitempty"`
}

// Result is the pictures made.
type Result struct {
	Files []llm.File `json:"files"`
	Model string     `json:"model"`
	Usage llm.Usage  `json:"usage"`
}
