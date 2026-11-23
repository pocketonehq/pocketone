package pages

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/subscriptions"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"
)

// waitFor polls cond every 10 ms, failing after 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("gave up after 2 s waiting for %s", what)
}

// openStream starts a GET on the test server in the background and sends each SSE "event: data" pair it reads to the returned channel,
// which closes when the response ends.
func openStream(t *testing.T, ctx context.Context, url, token string) <-chan string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	out := make(chan string, 256)
	go func() { // headers may come only with the first event
		defer close(out)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- "error " + err.Error()
			return
		}
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				out <- event + " " + strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

// The visitor's subscription gets only the records the collection's list rule and the subscription's filter let it
// see, and it's unregistered from the broker when the visitor leaves.
func TestRealtimeSubscribeRules(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+config.js": "module.exports = { plugins: ['realtime'] }",
		"notes.ts": `export async function get(ctx: any) {
			const s = ctx.res.sse()
			for await (const m of ctx.realtime.subscribe('notes/*', { filter: "title != 'skip'" })) {
				s.send(m.action, m.record.title)
			}
		}`,
	})
	h, p := newServer(t, dir)
	app := p.app
	notes := core.NewBaseCollection("notes")
	notes.Fields.Add(&core.TextField{Name: "title"}, &core.TextField{Name: "owner"})
	notes.ListRule = types.Pointer("owner = @request.auth.id")
	if err := app.Save(notes); err != nil {
		t.Fatal(err)
	}
	u1, err := app.FindAuthRecordByEmail("users", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := app.FindAuthRecordByEmail("users", "test2@example.com")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := u1.NewAuthToken()

	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := openStream(t, ctx, srv.URL+"/notes", token)
	broker := app.SubscriptionsBroker()
	waitFor(t, "the page's subscription", func() bool { return broker.TotalClients() == 1 })

	save := func(owner *core.Record, title string) {
		rec := core.NewRecord(notes)
		rec.Set("title", title)
		rec.Set("owner", owner.Id)
		if err := app.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	save(u2, "theirs")
	save(u1, "skip")
	save(u1, "mine")

	select {
	case got := <-events:
		if got != "create mine" {
			t.Fatalf("first event: %q, want only %q", got, "create mine")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2 s")
	}
	select {
	case got := <-events:
		t.Fatalf("unexpected event %q", got)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	waitFor(t, "the subscription to be unregistered", func() bool { return broker.TotalClients() == 0 })
}

// A page that stops reading falls behind: the subscription ends and the loop throws, instead of skipping messages.
// Custom topics give the data sent to them, and close() ends the loop; the handler returning unregisters it.
func TestRealtimeSubscribeOverflowAndClose(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+config.js": "module.exports = { plugins: ['realtime'] }",
		"flood.ts": `export async function get(ctx: any) {
			const sub = ctx.realtime.subscribe('flood')
			const s = ctx.res.sse()
			s.send('ready', '1')
			await new Promise((r) => setTimeout(r, 300))
			try {
				for await (const m of sub) s.send('msg', String(m.n))
			} catch (e: any) {
				s.send('error', e.message)
			}
		}`,
		"one.ts": `export async function get(ctx: any) {
			const s = ctx.res.sse()
			const sub = ctx.realtime.subscribe('chat')
			for await (const m of sub) {
				s.send('msg', m.text)
				sub.close()
			}
			s.send('done', '1')
		}`,
	})
	h, p := newServer(t, dir)
	broker := p.app.SubscriptionsBroker()
	srv := httptest.NewServer(h)
	defer srv.Close()

	send := func(topic, data string) {
		for _, c := range broker.Clients() {
			if c.HasSubscription(topic) {
				c.Send(subscriptions.Message{Name: topic, Data: []byte(data)})
			}
		}
	}

	events := openStream(t, context.Background(), srv.URL+"/flood", "")
	if got := <-events; got != "ready 1" {
		t.Fatalf("first event %q", got)
	}
	// One past the queue: each Send returns once the subscription has read it, so the last one is what ends it, and
	// no later Send races the client's Discard (a recovered send on a closed channel in PocketBase's client).
	for i := 0; i < realtimeQueue+1; i++ {
		send("flood", `{"n":1}`)
	}
	var got []string
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "error ") || !strings.Contains(got[0], "behind") {
		t.Fatalf("want only the fell-behind error, got %q", got)
	}
	waitFor(t, "the flooded subscription to be unregistered", func() bool { return broker.TotalClients() == 0 })

	events = openStream(t, context.Background(), srv.URL+"/one", "")
	waitFor(t, "the chat subscription", func() bool { return broker.TotalClients() == 1 })
	send("chat", `{"text":"hi"}`)
	got = nil
	for e := range events {
		got = append(got, e)
	}
	if strings.Join(got, "|") != "msg hi|done 1" {
		t.Fatalf("events %q", got)
	}
	if n := broker.TotalClients(); n != 0 {
		t.Fatalf("%d clients left after the handler returned", n)
	}
}

