---
title: Spend and the ledger
description: "Every model call is recorded: $llm.ledger tells you what's left of each limit, what a user or guest has spent, and where the money goes, by model, kind, person or day."
---

# Spend and the ledger

Every call that reaches a provider, and every call a limit refuses, is recorded in the app's ledger: its kind and
model, whom it was for, what it used and what it cost. The limits count from it, Settings → LLM shows today's and this
month's spend from it, and `$llm.ledger` reads it for you:

```ts
const llm = $llm.for(ctx)
llm.ledger.remaining()                                  // what's left of each limit, for this visitor
llm.ledger.spent("day")                                 // what this visitor has spent today
$llm.ledger.summary({ by: "model", window: "month" })   // where the money went this month
$llm.ledger.calls({ status: "refused", limit: 20 })     // the latest calls a limit refused
```

```go
ledger := ledgerv1.Contract.Must(app) // pocketone.dev/ext/contracts/llm/ledger/v1
ctx := llmc.WithSubject(e.Request.Context(), llm.SubjectOf(app, e))
ledger.Remaining(ctx, extkit.Internal, ledgerv1.RemainingInput{})                    // what's left, for this visitor
ledger.Spent(ctx, extkit.Internal, ledgerv1.SpentInput{Window: "day"})                // what this visitor has spent today
ledger.Summary(ctx, extkit.Internal, ledgerv1.SummaryInput{By: "model", Window: "month"}) // where the money went
ledger.Calls(ctx, extkit.Internal, ledgerv1.CallsInput{Status: "refused", Limit: 20})    // the latest refused calls
```

These read the app's own database, so they're plain calls, not `Async` ones. In Go they're the `llm.ledger/v1` contract;
each returns its result and an error.

## What's left

`remaining()` lists every limit in Settings → LLM that counts the subject's calls, with how much is used and left in
its current window, the tightest first:

```ts
// pb_pages/ask/index.ts, beside ask/index.ets: "4 questions left today"
export function load(ctx: pages.Context) {
  const tightest = $llm.for(ctx).ledger.remaining()[0]
  return { left: tightest }
}
```

```go
ctx := llmc.WithSubject(e.Request.Context(), llm.SubjectOf(app, e))
rem, err := ledgerv1.Contract.Must(app).Remaining(ctx, extkit.Internal, ledgerv1.RemainingInput{})
if err != nil {
	return err
}
var tightest *ledgerv1.LimitLeft
if len(rem.Limits) > 0 {
	tightest = &rem.Limits[0]
}
```

It answers:

```js
[
  {
    limit: "each guest, 10 calls a day",   // the limit in words
    scope: "guest", window: "day", metric: "calls",
    amount: 10, used: 6, left: 4,           // in the metric's unit: dollars for cost, else tokens or calls
    resets: "2026-11-23T00:00:00Z",         // when its window ends
  },
  { limit: "all guests together, $1.00 a day", metric: "cost", amount: 1, used: 0.31, left: 0.69, ... },
]
```

It's the same count the limits use, so it agrees with what the next refusal would say. Limits your own
[onCall handlers](custom-limits.md) enforce aren't in it: they're your code, so show their numbers your way.

## What someone has spent

`spent()` is the totals for one subject in a window:

```ts
$llm.for(ctx).ledger.spent("day")         // this request's user or guest, today (UTC)
$llm.for(ctx).ledger.spent("month")
$llm.ledger.spent({ subject: "user:users/abc", since: "2026-11-01", until: "2026-11-15" })
$llm.ledger.spent({ window: "month" })    // no subject: the whole app
// { calls: 42, refused: 3, input: 51200, cached: 20480, output: 9800, seconds: 0, costMicroUSD: 312000 }
```

```go
ledger := ledgerv1.Contract.Must(app)
ledger.Spent(ctx, extkit.Internal, ledgerv1.SpentInput{Window: "day"}) // ctx's subject (llmc.WithSubject), today (UTC)
ledger.Spent(ctx, extkit.Internal, ledgerv1.SpentInput{Window: "month"})
ledger.Spent(ctx, extkit.Internal, ledgerv1.SpentInput{Subject: "user:users/abc", Since: "2026-11-01", Until: "2026-11-15"})
ledger.Spent(context.Background(), extkit.Internal, ledgerv1.SpentInput{Window: "month"}) // no subject: the whole app
// ledgerv1.Totals{Calls: 42, Refused: 3, Input: 51200, Cached: 20480, Output: 9800, CostMicroUSD: 312000}
```

`calls` counts calls that reached a provider and `refused` the ones a limit refused. `costMicroUSD` is in millionths of
a US dollar, like every result's `usage.costMicroUSD`: divide by 1,000,000 for dollars. Add `kind` or `model` to count
only those calls.

