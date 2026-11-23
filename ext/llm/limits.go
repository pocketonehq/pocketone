package llm

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"

	llmc "pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/extkit"
)

// Limits: rules in Settings → LLM that every call passes before it reaches a provider. A rule is
// scope × window × metric ≤ amount, optionally narrowed to one kind or one model:
//
//	the whole app, $5 a day                 {scope: app, window: day, metric: cost, amount: 5}
//	each signed-in user, 200k output tokens  {scope: user, window: day, metric: output, amount: 200000}
//	all guests together, $0.50 a day         {scope: guests, window: day, metric: cost, amount: 0.5}
//
// Counters come from the ledger. A call is checked before and counted after, so the last call let through can go
// over by its own cost. Guest calls are refused until an all-guests rule covers them: a guest can reset a per-guest
// bucket by clearing cookies, so only the shared total caps what guests spend.

// Scopes: whose calls a rule counts.
const (
	ScopeApp    = "app"    // every call
	ScopeUser   = "user"   // each signed-in user, separately
	ScopeGuest  = "guest"  // each guest, separately
	ScopeGuests = "guests" // all guests together, one bucket
)

// Windows: UTC calendar days and months.
const (
	WindowDay   = "day"
	WindowMonth = "month"
)

// Metrics: what a rule counts.
const (
	MetricCost   = "cost"   // US dollars (amount is dollars; the ledger keeps millionths)
	MetricInput  = "input"  // input tokens
	MetricOutput = "output" // output tokens
	MetricCalls  = "calls"  // calls that reached a provider
)

// Limit is one rule.
type Limit struct {
	Scope  string  `json:"scope"`
	Window string  `json:"window"`
	Metric string  `json:"metric"`
	Amount float64 `json:"amount"`
	Kind   string  `json:"kind,omitempty"`  // only calls of this kind ("text", "image", ...); "" for all
	Model  string  `json:"model,omitempty"` // only calls to this provider/model; "" for all
}

var kinds = []string{llmc.KindText, llmc.KindImage, llmc.KindSpeech, llmc.KindTranscribe, llmc.KindAlign,
	llmc.KindMusic, llmc.KindVideo, llmc.KindEmbed}