// The Datastar example on the realtime plugin's docs page works as written.
func TestRealtimeSubscribeDatastarExample(t *testing.T) {
	addon, err := os.ReadFile("addons/datastar.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, map[string]string{
		"+config.js":              "module.exports = { plugins: ['realtime', 'datastar'] }",
		"_private/datastar.js":    string(addon),
		"todos/_private/todo.ets": `<li id="todo-<%= todo.id %>"><%= todo.title %></li>`,
		"todos/live.ts": `module.exports = {
  async get(ctx) {
    for await (const { action, record } of ctx.realtime.subscribe('todos/*')) {
      if (action === 'delete') {
        ctx.datastar.patchElements('', { selector: ` + "`#todo-${record.id}`" + `, mode: 'remove' })
      } else if (action === 'create') {
        ctx.datastar.patchElements(ctx.include('todo', { todo: record }), { selector: '#todos', mode: 'append' })
      } else {
        ctx.datastar.patchElements(ctx.include('todo', { todo: record }))
      }
    }
  },
}`,
	})
	h, p := newServer(t, dir)
	app := p.app
	todos := core.NewBaseCollection("todos")
	todos.Fields.Add(&core.TextField{Name: "title"})
	todos.ListRule = types.Pointer("")
	if err := app.Save(todos); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := openStream(t, ctx, srv.URL+"/todos/live", "")
	waitFor(t, "the page's subscription", func() bool { return app.SubscriptionsBroker().TotalClients() == 1 })

	next := func() string {
		t.Helper()
		var lines []string
		deadline := time.After(2 * time.Second)
		for {
			select {
			case e := <-events:
				lines = append(lines, e)
				if strings.HasPrefix(e, "datastar-patch-elements elements ") || strings.Contains(e, "mode remove") {
					if !strings.Contains(e, "selector") || len(lines) > 1 {
						return strings.Join(lines, "|")
					}
				}
			case <-deadline:
				t.Fatalf("no patch within 2 s; got %q", lines)
			}
		}
	}
	rec := core.NewRecord(todos)
	rec.Set("title", "milk")
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	li := `<li id="todo-` + rec.Id + `">`
	if got := next(); !strings.Contains(got, "selector #todos|datastar-patch-elements mode append|datastar-patch-elements elements "+li+"milk</li>") {
		t.Fatalf("create patch: %q", got)
	}
	rec.Set("title", "oat milk")
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "datastar-patch-elements elements "+li+"oat milk</li>" {
		t.Fatalf("update patch: %q", got)
	}
	if err := app.Delete(rec); err != nil {
		t.Fatal(err)
	}
	if got := next(); !strings.Contains(got, "selector #todo-"+rec.Id+"|datastar-patch-elements mode remove") {
		t.Fatalf("delete patch: %q", got)
	}
}
