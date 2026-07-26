package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/cel-go/common/types"
	"mvdan.cc/sh/v3/syntax"

	"github.com/seamark-dev/seamark/internal/effects"
)

// Decision severities, weakest to strongest.
const (
	VerdictAllow    = "allow"
	VerdictApproval = "require_approval"
	VerdictDeny     = "deny"
)

// Match is one rule that fired.
type Match struct {
	RuleID  string `json:"rule"`
	Verdict string `json:"verdict"`
	Message string `json:"message"`
}

// Decision is the gate's answer for one input.
type Decision struct {
	Verdict string   `json:"verdict"` // strongest of the matches
	Mode    string   `json:"mode"`    // warn | enforce
	Effects []string `json:"effects"`
	Matches []Match  `json:"matches,omitempty"`
}

// Blocking reports whether this decision should stop execution: only
// enforce mode blocks; warn mode always lets the command through.
func (d *Decision) Blocking() bool {
	return d.Mode == "enforce" && d.Verdict != VerdictAllow
}

// simpleCmd is one command invocation found in the parsed input.
type simpleCmd struct {
	argv    []string
	dynamic bool // argv[0] is not statically known (variable indirection)
}

// wrapper describes a command that runs another command: how many of its
// flags take separate values (which must not be mistaken for the wrapped
// command) and how many leading positionals it consumes (timeout's
// duration).
type wrapper struct {
	valueFlags  map[string]bool
	positionals int
}

// wrappers are skipped to find the effective command: `sudo -u root
// terraform apply` is a terraform invocation, and every entry here is a
// potential laundering route if mis-parsed.
var wrappers = map[string]wrapper{
	"sudo":    {valueFlags: map[string]bool{"-u": true, "-g": true, "-p": true, "-h": true}},
	"env":     {valueFlags: map[string]bool{"-u": true, "-S": true, "-C": true}},
	"nohup":   {},
	"time":    {},
	"command": {},
	"nice":    {valueFlags: map[string]bool{"-n": true}},
	"timeout": {valueFlags: map[string]bool{"-k": true, "-s": true}, positionals: 1},
	"watch":   {valueFlags: map[string]bool{"-n": true, "-d": true}},
	"stdbuf":  {valueFlags: map[string]bool{"-i": true, "-o": true, "-e": true}},
	"xargs":   {valueFlags: map[string]bool{"-n": true, "-P": true, "-I": true, "-L": true, "-a": true, "-d": true, "-E": true, "-s": true}},
}

// interpreters run a shell command passed as a string argument to -c;
// that payload must be re-parsed or it is a total classification bypass.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
}

// maxNestedParses bounds interpreter-payload recursion (`bash -c "bash
// -c …"`); depth beyond this is degenerate and classifies as dynamic.
const maxNestedParses = 8

// parseCommands parses one shell string into its command invocations
// (pipelines, chains, and substitutions included).
func parseCommands(commandLine string) ([]simpleCmd, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(commandLine), "")
	if err != nil {
		return nil, err
	}

	var cmds []simpleCmd

	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			cmds = append(cmds, extractCmd(call))
		}

		return true
	})

	return cmds, nil
}

// shellPayload extracts the command string an interpreter or eval carries:
// the argument after a -c flag (or a short-flag cluster ending in c, as
// in `bash -lc`), or eval's whole argument list. ok is false when there
// is none; a payload of "" means it exists but is not statically known.
func shellPayload(name string, args []string) (payload string, ok bool) {
	if name == "eval" {
		return strings.Join(args, " "), len(args) > 0
	}

	for i, a := range args {
		if a == "-c" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "c")) {
			if i+1 < len(args) {
				return args[i+1], true
			}

			return "", false
		}
	}

	return "", false
}

