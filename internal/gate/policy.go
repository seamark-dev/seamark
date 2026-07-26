// Package gate is the pre-execution and pre-edit policy engine
// (RFC-001 §5.5): commands are parsed with a real shell parser (never
// regex), classified against the effect catalogue, and evaluated against
// CEL rules over the declared environment. Every decision is appended to
// an audit log.
package gate

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"gopkg.in/yaml.v3"
)

//go:embed policy.yaml
var defaultPolicy []byte

// Rule is one policy predicate.
type Rule struct {
	ID      string `yaml:"id"`
	When    string `yaml:"when"`
	Message string `yaml:"message"`

	prg cel.Program
}

// Policy is the loaded rule set.
type Policy struct {
	// Mode "warn" reports verdicts without blocking; "enforce" makes deny
	// and require_approval binding. Warn is the default (§10: a policy
	// layer that cries wolf gets disabled).
	Mode        string `yaml:"mode"`
	Environment struct {
		Detect      []string `yaml:"detect"`
		ProdMarkers []string `yaml:"prod_markers"`
	} `yaml:"environment"`
	Deny            []Rule `yaml:"deny"`
	RequireApproval []Rule `yaml:"require_approval"`
}

// LoadPolicy returns the workspace policy at .seamark/policy.yaml, or the
// embedded warn-mode default when absent. The workspace file REPLACES the
// default rule set (policy is a whole, not a patch — partial merges would
// make the effective rules unreviewable). All CEL expressions compile at
// load time so a broken rule fails loudly, not at decision time.
func LoadPolicy(root string) (*Policy, error) {
	data := defaultPolicy
	source := "embedded default"

	workspacePath := filepath.Join(root, ".seamark", "policy.yaml")
	if b, err := os.ReadFile(workspacePath); err == nil {
		data = b
		source = workspacePath
	}

	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("gate: %s: %w", source, err)
	}

	if p.Mode == "" {
		p.Mode = "warn"
	}

	if p.Mode != "warn" && p.Mode != "enforce" {
		return nil, fmt.Errorf("gate: %s: mode must be warn or enforce, got %q", source, p.Mode)
	}

	env, err := celEnv()
	if err != nil {
		return nil, err
	}

	for _, rules := range [][]Rule{p.Deny, p.RequireApproval} {
		for i := range rules {
			ast, iss := env.Compile(rules[i].When)
			if iss != nil && iss.Err() != nil {
				return nil, fmt.Errorf("gate: rule %q: %w", rules[i].ID, iss.Err())
			}

			prg, err := env.Program(ast)
			if err != nil {
				return nil, fmt.Errorf("gate: rule %q: %w", rules[i].ID, err)
			}

			rules[i].prg = prg
		}
	}

	return &p, nil
}

// celEnv declares the policy vocabulary. `contains` is added as a member
// function on string lists so the RFC's `effect.contains("db:write")`
// reads naturally alongside CEL's own `"db:write" in effect`.
func celEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("effect", cel.ListType(cel.StringType)),
		cel.Variable("command", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("env", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("diff", cel.MapType(cel.StringType, cel.DynType)),
		cel.Function("contains",
			cel.MemberOverload("list_string_contains",
				[]*cel.Type{cel.ListType(cel.StringType), cel.StringType}, cel.BoolType,
				cel.BinaryBinding(listContains))),
	)
}

func listContains(lhs, rhs ref.Val) ref.Val {
	list, ok := lhs.(traits.Lister)
	if !ok {
		return types.Bool(false)
	}

	for it := list.Iterator(); it.HasNext() == types.True; {
		if it.Next().Equal(rhs) == types.True {
			return types.True
		}
	}

	return types.False
}

// DetectEnv inspects the process environment per policy: which declared
// variables are set, and whether any value carries a production marker.
func (p *Policy) DetectEnv(environ []string) map[string]any {
	vars := map[string]string{}

	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}

		for _, want := range p.Environment.Detect {
			if k == want {
				vars[k] = v
			}
		}
	}

	isProd := false

	for _, v := range vars {
		lower := strings.ToLower(v)

		for _, marker := range p.Environment.ProdMarkers {
			if strings.Contains(lower, marker) {
				isProd = true
			}
		}
	}

	return map[string]any{
		"is_prod":  isProd,
		"is_dev":   !isProd,
		"detected": vars,
	}
}
