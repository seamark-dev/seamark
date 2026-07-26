package parse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

const tsFixture = `import { fetchJSON } from "./client";
import { helper as h } from "./helpers";
import * as levels from "../levels/api";
import React from "react";

// Snapshot of one auction session.
export interface Snapshot {
	id: string;
}

export type SessionMap = Record<string, Snapshot>;

export enum Mode {
	Live,
	Replay,
}

/** Loads a snapshot by id. */
export async function loadSnapshot(id: string): Promise<Snapshot> {
	const raw = await fetchJSON("/snap/" + id);
	return normalize(raw);
}

function normalize(raw: unknown): Snapshot {
	h(raw);
	levels.annotate(raw);
	return raw as Snapshot;
}

export const parseKey = (key: string): string[] => {
	const parts = key.split(":");
	return parts;
};

export const MAX_RETRIES = 3;
let counter = 0;

export class SnapshotStore {
	// Loads from the backing cache.
	get(id: string): Snapshot | undefined {
		return this.cache.get(id);
	}
}
`

func extractTS(t *testing.T, relPath, src string) *FileResult {
	t.Helper()

	ex, err := NewEcmaExtractor(EcmaTS)
	require.NoError(t, err)
	t.Cleanup(ex.Close)

	res, err := ex.Extract(relPath, []byte(src))
	require.NoError(t, err)

	return res
}

func TestEcmaExtractSymbols(t *testing.T) {
	res := extractTS(t, "web/src/api/snapshots.ts", tsFixture)

	assert.Equal(t, "web/src/api/snapshots", res.Package)

	want := map[string]model.SymbolKind{
		"web/src/api/snapshots.Snapshot":          model.KindType,
		"web/src/api/snapshots.SessionMap":        model.KindType,
		"web/src/api/snapshots.Mode":              model.KindType,
		"web/src/api/snapshots.loadSnapshot":      model.KindFunction,
		"web/src/api/snapshots.normalize":         model.KindFunction,
		"web/src/api/snapshots.parseKey":          model.KindFunction, // arrow const
		"web/src/api/snapshots.MAX_RETRIES":       model.KindConst,
		"web/src/api/snapshots.counter":           model.KindVar,
		"web/src/api/snapshots.SnapshotStore":     model.KindType,
		"web/src/api/snapshots.SnapshotStore.get": model.KindMethod,
	}
	assert.Len(t, res.Symbols, len(want), "extracted FQNs: %v", fqns(res))

	for fqn, kind := range want {
		d := symbolByFQN(t, res, fqn)
		assert.Equal(t, kind, d.Symbol.Kind, "kind of %s", fqn)
	}

	// Function-local declarations (raw, parts) must not become symbols.
	for _, d := range res.Symbols {
		assert.NotContains(t, []string{"raw", "parts"}, d.Symbol.Name,
			"local binding leaked into module symbols")
	}
}

func TestEcmaExtractImports(t *testing.T) {
	res := extractTS(t, "web/src/api/snapshots.ts", tsFixture)

	byPath := map[string]Import{}
	for _, imp := range res.Imports {
		byPath[imp.Path] = imp
	}
	require.Len(t, byPath, 4)

	// Relative named import: resolved against the importing file's dir.
	client := byPath["./client"]
	assert.Equal(t, "web/src/api/client", client.Resolved)
	assert.Equal(t, []string{"fetchJSON"}, client.Named)
	assert.Empty(t, client.LocalName)

	// Aliased named import contributes nothing resolvable.
	helpers := byPath["./helpers"]
	assert.Empty(t, helpers.Named, "aliased imports must be omitted")

	// Namespace import: qualifier mapping, parent-relative resolution.
	lv := byPath["../levels/api"]
	assert.Equal(t, "levels", lv.LocalName)
	assert.Equal(t, "web/src/levels/api", lv.Resolved)

	// Bare specifier: external, unresolved.
	react := byPath["react"]
	assert.Empty(t, react.Resolved)
	assert.Empty(t, react.LocalName)
}

func TestEcmaExtractCallRefs(t *testing.T) {
	res := extractTS(t, "web/src/api/snapshots.ts", tsFixture)

	load := symbolByFQN(t, res, "web/src/api/snapshots.loadSnapshot")
	assert.Contains(t, load.Calls, CallRef{Name: "fetchJSON", Line: 20},
		"bare call to a named import")
	assert.Contains(t, load.Calls, CallRef{Name: "normalize", Line: 21},
		"bare call to a module-local function")

	norm := symbolByFQN(t, res, "web/src/api/snapshots.normalize")
	assert.Contains(t, norm.Calls, CallRef{Qualifier: "levels", Name: "annotate", Line: 26, Selector: true},
		"namespace-qualified call")

	// Arrow-function consts get their bodies mined too.
	parseKey := symbolByFQN(t, res, "web/src/api/snapshots.parseKey")
	assert.Contains(t, parseKey.Calls, CallRef{Qualifier: "key", Name: "split", Line: 31, Selector: true})
}