// EvalCommand parses a shell command line, classifies every command in it
// (pipelines, &&-chains, substitutions, and interpreter payloads like
// `bash -c "…"` included) against the catalogue, and evaluates the
// policy. Parse failures fail closed with an error — a gate that shrugs
// at unparseable input is a bypass.
func EvalCommand(p *Policy, catalog *effects.Catalog, root, commandLine string) (*Decision, error) {
	cmds, err := parseCommands(commandLine)
	if err != nil {
		return nil, fmt.Errorf("gate: cannot parse command: %w", err)
	}

	tagSet := map[string]bool{}
	dynamic := false
	push := false
	forcePush := false
	targetBranch := ""
	parses := 0

	var argv []string

	for len(cmds) > 0 {
		c := unwrap(cmds[0])
		cmds = cmds[1:]

		if len(c.argv) == 0 {
			continue
		}

		if c.dynamic {
			dynamic = true
			continue
		}

		name := path.Base(c.argv[0])
		if len(argv) == 0 {
			argv = c.argv
		}

		// Interpreter and eval payloads re-enter the analysis: `bash -c
		// "terraform apply"` IS a terraform invocation, and skipping the
		// string would be a total classification bypass.
		if interpreters[name] || name == "eval" {
			if payload, ok := shellPayload(name, c.argv[1:]); ok {
				switch {
				case strings.TrimSpace(payload) == "":
					dynamic = true // payload exists but is not literal
				case parses >= maxNestedParses:
					dynamic = true
				default:
					parses++

					nested, err := parseCommands(payload)
					if err != nil {
						dynamic = true // unparseable payload: never assume harmless
					} else {
						cmds = append(cmds, nested...)
					}
				}
			}
		}

		for _, tag := range catalog.MatchCommand(name, c.argv[1:]) {
			tagSet[tag] = true
		}

		if name == "git" {
			if isPush, isForce, branch := gitPush(c.argv[1:], root); isPush {
				push = true
				forcePush = forcePush || isForce
				targetBranch = branch
			}
		}
	}

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}

	name := ""
	if len(argv) > 0 {
		name = path.Base(argv[0])
	}

	activation := map[string]any{
		"effect": tags,
		"command": map[string]any{
			"name":          name,
			"argv":          argv,
			"raw":           commandLine,
			"is_push":       push,
			"is_force_push": forcePush,
			"target_branch": targetBranch,
			"dynamic":       dynamic,
		},
		"env":  p.DetectEnv(os.Environ()),
		"diff": emptyDiff(),
	}

	return p.evaluate(tags, activation)
}

// extractCmd statically resolves a CallExpr's words. Words containing
// expansions (variables, substitutions) cannot be known without executing
// them; argv[0] being one of those is exactly the indirection trick a
// regex denylist misses, so it is surfaced as dynamic instead of guessed.
func extractCmd(call *syntax.CallExpr) simpleCmd {
	var c simpleCmd

	for i, word := range call.Args {
		lit, ok := literalWord(word)
		if !ok {
			if i == 0 {
				c.dynamic = true
			}

			lit = "" // dynamic tail args carry no classification signal
		}

		c.argv = append(c.argv, lit)
	}

	return c
}

// unescapeLit removes backslash escapes from an unquoted literal:
// `t\erraform` and `\rm` are `terraform` and `rm` to the shell, and must
// be to the classifier too.
func unescapeLit(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}

	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}

		b.WriteByte(s[i])
	}

	return b.String()
}

// literalWord flattens a word made only of literals and quoted strings.
func literalWord(w *syntax.Word) (string, bool) {
	var b strings.Builder

	for _, part := range w.Parts {
		switch v := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeLit(v.Value))
		case *syntax.SglQuoted:
			b.WriteString(v.Value)
		case *syntax.DblQuoted:
			for _, inner := range v.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}

				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}

	return b.String(), true
}

// unwrap strips privilege/env/runner wrappers so `sudo -u root terraform
// apply` and `timeout 5 terraform apply` classify as terraform.
func unwrap(c simpleCmd) simpleCmd {
	for len(c.argv) > 0 && !c.dynamic {
		w, ok := wrappers[path.Base(c.argv[0])]
		if !ok {
			return c
		}

		rest := c.argv[1:]
		positionals := w.positionals
		done := false

		for len(rest) > 0 && !done {
			tok := rest[0]

			switch {
			case w.valueFlags[tok]:
				// A value-taking flag: consume the flag AND its value, or
				// the value would be mistaken for the wrapped command
				// (`sudo -u root terraform …` must not classify as root).
				if len(rest) > 1 {
					rest = rest[2:]
				} else {
					rest = nil
				}
			case strings.HasPrefix(tok, "-"), strings.Contains(tok, "="):
				rest = rest[1:] // plain flag or VAR=val assignment
			case positionals > 0:
				positionals--
				rest = rest[1:] // e.g. timeout's duration
			default:
				done = true
			}
		}

		if len(rest) == 0 {
			return simpleCmd{}
		}

		if rest[0] == "" {
			return simpleCmd{argv: rest, dynamic: true}
		}

		c = simpleCmd{argv: rest}
	}

	return c
}

