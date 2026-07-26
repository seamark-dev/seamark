package parse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

const pyFixture = `import os
import numpy as np
from .client import fetch_json, parse as p
from ..levels import annotate
from . import tracker

MAX_RETRIES = 3
default_mode = "live"


def load_snapshot(snap_id):
    """Loads a snapshot by id."""
    raw = fetch_json("/snap/" + snap_id)
    return normalize(raw)


def normalize(raw):
    annotate(raw)
    tracker.record(raw)
    np.asarray(raw)
    return raw


class SnapshotStore:
    """Caches snapshots."""

    def get(self, snap_id):
        """Reads through the cache."""
        return self._load(snap_id)

    def _load(self, snap_id):
        def helper(x):
            return x

        return helper(snap_id)

    @property
    def size(self):
        return 0
`

func extractPy(t *testing.T, relPath, src string) *FileResult {
	t.Helper()

	ex, err := NewPyExtractor()
	require.NoError(t, err)
	t.Cleanup(ex.Close)

	res, err := ex.Extract(relPath, []byte(src))
	require.NoError(t, err)

	return res
}

func TestPyExtractSymbols(t *testing.T) {
	res := extractPy(t, "api/services/snapshots.py", pyFixture)

	assert.Equal(t, "api/services/snapshots", res.Package)

	want := map[string]model.SymbolKind{
		"api/services/snapshots.MAX_RETRIES":         model.KindConst,
		"api/services/snapshots.default_mode":        model.KindVar,
		"api/services/snapshots.load_snapshot":       model.KindFunction,
		"api/services/snapshots.normalize":           model.KindFunction,
		"api/services/snapshots.SnapshotStore":       model.KindType,
		"api/services/snapshots.SnapshotStore.get":   model.KindMethod,
		"api/services/snapshots.SnapshotStore._load": model.KindMethod,
		"api/services/snapshots.SnapshotStore.size":  model.KindMethod, // decorated
	}
	assert.Len(t, res.Symbols, len(want), "extracted FQNs: %v", fqns(res))

	for fqn, kind := range want {
		d := symbolByFQN(t, res, fqn)
		assert.Equal(t, kind, d.Symbol.Kind, "kind of %s", fqn)
	}

	// The nested helper inside _load must not become a symbol.
	for _, d := range res.Symbols {
		assert.NotEqual(t, "helper", d.Symbol.Name, "nested function leaked")
	}
}

func TestPyExtractImports(t *testing.T) {
	res := extractPy(t, "api/services/snapshots.py", pyFixture)

	type row struct {
		local    string
		named    []string
		resolved string
	}

	got := map[string]row{}
	for _, imp := range res.Imports {
		got[imp.Path] = row{imp.LocalName, imp.Named, imp.Resolved}
	}

	assert.Equal(t, row{local: "os", resolved: "os"}, got["os"])
	assert.Equal(t, row{local: "np", resolved: "numpy"}, got["numpy"])
	// Relative from-import: resolved against api/services; alias dropped.
	assert.Equal(t, row{named: []string{"fetch_json"}, resolved: "api/services/client"}, got[".client"])
	// Two dots walk up one package.
	assert.Equal(t, row{named: []string{"annotate"}, resolved: "api/levels"}, got["..levels"])
	// `from . import tracker`: the submodule becomes a qualifier.
	assert.Equal(t, row{local: "tracker", resolved: "api/services/tracker"}, got["."])
}

func TestPyExtractCallRefs(t *testing.T) {
	res := extractPy(t, "api/services/snapshots.py", pyFixture)

	load := symbolByFQN(t, res, "api/services/snapshots.load_snapshot")
	assert.Contains(t, load.Calls, CallRef{Name: "fetch_json", Line: 13})
	assert.Contains(t, load.Calls, CallRef{Name: "normalize", Line: 14})

	norm := symbolByFQN(t, res, "api/services/snapshots.normalize")
	assert.Contains(t, norm.Calls, CallRef{Name: "annotate", Line: 18})
	assert.Contains(t, norm.Calls, CallRef{Qualifier: "tracker", Name: "record", Line: 19, Selector: true})
	assert.Contains(t, norm.Calls, CallRef{Qualifier: "np", Name: "asarray", Line: 20, Selector: true})

	// self-calls carry the Receiver flag for same-class resolution.
	get := symbolByFQN(t, res, "api/services/snapshots.SnapshotStore.get")
	assert.Contains(t, get.Calls,
		CallRef{Qualifier: "self", Name: "_load", Line: 29, Selector: true, Receiver: true})
}

