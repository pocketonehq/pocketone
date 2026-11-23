package subscriptions

import (
	"log"
	"runtime"
	"sync"
)

// outboxes holds the messages queued by SendOrdered, by client id
// (unique per client, see [Client.Id]).
// A client has an entry only while a goroutine delivers its queue.
var outboxes = struct {
	sync.Mutex
	queues map[string][]Message
}{queues: map[string][]Message{}}

// SendOrdered queues m for the client and returns without waiting for the client to read it.
//
// The queued messages of a client are delivered with client.Send one at a time,
// in the order they were queued, by a single goroutine that exits once the queue is empty.
// Use it instead of `go client.Send(m)`, which delivers concurrent sends in any order.
func SendOrdered(client Client, m Message) {
	id := client.Id()

	outboxes.Lock()
	queue, delivering := outboxes.queues[id]
	outboxes.queues[id] = append(queue, m)
	outboxes.Unlock()

	if delivering {
		return
	}

	go func() {
		for {
			outboxes.Lock()
			queue := outboxes.queues[id]
			if len(queue) == 0 {
				delete(outboxes.queues, id)
				outboxes.Unlock()
				return
			}
			outboxes.queues[id] = nil // keep the entry: still delivering
			outboxes.Unlock()

			for _, m := range queue {
				sendRecovered(client, m) // returns right away once the client is discarded
			}
		}
	}()
}

// sendRecovered calls client.Send and logs a panic instead of crashing,
// so the rest of the client's queue is still delivered.
func sendRecovered(client Client, m Message) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Println("[SendOrdered] RECOVERED FROM PANIC:", rec)

			stack := make([]byte, 2<<10) // 2 KB
			length := runtime.Stack(stack, false)
			log.Println(string(stack[:length]))
		}
	}()

	client.Send(m)
}