// Git global flags that consume the next argument; they must be skipped
// so `git -C somewhere push` finds its real subcommand, and `git log
// --grep push` does not look like one.
var gitValueFlags = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
}

// gitSubcommandArgs returns the arguments following git's subcommand when
// it matches want. Value-taking global flags are skipped so `git -C x
// push` resolves, and only the true subcommand position is considered so
// `git log --grep push` never looks like a push.
func gitSubcommandArgs(args []string, want string) ([]string, bool) {
	for i := 0; i < len(args); {
		a := args[i]

		switch {
		case gitValueFlags[a]:
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			if a == want {
				return args[i+1:], true
			}

			return nil, false
		}
	}

	return nil, false
}

// gitPush detects pushes, force pushes, and the target branch. The branch
// comes from the refspec when present, else the current branch.
func gitPush(args []string, root string) (isPush, isForce bool, branch string) {
	rest, ok := gitSubcommandArgs(args, "push")
	if !ok {
		return false, false, ""
	}

	var positional []string

	for _, a := range rest {
		switch {
		case a == "--force" || a == "-f" || strings.HasPrefix(a, "--force-with-lease"):
			isForce = true
		case strings.HasPrefix(a, "+"):
			isForce = true

			positional = append(positional, strings.TrimPrefix(a, "+"))
		case !strings.HasPrefix(a, "-"):
			positional = append(positional, a)
		}
	}

	// positional: [remote] [refspec]; refspec may be src:dst.
	if len(positional) >= 2 {
		ref := positional[len(positional)-1]
		if _, dst, ok := strings.Cut(ref, ":"); ok {
			ref = dst
		}

		ref = strings.TrimPrefix(ref, "refs/heads/")

		// `git push origin HEAD` pushes the CURRENT branch — resolve it,
		// or "HEAD" slips past branch-name policy rules.
		if ref != "HEAD" && ref != "@" {
			return true, isForce, ref
		}
	}

	// No refspec (or a HEAD alias): the push targets the current branch.
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root

	if out, err := cmd.Output(); err == nil {
		return true, isForce, strings.TrimSpace(string(out))
	}

	return true, isForce, ""
}

func emptyDiff() map[string]any {
	return map[string]any{"files": []string{}, "effects": []string{}}
}

// evaluate runs every rule and keeps the strongest verdict.
func (p *Policy) evaluate(tags []string, activation map[string]any) (*Decision, error) {
	d := &Decision{Verdict: VerdictAllow, Mode: p.Mode, Effects: tags}

	run := func(rules []Rule, verdict string) error {
		for i := range rules {
			out, _, err := rules[i].prg.Eval(activation)
			if err != nil {
				return fmt.Errorf("gate: rule %q: %w", rules[i].ID, err)
			}

			if out == types.True {
				d.Matches = append(d.Matches, Match{
					RuleID: rules[i].ID, Verdict: verdict, Message: rules[i].Message,
				})

				if verdict == VerdictDeny || d.Verdict == VerdictAllow {
					d.Verdict = verdict
				}
			}
		}

		return nil
	}

	if err := run(p.Deny, VerdictDeny); err != nil {
		return nil, err
	}

	if err := run(p.RequireApproval, VerdictApproval); err != nil {
		return nil, err
	}

	return d, nil
}

// Audit appends the decision to <root>/.seamark/audit.jsonl. The log is
// append-only JSONL: no open-source tool in this category has an audit
// trail, and several enterprises need one (RFC-001 §5.5).
func Audit(root, kind, input string, d *Decision) error {
	dir := filepath.Join(root, ".seamark")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(filepath.Join(dir, "audit.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // write errors surface via Encode

	entry := map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339),
		"kind":    kind,
		"input":   input,
		"verdict": d.Verdict,
		"mode":    d.Mode,
		"effects": d.Effects,
		"matches": d.Matches,
	}

	return json.NewEncoder(f).Encode(entry)
}