func TestEcmaSignatureAndDoc(t *testing.T) {
	res := extractTS(t, "web/src/api/snapshots.ts", tsFixture)

	load := symbolByFQN(t, res, "web/src/api/snapshots.loadSnapshot")
	assert.Equal(t, "export async function loadSnapshot(id: string): Promise<Snapshot>", load.Symbol.Sig)
	assert.NotEmpty(t, load.Symbol.DocHash, "JSDoc above an exported function must hash")

	norm := symbolByFQN(t, res, "web/src/api/snapshots.normalize")
	assert.Empty(t, norm.Symbol.DocHash)

	iface := symbolByFQN(t, res, "web/src/api/snapshots.Snapshot")
	assert.NotEmpty(t, iface.Symbol.DocHash, "line comment above an exported interface must hash")

	get := symbolByFQN(t, res, "web/src/api/snapshots.SnapshotStore.get")
	assert.NotEmpty(t, get.Symbol.DocHash, "comment above a class method must hash")
}

func TestEcmaObjectLiteralMethodsAreNotClassMethods(t *testing.T) {
	src := `export class A {
	build() {
		const handlers = { onClick() { fire(); } };
		return handlers;
	}

	cfg = { onKey() {} };

	static {
		const setup = { onInit() {} };
	}
}
`
	res := extractTS(t, "a.ts", src)

	var methods []string

	for _, d := range res.Symbols {
		if d.Symbol.Kind == model.KindMethod {
			methods = append(methods, d.Symbol.FQN)
		}
	}

	assert.Equal(t, []string{"a.A.build"}, methods,
		"object-literal handlers inside class members must not become class methods")
}

func TestEcmaAssetImportsKeepExtension(t *testing.T) {
	src := `import "./button.css";
import "./logo.svg";
import x from "./v1.2";
import y from "./button";
`
	res := extractTS(t, "web/page.ts", src)

	got := map[string]string{}
	for _, imp := range res.Imports {
		got[imp.Path] = imp.Resolved
	}
	assert.Equal(t, map[string]string{
		"./button.css": "web/button.css", // asset keeps its extension: no collision with button.ts
		"./logo.svg":   "web/logo.svg",
		"./v1.2":       "web/v1.2", // dotted basename is not an extension
		"./button":     "web/button",
	}, got)
}

func TestEcmaMultiLineDecoratorSkipped(t *testing.T) {
	src := `@Component({
  selector: "app-x",
  template: "<div/>",
})
class Widget {
  render(): string { return "w"; }
}
`
	res := extractTS(t, "widget.ts", src)

	widget := symbolByFQN(t, res, "widget.Widget")
	assert.Equal(t, "class Widget", widget.Symbol.Sig,
		"multi-line decorator bodies must not leak into the signature")
}

func TestEcmaExportedArrowConstSignature(t *testing.T) {
	res := extractTS(t, "web/src/api/snapshots.ts", tsFixture)

	parseKey := symbolByFQN(t, res, "web/src/api/snapshots.parseKey")
	assert.Equal(t, "export const parseKey = (key: string): string[] =>", parseKey.Symbol.Sig,
		"export/const wrappers are part of the signature")
}

func TestEcmaThisReceiverAndRebinding(t *testing.T) {
	src := `export class Panel {
	tick(): number {
		const f = () => this.refresh();
		function inner() {
			return this.refresh();
		}
		this.refresh();
		return f();
	}

	refresh(): number { return 1; }
}
`
	res := extractTS(t, "panel.ts", src)
	tick := symbolByFQN(t, res, "panel.Panel.tick")

	receivers, rebound := 0, 0

	for _, c := range tick.Calls {
		if c.Name != "refresh" {
			continue
		}
		if c.Receiver {
			receivers++
		} else {
			rebound++
		}
	}

	// Direct `this.refresh()` and the arrow-wrapped one keep lexical this;
	// the one inside the plain nested function is rebound.
	assert.Equal(t, 2, receivers, "direct + arrow this-calls are receiver calls")
	assert.Equal(t, 1, rebound, "nested plain function rebinds this")
}

func TestEcmaJavaScriptAndTSX(t *testing.T) {
	jsSrc := `const compute = function (a, b) {
	return a + b;
};

function main() {
	compute(1, 2);
}
`
	ex, err := NewEcmaExtractor(EcmaJS)
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("lib/calc.js", []byte(jsSrc))
	require.NoError(t, err)

	compute := symbolByFQN(t, res, "lib/calc.compute")
	assert.Equal(t, model.KindFunction, compute.Symbol.Kind,
		"function-expression consts are functions")
	symbolByFQN(t, res, "lib/calc.main")

	tsxSrc := `import { useState } from "react";

export const Panel = () => {
	const [n, setN] = useState(0);
	return <div onClick={() => setN(n + 1)}>{n}</div>;
};
`
	exx, err := NewEcmaExtractor(EcmaTSX)
	require.NoError(t, err)
	defer exx.Close()

	rex, err := exx.Extract("web/src/Panel.tsx", []byte(tsxSrc))
	require.NoError(t, err)

	panel := symbolByFQN(t, rex, "web/src/Panel.Panel")
	assert.Equal(t, model.KindFunction, panel.Symbol.Kind, "TSX component arrow const")
	assert.Contains(t, panel.Calls, CallRef{Name: "useState", Line: 4})
}
