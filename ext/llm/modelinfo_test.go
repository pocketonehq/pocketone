package llm

import (
	"context"
	"math"
	"testing"

	llmc "pocketone.dev/ext/contracts/llm"
	"pocketone.dev/ext/llm/providers/cloudflare"
)

func TestSizeOf(t *testing.T) {
	cases := map[string]string{
		"Qwen/Qwen3-TTS-12Hz-1.7B-Base":                      "1.7B",
		"nvidia/Nemotron-3-Nano-Omni-30B-A3B-Reasoning-BF16": "30B (3B active)",
		"Qwen/Qwen3.8-27B-FP8":                               "27B",
		"ibm-granite/granite-speech-5.0-2b":                  "2B",
		"deepseek-ai/DeepSeek-V4.1-Flash-1017":               "",
		"Tongyi-MAI/Z-Image-Turbo":                           "",
	}
	for name, want := range cases {
		if got := sizeOf(name); got != want {
			t.Errorf("sizeOf(%q) = %q, want %q", name, got, want)
		}
	}
	if got := sizeOf("", "qwen3-embedding-4b"); got != "4B" {
		t.Errorf("the second name: %q", got)
	}
}

func TestCloudflarePrices(t *testing.T) {
	got := cloudflarePrices([]cloudflare.Price{{Unit: "per M input tokens", Price: 0.3}, {Unit: "per M output tokens", Price: 2},
		{Unit: "per audio minute", Price: 0.6}, {Unit: "per audio minute (websocket)", Price: 1}})
	want := map[string]float64{llmc.UnitInputTokens: 0.3, llmc.UnitOutputTokens: 2, llmc.UnitSecond: 0.01}
	if len(got) != len(want) {
		t.Fatalf("prices: %+v", got)
	}
	for _, p := range got {
		if math.Abs(p.USD-want[p.Unit]) > 1e-12 || p.Source != llmc.SourceListed {
			t.Errorf("%s: %+v", p.Unit, p)
		}
	}
	if tile := cloudflarePrices([]cloudflare.Price{{Unit: "per 512 by 512 tile", Price: 0.0000528}}); len(tile) != 1 ||
		tile[0].Unit != llmc.UnitMegapixel || math.Abs(tile[0].USD-0.0000528/0.262144) > 1e-12 {
		t.Errorf("tile: %+v", tile)
	}
	// a price in a unit it can't convert would leave the rest looking cheaper than the model is: no prices at all
	if steps := cloudflarePrices([]cloudflare.Price{{Unit: "per 512 by 512 tile", Price: 1}, {Unit: "per step", Price: 1}}); steps != nil {
		t.Errorf("per step: %+v", steps)
	}
}

// TestMeasured: a model's measured price is what billed calls cost per unit used; estimated costs and refused calls
// don't count.
func TestMeasured(t *testing.T) {
	_, r := ledgerApp(t)
	ctx := context.Background()
	r.record(ctx, &call{kind: "speech", model: "v/voice"}, llmc.Usage{Seconds: 10, CostMicroUSD: 3000}, "")
	r.record(ctx, &call{kind: "speech", model: "v/voice"}, llmc.Usage{Seconds: 30, CostMicroUSD: 9000}, "")
	r.record(ctx, &call{kind: "speech", model: "v/voice"}, llmc.Usage{Seconds: 99, CostMicroUSD: 1, CostEstimated: true}, "")
	r.record(ctx, &call{kind: "speech", model: "v/voice"}, llmc.Usage{}, "over the limit")
	r.record(ctx, &call{kind: "text", model: "o/chat"}, llmc.Usage{InputTokens: 900, OutputTokens: 100, CostMicroUSD: 500}, "")
	r.record(ctx, &call{kind: "image", model: "v/paint"}, llmc.Usage{CostMicroUSD: 20000}, "")

	speech := r.measured(llmc.KindSpeech)["v/voice"]
	if speech.Unit != llmc.UnitSecond || speech.Calls != 2 || math.Abs(speech.USD-0.0003) > 1e-12 || speech.Source != llmc.SourceMeasured {
		t.Errorf("speech: %+v", speech)
	}
	if text := r.measured(llmc.KindText)["o/chat"]; text.Unit != llmc.UnitTokens || math.Abs(text.USD-0.5) > 1e-12 {
		t.Errorf("text: %+v", text)
	}
	if img := r.measured(llmc.KindImage)["v/paint"]; img.Unit != llmc.UnitRequest || math.Abs(img.USD-0.02) > 1e-12 {
		t.Errorf("image: %+v", img)
	}
	if _, ok := r.measured(llmc.KindMusic)["v/voice"]; ok {
		t.Error("another kind's rows counted")
	}
}