// checkLimit validates a rule from Settings → LLM, trimming its strings.
func checkLimit(l *Limit, i int) error {
	l.Scope, l.Window, l.Metric = strings.TrimSpace(l.Scope), strings.TrimSpace(l.Window), strings.TrimSpace(l.Metric)
	l.Kind, l.Model = strings.TrimSpace(l.Kind), strings.TrimSpace(l.Model)
	where := fmt.Sprintf("limit %d", i+1)
	switch l.Scope {
	case ScopeApp, ScopeUser, ScopeGuest, ScopeGuests:
	default:
		return badRequest(where + ": scope must be app, user, guest or guests")
	}
	switch l.Window {
	case WindowDay, WindowMonth:
	default:
		return badRequest(where + ": window must be day or month")
	}
	switch l.Metric {
	case MetricCost, MetricInput, MetricOutput, MetricCalls:
	default:
		return badRequest(where + ": metric must be cost, input, output or calls")
	}
	if l.Amount < 0 || math.IsNaN(l.Amount) || math.IsInf(l.Amount, 0) {
		return badRequest(where + ": the amount must be zero or more")
	}
	if l.Metric != MetricCost && l.Amount != math.Trunc(l.Amount) {
		return badRequest(where + ": tokens and calls are whole numbers")
	}
	if l.Kind != "" && !contains(kinds, l.Kind) {
		return badRequest(where + ": kind must be one of " + strings.Join(kinds, ", "))
	}
	if l.Model != "" {
		if _, _, ok := llmc.SplitModel(l.Model); !ok {
			return badRequest(where + ": name the model as provider/model")
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// applies reports whether rule l counts calls like c (its scope, kind and model).
func (l Limit) applies(c *call) bool {
	if l.Kind != "" && l.Kind != c.kind {
		return false
	}
	if l.Model != "" && l.Model != c.model {
		return false
	}
	switch l.Scope {
	case ScopeUser:
		return llmc.IsUser(c.subject)
	case ScopeGuest, ScopeGuests:
		return llmc.IsGuest(c.subject)
	}
	return true
}

// used is how much of rule l's metric calls like c have used in the window starting at since.
func (r *router) used(ctx context.Context, l Limit, c *call, since time.Time) (float64, error) {
	col := map[string]string{MetricCost: "SUM([[cost]])", MetricInput: "SUM([[input]])",
		MetricOutput: "SUM([[output]])", MetricCalls: "COUNT(*)"}[l.Metric]
	where := dbx.And(
		dbx.NewExp("[[created]] >= {:since}", dbx.Params{"since": since.Format("2006-01-02 15:04:05.000Z")}),
		dbx.HashExp{"refused": ""},
	)
	switch l.Scope {
	case ScopeUser, ScopeGuest:
		where = dbx.And(where, dbx.HashExp{"subject": c.subject})
	case ScopeGuests:
		where = dbx.And(where, dbx.NewExp("[[subject]] LIKE 'guest:%'"))
	}
	if l.Kind != "" {
		where = dbx.And(where, dbx.HashExp{"kind": l.Kind})
	}
	if l.Model != "" {
		where = dbx.And(where, dbx.HashExp{"model": l.Model})
	}
	var n float64
	err := r.app.DB().Select("COALESCE(" + col + ", 0)").From(LedgerCollection).Where(where).WithContext(ctx).Row(&n)
	if l.Metric == MetricCost {
		n /= 1e6
	}
	return n, err
}

// errGuestsUncapped refuses a guest's call when no all-guests rule covers it.
var errGuestsUncapped = &extkit.Error{Code: llmc.ErrLimit.Code, Status: llmc.ErrLimit.Status,
	Message: "guest calls are off until the app caps what all guests together may spend: add a limit for all guests in Settings → LLM"}

// admit checks every rule that applies to c. The first one used up refuses it with ErrLimit's code, naming the
// rule and when its window resets.
func (r *router) admit(ctx context.Context, limits []Limit, c *call, guestsCapped bool) error {
	if llmc.IsGuest(c.subject) && !guestsCapped {
		capped := false
		for _, l := range limits {
			if l.Scope == ScopeGuests && l.applies(c) {
				capped = true
				break
			}
		}
		if !capped {
			return errGuestsUncapped
		}
	}
	now := time.Now()
	for _, l := range limits {
		if !l.applies(c) {
			continue
		}
		since, next := windowStart(now, l.Window)
		n, err := r.used(ctx, l, c, since)
		if err != nil {
			return err
		}
		if n >= l.Amount {
			return &extkit.Error{Code: llmc.ErrLimit.Code, Status: llmc.ErrLimit.Status,
				Message: fmt.Sprintf("limit reached: %s (used %s); it resets %s", l.describe(), l.amount(n), resets(next))}
		}
	}
	return nil
}

// describe says a rule in words: "each guest, $0.10 a day of text".
func (l Limit) describe() string {
	who := map[string]string{ScopeApp: "the whole app", ScopeUser: "each user", ScopeGuest: "each guest",
		ScopeGuests: "all guests together"}[l.Scope]
	per := map[string]string{WindowDay: "a day", WindowMonth: "a month"}[l.Window]
	s := fmt.Sprintf("%s, %s %s", who, l.amount(l.Amount), per)
	switch {
	case l.Model != "":
		s += " of " + l.Model
	case l.Kind != "":
		s += " of " + l.Kind
	}
	return s
}

// amount writes n in the rule's metric: "$0.10", "200000 output tokens", "50 calls".
func (l Limit) amount(n float64) string {
	switch l.Metric {
	case MetricCost:
		if n < 0.01 && n > 0 {
			return fmt.Sprintf("$%.4f", n)
		}
		return fmt.Sprintf("$%.2f", n)
	case MetricInput:
		return fmt.Sprintf("%.0f input tokens", n)
	case MetricOutput:
		return fmt.Sprintf("%.0f output tokens", n)
	}
	return fmt.Sprintf("%.0f calls", n)
}

// resets says when a window ends: "at 00:00 UTC" for tomorrow, "on Dec 1 (UTC)" for a month.
func resets(next time.Time) string {
	if next.Sub(time.Now()) <= 24*time.Hour {
		return "at 00:00 UTC"
	}
	return "on " + next.Format("Jan 2") + " (UTC)"
}

// spend is Settings → LLM's summary: what the whole app has spent today and this month, from the ledger.
type spend struct {
	TodayMicroUSD int64 `json:"todayMicroUSD"`
	MonthMicroUSD int64 `json:"monthMicroUSD"`
	TodayCalls    int64 `json:"todayCalls"`
	Refused       int64 `json:"refusedToday"`
}

func (r *router) spend(ctx context.Context) (spend, error) {
	var s spend
	now := time.Now()
	day, _ := windowStart(now, WindowDay)
	month, _ := windowStart(now, WindowMonth)
	var err error
	q := func(since time.Time, expr string, out *int64, extra dbx.Expression) {
		if err != nil {
			return
		}
		where := dbx.And(dbx.NewExp("[[created]] >= {:since}", dbx.Params{"since": since.Format("2006-01-02 15:04:05.000Z")}), extra)
		err = r.app.DB().Select("COALESCE(" + expr + ", 0)").From(LedgerCollection).Where(where).WithContext(ctx).Row(out)
	}
	q(day, "SUM([[cost]])", &s.TodayMicroUSD, dbx.HashExp{"refused": ""})
	q(month, "SUM([[cost]])", &s.MonthMicroUSD, dbx.HashExp{"refused": ""})
	q(day, "COUNT(*)", &s.TodayCalls, dbx.HashExp{"refused": ""})
	q(day, "COUNT(*)", &s.Refused, dbx.NewExp("[[refused]] != ''"))
	return s, err
}
