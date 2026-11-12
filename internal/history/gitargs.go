package history

// parsedGitConfig holds the config overrides that go before the git
// subcommand. A -c override works on every git version, and an older git
// can lack the matching command-line flag.
var parsedGitConfig = []string{
	// core.quotePath=true writes a non-ASCII path as a C-quoted string.
	// The parse layer and the index store raw paths, so every caller
	// reads the raw form.
	"-c", "core.quotePath=off",
	// diff.relative=true limits the output to the current directory and
	// strips that directory from the paths. --no-relative needs git 2.28.
	"-c", "diff.relative=false",
	// log.follow=true follows a rename of a single file although the
	// call sets --no-renames, so the commit set grows.
	"-c", "log.follow=false",
	// log.showSignature=true writes signature lines to stdout before
	// each signed commit, so they mix with the format fields.
	"-c", "log.showSignature=false",
}

// parsedGitFlags holds the options that go after the git subcommand.
// diff, log, and show all accept them.
var parsedGitFlags = []string{
	// color.ui or color.diff set to always wraps each line in color codes.
	"--no-color",
	// diff.external and GIT_EXTERNAL_DIFF replace the unified diff with
	// the output of another program, and that program runs.
	"--no-ext-diff",
	// A textconv driver of the gitattributes replaces the file content
	// before git compares it. A local driver also differs between clones.
	"--no-textconv",
	// diff.noprefix and diff.mnemonicPrefix change the a/ and b/ path
	// prefixes. The explicit prefixes work on older git than
	// --default-prefix.
	"--src-prefix=a/", "--dst-prefix=b/",
	// diff.submodule=log or diff writes a submodule change without a
	// diff --git header. The short form keeps that header.
	"--submodule=short",
}

// ParsedGitArgs returns the git arguments for a diff, log, or show call
// whose output seamark parses. The fixed options keep user, repository,
// and attribute config from changing that output. sub is the subcommand,
// and args follow the fixed options. With no config and ASCII paths,
// the output is the same as without the fixed options.
func ParsedGitArgs(sub string, args ...string) []string {
	out := make([]string, 0, len(parsedGitConfig)+1+len(parsedGitFlags)+len(args))
	out = append(out, parsedGitConfig...)
	out = append(out, sub)
	out = append(out, parsedGitFlags...)

	return append(out, args...)
}
