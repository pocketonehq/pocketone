package llm

import (
	"context"
	"strings"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"

	"pocketone.dev/ext/extkit"
)

// Limits middleware: two hooks every billable llm.* call passes through, whichever kind it is and however it arrived
// (page code, HTTP, MCP). OnCall runs before the call reaches a provider and may change or refuse it; OnCallDone runs
// after, with what the call used and cost. The provider of the llm.* contracts triggers them; anyone binds them: an
// app's own code ($llm.onCall in page code), or another extension (a billing extension debiting credits).
//
// The limits an app's admins set in Settings → LLM are one OnCall handler (LimitsHandlerID), bound with
// LimitsPriority so it runs after handlers bound with the default priority: it counts the model and subject they
// settled on.

// LimitsHandlerID is the id of the built-in limits handler on OnCall.
const LimitsHandlerID = "__llmLimits__"

// LimitsPriority is the built-in limits handler's priority: after handlers with the default (0).
const LimitsPriority = 1000

// CallEvent is one billable call on its way to a provider.
type CallEvent struct {
	hook.Event
	App     core.App
	Context context.Context

	Kind string // "text", "image", "speech", "transcribe", "align", "music", "video" or "embed"
	Op   string // the contract operation: "complete", "stream", "generate", "speak", ...
	// Model is the call's "provider/model"; a handler may change it.
	Model string
	// Subject is whom the call is for (see WithSubject): "user:<collection>/<id>", "guest:<id>",
	// "guest:ip:<address>", "" for nobody in particular, or any name a handler gives it ("team:acme").
	Subject string
	// Input is a pointer to the operation's input (*textv1.CompleteInput, *imagev1.GenerateInput, ...); a handler may
	// change its fields. Its model field is ignored: change Model.
	Input any
	// Auth is who made the call.
	Auth extkit.Auth
	// GuestsCapped says a handler takes care of what guests may spend, so the built-in limits don't refuse guest
	// calls for want of a limit on all guests together.
	GuestsCapped bool
}

// Refuse returns the error that refuses the call with message, the way a used-up limit does (code "limit", 429).
// Return it from a handler instead of calling e.Next().
func (e *CallEvent) Refuse(message string) error {
	return &extkit.Error{Code: ErrLimit.Code, Status: ErrLimit.Status, Message: message}
}

// CallDoneEvent is a call that reached a provider, with what it used. A music or video job's comes once its cost is
// known (when it's done), or never for a job nobody checks again.
type CallDoneEvent struct {
	hook.Event
	App     core.App
	Context context.Context

	Kind    string
	Op      string
	Model   string
	Subject string
	Usage   Usage
	Job     string // a job's id, for music and video
}

type hooks struct {
	call *hook.Hook[*CallEvent]
	done *hook.Hook[*CallDoneEvent]
}

var (
	hooksMu  sync.Mutex
	hooksKey = "__pocketone_llm_hooks"
)

func hooksOf(app core.App) *hooks {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	if h, ok := app.Store().Get(hooksKey).(*hooks); ok {
		return h
	}
	h := &hooks{call: &hook.Hook[*CallEvent]{}, done: &hook.Hook[*CallDoneEvent]{}}
	app.Store().Set(hooksKey, h)
	return h
}

// OnCall is the app's hook before every billable llm.* call. A handler calls e.Next() to let the call go on, or
// returns e.Refuse(message) (or any error) to refuse it. A chain that doesn't reach its end refuses the call.
func OnCall(app core.App) *hook.Hook[*CallEvent] { return hooksOf(app).call }

// OnCallDone is the app's hook after every llm.* call that reached a provider. Its errors are logged; the call has
// already happened.
func OnCallDone(app core.App) *hook.Hook[*CallDoneEvent] { return hooksOf(app).done }

// SubjectParts is a subject taken apart.
type SubjectParts struct {
	Kind       string `json:"kind"`                 // "user", "guest", "" (nobody), or a custom subject's prefix
	Collection string `json:"collection,omitempty"` // a user's auth collection
	ID         string `json:"id,omitempty"`         // a user's record id, a guest's cookie id, or what follows a custom prefix
	IP         string `json:"ip,omitempty"`         // a guest counted by IP
}

// ParseSubject takes a subject apart: "user:users/abc" is {user, users, abc}, "guest:ip:10.0.0.1" is {guest, ip
// 10.0.0.1}, "team:acme" is {team, acme}.
func ParseSubject(s string) SubjectParts {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		return SubjectParts{Kind: s}
	}
	switch kind {
	case "user":
		col, id, _ := strings.Cut(rest, "/")
		return SubjectParts{Kind: kind, Collection: col, ID: id}
	case "guest":
		if ip, isIP := strings.CutPrefix(rest, "ip:"); isIP {
			return SubjectParts{Kind: kind, IP: ip}
		}
	}
	return SubjectParts{Kind: kind, ID: rest}
}
