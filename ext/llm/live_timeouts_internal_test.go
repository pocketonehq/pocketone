package llm

// Live: text calls stream under the hood with a short idle limit against real providers, and still answer.
//
//	P1_TEST_VULTR_KEY, P1_TEST_OPENROUTER_KEY, P1_TEST_CF_ACCOUNT + P1_TEST_CF_TOKEN (each optional)
//	P1_TEST_IDLE=2s     the idle limit to use (default 2s)

import (
	"context"
	"os"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/extkit"
)

func TestLiveShortIdle(t *testing.T) {
	var ps []Provider
	var models []string
	if k := os.Getenv("P1_TEST_VULTR_KEY"); k != "" {
		ps = append(ps, Provider{Name: "vultr", Type: TypeVultr, Key: k})
		models = append(models, "vultr/deepseek-v4.1-flash-1017")
	}
	if k := os.Getenv("P1_TEST_OPENROUTER_KEY"); k != "" {
		ps = append(ps, Provider{Name: "or", Type: TypeOpenRouter, Key: k})
		models = append(models, "or/google/gemini-2.5-pro", "or/openai/gpt-5-mini")
	}
	if a, k := os.Getenv("P1_TEST_CF_ACCOUNT"), os.Getenv("P1_TEST_CF_TOKEN"); a != "" && k != "" {
		ps = append(ps, Provider{Name: "cf", Type: TypeCloudflare, Account: a, Key: k})
		models = append(models, "cf/@cf/meta/llama-3.3-70b-instruct-fp8-fast")
	}
	if len(ps) == 0 {
		t.Skip("no P1_TEST_* keys")
	}
	idle := 2 * time.Second
	if d, err := time.ParseDuration(os.Getenv("P1_TEST_IDLE")); err == nil {
		idle = d
	}
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	if _, err := extkit.Setup(app, []extkit.Extension{New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(app, Settings{Providers: ps}); err != nil {
		t.Fatal(err)
	}
	old := idleText
	idleText = func(string) time.Duration { return idle }
	defer func() { idleText = old }()
	tx := textv1.Contract.Must(app)
	schema := []byte(`{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"}},"required":["title","body"]}`)
	for _, m := range models {
		for _, withSchema := range []bool{false, true} {
			in := textv1.CompleteInput{Model: m, Prompt: "Write a 150-word bedtime story about a fox who learns to sail.",
				MaxTokens: 4000, Reasoning: "low"}
			if withSchema {
				in.Schema, in.Prompt = schema, in.Prompt+" Answer as JSON {title, body}."
			}
			start := time.Now()
			r, err := tx.Complete(context.Background(), extkit.Internal, in)
			t.Logf("%s schema=%v: %s, %d chars, stop %q, usage %+v, err %v", m, withSchema, time.Since(start).Round(time.Millisecond),
				len(r.Text), r.StopReason, r.Usage, err)
			if err != nil {
				t.Errorf("%s schema=%v: %v", m, withSchema, err)
			}
		}
	}
}
