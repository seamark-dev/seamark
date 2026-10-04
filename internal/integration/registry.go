package integration

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrUnknownClient reports a client ID the registry does not hold.
var ErrUnknownClient = errors.New("unknown client")

// ErrUnsupported reports an operation a client's descriptor does not
// declare. Callers must treat it as "not applicable", never as a
// broken installation.
var ErrUnsupported = errors.New("unsupported by client")

// clientIDPattern bounds identifiers to what flags, config keys, and
// file names accept without quoting.
var clientIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Registry is the immutable, ordered set of known clients. Order is
// the registration order, which is the presentation order everywhere:
// help text, selection, and diagnostics.
type Registry struct {
	ordered []Client
	byID    map[string]int
}

// NewRegistry validates the descriptors and builds a registry. It
// rejects an invalid or duplicate ID, an empty name, a skill directory
// that is not a clean path inside the repository, and a setup operation
// declared without a SetupAdapter. It stores a copy of each descriptor,
// so a later change to the caller's slices cannot reach the registry.
func NewRegistry(clients ...Client) (*Registry, error) {
	r := &Registry{byID: make(map[string]int, len(clients))}

	for _, c := range clients {
		if err := validateClient(c); err != nil {
			return nil, err
		}

		if _, dup := r.byID[c.ID]; dup {
			return nil, fmt.Errorf("client %q registered twice", c.ID)
		}

		r.byID[c.ID] = len(r.ordered)
		r.ordered = append(r.ordered, c.clone())
	}

	return r, nil
}

// validateClient checks one descriptor's invariants.
func validateClient(c Client) error {
	if !clientIDPattern.MatchString(c.ID) {
		return fmt.Errorf("client id %q: must match %s", c.ID, clientIDPattern)
	}

	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("client %q: name is empty", c.ID)
	}

	for _, dir := range c.SkillDirs {
		if !isCleanRel(dir) {
			return fmt.Errorf("client %q: skill dir %q is not a clean repository-relative path", c.ID, dir)
		}
	}

	// The firing log stores the mechanism name, so a bad name must fail
	// here and not in a log that a user already has.
	if c.Edits != nil && !clientIDPattern.MatchString(c.Edits.AdviceMechanism()) {
		return fmt.Errorf("client %q: advice mechanism %q: must match %s",
			c.ID, c.Edits.AdviceMechanism(), clientIDPattern)
	}

	if c.SetupOps.GateHook && !c.SetupOps.Hooks {
		return fmt.Errorf("client %q: a gate hook is declared without hook installation", c.ID)
	}

	if c.SetupOps.ResetHook && !c.SetupOps.Hooks {
		return fmt.Errorf("client %q: a reset hook is declared without hook installation", c.ID)
	}

	// A declared operation without an adapter is a broken descriptor, not
	// an unsupported operation: nothing could plan the declared work.
	if c.Setup == nil && c.SetupOps != (SetupSupport{}) {
		return fmt.Errorf("client %q: setup operations declared without a setup adapter", c.ID)
	}

	return nil
}

// cloneClients copies descriptors so the result shares no skill
// directory backing array with the registry.
func cloneClients(clients []Client) []Client {
	out := make([]Client, 0, len(clients))

	for _, c := range clients {
		out = append(out, c.clone())
	}

	return out
}

// Builtin returns the registry of shipped clients in stable order.
// The descriptors are constants checked by the contract tests, so a
// validation failure here is a programming error, not a runtime state.
func Builtin() *Registry {
	r, err := NewRegistry(claudeClient(), codexClient())
	if err != nil {
		panic("integration: built-in registry is invalid: " + err.Error())
	}

	return r
}

// Clients returns the descriptors in registration order. The slice
// and every SkillDirs slice are copies; the registry stays immutable.
func (r *Registry) Clients() []Client {
	return cloneClients(r.ordered)
}

// IDs returns the client IDs in registration order.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.ordered))

	for _, c := range r.ordered {
		ids = append(ids, c.ID)
	}

	return ids
}

// Lookup finds a client by ID. The descriptor is a copy.
func (r *Registry) Lookup(id string) (Client, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Client{}, false
	}

	return r.ordered[i].clone(), true
}

// Select resolves user-supplied IDs into descriptors: duplicates
// collapse, unknown IDs fail before any work, and the result follows
// registration order regardless of the input order, so repeated runs
// with reordered flags produce the same plan. The descriptors are copies.
func (r *Registry) Select(ids []string) ([]Client, error) {
	want := make(map[string]bool, len(ids))

	for _, id := range ids {
		if _, ok := r.byID[id]; !ok {
			return nil, fmt.Errorf("%w %q (known: %s)", ErrUnknownClient, id, strings.Join(r.IDs(), ", "))
		}

		want[id] = true
	}

	var out []Client

	for _, c := range r.ordered {
		if want[c.ID] {
			out = append(out, c.clone())
		}
	}

	return out, nil
}

// Require returns ErrUnsupported when the client does not declare the
// capability. It gives every caller one error shape for "this client
// has no such surface".
func (c Client) Require(capability Capability) error {
	if !c.Supports(capability) {
		return fmt.Errorf("%w: %s has no %s capability", ErrUnsupported, c.ID, capability)
	}

	return nil
}

// SkillDestinations groups the selected clients' skill directories by
// directory, in first-appearance order, with every consumer listed in
// the given client order. A shared directory is planned and written
// once; its consumers are reported together so no client claims a
// status the other contradicts.
func SkillDestinations(clients []Client) []SkillDestination {
	var (
		out   []SkillDestination
		index = map[string]int{}
	)

	for _, c := range clients {
		for _, dir := range c.SkillDirs {
			i, ok := index[dir]
			if !ok {
				index[dir] = len(out)
				out = append(out, SkillDestination{Dir: dir})
				i = len(out) - 1
			}

			if !slices.Contains(out[i].Consumers, c.ID) {
				out[i].Consumers = append(out[i].Consumers, c.ID)
			}
		}
	}

	return out
}
