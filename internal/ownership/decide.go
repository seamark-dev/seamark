// Package ownership holds the one rule that says which repository an
// index database belongs to. One decision must change one repository. A
// pin write goes under the recorded root R, and its ledger write goes to
// the database file F. A build writes the workspace W into F as the new R.
//
// Each surface uses three steps. Gather reads the disk and git once.
// Decide answers from those facts. State.Route gives the literal step. A
// surface adds only its own words.
package ownership

// FileClass relates the database file F to the workspace W.
type FileClass int

// The file classes of F.
const (
	FileOther   FileClass = iota // F is not the default database of W.
	FileDefault                  // F is the default database of W.
	FileLinked                   // The default path of W links to F outside W.
	FileAbsent                   // F does not exist or has no bytes.
)

// RootState says what F records and what the disk shows at that path. R
// is gone on ENOENT or ENOTDIR, and hidden on any other stat failure. A
// gone root can be a moved repository or a detached disk.
type RootState int

// The states of R.
const (
	RootNone       RootState = iota // F records no root.
	RootSame                        // R is the same directory as W.
	RootFound                       // A command can enter R.
	RootWidened                     // R exists, but git widens a command there to a top level above R.
	RootGone                        // Nothing exists at R.
	RootHidden                      // R can exist, but no route can enter it.
	RootUnreadable                  // F is damaged or locked, and it can belong to another repository.
)

// OwnerKind names the nearest checkout above F, relative to W and R.
type OwnerKind int

// The owner kinds of F.
const (
	OwnerNone      OwnerKind = iota // No checkout shows above F.
	OwnerWorkspace                  // W is the nearest checkout.
	OwnerRoot                       // R is that checkout, or F lies inside R, also in a submodule.
	OwnerThird                      // A third checkout with a state directory.
	OwnerGitOnly                    // A git work tree without a state directory owns no database.
)

// Facts holds every input of Decide. Gather fills all fields but MoveRoot,
// which a build sets from its flag.
type Facts struct {
	File            FileClass
	Root            RootState
	Owner           OwnerKind
	FileInWorkspace bool // F lies below W.
	RootDefault     bool // F is the default database of R, also through a link.
	RootOwnsFile    bool // RootDefault, or R exists and has no default database.
	NewerSchema     bool // The schema of F is above the schema of this binary.
	MoveRoot        bool // The user allows a build to record W in F.
}

// Build is the answer for an index build.
type Build int

// The answers for a build.
const (
	BuildAllowed Build = iota // A build runs.
	BuildRecords              // A build runs and must not skip, because only a rebuild records W.
	BuildRefused              // A build writes nothing. A fingerprint match can still skip read-only.
)

// Write is the answer for a pin write and its ledger write.
type Write int

// The answers for a write.
const (
	WriteAllowed Write = iota // Both writes change one repository.
	WriteNoIndex              // F holds no index yet. The open that follows reports this.
	WriteRefused              // The two writes would split two repositories.
)

// Route is the kind of next step. State.Route gives the literal text.
type Route int

// The kinds of next step.
const (
	RouteNone        Route = iota
	RouteIndexHere         // A plain index run in W.
	RouteIndexHereDB       // An index run in W that names F.
	RouteMoveHere          // An index run in W with --move-root.
	RouteRunInRoot         // The same work in R.
	RouteRunInRootDB       // The same work in R with --db F, because a plain run opens another ledger.
	RouteAnotherDB         // Another database file, because no command can enter or take this state.
	RouteFixFile           // A repair of F, or another database file.
)

// Decision is the whole answer for one state.
type Decision struct {
	Build Build
	Write Write
	Route Route
}

// Decide answers from facts alone. It reads no disk and no git, and it
// holds no text, so a test can cover each state without a repository.
//
// The flags act after answer. --move-root asks for the move, so it
// passes each refused build. A root that does not read names no move
// that the user can allow, so that refusal stays. A newer schema goes to
// the open, which writes nothing and asks for an upgrade. --force has no
// input here on purpose, because it must not pass a refusal.
func Decide(f Facts) Decision {
	d := answer(f)

	if f.Root == RootUnreadable {
		return d
	}

	if d.Build == BuildRefused && (f.MoveRoot || f.NewerSchema) {
		d.Build = BuildAllowed
	}

	if d.Write == WriteRefused && f.NewerSchema {
		d.Write = WriteAllowed
	}

	return d
}

// answer gives the plain decision. The first case that fits wins, so a later
// case can ignore what an earlier case took.
func answer(f Facts) Decision {
	switch {
	case f.Root == RootUnreadable:
		return Decision{BuildRefused, WriteRefused, RouteFixFile}
	case f.File == FileAbsent:
		return Decision{BuildAllowed, WriteNoIndex, RouteIndexHere}
	case f.Root == RootNone, f.Root == RootSame:
		return Decision{BuildAllowed, WriteAllowed, RouteNone}
	case f.File == FileDefault:
		// A copied or a moved checkout. F has no other owner, so the write
		// waits for the rebuild that records W.
		return Decision{BuildRecords, WriteRefused, RouteIndexHere}
	case f.Root == RootFound:
		return answerFound(f)
	case f.Root == RootGone:
		return answerGone(f)
	default:
		return answerClosed(f, WriteRefused)
	}
}

// answerFound answers for an R that a command can enter. A build here
// takes F from a repository that still needs it, so each case refuses.
func answerFound(f Facts) Decision {
	write := WriteRefused
	if f.RootOwnsFile {
		write = WriteAllowed
	}

	switch {
	case f.Owner == OwnerWorkspace:
		// A custom file of a copy. A route to R would split two repositories.
		return Decision{BuildRefused, write, RouteMoveHere}
	case f.RootDefault:
		return Decision{BuildRefused, write, RouteRunInRoot}
	case f.Owner == OwnerThird:
		// A command in R with this file would write two repositories, and
		// a command without it opens another ledger.
		return answerClosed(f, write)
	default:
		return Decision{BuildRefused, write, RouteRunInRootDB}
	}
}

// answerGone answers for an R that a stat cannot find. Only a file that
// moved with W proves a moved repository. Any other file can belong to a
// detached disk, so a build must not take it without the flag.
func answerGone(f Facts) Decision {
	switch {
	case fileOfWorkspace(f):
		return Decision{BuildRecords, WriteRefused, RouteIndexHereDB}
	case !f.FileInWorkspace && f.Owner == OwnerNone:
		return Decision{BuildRefused, WriteRefused, RouteMoveHere}
	default:
		return answerClosed(f, WriteRefused)
	}
}

// answerClosed answers for a state that no command can enter or take. A
// refusal names the flag only for a file of W, because that move keeps
// one repository.
func answerClosed(f Facts, write Write) Decision {
	if fileOfWorkspace(f) {
		return Decision{BuildRefused, write, RouteMoveHere}
	}

	return Decision{BuildRefused, write, RouteAnotherDB}
}

// fileOfWorkspace reports a file in W and in no checkout nested in W. A
// home directory shows no owner, so a file there can still lie in W.
func fileOfWorkspace(f Facts) bool {
	return f.FileInWorkspace && (f.Owner == OwnerWorkspace || f.Owner == OwnerNone)
}
