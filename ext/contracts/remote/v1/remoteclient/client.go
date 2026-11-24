// Package remoteclient is remote/v1's CLI client: the remotes a project links (.pocketone/remotes.yaml), saved logins,
// sign-in (an API token, a password, or OAuth in the browser or with a device code), and an authenticated HTTP client
// for a remote. ext/remotes' commands and other extensions' commands (deploy) build on it.
package remoteclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"pocketone.dev/ext/extkit"
	"strconv"
	"strings"
	"time"
)

// UserAgent is sent with every client request.
var UserAgent = "pocketone-cli"

// WhoamiPath is remote/v1's whoami (public): who the caller is and what the remote offers.
const WhoamiPath = "/api/_c/remote/v1"

// NewHTTPClient returns the HTTP client remote commands use: short connect and TLS timeouts, no overall timeout
// (uploads and log streams run long; callers pass contexts).
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute, // a commit may restart the instance
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}}
}

// Client talks to one remote with its credential. Get one with [Open], or build it directly.
type Client struct {
	Remote *Remote
	// Token is sent as "Authorization: Bearer <token>" (empty: anonymous).
	Token string
	// TokenSource says where Token came from, for messages ("P1_TOKEN", "saved login", ...).
	TokenSource string
	HTTP        *http.Client
}

// ErrNotLoggedIn is returned by [Open] when there's no credential for the remote.
var ErrNotLoggedIn = errors.New("not logged in")

// Open resolves a remote of the project containing dir (name may be empty or a URL, see [Project.Resolve]) and its
// credential: the CI environment first ([EnvToken]), then the saved login. With no credential it returns the
// client and an error wrapping [ErrNotLoggedIn].
func Open(dir, name string) (*Client, error) {
	p, err := LoadProject(dir)
	if err != nil {
		return nil, err
	}
	r, err := p.Resolve(name)
	if err != nil {
		return nil, err
	}
	c := &Client{Remote: r, HTTP: NewHTTPClient()}
	if tok, src := EnvToken(); tok != "" {
		c.Token, c.TokenSource = tok, src
		return c, nil
	}
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	if cr := creds.ByURL[r.URL]; cr != nil && cr.Token != "" {
		c.Token, c.TokenSource = cr.Token, "saved login ("+cr.Via+")"
		// an OAuth login renews itself a day before it expires
		if cr.RefreshToken != "" && cr.TokenEndpoint != "" && !cr.Expires.IsZero() && time.Until(cr.Expires) < 24*time.Hour {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := refreshOAuth(ctx, c, creds, cr)
			cancel()
			if err == nil {
				c.Token = cr.Token
			}
		}
		if !cr.Expires.IsZero() && time.Now().After(cr.Expires) {
			return c, fmt.Errorf("the saved login for %s expired: run '%s login %s': %w", r.URL, extkit.Command, r.Name, ErrNotLoggedIn)
		}
		return c, nil
	}
	return c, fmt.Errorf("%s (%s): run '%s login %s', or set %s: %w", r.Name, r.URL, extkit.Command, loginArg(r), EnvVar("TOKEN"), ErrNotLoggedIn)
}

func loginArg(r *Remote) string {
	if r.Name == r.URL {
		return r.URL
	}
	return r.Name
}

// URL joins the remote's base URL and path.
func (c *Client) URL(path string) string {
	return c.Remote.URL + "/" + strings.TrimLeft(path, "/")
}

// NewRequest builds an authenticated request to path on the remote.
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", UserAgent)
	return req, nil
}