Windows are UTC: `"day"` and `"month"` are the calendar day and month holding now, the same windows limits count in.
`since` and `until` take a date (`2026-11-01`) or a full time; `until` is exclusive.

## Where the money goes

`summary()` groups the totals, costliest first:

```ts
$llm.ledger.summary({ by: "model", window: "month" })
// [{ key: "vultr/glm-5.3", calls: 812, costMicroUSD: 4120000, ... }, { key: "vultr/z-image-turbo", ... }]

$llm.ledger.summary({ by: "subject", window: "day", limit: 10 })   // today's ten biggest spenders
$llm.ledger.summary({ by: "kind", window: "month" })                 // text vs pictures vs video
$llm.ledger.summary({ by: "day", since: "2026-11-01" })              // a row per day, latest first
```

```go
ledger := ledgerv1.Contract.Must(app)
ledger.Summary(ctx, extkit.Internal, ledgerv1.SummaryInput{By: "model", Window: "month"})
// .Rows: []ledgerv1.SummaryRow{{Key: "vultr/glm-5.3", Totals: ledgerv1.Totals{Calls: 812, CostMicroUSD: 4120000}}, ...}

ledger.Summary(ctx, extkit.Internal, ledgerv1.SummaryInput{By: "subject", Window: "day", Limit: 10}) // today's ten biggest spenders
ledger.Summary(ctx, extkit.Internal, ledgerv1.SummaryInput{By: "kind", Window: "month"})            // text vs pictures vs video
ledger.Summary(ctx, extkit.Internal, ledgerv1.SummaryInput{By: "day", Since: "2026-11-01"})          // a row per day, latest first
```

Narrow it with `subject`, `kind` or `model`, as with `spent`.

## The calls themselves

`calls()` lists the rows, newest first, 50 at a time:

```ts
const page = $llm.ledger.calls({ subject: "user:users/abc", window: "month", limit: 20 })
page.calls  // [{ id, created, kind, model, subject, via, input, cached, output, seconds, costMicroUSD, estimated, refused, job }]
page.more   // true when there are more: ask again with offset: 20
```

```go
page, err := ledgerv1.Contract.Must(app).Calls(ctx, extkit.Internal,
	ledgerv1.CallsInput{Subject: "user:users/abc", Window: "month", Limit: 20})
// page.Calls: []ledgerv1.Call{{ID, Created, Kind, Model, Subject, Via, Input, Cached, Output, Seconds, CostMicroUSD, ...}}
// page.More: true when there are more: ask again with Offset: 20
```

`status: "refused"` lists only refused calls (each with the limit's message in `refused`), and `status: "ok"` only the
ones that reached a provider.

## Over HTTP

The same four operations are the `llm.ledger/v1` contract, at `/api/_c/llm.ledger/v1/remaining`, `spent`, `summary`
and `calls`, with the options as query parameters (`?window=day`). `remaining` and `spent` answer a signed-in user
about themselves, so a frontend can show "4 left" with the user's own token; superusers may name any `subject`.
`summary` and `calls` are for superusers (or a superuser's token with the `llm:read` scope). Guests can't ask over
HTTP, since the operation sees credentials, not the guest cookie: answer them from page code with `$llm.for(ctx)`.

## The rows

The ledger is the `_llm_calls` collection, if you'd rather query it yourself:

| Field | |
|---|---|
| `kind` | `text`, `image`, `speech`, `transcribe`, `align`, `music`, `video` or `embed`. |
| `model` | The model, as `provider/model`. |
| `subject` | Whom the call was for: `user:<collection>/<id>`, `guest:<id>`, `guest:ip:<address>`, a name an `onCall` handler gave it, or empty for calls made with plain `$llm`. |
| `via` | How the call arrived: `go` (page code and hooks), `http`, `mcp`. |
| `input`, `cached`, `output` | Input tokens, the input tokens billed at the cheaper cached rate, and output tokens. |
| `seconds` | Seconds of audio or video, for the kinds billed by length. |
| `cost` | What it cost, in millionths of a US dollar. |
| `estimated` | True when the cost was worked out from the model's listed prices because the provider didn't report one. |
| `refused` | Why the call was refused (the limit's or handler's message); empty for a call that reached the provider. |
| `job` | A music or video job's id. Its cost is filled in once the job is done. |
| `created` | When the call was made. |

It's a system collection with no API rules: superusers see it in the dashboard (Collections → `_llm_calls`), and only
your app's code and the contract above read it. Rows are kept for 13 months; a daily job deletes older ones.

To act on each call as it's recorded, such as spending a user's credits, use
[`$llm.onCallDone`](custom-limits.md#credits) rather than a hook on the collection: it gets the call's usage and its
user already taken apart.
