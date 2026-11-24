package llm

import (
	"context"
	"errors"
	"time"

	llmc "pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/llm/providers/oai"
)

// How long a call to a provider may take when the caller's context allows longer (or has no deadline). A caller
// that wants less passes a context with an earlier deadline, or cancels it (in page code, an AbortSignal).
const (
	limitText       = 10 * time.Minute // streamed, so idleText ends a stalled answer long before this
	limitImage      = 3 * time.Minute
	limitSpeech     = 2 * time.Minute
	limitTranscribe = 5 * time.Minute
	limitAlign      = 2 * time.Minute
	limitEmbed      = time.Minute
	limitJobStart   = time.Minute     // music and video create; following the job has create's own wait
	limitJobFollow  = 2 * time.Minute // get, content
	limitList       = 30 * time.Second
)

// Idle limits for streamed text, by provider type: the longest a streamed answer may go without the provider
// sending anything (keep-alives count). Vultr streams a piece every ~0.1 s, thinking included, from the first
// second; OpenRouter sends a keep-alive comment every ~0.4 s while a model works. Cloudflare's cadence is less known,
// and an OpenAI-compatible endpoint may be OpenAI (which streams nothing while an o-series model thinks) or a local
// server reading a long prompt, so those wait longer.
var idleText = idleTextFor // a variable so tests can shorten it

func idleTextFor(providerType string) time.Duration {
	switch providerType {
	case TypeVultr, TypeOpenRouter:
		return 10 * time.Second
	case TypeCloudflare:
		return 30 * time.Second
	}
	return 2 * time.Minute
}

// limitError is the cause of a call's context when its limit above passed.
type limitError struct{ after time.Duration }

func (e *limitError) Error() string { return "no answer in " + e.after.String() }

// within runs f with ctx bounded by limit, and turns its error into the contract's: a stalled stream or a passed
// limit is a timeout naming the model and how long it waited; anything else goes through providerError.
func within[T any](ctx context.Context, t target, limit time.Duration, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, limit, &limitError{limit})
	defer cancel()
	res, err := f(ctx)
	return res, callError(ctx, t, err)
}

func callError(ctx context.Context, t target, err error) error {
	if err == nil {
		return nil
	}
	var idle *oai.IdleError
	if errors.As(err, &idle) {
		return timeout(t, "the provider sent nothing for "+idle.After.String())
	}
	var le *limitError
	if errors.Is(err, context.DeadlineExceeded) && errors.As(context.Cause(ctx), &le) {
		return timeout(t, le.Error())
	}
	return providerError(t.p, err)
}

func timeout(t target, why string) error {
	who := t.p.Name
	if t.model != "" {
		who = t.ref()
	}
	return &extkit.Error{Code: llmc.ErrTimeout.Code, Status: llmc.ErrTimeout.Status,
		Message: who + ": " + why + ", so the call was cancelled"}
}
