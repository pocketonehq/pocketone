package subscriptions_test

import (
	"strconv"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/tools/subscriptions"
)

func TestSendOrdered(t *testing.T) {
	t.Parallel()

	client := subscriptions.NewDefaultClient()

	const total = 500

	// nobody reads yet: SendOrdered must not wait for the reader
	for i := 0; i < total; i++ {
		subscriptions.SendOrdered(client, subscriptions.Message{Name: strconv.Itoa(i)})
	}

	for i := 0; i < total; i++ {
		select {
		case m := <-client.Channel():
			if m.Name != strconv.Itoa(i) {
				t.Fatalf("message %d is %q, want the order they were sent in", i, m.Name)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("got %d of %d messages", i, total)
		}
	}
}

func TestSendOrderedConcurrentSenders(t *testing.T) {
	t.Parallel()

	client := subscriptions.NewDefaultClient()

	const senders, each = 8, 200

	done := make(chan struct{})
	for s := 0; s < senders; s++ {
		go func() {
			for i := 0; i < each; i++ {
				subscriptions.SendOrdered(client, subscriptions.Message{Name: strconv.Itoa(s), Data: []byte(strconv.Itoa(i))})
			}
			done <- struct{}{}
		}()
	}
	for s := 0; s < senders; s++ {
		<-done
	}

	// each sender's messages arrive in the order it sent them
	next := map[string]int{}
	for n := 0; n < senders*each; n++ {
		select {
		case m := <-client.Channel():
			i, _ := strconv.Atoi(string(m.Data))
			if i != next[m.Name] {
				t.Fatalf("sender %s: got message %d, want %d", m.Name, i, next[m.Name])
			}
			next[m.Name]++
		case <-time.After(3 * time.Second):
			t.Fatalf("got %d of %d messages", n, senders*each)
		}
	}
}

func TestSendOrderedDiscardedClient(t *testing.T) {
	t.Parallel()

	client := subscriptions.NewDefaultClient()
	subscriptions.SendOrdered(client, subscriptions.Message{Name: "a"}) // never read
	client.Discard()                                                    // ends the pending send
	subscriptions.SendOrdered(client, subscriptions.Message{Name: "b"}) // dropped, as Send drops it

	if _, ok := <-client.Channel(); ok {
		t.Fatal("a discarded client's channel should be closed")
	}
}

// panickyClient panics on its first Send and delivers the rest.
type panickyClient struct {
	*subscriptions.DefaultClient
	sends int
}

func (c *panickyClient) Send(m subscriptions.Message) {
	c.sends++
	if c.sends == 1 {
		panic("first send")
	}
	c.DefaultClient.Send(m)
}

func TestSendOrderedRecoversFromPanickingSend(t *testing.T) {
	t.Parallel()

	client := &panickyClient{DefaultClient: subscriptions.NewDefaultClient()}

	subscriptions.SendOrdered(client, subscriptions.Message{Name: "lost"})
	subscriptions.SendOrdered(client, subscriptions.Message{Name: "a"})
	subscriptions.SendOrdered(client, subscriptions.Message{Name: "b"})

	for _, want := range []string{"a", "b"} {
		select {
		case m := <-client.Channel():
			if m.Name != want {
				t.Fatalf("got %q, want %q", m.Name, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%q never arrived after the panicking send", want)
		}
	}
}