func TestPyReceiverIsFirstParamNotSpelling(t *testing.T) {
	src := `class Worker:
    def run(self):
        cls = get_other()
        cls.helper()
        self.step()
        return 1

    def step(self):
        return 2

    def odd(me):
        me.step()
`
	res := extractPy(t, "w.py", src)

	run := symbolByFQN(t, res, "w.Worker.run")
	assert.Contains(t, run.Calls,
		CallRef{Qualifier: "cls", Name: "helper", Line: 4, Selector: true},
		"a LOCAL named cls is not the receiver — Receiver must be false")
	assert.Contains(t, run.Calls,
		CallRef{Qualifier: "self", Name: "step", Line: 5, Selector: true, Receiver: true})

	// Any first-parameter spelling is the receiver, not just "self".
	odd := symbolByFQN(t, res, "w.Worker.odd")
	assert.Contains(t, odd.Calls,
		CallRef{Qualifier: "me", Name: "step", Line: 12, Selector: true, Receiver: true})
}

func TestPySignatureAndDoc(t *testing.T) {
	res := extractPy(t, "api/services/snapshots.py", pyFixture)

	load := symbolByFQN(t, res, "api/services/snapshots.load_snapshot")
	assert.Equal(t, "def load_snapshot(snap_id)", load.Symbol.Sig)
	assert.NotEmpty(t, load.Symbol.DocHash, "docstring must hash")

	norm := symbolByFQN(t, res, "api/services/snapshots.normalize")
	assert.Empty(t, norm.Symbol.DocHash, "no docstring, no hash")

	store := symbolByFQN(t, res, "api/services/snapshots.SnapshotStore")
	assert.Equal(t, "class SnapshotStore", store.Symbol.Sig)
	assert.NotEmpty(t, store.Symbol.DocHash, "class docstring must hash")

	size := symbolByFQN(t, res, "api/services/snapshots.SnapshotStore.size")
	assert.Equal(t, "def size(self)", size.Symbol.Sig, "decorator must not leak into the sig")
}

func TestPyMultiLineSignatureCollapses(t *testing.T) {
	src := `async def patch_me(
    user_id: int,
    payload: dict,
) -> dict:
    return payload
`
	res := extractPy(t, "m.py", src)

	patch := symbolByFQN(t, res, "m.patch_me")
	assert.Equal(t, "async def patch_me( user_id: int, payload: dict, ) -> dict",
		patch.Symbol.Sig, "multi-line headers fold into one line instead of truncating")
}

func TestPyInitModuleKey(t *testing.T) {
	src := `def make():
    return 1
`
	res := extractPy(t, "api/services/__init__.py", src)
	assert.Equal(t, "api/services", res.Package, "__init__.py is the package itself")
	symbolByFQN(t, res, "api/services.make")

	rootRes := extractPy(t, "__init__.py", src)
	assert.Equal(t, "", rootRes.Package, "root __init__.py is the root package")
}

func TestPyRelativeImportFromInit(t *testing.T) {
	src := `from .engine import run
from ..shared import util_fn
`
	// For api/__init__.py (module "api"), "." is api itself.
	res := extractPy(t, "api/__init__.py", src)

	got := map[string]string{}
	for _, imp := range res.Imports {
		got[imp.Path] = imp.Resolved
	}
	assert.Equal(t, map[string]string{
		".engine":  "api/engine",
		"..shared": "shared",
	}, got)
}

func TestPyAliasedSubmoduleAndEscapes(t *testing.T) {
	src := `from . import tracker as tk
from .. import parentmod
from ... import rootmod
from ....outside import thing
`
	res := extractPy(t, "api/services/x.py", src)

	type row struct{ local, resolved string }

	var rows []row
	for _, imp := range res.Imports {
		rows = append(rows, row{imp.LocalName, imp.Resolved})
	}
	assert.ElementsMatch(t, []row{
		// Aliased submodule: the alias is the qualifier, target is known.
		{"tk", "api/services/tracker"},
		// Two dots = the parent package (api), three = the repo root.
		{"parentmod", "api/parentmod"},
		{"rootmod", "rootmod"},
		// Four dots climb past the repo: unresolvable, never guessed.
		{"", ""},
	}, rows)
}
