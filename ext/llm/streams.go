package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	llmc "pocketone.dev/ext/contracts/llm"
	chatv1 "pocketone.dev/ext/contracts/llm/chat/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/extkit"
)

// Streams for page code, without blocking the app's JS VM: streamStart runs a stream on its own goroutine and buffers
// its pieces; JS awaits streamNext.async(id) in a loop (each wait runs off the VM) and calls onText with what came back,
// on the VM, between waits. streamStop ends one early. A stream nobody asks for in streamIdle is stopped.

const streamIdle = 2 * time.Minute

type jsStream struct {
	ch     chan textv1.Chunk // pieces, then closed
	cancel context.CancelFunc
	err    error // set before ch closes
	used   time.Time
	mu     sync.Mutex
}

var (
	streamsMu sync.Mutex
	streams   = map[string]*jsStream{}
)

func init() {
	go func() {
		for range time.Tick(30 * time.Second) {
			streamsMu.Lock()
			for id, s := range streams {
				s.mu.Lock()
				idle := time.Since(s.used) > streamIdle
				s.mu.Unlock()
				if idle {
					s.cancel()
					delete(streams, id)
				}
			}
			streamsMu.Unlock()
		}
	}()
}

// streamStart starts a text answer (kind "text", opts a llm.text/v1 complete input) or a chat send (kind "chat", opts
// a llm.chat/v1 send input) and returns its id. Errors before the answer starts (a bad model, no provider) come back
// here, as for a plain call.
func streamStart(app core.App, kind, opts string) (string, error) {
	var s extkit.Stream
	var err error
	switch kind {
	case "text":
		var in textv1.CompleteInput
		if err := json.Unmarshal([]byte(opts), &in); err != nil {
			return "", errors.New("$llm.text.streamAsync: " + err.Error())
		}
		t, ok := textv1.Contract.Of(app)
		if !ok {
			return "", llmc.ErrNotConfigured
		}
		s, err = t.Stream(subjectCtx(context.Background(), opts), extkit.Internal, in)
	case "chat":
		var in chatv1.SendInput
		if err := json.Unmarshal([]byte(opts), &in); err != nil {
			return "", errors.New("chat.sendAsync: " + err.Error())
		}
		c, ok := chatv1.Contract.Of(app)
		if !ok {
			return "", errNoChat
		}
		s, err = c.SendStream(subjectCtx(context.Background(), opts), extkit.Internal, in)
	default:
		return "", fmt.Errorf("$llm: no stream kind %q", kind)
	}
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(subjectCtx(context.Background(), opts)) // the answer's own calls count for the same subject
	js := &jsStream{ch: make(chan textv1.Chunk, 1024), cancel: cancel, used: time.Now()}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	streamsMu.Lock()
	streams[id] = js
	streamsMu.Unlock()
	go func() {
		defer cancel()
		if s.Close != nil {
			defer s.Close()
		}
		js.err = s.Run(ctx, func(e any) error {
			ch, _ := e.(textv1.Chunk)
			select {
			case js.ch <- ch:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		close(js.ch)
	}()
	return id, nil
}

// streamStep is what streamNext answers: the pieces since the last call, and, at the end, the result or the error.
type streamStep struct {
	Pieces []textv1.Chunk         `json:"pieces"`
	Done   bool                   `json:"done,omitempty"`
	Result *textv1.CompleteResult `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// streamNext waits for the stream's next pieces (at least one, or its end) and returns everything buffered by then.
// JS calls it with .async, so the wait is off the VM.
func streamNext(id string) (string, error) {
	streamsMu.Lock()
	s := streams[id]
	streamsMu.Unlock()
	if s == nil {
		return "", fmt.Errorf("$llm: no stream %q (it ended, or nobody read it for %s)", id, streamIdle)
	}
	s.mu.Lock()
	s.used = time.Now()
	s.mu.Unlock()
	step := streamStep{Pieces: []textv1.Chunk{}}
	take := func(ch textv1.Chunk) {
		if ch.Done {
			step.Result = ch.Result
		} else {
			step.Pieces = append(step.Pieces, ch)
		}
	}
	ch, ok := <-s.ch
	if ok {
		take(ch)
	drain:
		for {
			select {
			case ch, ok = <-s.ch:
				if !ok {
					break drain
				}
				take(ch)
			default:
				break drain
			}
		}
	}
	if !ok {
		step.Done = true
		if s.err != nil && !errors.Is(s.err, context.Canceled) {
			step.Error = s.err.Error()
		}
		streamsMu.Lock()
		delete(streams, id)
		streamsMu.Unlock()
	}
	b, err := json.Marshal(step)
	return string(b), err
}

// streamStop ends a stream early (onText returned false).
func streamStop(id string) {
	streamsMu.Lock()
	s := streams[id]
	delete(streams, id)
	streamsMu.Unlock()
	if s != nil {
		s.cancel()
	}
}

// streamPrelude is the JS side: it reads a stream until it ends, calling onText and onReasoning on the VM. It returns
// the whole answer; when onText says stop, the text so far with stopReason "cancelled".
const streamPrelude = `
  const readStream = async (kind, o, onText, onReasoning) => {
    const { signal, ...rest } = o ?? {};
    signal?.throwIfAborted();
    const id = n.streamStart(kind, S(rest));
    const stop = () => n.streamStop(id);
    signal?.addEventListener("abort", stop);
    try {
      let text = "";
      for (;;) {
        let r;
        try { r = J(await n.streamNext.asyncWith({ signal }, id)); } catch (err) { throw signal?.aborted ? err : clean(err); }
        for (const p of r.pieces) {
          if (p.reasoning && onReasoning && onReasoning(p.reasoning) === false) { stop(); return { text, stopReason: "cancelled" }; }
          if (p.text) {
            text += p.text;
            if (onText && onText(p.text) === false) { stop(); return { text, stopReason: "cancelled" }; }
          }
        }
        if (r.done) {
          if (r.error) throw clean(new Error(r.error));
          return r.result || { text, stopReason: "cancelled" };
        }
      }
    } finally {
      signal?.removeEventListener("abort", stop);
    }
  };
`
