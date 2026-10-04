package hooks

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// This file holds the syntax side of the shell reader. The mvdan.cc/sh
// parser, which the gate already uses for command classification, reads
// the command line. This file turns its syntax tree into the parsed
// forms that shell.go walks. The split keeps the grammar apart from the
// rules about hooks, and one shell parser serves the whole module.

// shellAndOr is one AND-OR list: pipelines joined by "&&" and "||".
type shellAndOr struct {
	pipelines []shellPipeline
	// ops[i] joins pipelines[i] and pipelines[i+1]: "&&" or "||".
	ops []string
	// background is true when "&" ends the list, so the shell does not
	// wait for it.
	background bool
}

// shellPipeline is the commands of one pipeline, joined by "|".
type shellPipeline []shellCommand

// shellCommand is one command of a pipeline: a simple command, a
// subshell in parentheses, or a group in braces.
type shellCommand struct {
	// argv holds the words of a simple command after quote removal,
	// without its redirections and its leading variable assignments.
	argv []string
	// subshell holds the list of a "( list )" command; nil otherwise.
	// The list runs in a copy of the shell.
	subshell []shellAndOr
	// group holds the list of a "{ list; }" command; nil otherwise. The
	// list runs in the shell itself, so an exit in it ends the shell.
	group []shellAndOr
	// stdin is true when a redirection replaces standard input.
	stdin bool
}

// maxShellTokens bounds the words and operators of one command line
// that the reader accepts. A hook command comes from a file in the
// repository. The status model walks the rest of a list after each run
// of the hook, so its cost grows with the square of the length. A
// longer line gets the loose test. A real hook command is far shorter.
const maxShellTokens = 1024

// maxShellNesting bounds the nesting of subshells and brace groups that
// the reader accepts. The status model reads each level again for each
// level around it. A deeper form gets the loose test.
const maxShellNesting = 32

// maxShellBytes bounds the length of one command line that the reader
// parses. maxShellTokens bounds the words, not their length, and the
// reader parses a nested script once per level and per marker. A
// longer line gets the loose test, which reads it once. A real hook
// command is far shorter.
const maxShellBytes = 64 << 10

// shellList parses a command line into its list. The parser reads the
// bash dialect, which accepts every form of POSIX sh. A form that the
// shells read in different ways is then seen and refused, not misread.
//
// expands is true when the line holds an expansion or a here-document,
// whose words change when the shell runs the line. ok is false for a
// syntax error and for a line of more than maxShellTokens words and
// operators. It is also false for a form outside the grammar that
// shell.go walks. Such forms are a compound command such as "if" or
// "for", a negation, and a function. A redirection such as "&>", which
// bash and dash read in different ways, and a bash-only operator such
// as "|&" are such forms too. The caller then uses the loose test,
// which never answers a wrong HookRuns.
func shellList(cmd string) (list []shellAndOr, expands, ok bool) {
	if !withinBounds(cmd) {
		return nil, false, false
	}

	// A parser is not safe for concurrent use, and one costs little.
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, false, false
	}

	c := &astReader{}
	list, ok = c.list(file.Stmts)

	return list, c.expands, ok && c.tokens <= maxShellTokens
}

// withinBounds reports whether the command line is small enough to
// parse. The parser recurses once for each nested bracket. It has no
// bound of its own, and a stack overflow is fatal. A command line from a
// repository file is untrusted input. The function therefore checks the
// text before the parser reads it. It counts the words, split at blanks,
// and the deepest nesting of brackets. It ignores quotes, so a quoted
// bracket counts too. That only makes the bound stricter.
func withinBounds(cmd string) bool {
	if len(cmd) > maxShellBytes || len(strings.Fields(cmd)) > maxShellTokens {
		return false
	}

	depth := 0

	for _, r := range cmd {
		switch r {
		case '(', '{', '[':
			depth++
			if depth > maxShellNesting {
				return false
			}
		case ')', '}', ']':
			depth = max(depth-1, 0)
		}
	}

	return true
}

// astReader turns the syntax tree into the parsed forms. It counts the
// words and operators it reads, records an expansion, and tracks the
// nesting of subshells and brace groups.
type astReader struct {
	expands bool
	tokens  int
	depth   int
}

// list reads the statements of a script, a subshell, or a brace group.
func (c *astReader) list(stmts []*syntax.Stmt) ([]shellAndOr, bool) {
	list := make([]shellAndOr, 0, len(stmts))

	for _, stmt := range stmts {
		// The shell does not wait for a coprocess, and the reader does
		// not know its form.
		if stmt.Coprocess {
			return nil, false
		}

		andOr, ok := c.andOr(stmt)
		if !ok {
			return nil, false
		}

		andOr.background = stmt.Background
		c.tokens++

		list = append(list, andOr)
	}

	return list, true
}

