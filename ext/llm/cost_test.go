package llm

import (
	"math"
	"testing"

	llmc "pocketone.dev/ext/contracts/llm"
)

// Prices are in the contract's units, as the drivers' models() give them.
func TestEstimate(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	cases := []struct {
		name   string
		prices []llmc.Price
		u      llmc.Usage
		x      used
		want   float64
		ok     bool
	}{
		{"output only", []llmc.Price{{Unit: llmc.UnitOutputTokens, USD: 3}},
			llmc.Usage{InputTokens: 787, OutputTokens: 190}, used{}, 190 * 3e-6, true},
		{"input and output", []llmc.Price{{Unit: llmc.UnitInputTokens, USD: 0.293}, {Unit: llmc.UnitOutputTokens, USD: 2.253}},
			llmc.Usage{InputTokens: 1000, OutputTokens: 500}, used{}, 1000*0.293/1e6 + 500*2.253/1e6, true},
		{"cached input billed at its own price", []llmc.Price{{Unit: llmc.UnitCachedTokens, USD: 0.075},
			{Unit: llmc.UnitInputTokens, USD: 0.15}, {Unit: llmc.UnitOutputTokens, USD: 0.6}},
			llmc.Usage{InputTokens: 949, CachedTokens: 256, OutputTokens: 117}, used{}, 693*0.15/1e6 + 256*0.075/1e6 + 117*0.6/1e6, true},
		{"cached tokens at the input price when no cached price is listed", []llmc.Price{{Unit: llmc.UnitInputTokens, USD: 1}},
			llmc.Usage{InputTokens: 100, CachedTokens: 40}, used{}, 100 / 1e6, true},
		{"per picture", []llmc.Price{{Unit: llmc.UnitImage, USD: 0.04}}, llmc.Usage{}, used{files: 2}, 0.08, true},
		{"per second of audio", []llmc.Price{{Unit: llmc.UnitSecond, USD: 0.002}}, llmc.Usage{Seconds: 10}, used{}, 0.02, true},
		{"per thousand characters", []llmc.Price{{Unit: llmc.UnitCharacters, USD: 0.015}}, llmc.Usage{}, used{chars: 2000}, 0.03, true},
		{"per request", []llmc.Price{{Unit: llmc.UnitRequest, USD: 0.005}}, llmc.Usage{}, used{}, 0.005, true},
		{"a unit it can't count", []llmc.Price{{Unit: llmc.UnitMegapixel, USD: 0.01}}, llmc.Usage{InputTokens: 5}, used{files: 1}, 0, false},
		{"no prices", nil, llmc.Usage{InputTokens: 5, OutputTokens: 5}, used{}, 0, false},
	}
	for _, c := range cases {
		got, ok := estimate(c.prices, c.u, c.x)
		if ok != c.ok || !near(got, c.want) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
