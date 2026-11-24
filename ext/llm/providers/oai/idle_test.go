package oai

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A provider that accepts the connection and never answers: the call ends after IdleTimeout with an IdleError.
func TestChatStreamIdleNoAnswer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn) // reads the request, never writes; returns when the client hangs up
		close(closed)
	}()
	c := &Client{BaseURL: "http://" + ln.Addr().String() + "/v1"}
	start := time.Now()
	_, err = c.ChatStream(context.Background(), ChatRequest{Model: "m", IdleTimeout: 200 * time.Millisecond}, nil)
	var idle *IdleError
	if !errors.As(err, &idle) || idle.After != 200*time.Millisecond {
		t.Fatalf("want an IdleError, got %v", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
	select {
	case <-closed: // the request was really cancelled: the connection was closed
	case <-time.After(2 * time.Second):
		t.Fatal("the connection stayed open")
	}
}

// A stream that starts, then goes quiet: the gap ends it. Keep-alive comments count as activity.
func TestChatStreamIdleMidStream(t *testing.T) {
	gone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"Once"}}]}`+"\n\n")
		fl.Flush()
		// keep-alives for 3 idle periods: the call must survive them
		for i := 0; i < 6; i++ {
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(w, ": keepalive\n\n")
			fl.Flush()
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":" upon"}}]}`+"\n\n")
		fl.Flush()
		<-r.Context().Done() // then silence until the client gives up
		close(gone)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1"}
	var got string
	start := time.Now()
	_, err := c.ChatStream(context.Background(), ChatRequest{Model: "m", IdleTimeout: 100 * time.Millisecond}, func(d Delta) bool {
		got += d.Text
		return true
	})
	var idle *IdleError
	if !errors.As(err, &idle) {
		t.Fatalf("want an IdleError, got %v", err)
	}
	if got != "Once upon" {
		t.Fatalf("pieces before the stall: %q", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("the server never saw the request cancelled")
	}
}

// Cancelling the caller's context is the caller's error, not an IdleError, and errors.Is sees it.
func TestChatStreamCallerCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server notices the client hanging up only once the body is read
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.ChatStream(ctx, ChatRequest{Model: "m", IdleTimeout: time.Minute}, nil)
	var idle *IdleError
	if errors.As(err, &idle) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's deadline, got %v", err)
	}
	_, err = c.Chat(ctx, ChatRequest{Model: "m"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Chat: want the caller's deadline through Error.Unwrap, got %v", err)
	}
}
