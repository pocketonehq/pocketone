package llm

import (
	"regexp"
	"slices"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"

	llmc "pocketone.dev/ext/contracts/llm"
)

// What drivers share to describe a model in the contract's terms: its size from its names, and its prices in the
// contract's units.

// sizePattern finds a parameter count in a model's name: "1.7B", "27B", "30B-A3B" (a mixture of experts: 30B in all,
// 3B active), "2b". The count stands on its own: "v4.1" or "12Hz" aren't sizes.
var sizePattern = regexp.MustCompile(`(?i)(?:^|[-_/ .])(\d+(?:\.\d+)?)b(?:-a(\d+(?:\.\d+)?)b)?(?:$|[-_/ .])`)

// sizeOf is the parameter count named in the first of names that has one ("27B", "30B (3B active)"), or "".
func sizeOf(names ...string) string {
	for _, n := range names {
		m := sizePattern.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		size := strings.ToUpper(m[1]) + "B"
		if m[2] != "" {
			size += " (" + m[2] + "B active)"
		}
		return size
	}
	return ""
}

// addFeature adds f to info's features once.
func addFeature(info *llmc.ModelInfo, f string) {
	if !slices.Contains(info.Features, f) {
		info.Features = append(info.Features, f)
	}
}

// addPrice adds a price in the contract's units, once per unit (the first one given wins).
func addPrice(info *llmc.ModelInfo, unit string, usd float64) {
	if usd <= 0 {
		return
	}
	for _, p := range info.Prices {
		if p.Unit == unit {
			return
		}
	}
	info.Prices = append(info.Prices, llmc.Price{Unit: unit, USD: usd, Source: llmc.SourceListed})
}

// measured is, for each model of kind that this app's calls have been billed for, its price as measured from the
// ledger: what the provider billed divided by what the calls used, in the kind's own unit. Only billed costs count
// (not ones estimated from listed prices), so a measured price is never a listed one read back.
func (r *router) measured(kind string) map[string]llmc.Price {
	var rows []struct {
		Model   string  `db:"model"`
		Calls   int     `db:"calls"`
		Tokens  int64   `db:"tokens"`
		Input   int64   `db:"input"`
		Seconds float64 `db:"seconds"`
		Cost    int64   `db:"cost"`
	}
	err := r.app.DB().NewQuery(`SELECT [[model]] AS model, COUNT(*) AS calls,
		COALESCE(SUM([[input]] + [[output]]), 0) AS tokens, COALESCE(SUM([[input]]), 0) AS input,
		COALESCE(SUM([[seconds]]), 0) AS seconds, COALESCE(SUM([[cost]]), 0) AS cost
		FROM {{` + LedgerCollection + `}}
		WHERE [[kind]] = {:kind} AND [[refused]] = '' AND [[estimated]] = FALSE AND [[cost]] > 0
		GROUP BY [[model]]`).Bind(dbx.Params{"kind": kind}).All(&rows)
	out := map[string]llmc.Price{}
	if err != nil {
		return out
	}
	for _, row := range rows {
		usd := float64(row.Cost) / 1e6
		p := llmc.Price{Source: llmc.SourceMeasured, Calls: row.Calls}
		switch kind {
		case llmc.KindText:
			if row.Tokens == 0 {
				continue
			}
			p.Unit, p.USD = llmc.UnitTokens, usd/float64(row.Tokens)*1e6
		case llmc.KindEmbed:
			if row.Input == 0 {
				continue
			}
			p.Unit, p.USD = llmc.UnitInputTokens, usd/float64(row.Input)*1e6
		case llmc.KindImage:
			p.Unit, p.USD = llmc.UnitRequest, usd/float64(row.Calls)
		default: // speech, transcribe, align, music, video: by the second
			if row.Seconds == 0 {
				p.Unit, p.USD = llmc.UnitRequest, usd/float64(row.Calls)
			} else {
				p.Unit, p.USD = llmc.UnitSecond, usd/row.Seconds
			}
		}
		out[row.Model] = p
	}
	return out
}
