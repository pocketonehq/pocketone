package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/extkit"
)

// A provider that accepts text calls and never answers: Complete fails with the timeout code in about the idle
// limit, naming the model, and the connection is closed.
func TestCompleteStalledProvider(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hungUp := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, conn); hungUp <- struct{}{} }()
		}
	}()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	if _, err := extkit.Setup(app, []extkit.Extension{New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(app, Settings{Providers: []Provider{{Name: "stall", Type: TypeOpenAI,
		Endpoint: "http://" + ln.Addr().String() + "/v1", Key: "k"}}}); err != nil {
		t.Fatal(err)
	}
	old := idleText
	idleText = func(string) time.Duration { return 300 * time.Millisecond }
	defer func() { idleText = old }()

	for _, schema := range []string{"", `{"type":"object"}`} {
		start := time.Now()
		_, err = textv1.Contract.Must(app).Complete(context.Background(), extkit.Internal,
			textv1.CompleteInput{Model: "stall/chat-1", Prompt: "hi", Schema: []byte(schema)})
		took := time.Since(start)
		var xe *extkit.Error
		if !errors.As(err, &xe) || xe.Code != "timeout" || xe.Status != 504 ||
			!strings.Contains(xe.Message, "stall/chat-1: the provider sent nothing for 300ms") {
			t.Fatalf("schema %q: want a timeout naming the model, got %v", schema, err)
		}
		if took < 300*time.Millisecond || took > 3*time.Second {
			t.Fatalf("schema %q: took %s", schema, took)
		}
		select {
		case <-hungUp:
		case <-time.After(2 * time.Second):
			t.Fatalf("schema %q: the request wasn't cancelled", schema)
		}
	}
}

// A call past its own limit is a timeout saying how long it waited; a caller's earlier deadline is the caller's.
func TestWithinLimit(t *testing.T) {
	tg := target{p: Provider{Name: "p"}, model: "m"}
	block := func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() }
	_, err := within(context.Background(), tg, 100*time.Millisecond, block)
	var xe *extkit.Error
	if !errors.As(err, &xe) || xe.Code != "timeout" || xe.Message != "p/m: no answer in 100ms, so the call was cancelled" {
		t.Fatalf("limit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = within(ctx, tg, time.Minute, block)
	if !errors.As(err, &xe) || xe.Code != "timeout" || !strings.Contains(xe.Message, "caller's deadline") {
		t.Fatalf("caller's deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = within(ctx, tg, time.Minute, block)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller's cancel: %v", err)
	}
}
