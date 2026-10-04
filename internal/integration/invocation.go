package integration

import (
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/agent"
)

// ResolveInvocation resolves the configured agent's command for root.
// It skips PATH and authentication checks so dry runs and diagnostics work
// without the client installed. Use agent.NewCommand to create the invoker.
//
// Custom argv takes precedence, uses the name "custom", and inherits the
// caller's directory. Otherwise agent.cli selects a registered client,
// defaulting to Claude, independently of client setup. Unknown clients and
// clients without invocation support return an error listing valid choices.
func (r *Registry) ResolveInvocation(cfg *agent.Config, root string) (agent.CommandSpec, error) {
	if len(cfg.Agent.Argv) > 0 {
		return agent.ResolveCommand(cfg)
	}

	id := cfg.Agent.CLI
	if id == "" {
		id = ClaudeID
	}

	known := strings.Join(r.InvocationIDs(), ", ")

	client, ok := r.Lookup(id)
	if !ok {
		return agent.CommandSpec{}, fmt.Errorf("unknown agent cli %q (known: %s; or set agent.argv)", id, known)
	}

	if client.Invocation == nil {
		return agent.CommandSpec{}, fmt.Errorf("agent cli %q has no one-shot command (known: %s; or set agent.argv)", id, known)
	}

	spec, err := client.Invocation(root)
	if err != nil {
		return agent.CommandSpec{}, fmt.Errorf("agent cli %q: %w", id, err)
	}

	if err := spec.Validate(); err != nil {
		return agent.CommandSpec{}, err
	}

	return spec, nil
}

// InvocationIDs lists valid agent.cli values in registry order.
func (r *Registry) InvocationIDs() []string {
	var ids []string

	for _, c := range r.ordered {
		if c.Invocation != nil {
			ids = append(ids, c.ID)
		}
	}

	return ids
}