// andOr reads a statement as an AND-OR list: a chain of "&&" and "||"
// in the tree, which associates to the left, or one pipeline.
func (c *astReader) andOr(stmt *syntax.Stmt) (shellAndOr, bool) {
	var andOr shellAndOr

	// A redirection or a negation of the whole list is a form the walk
	// does not read.
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && isAndOr(binary.Op) {
		if stmt.Negated || len(stmt.Redirs) > 0 {
			return andOr, false
		}

		left, ok := c.andOr(binary.X)
		if !ok {
			return andOr, false
		}

		right, ok := c.pipeline(binary.Y)
		if !ok {
			return andOr, false
		}

		c.tokens++
		left.pipelines = append(left.pipelines, right)
		left.ops = append(left.ops, binary.Op.String())

		return left, true
	}

	pipeline, ok := c.pipeline(stmt)
	if !ok {
		return andOr, false
	}

	andOr.pipelines = []shellPipeline{pipeline}

	return andOr, true
}

// isAndOr reports whether the operator joins an AND-OR list.
func isAndOr(op syntax.BinCmdOperator) bool {
	return op == syntax.AndStmt || op == syntax.OrStmt
}

// pipeline reads a statement as a pipeline: a chain of "|" in the tree,
// or one command. The bash "|&" operator is a form the walk does not
// read.
func (c *astReader) pipeline(stmt *syntax.Stmt) (shellPipeline, bool) {
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && !isAndOr(binary.Op) {
		// list reads the background flag of the statement, so a pipeline
		// in the background is read like any other.
		if binary.Op != syntax.Pipe || stmt.Negated || len(stmt.Redirs) > 0 {
			return nil, false
		}

		left, ok := c.pipeline(binary.X)
		if !ok {
			return nil, false
		}

		right, ok := c.command(binary.Y)
		if !ok {
			return nil, false
		}

		c.tokens++

		return append(left, right), true
	}

	command, ok := c.command(stmt)
	if !ok {
		return nil, false
	}

	return shellPipeline{command}, true
}

// command reads one command of a pipeline: a simple command, a subshell,
// a brace group, or a timed command. The walk does not read a compound
// command such as "if", a negation, a function, a bash test "[[", or an
// arithmetic command "(( ))". Their exit status depends on what they
// compute. A declaration such as "export X=1" is a simple command that
// runs no hook. The redirections of the command are read for standard
// input and for the forms that the shells read in different ways.
func (c *astReader) command(stmt *syntax.Stmt) (shellCommand, bool) {
	var command shellCommand

	if stmt.Negated {
		return command, false
	}

	switch x := stmt.Cmd.(type) {
	case nil:
		// A command of redirections alone.
	case *syntax.CallExpr:
		for _, assign := range x.Assigns {
			c.tokens++
			c.assignment(assign)
		}

		for _, word := range x.Args {
			c.tokens++
			command.argv = append(command.argv, c.literal(word))
		}
	case *syntax.Subshell:
		list, ok := c.nested(x.Stmts)
		if !ok {
			return command, false
		}

		command.subshell = list
	case *syntax.Block:
		list, ok := c.nested(x.Stmts)
		if !ok {
			return command, false
		}

		command.group = list
	case *syntax.TimeClause:
		// "time" runs its command and returns the status of that command,
		// as the time program does.
		if x.Stmt == nil {
			break
		}

		inner, ok := c.command(x.Stmt)
		if !ok {
			return command, false
		}

		command = inner
	case *syntax.DeclClause:
		c.tokens++
		command.argv = []string{x.Variant.Value}

		for _, assign := range x.Args {
			c.tokens++
			c.assignment(assign)
		}
	default:
		return command, false
	}

	for _, redirect := range stmt.Redirs {
		c.tokens++

		if !c.redirection(&command, redirect) {
			return command, false
		}
	}

	return command, true
}

// nested reads the list of a subshell or a brace group, one level
// deeper. An empty list and a level deeper than maxShellNesting are
// forms the walk does not read.
func (c *astReader) nested(stmts []*syntax.Stmt) ([]shellAndOr, bool) {
	if len(stmts) == 0 || c.depth == maxShellNesting {
		return nil, false
	}

	c.depth++
	list, ok := c.list(stmts)
	c.depth--

	return list, ok
}

