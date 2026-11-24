package llm

import (
	"context"

	llmc "pocketone.dev/ext/contracts/llm"
)

// Cost: every result's usage.costMicroUSD, filled in when the provider didn't report one, from the model's listed prices
// (the provider's model list, cached for listTTL) and what the call used. usage.costEstimated marks it.

// used is what a call used beyond its usage's tokens and seconds, for prices per picture, character or request.
type used struct {
	files int // pictures made
	chars int // characters read aloud
}

// fillCost sets u.CostMicroUSD from t's listed prices when the provider didn't report a cost, then writes the call's
// ledger row. It never fails a call: without a list, a listed price or a unit it can count, the cost stays empty.
func (r *router) fillCost(ctx context.Context, t target, u *llmc.Usage, x used) {
	if u == nil {
		return
	}
	if u.CostMicroUSD == 0 {
		r.estimateCost(ctx, t, u, x)
	}
	r.record(ctx, t.c, *u, "")
}

func (r *router) estimateCost(ctx context.Context, t target, u *llmc.Usage, x used) {
	ms, err := r.catalog(ctx, t.p, false)
	if err != nil {
		return
	}
	for _, m := range ms {
		if m.ID == t.model {
			if usd, ok := estimate(m.Prices, *u, x); ok {
				u.CostMicroUSD, u.CostEstimated = llmc.MicroUSD(&usd), true
			}
			return
		}
	}
}

// estimate works out a cost from listed prices (in the contract's units). ok is false when no price applied to
// anything the call used. A price per megapixel isn't counted: the call doesn't say how many pixels it made.
func estimate(prices []llmc.Price, u llmc.Usage, x used) (usd float64, ok bool) {
	var input, cachedInput, output float64
	var haveInput, haveCached, haveOutput bool
	add := func(price float64, n float64) {
		if n > 0 && price > 0 {
			usd += price * n
			ok = true
		}
	}
	for _, p := range prices {
		switch p.Unit {
		case llmc.UnitSecond:
			add(p.USD, u.Seconds)
		case llmc.UnitCharacters:
			add(p.USD/1e3, float64(x.chars))
		case llmc.UnitRequest:
			add(p.USD, 1)
		case llmc.UnitImage:
			add(p.USD, float64(x.files))
		case llmc.UnitCachedTokens:
			cachedInput, haveCached = p.USD/1e6, true
		case llmc.UnitInputTokens:
			input, haveInput = p.USD/1e6, true
		case llmc.UnitOutputTokens:
			output, haveOutput = p.USD/1e6, true
		}
	}
	in, cached := float64(u.InputTokens), float64(u.CachedTokens)
	if cached > in {
		cached = in
	}
	if haveInput {
		add(input, in-cached)
		if !haveCached {
			add(input, cached)
		}
	}
	if haveCached {
		add(cachedInput, cached)
	}
	if haveOutput {
		add(output, float64(u.OutputTokens))
	}
	return usd, ok
}
