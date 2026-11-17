package ownership

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// refused is the decision of a state whose build refuses.
func refused(write Write, route Route) Decision {
	return Decision{BuildRefused, write, route}
}

// decideCases holds one case or more for each state that Decide separates.
var decideCases = []struct {
	name  string
	facts Facts
	want  Decision
}{
	{
		"no recorded root",
		Facts{File: FileDefault, Root: RootNone},
		Decision{BuildAllowed, WriteAllowed, RouteNone},
	},
	{
		"the recorded root is the workspace",
		Facts{File: FileOther, Root: RootSame, Owner: OwnerWorkspace},
		Decision{BuildAllowed, WriteAllowed, RouteNone},
	},
	{
		"a linked file that records the workspace",
		Facts{File: FileLinked, Root: RootSame},
		Decision{BuildAllowed, WriteAllowed, RouteNone},
	},
	{
		"no file or an empty file",
		Facts{File: FileAbsent},
		Decision{BuildAllowed, WriteNoIndex, RouteIndexHere},
	},
	{
		"a copy of a live repository",
		Facts{File: FileDefault, Root: RootFound},
		Decision{BuildRecords, WriteRefused, RouteIndexHere},
	},
	{
		"a moved checkout",
		Facts{File: FileDefault, Root: RootGone},
		Decision{BuildRecords, WriteRefused, RouteIndexHere},
	},
	{
		"a copy of a repository that a stat cannot reach",
		Facts{File: FileDefault, Root: RootHidden},
		Decision{BuildRecords, WriteRefused, RouteIndexHere},
	},
	{
		"a copy of a directory that git widens",
		Facts{File: FileDefault, Root: RootWidened},
		Decision{BuildRecords, WriteRefused, RouteIndexHere},
	},
	{
		"a custom file in a copy",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerWorkspace, FileInWorkspace: true},
		refused(WriteRefused, RouteMoveHere),
	},
	{
		"the known limit of an origin without a default database",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerWorkspace, FileInWorkspace: true, RootOwnsFile: true},
		refused(WriteAllowed, RouteMoveHere),
	},
	{
		"the default database of another repository",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerRoot, RootDefault: true, RootOwnsFile: true},
		refused(WriteAllowed, RouteRunInRoot),
	},
	{
		"a custom file of a repository without a default database",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerRoot, RootOwnsFile: true},
		refused(WriteAllowed, RouteRunInRootDB),
	},
	{
		"a custom file beside a default database",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerRoot},
		refused(WriteRefused, RouteRunInRootDB),
	},
	{
		"a linked worktree",
		Facts{File: FileLinked, Root: RootFound, Owner: OwnerRoot, RootDefault: true, RootOwnsFile: true},
		refused(WriteAllowed, RouteRunInRoot),
	},
	{
		"a default path that links to a file the root does not own",
		Facts{File: FileLinked, Root: RootFound, Owner: OwnerNone},
		refused(WriteRefused, RouteRunInRootDB),
	},
	{
		"a default path that links to a file of a git work tree",
		Facts{File: FileLinked, Root: RootFound, Owner: OwnerGitOnly},
		refused(WriteRefused, RouteRunInRootDB),
	},
	{
		"a default path that links to a file of a third checkout",
		Facts{File: FileLinked, Root: RootFound, Owner: OwnerThird},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a shared file without an owner",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerNone, RootOwnsFile: true},
		refused(WriteAllowed, RouteRunInRootDB),
	},
	{
		"a shared file in a git work tree without a state directory",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerGitOnly},
		refused(WriteRefused, RouteRunInRootDB),
	},
	{
		"a file that moved with the workspace",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerWorkspace, FileInWorkspace: true},
		Decision{BuildRecords, WriteRefused, RouteIndexHereDB},
	},
	{
		"a file in a home directory that shows no owner",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerNone, FileInWorkspace: true},
		Decision{BuildRecords, WriteRefused, RouteIndexHereDB},
	},
	{
		"a file elsewhere that the workspace can take",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerNone},
		refused(WriteRefused, RouteMoveHere),
	},
	{
		"a third checkout with a live root",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerThird},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"the known limit of an origin without a default database",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerThird, RootOwnsFile: true},
		refused(WriteAllowed, RouteAnotherDB),
	},
	{
		"a third checkout nested in the workspace",
		Facts{File: FileOther, Root: RootFound, Owner: OwnerThird, FileInWorkspace: true},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a third checkout with a gone root",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerThird},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a git work tree with a gone root",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerGitOnly},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a repository nested in the workspace with a gone root",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerGitOnly, FileInWorkspace: true},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a third checkout nested in the workspace with a gone root",
		Facts{File: FileOther, Root: RootGone, Owner: OwnerThird, FileInWorkspace: true},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a nested file of a hidden root",
		Facts{File: FileOther, Root: RootHidden, Owner: OwnerGitOnly, FileInWorkspace: true},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a hidden root",
		Facts{File: FileOther, Root: RootHidden, Owner: OwnerNone},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a hidden root with a file of the workspace",
		Facts{File: FileOther, Root: RootHidden, Owner: OwnerWorkspace, FileInWorkspace: true},
		refused(WriteRefused, RouteMoveHere),
	},
	{
		"a widened root",
		Facts{File: FileOther, Root: RootWidened, Owner: OwnerRoot, RootOwnsFile: true},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"a widened root with a file of the workspace",
		Facts{File: FileOther, Root: RootWidened, Owner: OwnerWorkspace, FileInWorkspace: true},
		refused(WriteRefused, RouteMoveHere),
	},
	{
		"a linked file with a gone root that another checkout owns",
		Facts{File: FileLinked, Root: RootGone, Owner: OwnerThird},
		refused(WriteRefused, RouteAnotherDB),
	},
	{
		"an unreadable root",
		Facts{File: FileOther, Root: RootUnreadable},
		refused(WriteRefused, RouteFixFile),
	},
	{
		"an unreadable default database",
		Facts{File: FileDefault, Root: RootUnreadable},
		refused(WriteRefused, RouteFixFile),
	},
}

