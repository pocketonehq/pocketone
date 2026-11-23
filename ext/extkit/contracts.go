package extkit

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// ContractID names a contract and its major version: "deploy/v1", "mail.fallback/v1".
type ContractID string

// Cardinality says how many providers of a contract one scope may have.
type Cardinality int

const (
	// One: at most one provider. Two non-default providers is a seal error unless a choice picks one.
	One Cardinality = iota
	// Many: any number; consumers get them all, in order.
	Many
	// Keyed: at most one provider per key (a CLI command path, an MCP tool name).
	Keyed
)

func (c Cardinality) String() string {
	return [...]string{"One", "Many", "Keyed"}[c]
}

// Contract is a typed key for the interface T. Declare one per contract, in the contract's package:
//
//	var Contract = extkit.NewContract[Sender]("mail.fallback/v1", extkit.One)
type Contract[T any] struct {
	id   ContractID
	card Cardinality
	key  func(T) string // Keyed only
	// HostProvidable: a host may provide it for its instances (ProvideForInstances).
	hostProvidable bool
	// guard wraps the sealed value as the outermost layer (wire contracts: scope check + action hooks).
	guard func(owner any, inner T) T
}

// NewContract declares a One or Many contract.
func NewContract[T any](id ContractID, card Cardinality) *Contract[T] {
	if card == Keyed {
		panic("extkit: use NewKeyedContract for keyed contracts")
	}
	return &Contract[T]{id: id, card: card}
}

// NewKeyedContract declares a Keyed contract; key extracts each provided value's key.
func NewKeyedContract[T any](id ContractID, key func(T) string) *Contract[T] {
	return &Contract[T]{id: id, card: Keyed, key: key}
}

// HostProvidable marks the contract as one a multisite host may provide for its instances.
func (c *Contract[T]) HostProvidable() *Contract[T] { c.hostProvidable = true; return c }

// ID is the contract's id.
func (c *Contract[T]) ID() ContractID { return c.id }

// Provide registers impl as a provider on app's scope. Call it from an extension's Register.
func (c *Contract[T]) Provide(app core.App, impl T) {
	c.provide(scopeOf(app), impl, false)
	autoSeal(app)
}

// ProvideDefault registers impl as a default: it steps aside when any other provider exists.
func (c *Contract[T]) ProvideDefault(app core.App, impl T) {
	c.provide(scopeOf(app), impl, true)
	autoSeal(app)
}

// lazySeal seals an app that has bootstrapped but that nobody sealed (extensions registered after bootstrap, as host
// code may do, or none providing anything), on its first lookup. A lookup before bootstrap, in Register, still panics.
func lazySeal(app core.App, s *Scope) {
	s.mu.Lock()
	registering := s.current != "" && s.current != "pb_hooks" // inside an extension's Register: let it panic
	nothing := len(s.slots) == 0 && s.host == nil             // nobody provided anything: every lookup is "not provided"
	need := !s.sealed && (s.autoSealBound || nothing) && !registering
	s.mu.Unlock()
	if !need || !app.IsBootstrapped() {
		return
	}
	s.Registering("")
	if err := runJSDeclarations(app); err != nil {
		app.Logger().Error(err.Error())
		return
	}
	if err := s.Seal(nil); err != nil {
		app.Logger().Error(err.Error())
		return
	}
	if err := checkJSDeclarations(app, s); err != nil {
		app.Logger().Error(err.Error())
	}
	s.mu.Lock()
	s.autoSealed = true
	s.mu.Unlock()
}

// autoSeal makes an app whose contracts nobody sealed (extensions registered one by one with their MustRegister,
// not through the standard set or Setup) seal them, with defaults and no providers: choices, once it has bootstrapped
// (or starts serving), and mount their routes on every serve.
func autoSeal(app core.App) {
	s := scopeOf(app)
	s.mu.Lock()
	bound := s.autoSealBound
	s.autoSealBound = true
	s.mu.Unlock()
	if bound {
		return
	}
	seal := func() (auto bool, err error) {
		s.mu.Lock()
		sealed, auto := s.sealed, s.autoSealed
		s.mu.Unlock()
		if sealed {
			return auto, nil
		}
		s.Registering("")
		if err := runJSDeclarations(app); err != nil {
			return false, err
		}
		if err := s.Seal(nil); err != nil {
			return false, err
		}
		if err := checkJSDeclarations(app, s); err != nil {
			return false, err
		}
		s.mu.Lock()
		s.autoSealed = true
		s.mu.Unlock()
		return true, nil
	}
	// after bootstrap: contracts work for commands and cron too, not only once the app serves
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		_, err := seal()
		return err
	})
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		auto, err := seal()
		if err != nil {
			return err
		}
		if auto { // every serve builds a new router (a restart serves again): mount each time
			for _, w := range providedWires(app) {
				w.Mount(app, se)
			}
			mountUIs(app, se)
		}
		return se.Next()
	})
}