// Do sends a request with the client's HTTP client.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	h := c.HTTP
	if h == nil {
		h = NewHTTPClient()
		c.HTTP = h
	}
	for attempt := 0; ; attempt++ {
		res, err := h.Do(req)
		if err != nil {
			return nil, &APIError{Status: 0, Message: fmt.Sprintf("could not reach %s: %v", c.Remote.URL, err)}
		}
		// rate limited (by the app, or a proxy in front of it): wait as long as it says and try again, a few times,
		// when the request can be sent again
		if res.StatusCode != http.StatusTooManyRequests || attempt >= maxRateLimitRetries || (req.Body != nil && req.GetBody == nil) {
			return res, nil
		}
		wait := retryAfter(res.Header.Get("Retry-After"), attempt)
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if wait > maxRateLimitWait {
			return nil, &APIError{Status: http.StatusTooManyRequests, Message: fmt.Sprintf("%s is rate limiting requests; try again in %s", c.Remote.URL, wait.Round(time.Second))}
		}
		if err := RetrySleep(req.Context(), wait); err != nil {
			return nil, err
		}
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
	}
}

const (
	maxRateLimitRetries = 4
	maxRateLimitWait    = 60 * time.Second
)

// RetrySleep waits d before a rate-limited request is sent again, or until ctx ends (overridable for tests).
var RetrySleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads a Retry-After header (seconds or an HTTP date); without one, 1, 2, 4, 8 s by attempt.
func retryAfter(v string, attempt int) time.Duration {
	if v = strings.TrimSpace(v); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(time.Until(t), 0)
		}
	}
	return time.Second << attempt
}

// APIError is a non-2xx answer (Status 0: the remote couldn't be reached).
type APIError struct {
	Status  int
	Message string
	Body    map[string]any
}

func (e *APIError) Error() string { return e.Message }

// IsStatus reports whether err is an [APIError] with this status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// JSON sends in (nil: no body; []byte: raw octet stream) and decodes a 2xx answer into out (may be nil).
func (c *Client) JSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	ctype := ""
	switch v := in.(type) {
	case nil:
	case []byte:
		body, ctype = bytes.NewReader(v), "application/octet-stream"
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	req, err := c.NewRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return DecodeResponse(res, out)
}

// DecodeResponse turns a non-2xx response into an [APIError] (PocketBase's {message} or the deploy API's), else
// decodes JSON into out.
func DecodeResponse(res *http.Response, out any) error {
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		ae := &APIError{Status: res.StatusCode, Body: map[string]any{}}
		if json.Unmarshal(b, &ae.Body) == nil {
			ae.Message, _ = ae.Body["message"].(string)
		} else if s := strings.TrimSpace(string(b)); s != "" {
			ae.Message = clip(s, 300)
		}
		if ae.Message == "" {
			ae.Message = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return ae
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Whoami is remote/v1's whoami (GET /api/_c/remote/v1).
type Whoami struct {
	Auth *struct {
		Collection string `json:"collection"`
		Id         string `json:"id"`
		Email      string `json:"email"`
	} `json:"auth"`
	// Via: "token" (API token), "jwt", or "" (anonymous).
	Via     string   `json:"via"`
	Scopes  []string `json:"scopes"`
	TokenId string   `json:"tokenId"`
	// Contracts are the wire contracts the remote offers ("remote/v1", "deploy/v1"), each at /api/_c/<id>/.
	Contracts []string `json:"contracts"`
	App       struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"app"`
}

// Offers reports whether the remote offers a contract ("deploy/v1"): its API is at /api/_c/<id>/.
func (w *Whoami) Offers(id string) bool {
	for _, c := range w.Contracts {
		if c == id {
			return true
		}
	}
	return false
}

// ErrNoWhoami: the remote doesn't offer remote/v1 (no remotes extension, e.g. a PocketHost edge today).
var ErrNoWhoami = errors.New("the remote doesn't offer remote/v1")

// Whoami asks the remote who this client is and what it offers.
func (c *Client) Whoami(ctx context.Context) (*Whoami, error) {
	var w Whoami
	err := c.JSON(ctx, http.MethodGet, WhoamiPath, nil, &w)
	if IsStatus(err, http.StatusNotFound) {
		return nil, ErrNoWhoami
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}
