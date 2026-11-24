// Package remotes gives the command named remotes: other PocketBase instances (or PocketHost instances) a project
// deploys to, signed in once with `pocketone login`.
//
//	pocketone remote add prod https://myapp.example.com
//	pocketone login prod
//	pocketone deploy prod
//
// Remote names and URLs live in the project's .pocketbase/remotes.json (commit it); secrets live in the user's
// ~/.config/pocketone/credentials.json (0600), keyed by URL. P1_TOKEN overrides the saved login (CI sets it). The folder is
// named after [extkit.Command] (~/.config/pockethost in that build); the variables are PocketOne's (P1_) in every
// build.
//
// The server half is remote/v1's whoami (GET /api/_c/remote/v1): who the request's token is, its scopes, and which
// contracts the app offers (deploy/v1, oauth/v1, api-tokens/v1, ...), so clients discover what a remote supports.
package remoteclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"pocketone.dev/ext/extkit"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// ProjectDir holds the project's remotes file. Not a pb_* name: pb_* folders are deployed.
	ProjectDir  = ".pocketbase"
	ProjectFile = "remotes.json"

	// DefaultName is the remote used when a command names none and the project sets no default.
	DefaultName = "default"
)

// Remote is one named remote.
type Remote struct {
	Name string `json:"-"`
	URL  string `json:"url"`
}

// Project is a project's remotes file.
type Project struct {
	Default string             `json:"default,omitempty"`
	Remotes map[string]*Remote `json:"remotes"`

	// Root is the project folder (where .pocketbase/ is, or will be created).
	Root string `json:"-"`
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// ValidName reports whether s can name a remote.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// FindProjectRoot walks up from dir to the nearest folder with .pocketbase/remotes.json. None: dir itself.
func FindProjectRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; {
		if exists(filepath.Join(d, ProjectDir, ProjectFile)) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs
		}
		d = parent
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LoadProject reads the remotes file of the project containing dir (see [FindProjectRoot]). A project with no file
// has no remotes.
func LoadProject(dir string) (*Project, error) {
	root := FindProjectRoot(dir)
	p := &Project{Root: root, Remotes: map[string]*Remote{}}
	b, err := os.ReadFile(filepath.Join(root, ProjectDir, ProjectFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(b, p); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(ProjectDir, ProjectFile), err)
		}
		if p.Remotes == nil {
			p.Remotes = map[string]*Remote{}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	for name, r := range p.Remotes {
		r.Name = name
	}
	return p, nil
}

// Save writes the remotes file.
func (p *Project) Save() error {
	out := Project{Default: p.Default, Remotes: map[string]*Remote{}}
	for name, r := range p.Remotes {
		out.Remotes[name] = r
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(p.Root, ProjectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ProjectFile), append(b, '\n'), 0o644)
}

// Names returns the remote names, sorted.
func (p *Project) Names() []string {
	out := make([]string, 0, len(p.Remotes))
	for n := range p.Remotes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Add adds or replaces a remote. The first remote becomes the default.
func (p *Project) Add(name, rawURL string) (*Remote, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid remote name %q (letters, digits, _ . -)", name)
	}
	u, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	r := &Remote{Name: name, URL: u}
	p.Remotes[name] = r
	if p.Default == "" {
		p.Default = name
	}
	return r, nil
}

// Remove deletes a remote.
func (p *Project) Remove(name string) error {
	if _, ok := p.Remotes[name]; !ok {
		return fmt.Errorf("no remote %q", name)
	}
	delete(p.Remotes, name)
	if p.Default == name {
		p.Default = ""
	}
	return nil
}

// Resolve picks a remote: by name, else the project default, else the only remote, else "default". A URL works as a
// name too (an unnamed remote).
func (p *Project) Resolve(name string) (*Remote, error) {
	if name == "" {
		name = p.Default
	}
	if name == "" {
		if len(p.Remotes) == 1 {
			for _, r := range p.Remotes {
				return r, nil
			}
		}
		name = DefaultName
	}
	if r, ok := p.Remotes[name]; ok {
		return r, nil
	}
	if strings.Contains(name, "://") {
		u, err := NormalizeURL(name)
		if err != nil {
			return nil, err
		}
		return &Remote{Name: u, URL: u}, nil
	}
	if len(p.Remotes) == 0 {
		return nil, fmt.Errorf("no remotes yet: run '%[1]s remote add %[2]s <url>' or '%[1]s link <url>'", extkit.Command, name)
	}
	return nil, fmt.Errorf("no remote %q (have: %s)", name, strings.Join(p.Names(), ", "))
}

// NormalizeURL checks an instance URL and trims it to scheme://host[:port][/path] without a trailing slash. A bare
// host gets https://.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return strings.TrimRight(u.String(), "/"), nil
}

// ---------------------------------------------------------------------------------------------------------------
// credentials

// Credential is a saved sign-in for one URL.
type Credential struct {
	Token string `json:"token"`
	// Via: "token" (a pasted pbt_ or PocketHost token), "jwt" (a pasted PocketBase auth token), "oauth".
	Via          string    `json:"via"`
	Email        string    `json:"email,omitempty"`
	Collection   string    `json:"collection,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expires      time.Time `json:"expires,omitzero"`
	// TokenEndpoint and RevokeEndpoint (OAuth logins) refresh and revoke it.
	TokenEndpoint  string    `json:"tokenEndpoint,omitempty"`
	RevokeEndpoint string    `json:"revokeEndpoint,omitempty"`
	Created        time.Time `json:"created"`
}

// Credentials is the user's credential store.
type Credentials struct {
	path  string
	ByURL map[string]*Credential `json:"credentials"`
}

// EnvVar is PocketOne's environment variable for suffix ([extkit.EnvVar]): EnvVar("TOKEN") is P1_TOKEN.
// in pocketone.
func EnvVar(suffix string) string { return extkit.EnvVar(suffix) }

// ConfigDir is the user's config folder, named after [extkit.Command]: $P1_CONFIG_DIR, else
// $XDG_CONFIG_HOME/pocketone, else ~/.config/pocketone.
func ConfigDir() (string, error) {
	if d := os.Getenv(EnvVar("CONFIG_DIR")); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, extkit.Command), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", extkit.Command), nil
}

// LoadCredentials reads the credential store (empty if there's none).
func LoadCredentials() (*Credentials, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	c := &Credentials{path: filepath.Join(dir, "credentials.json"), ByURL: map[string]*Credential{}}
	b, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path, err)
	}
	if c.ByURL == nil {
		c.ByURL = map[string]*Credential{}
	}
	return c, nil
}

// Save writes the store with 0600 permissions.
func (c *Credentials) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path is the store's file.
func (c *Credentials) Path() string { return c.path }

// EnvToken returns the token in P1_TOKEN, which overrides the saved login for whatever remote a command uses, and
// the variable's name as its source. Set it for one command (`P1_TOKEN=... pocketone deploy staging`) to use another
// token for that command only.
func EnvToken() (token, source string) {
	key := EnvVar("TOKEN")
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v, key
	}
	return "", ""
}