func (c *Contract[T]) provide(s *Scope, impl T, isDefault bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustNotBeSealed("Provide", c.id)
	sl := s.slot(c.id, c.card)
	p := provision{ext: s.current, value: impl, isDefault: isDefault}
	if c.card == Keyed {
		p.key = c.key(impl)
	}
	sl.provisions = append(sl.provisions, p)
}

// Wrap decorates whatever provides the contract on app (One), or every provider (Many). Wrappers apply at the seal,
// in registration order: the first one registered is innermost.
func (c *Contract[T]) Wrap(app core.App, wrap func(inner T) T) {
	c.WrapKey(app, "", wrap)
}

// WrapKey decorates the provider of one key (Keyed). An empty key means every provider.
func (c *Contract[T]) WrapKey(app core.App, key string, wrap func(inner T) T) {
	s := scopeOf(app)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustNotBeSealed("Wrap", c.id)
	sl := s.slot(c.id, c.card)
	sl.wrappers = append(sl.wrappers, wrapper{ext: s.current, key: key, fn: func(v any) any { return wrap(v.(T)) }})
}

// Of returns the provider (One), after the seal. ok is false when nothing provides it.
func (c *Contract[T]) Of(app core.App) (T, bool) {
	var zero T
	s := scopeOf(app)
	lazySeal(app, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("Of", c.id)
	sl := s.slots[c.id]
	if sl == nil || len(sl.sealed) == 0 {
		return zero, false
	}
	return c.outer(app, sl.sealed[0].value.(T)), true
}

// Must is Of for a contract the caller Requires: the resolver guarantees a provider, so a missing one is a bug.
func (c *Contract[T]) Must(app core.App) T {
	v, ok := c.Of(app)
	if !ok {
		panic(fmt.Sprintf("extkit: %s has no provider on this app (declare it in Requires)", c.id))
	}
	return v
}

// All returns every provider (Many, Keyed), after the seal, in order.
func (c *Contract[T]) All(app core.App) []T {
	s := scopeOf(app)
	lazySeal(app, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("All", c.id)
	sl := s.slots[c.id]
	if sl == nil {
		return nil
	}
	out := make([]T, 0, len(sl.sealed))
	for _, p := range sl.sealed {
		out = append(out, c.outer(app, p.value.(T)))
	}
	return out
}

// Get returns the provider of one key (Keyed), after the seal.
func (c *Contract[T]) Get(app core.App, key string) (T, bool) {
	var zero T
	s := scopeOf(app)
	lazySeal(app, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("Get", c.id)
	if sl := s.slots[c.id]; sl != nil {
		for _, p := range sl.sealed {
			if p.key == key {
				return c.outer(app, p.value.(T)), true
			}
		}
	}
	return zero, false
}

// Provider names the extension that provides the contract on app (One), after the seal: "pb_hooks" for the app's
// own JS, "host" when its host provides it, "" for Go code outside any extension. ok is false when nothing does.
func (c *Contract[T]) Provider(app core.App) (name string, ok bool) {
	s := scopeOf(app)
	lazySeal(app, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("Provider", c.id)
	sl := s.slots[c.id]
	if sl == nil || len(sl.sealed) == 0 {
		return "", false
	}
	if sl.sealed[0].ext == "" && s.host != nil {
		return "host", true
	}
	return sl.sealed[0].ext, true
}

func (c *Contract[T]) outer(app core.App, v T) T {
	if c.guard != nil {
		return c.guard(app, v)
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------
// Scope: one app's (or one host's) contracts.

// Scope holds the contracts provided in one scope: an app, or a multisite host.
type Scope struct {
	mu      sync.Mutex
	name    string // "standard", or "standard[<instance>]", or "multisite"
	slots   map[ContractID]*slot
	order   []ContractID
	sealed  bool
	current string // the extension whose Register is running

	// host and instance: set when the app is an instance of a host (AttachHost).
	host     *Scope
	instance string
	// cacheRoot: a host's own pb_cache folder (SetCacheRoot), for its instances' process-wide caches.
	cacheRoot string
	// forInstances: contracts this scope (a host) provides for its instances.
	forInstances map[ContractID]instanceProvision
	// Lines: what the seal decided, one per contract; the startup line is built from them.
	Lines []string

	autoSealBound bool
	autoSealed    bool // sealed by autoSeal: it mounts the routes on every serve
}

type slot struct {
	id         ContractID
	card       Cardinality
	provisions []provision
	wrappers   []wrapper
	sealed     []provision
}

type provision struct {
	ext       string
	key       string
	value     any
	isDefault bool
}

type wrapper struct {
	ext string
	key string
	fn  func(any) any
}

type instanceProvision struct {
	ext     string
	factory func(instance string) any
}

const scopeKey = "extkit.contracts"

// NewScope returns an empty scope, for a host (an app gets its own from its store).
func NewScope(name string) *Scope {
	return &Scope{name: name, slots: map[ContractID]*slot{}}
}

func scopeOf(app core.App) *Scope {
	st := app.Store()
	return st.GetOrSet(scopeKey, func() any { return NewScope("standard") }).(*Scope)
}

// AppScope returns app's scope (for hosts and tests).
func AppScope(app core.App) *Scope { return scopeOf(app) }

func (s *Scope) slot(id ContractID, card Cardinality) *slot {
	sl := s.slots[id]
	if sl == nil {
		sl = &slot{id: id, card: card}
		s.slots[id] = sl
		s.order = append(s.order, id)
	}
	return sl
}

func (s *Scope) mustNotBeSealed(op string, id ContractID) {
	if s.sealed {
		panic(fmt.Sprintf("extkit: %s(%s) after the seal; providers and wrappers are registered in Register", op, id))
	}
}

func (s *Scope) mustBeSealed(op string, id ContractID) {
	if !s.sealed {
		panic(fmt.Sprintf("extkit: %s(%s) before the seal; look contracts up in hooks or at serve time, not in Register", op, id))
	}
}

// Registering marks ext as the extension whose Register is running, so its Provide calls are attributed to it. It
// returns the extension marked before, for nested registration to restore.
func (s *Scope) Registering(ext string) (prev string) {
	s.mu.Lock()
	prev, s.current = s.current, ext
	s.mu.Unlock()
	return prev
}

// Choices are a `providers:` section: for each contract, an extension name (One), a list (Many), a map of key to name
// (Keyed), or "off".
type Choices map[ContractID]any

// Seal resolves every contract in the scope: host-provided ones first, then choices, defaults stepping aside, conflicts,
// wrappers. After it, Of/All/Get work and Provide/Wrap panic.
func (s *Scope) Seal(choices Choices) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return fmt.Errorf("extkit: %s sealed twice", s.name)
	}
	var errs []string
	// contracts the host provides for this instance: closed to the app
	if s.host != nil {
		for id, ip := range s.host.forInstances {
			sl := s.slot(id, One)
			if n := len(sl.provisions); n > 0 {
				s.Lines = append(s.Lines, fmt.Sprintf("%s ← host (%s); stepped aside in the app: %s", id, ip.ext, names(sl.provisions)))
			} else {
				s.Lines = append(s.Lines, fmt.Sprintf("%s ← host (%s)", id, ip.ext))
			}
			sl.provisions = []provision{{ext: "host:" + ip.ext, value: ip.factory(s.instance)}}
		}
	}
	for _, id := range s.order {
		sl := s.slots[id]
		chosen, line, err := resolveSlot(sl, choices[id])
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		hostLine := strings.HasPrefix(firstExt(chosen), "host:")
		for i := range chosen {
			orig := chosen[i].ext
			for _, w := range sl.wrappers {
				if w.key == "" || w.key == chosen[i].key {
					chosen[i].value = w.fn(chosen[i].value)
					chosen[i].ext = w.ext + "(" + chosen[i].ext + ")"
				}
			}
			if orig != chosen[i].ext {
				line = strings.Replace(line, "← "+orig, "← "+chosen[i].ext, 1)
			}
		}
		if line != "" && !hostLine {
			s.Lines = append(s.Lines, line)
		}
		sl.sealed = chosen
	}
	for id := range choices {
		if _, ok := s.slots[id]; !ok {
			errs = append(errs, fmt.Sprintf("providers: %s: no extension here provides or uses it (a typo?)", id))
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("extkit: %s: %s", s.name, strings.Join(errs, "; "))
	}
	s.sealed = true
	return nil
}

func firstExt(ps []provision) string {
	if len(ps) == 0 {
		return ""
	}
	return ps[0].ext
}

func names(ps []provision) string {
	n := make([]string, 0, len(ps))
	for _, p := range ps {
		n = append(n, p.ext)
	}
	return strings.Join(n, ", ")
}

// resolveSlot picks the providers of one contract.
func resolveSlot(sl *slot, choice any) ([]provision, string, error) {
	ps := slices.Clone(sl.provisions)
	if choice == "off" {
		return nil, fmt.Sprintf("%s ← none (providers: off)", sl.id), nil
	}
	pick := func(name string) (provision, bool) {
		for _, p := range ps {
			if p.ext == name {
				return p, true
			}
		}
		return provision{}, false
	}
	switch sl.card {
	case One:
		if name, ok := choice.(string); ok {
			p, found := pick(name)
			if !found {
				return nil, "", fmt.Errorf("providers: %s: %s doesn't provide it here (providers: %s)", sl.id, name, names(ps))
			}
			return []provision{p}, fmt.Sprintf("%s ← %s (providers:)", sl.id, name), nil
		}
		nonDefault := slices.DeleteFunc(slices.Clone(ps), func(p provision) bool { return p.isDefault })
		switch {
		case len(ps) == 0:
			return nil, "", nil
		case len(nonDefault) == 1:
			line := fmt.Sprintf("%s ← %s", sl.id, nonDefault[0].ext)
			if len(ps) > 1 {
				line += fmt.Sprintf(" (default %s stepped aside)", ps[slices.IndexFunc(ps, func(p provision) bool { return p.isDefault })].ext)
			}
			return nonDefault, line, nil
		case len(nonDefault) == 0 && len(ps) == 1:
			return ps, fmt.Sprintf("%s ← %s (default)", sl.id, ps[0].ext), nil
		case len(nonDefault) == 0:
			return nil, "", fmt.Errorf("%s has %d defaults (%s); only one extension may provide the default", sl.id, len(ps), names(ps))
		default:
			return nil, "", fmt.Errorf("conflict: %s has %d providers (%s); pick one under providers: in pocketone.yaml", sl.id, len(nonDefault), names(nonDefault))
		}
	case Many:
		if list, ok := asList(choice); ok {
			var out []provision
			for _, name := range list {
				p, found := pick(name)
				if !found {
					return nil, "", fmt.Errorf("providers: %s: %s doesn't provide it here (providers: %s)", sl.id, name, names(ps))
				}
				out = append(out, p)
			}
			return out, fmt.Sprintf("%s ← %s (providers:)", sl.id, names(out)), nil
		}
		if len(ps) == 0 {
			return nil, "", nil
		}
		// defaults step aside for any other provider here too: the chain is the others
		nonDefault := slices.DeleteFunc(slices.Clone(ps), func(p provision) bool { return p.isDefault })
		if len(nonDefault) > 0 && len(nonDefault) < len(ps) {
			var aside []provision
			for _, p := range ps {
				if p.isDefault {
					aside = append(aside, p)
				}
			}
			return nonDefault, fmt.Sprintf("%s ← %s (default %s stepped aside)", sl.id, names(nonDefault), names(aside)), nil
		}
		return ps, fmt.Sprintf("%s ← %s", sl.id, names(ps)), nil
	case Keyed:
		byKey := map[string][]provision{}
		var keys []string
		for _, p := range ps {
			if _, seen := byKey[p.key]; !seen {
				keys = append(keys, p.key)
			}
			byKey[p.key] = append(byKey[p.key], p)
		}
		picks, _ := choice.(map[string]string)
		var out []provision
		var notes []string
		for _, k := range keys {
			sub := &slot{id: ContractID(fmt.Sprintf("%s[%s]", sl.id, k)), card: One, provisions: byKey[k]}
			var c any
			if name, ok := picks[k]; ok {
				c = name
			}
			chosen, line, err := resolveSlot(sub, c)
			if err != nil {
				return nil, "", err
			}
			out = append(out, chosen...)
			if strings.Contains(line, "(") { // only interesting keys: chosen, or a default stepped aside
				notes = append(notes, strings.TrimPrefix(line, string(sl.id)))
			}
		}
		if len(out) == 0 {
			return nil, "", nil
		}
		line := fmt.Sprintf("%s ← %d keys", sl.id, len(out))
		if len(notes) > 0 {
			line += ": " + strings.Join(notes, ", ")
		}
		return out, line, nil
	}
	return nil, "", fmt.Errorf("%s: unknown cardinality", sl.id)
}

func asList(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------------------------------------------
// Host-provided contracts.

// ProvideForInstances registers, on a host scope, a provider for each of the host's instances. The host answers the
// contract for every instance (whatever runtime runs it), and inside an instance's app the contract resolves to the
// host's provider for that instance: closed to the app.
func (c *Contract[T]) ProvideForInstances(host *Scope, factory func(instance string) T) {
	if !c.hostProvidable {
		panic(fmt.Sprintf("extkit: %s isn't host-providable", c.id))
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.forInstances == nil {
		host.forInstances = map[ContractID]instanceProvision{}
	}
	host.forInstances[c.id] = instanceProvision{ext: host.current, factory: func(i string) any { return factory(i) }}
}

// ForInstance returns the host's provider of c for one instance, for the host's own HTTP handler.
func (c *Contract[T]) ForInstance(host *Scope, instance string) (T, bool) {
	var zero T
	host.mu.Lock()
	ip, ok := host.forInstances[c.id]
	host.mu.Unlock()
	if !ok {
		return zero, false
	}
	return ip.factory(instance).(T), true
}

// HostProvided lists the contracts this scope (a host) provides for its instances.
func (s *Scope) HostProvided() []ContractID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ContractID, 0, len(s.forInstances))
	for id := range s.forInstances {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// HostProvides reports whether host provides id for its instances.
func (s *Scope) HostProvides(id ContractID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.forInstances[id]
	return ok
}

// AttachHost makes app an instance of host: contracts the host provides for its instances resolve to the host's provider
// in app's scope. Call it before registering extensions on app.
func AttachHost(app core.App, host *Scope, instance string) {
	s := scopeOf(app)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.host, s.instance = host, instance
	s.name = "standard[" + instance + "]"
}

// ---------------------------------------------------------------------------------------------------------------
// Scope-level use: a multisite host's own contracts (runtime/v1, gate/v1, ...) live on a Scope, not an app.

// ProvideIn registers impl as a provider on scope s (a host's), attributed to the extension s is registering.
func (c *Contract[T]) ProvideIn(s *Scope, impl T) { c.provide(s, impl, false) }

// ProvideDefaultIn registers impl as a default on scope s: it steps aside when any other provider exists.
func (c *Contract[T]) ProvideDefaultIn(s *Scope, impl T) { c.provide(s, impl, true) }

// In returns the provider (One) on scope s, after its seal.
func (c *Contract[T]) In(s *Scope) (T, bool) {
	var zero T
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("In", c.id)
	sl := s.slots[c.id]
	if sl == nil || len(sl.sealed) == 0 {
		return zero, false
	}
	return c.guardFor(s, sl.sealed[0].value.(T)), true
}

// AllIn returns every provider (Many) on scope s, after its seal, in order.
func (c *Contract[T]) AllIn(s *Scope) []T {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("AllIn", c.id)
	sl := s.slots[c.id]
	if sl == nil {
		return nil
	}
	out := make([]T, 0, len(sl.sealed))
	for _, p := range sl.sealed {
		out = append(out, c.guardFor(s, p.value.(T)))
	}
	return out
}

// ProvidersIn names the extensions providing the contract on scope s, after its seal, in order.
func (c *Contract[T]) ProvidersIn(s *Scope) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mustBeSealed("ProvidersIn", c.id)
	var out []string
	if sl := s.slots[c.id]; sl != nil {
		for _, p := range sl.sealed {
			out = append(out, p.ext)
		}
	}
	return out
}

// HostProvides reports whether the host app runs under (see [AttachHost]) provides id for its instances: the
// capability is hoisted to the host, so the app's own provider steps aside. An extension's app half checks it in
// Register to leave out its user-facing parts too (a settings section, a schedule): on a host that owns the
// capability, the operator decides, not the app's admins. A single app has no host: always false.
func HostProvides(app core.App, id ContractID) bool {
	s := scopeOf(app)
	s.mu.Lock()
	host := s.host
	s.mu.Unlock()
	return host != nil && host.HostProvides(id)
}
