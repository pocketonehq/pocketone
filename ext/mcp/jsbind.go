package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/extkit/js"
)

// BindJS exposes the MCP server to an app's JS hooks as the $mcp global. Add it to the jsvm OnInit:
//
//	jsvm.MustRegister(app, jsvm.Config{OnInit: func(rt js.Runtime) { mcp.BindJS(app, rt) }})
//
// A tool maps one REST endpoint. Write the route as usual (routerAdd, a pb_pages .ts endpoint), then declare its tool at
// the top level of a pb_hooks file:
//
//	$mcp.addEndpoint({
//	  name: "blog_publish_post",
//	  description: "Publish a draft post.",
//	  method: "POST",
//	  path: "/api/blog/posts/{id}/publish", // {id} comes from args.id; other args go to the JSON body
//	  inputSchema: { type: "object", required: ["id"], properties: { id: { type: "string" } } },
//	  // optional: shape the request (sees the caller, can't change who it runs as)
//	  request: ({ args, auth, request }) => ({ ...request, body: { at: args.when } }),
//	  // optional: shape the result; throw for an error the model can read
//	  response: ({ status, json, body }) => (status === 200 ? { published: json.id } : json),
//	})
//	$mcp.instructions("blog_publish_post also emails subscribers.")
//	$mcp.tools() // -> ["blog_publish_post", "pb_check_health", …]
//
// request and response run on the app's VM (they may use the file's top-level variables); the REST call itself runs
// outside it. Keep them pure: the work belongs in the endpoint.
func BindJS(app core.App, rt js.Runtime) {
	// jsvm runs pb_hooks when it's registered, which may be before mcp.Register; until then tools and instructions
	// wait in the app store and Register adds them.
	addEndpoint := func(t Endpoint) error {
		if s := For(app); s != nil {
			return s.AddEndpoint(t)
		}
		p := pendingFor(app)
		p.mu.Lock()
		p.tools = append(p.tools, t)
		p.mu.Unlock()
		return nil
	}

	// call runs a JS transform (on the app's VM, from any goroutine) and turns a throw into its message.
	call := func(fn js.Func, ctx map[string]any) (any, error) {
		v, err := fn.Call(ctx)
		if err != nil {
			return nil, errors.New(js.Message(err))
		}
		return v, nil
	}

	_ = rt.Set("$mcp", map[string]any{
		"addEndpoint": js.NativeFunc(func(c js.Call) (any, error) {
			def := c.Arg(0).Object()
			if def == nil {
				return nil, errors.New("$mcp.addEndpoint: the argument must be an object")
			}
			t := Endpoint{
				Name:        jsString(def.Get("name")),
				Title:       jsString(def.Get("title")),
				Description: jsString(def.Get("description")),
				Method:      jsString(def.Get("method")),
				Path:        jsString(def.Get("path")),
			}
			var err error
			if t.InputSchema, err = jsObject(def.Get("inputSchema")); err != nil {
				return nil, errors.New("$mcp.addEndpoint: inputSchema: " + err.Error())
			}
			if t.OutputSchema, err = jsObject(def.Get("outputSchema")); err != nil {
				return nil, errors.New("$mcp.addEndpoint: outputSchema: " + err.Error())
			}
			if a := def.Get("annotations"); !a.Nullish() {
				raw, err := json.Marshal(a.Export())
				if err == nil {
					t.Annotations = &Annotations{}
					err = json.Unmarshal(raw, t.Annotations)
				}
				if err != nil {
					return nil, errors.New("$mcp.addEndpoint: annotations: " + err.Error())
				}
			}
			if fn := def.Get("request").Func(); fn != nil {
				t.Request = func(c *Call) (*Request, error) {
					dflt, err := c.defaultRequest()
					if err != nil {
						return nil, err
					}
					v, err := call(fn, map[string]any{"args": c.Args, "auth": c.Auth(), "request": requestToJS(dflt)})
					if err != nil {
						return nil, err
					}
					if v == nil {
						return dflt, nil
					}
					return requestFromJS(v, dflt)
				}
			}
			if fn := def.Get("response").Func(); fn != nil {
				t.Response = func(c *Call, res *Response) (any, error) {
					var parsed any
					_ = json.Unmarshal(res.Body, &parsed)
					hdr := map[string]any{}
					for k := range res.Header {
						hdr[strings.ToLower(k)] = res.Header.Get(k)
					}
					v, err := call(fn, map[string]any{"args": c.Args, "auth": c.Auth(), "status": res.Status,
						"headers": hdr, "body": string(res.Body), "json": parsed, "truncated": res.Truncated})
					if err != nil {
						return nil, err
					}
					if v == nil {
						return map[string]any{}, nil
					}
					return v, nil
				}
			}
			return nil, addEndpoint(t)
		}),
		"pathValue": func(v string) (string, error) { return PathValue(v) },
		"instructions": func(text string) {
			if s := For(app); s != nil {
				s.AddInstructions(text)
				return
			}
			p := pendingFor(app)
			p.mu.Lock()
			p.instructions = append(p.instructions, text)
			p.mu.Unlock()
		},
		"tools": func() []string {
			names := []string{}
			if s := For(app); s != nil {
				for _, t := range s.Tools() {
					names = append(names, t.Name)
				}
			}
			return names
		},
	})
}

const pendingKey = "mcp.pending"

// pending holds what pb_hooks declared before mcp.Register ran.
type pending struct {
	mu           sync.Mutex
	tools        []Endpoint
	instructions []string
}

func pendingFor(app core.App) *pending {
	app.Store().SetFunc(pendingKey, func(old any) any {
		if p, ok := old.(*pending); ok {
			return p
		}
		return &pending{}
	})
	return app.Store().Get(pendingKey).(*pending)
}

func jsString(v js.Value) string {
	if v == nil || v.Nullish() {
		return ""
	}
	return v.String()
}

func jsObject(v js.Value) (map[string]any, error) {
	if v == nil || v.Nullish() {
		return nil, nil
	}
	raw, err := json.Marshal(v.Export())
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("must be an object")
	}
	return m, nil
}

func requestToJS(r *Request) map[string]any {
	q := map[string]any{}
	for k := range r.Query {
		q[k] = r.Query.Get(k)
	}
	return map[string]any{"method": r.Method, "path": r.Path, "query": q, "body": r.Body}
}

// requestFromJS reads {method, path, query, body, headers, files} over the default request.
func requestFromJS(v any, dflt *Request) (*Request, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("request must return an object")
	}
	out := *dflt
	if x, ok := m["method"].(string); ok && x != "" {
		out.Method = strings.ToUpper(x)
	}
	if x, ok := m["path"].(string); ok && x != "" {
		out.Path = x
	}
	if x, ok := m["query"].(map[string]any); ok {
		out.Query = url.Values{}
		for k, v := range x {
			out.Query.Set(k, queryValue(v))
		}
	}
	if x, ok := m["body"]; ok {
		out.Body = x
	}
	if x, ok := m["headers"].(map[string]any); ok {
		out.Header = http.Header{}
		for k, v := range x {
			out.Header.Set(k, fmt.Sprint(v))
		}
	}
	if x, ok := m["files"].(map[string]any); ok {
		out.Files = map[string][]File{}
		for k, v := range x {
			files, isFile, err := asFiles(normalizeJS(v), DefaultMaxFile)
			if err != nil || !isFile {
				return nil, fmt.Errorf("files.%s: want {name, base64} or a list of them", k)
			}
			out.Files[k] = files
		}
	}
	return &out, nil
}

// normalizeJS turns JS exports (map[string]any, []any, int64 …) into JSON-decoded shapes.
func normalizeJS(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}