// TestDecide pins the answer of each state, and no case reads the disk.
func TestDecide(t *testing.T) {
	for _, tc := range decideCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Decide(tc.facts))
		})
	}
}

// TestDecideMoveRootPassesEveryRefusedBuildButUnreadable covers the flag.
// A root that does not read names no move that the user can allow. The
// flag asks only about a build, so it changes no write and no route.
func TestDecideMoveRootPassesEveryRefusedBuildButUnreadable(t *testing.T) {
	for _, tc := range decideCases {
		t.Run(tc.name, func(t *testing.T) {
			facts := tc.facts
			facts.MoveRoot = true

			want := tc.want
			if want.Build == BuildRefused && facts.Root != RootUnreadable {
				want.Build = BuildAllowed
			}

			assert.Equal(t, want, Decide(facts))
		})
	}
}

// TestDecideNewerSchemaGoesToTheOpen covers a schema above this binary.
// The open writes nothing and asks for an upgrade, so no refusal of the
// table stands before it. A root that does not read stays refused.
func TestDecideNewerSchemaGoesToTheOpen(t *testing.T) {
	for _, tc := range decideCases {
		t.Run(tc.name, func(t *testing.T) {
			facts := tc.facts
			facts.NewerSchema = true

			want := tc.want

			if facts.Root != RootUnreadable {
				if want.Build == BuildRefused {
					want.Build = BuildAllowed
				}

				if want.Write == WriteRefused {
					want.Write = WriteAllowed
				}
			}

			assert.Equal(t, want, Decide(facts))
		})
	}
}

// TestDecideNamesTheMoveOnlyForAFileThatStaysInOneRepository covers the
// route of each state: a live root that a command can enter keeps its own
// route, and a file of a third checkout outside the workspace gets none.
func TestDecideNamesTheMoveOnlyForAFileThatStaysInOneRepository(t *testing.T) {
	for _, tc := range decideCases {
		if tc.want.Route != RouteMoveHere {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, []OwnerKind{OwnerWorkspace, OwnerNone}, tc.facts.Owner,
				"a move to the workspace must not take a file of another checkout")
		})
	}
}