// assignment records an expansion in the value of a variable
// assignment. The assignment itself changes which program runs in no
// way the walk reads, so its words are not kept.
func (c *astReader) assignment(assign *syntax.Assign) {
	if assign.Value != nil {
		c.literal(assign.Value)
	}

	if assign.Array != nil || assign.Index != nil {
		c.expands = true
	}
}

// redirection reads one redirection of a command. It records a
// redirection of standard input, which replaces the client's payload,
// and a here-document, whose body is an expansion. Any redirection of
// descriptor 0 replaces the payload, also one that opens the descriptor
// for output ("0>/dev/null") or closes it ("0>&-").
//
// It returns false for a form that the shells read in different ways.
// bash and zsh read "&>", "&>>", and ">&file" as redirections of the
// output and the errors. dash reads "x &>f" as "x &" and then ">f", and
// it rejects ">&file". The reader cannot tell which shell runs the hook.
func (c *astReader) redirection(command *shellCommand, redirect *syntax.Redirect) bool {
	fd := ""
	if redirect.N != nil {
		fd = redirect.N.Value
	}

	switch redirect.Op {
	case syntax.RdrAll, syntax.AppAll:
		return false
	case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		c.expands = true

		return true
	}

	// The input operators act on descriptor 0 when no number is given.
	input := redirect.Op == syntax.RdrIn || redirect.Op == syntax.RdrInOut || redirect.Op == syntax.DplIn
	command.stdin = command.stdin || fd == "0" || (fd == "" && input)

	if redirect.Op == syntax.DplIn || redirect.Op == syntax.DplOut {
		// ">&" and "<&" copy or close a descriptor. POSIX sh defines only
		// a descriptor number or "-" as their target.
		target := c.literal(redirect.Word)

		return target == "-" || (target != "" && strings.Trim(target, "0123456789") == "")
	}

	if redirect.Word != nil {
		c.literal(redirect.Word)
	}

	return true
}

// literal returns the word after quote removal. It records an expansion
// for a part whose value the shell computes when it runs the line. Such
// parts are a parameter, a command substitution, an arithmetic
// expansion, and a process substitution. A brace expansion, an extended
// glob, and the "$'…'" and "$\"…\"" forms of bash are such parts too.
// The word is then not reliable, and the caller uses the loose test.
func (c *astReader) literal(word *syntax.Word) string {
	var text strings.Builder

	// The parser keeps "{a,b}" as a literal; the shell expands it.
	if syntax.SplitBraces(word) {
		c.expands = true
	}

	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			// A brace pair that SplitBraces does not read, such as one
			// with an empty element, can still expand in bash. An unquoted
			// glob expands to the files it matches, or to nothing.
			if (strings.Contains(p.Value, "{") && strings.Contains(p.Value, "}")) || hasGlob(p.Value) {
				c.expands = true
			}

			text.WriteString(unescape(p.Value, false))
		case *syntax.SglQuoted:
			if p.Dollar {
				c.expands = true
			}

			text.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				c.expands = true
			}

			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					c.expands = true

					continue
				}

				text.WriteString(unescape(lit.Value, true))
			}
		default:
			c.expands = true
		}
	}

	return text.String()
}

// hasGlob reports whether an unquoted literal holds a glob pattern: a
// "*", a "?", or a "[" with a "]" after it, none of them escaped. A
// lone "[" or "]" is a word of its own, as in "[ -x f ]", and the shell
// keeps it. The shell replaces a pattern by the files it matches, so a
// path such as "/opt/*/seamark" can name no file or several.
func hasGlob(value string) bool {
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			if strings.IndexByte(value[i+1:], ']') >= 0 {
				return true
			}
		}
	}

	return false
}

// unescape removes the backslashes of a literal, as the shell does
// during quote removal. Outside quotes, a backslash keeps the next
// character. Inside double quotes, it keeps only a double quote, a
// backslash, a dollar sign, and a backquote. A backslash before a
// newline continues the line, and the shell removes both.
func unescape(value string, doubleQuoted bool) string {
	if !strings.Contains(value, `\`) {
		return value
	}

	var out strings.Builder

	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			out.WriteByte(value[i])

			continue
		}

		next := value[i+1]

		switch {
		case next == '\n':
			i++
		case !doubleQuoted || strings.IndexByte("\"\\$`", next) >= 0:
			out.WriteByte(next)
			i++
		default:
			out.WriteByte('\\')
		}
	}

	return out.String()
}
