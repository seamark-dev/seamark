package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// The tests of the lesson-check paths. Every root is a temporary
// directory, and every hook binary is a file that the test writes and
// never runs, because inspection runs nothing.

// fakeSeamark writes an executable file named seamark in a new
// directory under dir, so a hook command that names it is owned. The
// content is not a Go binary, so a real build-version read finds no
// version.
func fakeSeamark(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name, "seamark")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	return path
}

// stubBuildVersions replaces the build-version reader for one test. It
// returns the version by path and records every read, so a test proves
// which files the identification opened.
func stubBuildVersions(t *testing.T, versions map[string]string) *[]string {
	t.Helper()

	var reads []string

	saved := readBuildVersion
	readBuildVersion = func(path string) string {
		reads = append(reads, path)

		return versions[path]
	}

	t.Cleanup(func() { readBuildVersion = saved })

	return &reads
}

// installLessonHooks runs the lesson-check setup of one client on root
// with binary as the hook binary.
func installLessonHooks(t *testing.T, root, clientID, binary, checks, exploration string) {
	t.Helper()

	applyLessonSetupWithBinary(t, root, binary, ClientSetup{
		ClientID: clientID, Hooks: true, LessonChecks: checks, LessonExploration: exploration,
	})
}

// inspectionOf returns the registry inspection of one client.
func inspectionOf(t *testing.T, root, clientID string) Inspection {
	t.Helper()

	for _, insp := range Builtin().Inspect(root) {
		if insp.ClientID == clientID {
			return insp
		}
	}

	require.Failf(t, "no inspection", "client %s", clientID)

	return Inspection{}
}

// installedPathOf returns the installed path of one tool, stage, and
// context.
func installedPathOf(t *testing.T, paths []lessoncheck.InstalledPath, tool string, stage lessoncheck.HookStage,
	context lessoncheck.PathContext,
) lessoncheck.InstalledPath {
	t.Helper()

	i := slices.IndexFunc(paths, func(p lessoncheck.InstalledPath) bool {
		return p.Tool == tool && p.Stage == stage && p.Context == context
	})
	require.GreaterOrEqual(t, i, 0, "%s %s %s", tool, stage, context)

	return paths[i]
}

func TestLessonCheckPathsSupplementalDocumentUnreadable(t *testing.T) {
	for _, clientID := range []string{ClaudeID, CodexID} {
		for _, failure := range []string{"parse", "read"} {
			t.Run(clientID+"/"+failure, func(t *testing.T) {
				root := t.TempDir()
				binary := fakeSeamark(t, t.TempDir(), "self")
				self := BinaryIdentity{Path: binary, Version: "v0.9.0"}
				installLessonHooks(t, root, clientID, binary, hooks.LessonChecksEnforce, "")
				writeRel(t, root, ".seamark/lessons.yaml", lessonPatternYAML)

				before := LessonCapability([]Inspection{inspectionOf(t, root, clientID)}, root, self)
				absent, ok := before.Client(clientID)
				require.True(t, ok)
				rel := claudeLocalSettings
				if clientID == CodexID {
					rel = ".codex/config.toml"
				}

				if failure == "parse" {
					writeRel(t, root, rel, "BROKEN [[")
				} else {
					require.NoError(t, os.MkdirAll(filepath.Join(root, rel), 0o755))
				}

				insp := inspectionOf(t, root, clientID)
				assert.Empty(t, insp.HookDocumentError, "the primary hook document is still readable")
				report := LessonCapability([]Inspection{insp}, root, self)
				client, ok := report.Client(clientID)
				require.True(t, ok)
				assert.NotEqual(t, absent.Paths, client.Paths, "a failed read is not an absent supplemental source")
				for _, path := range client.Paths {
					if path.HookStage == lessoncheck.StagePreWrite || path.HookStage == lessoncheck.StagePostEdit {
						assert.Equal(t, lessoncheck.PathInactive, path.State)
						assert.Contains(t, path.Reasons, lessoncheck.CapDocumentUnreadable)
						assert.NotContains(t, path.Reasons, lessoncheck.CapNotInstalled)
					}
				}
				require.Len(t, report.Pins, 1)
				assert.Equal(t, lessoncheck.BadgeRequestedInactive, report.Pins[0].Clients[0].Badge)
			})
		}
	}
}

// pathKey names one path for a list comparison.
func pathKey(p lessoncheck.InstalledPath) string {
	return p.Client + " " + string(p.Stage) + " " + p.Tool + " " + string(p.Context)
}

// TestLessonCheckPathsIdentifiesHookBinary proves the static binary
// identification. The same file or the same version is current. A
// readable version at or above MinHandlerRelease is other, and one below
// it is old. No readable version is unknown. No file or no execute bit
// is missing. Nothing runs, a non-regular file is never opened, and each
// path appears once per client, tool, hook stage, and context.
func TestLessonCheckPathsIdentifiesHookBinary(t *testing.T) {
	dir := t.TempDir()
	self := BinaryIdentity{Path: fakeSeamark(t, dir, "self"), Version: "v0.9.2"}

	newer := fakeSeamark(t, dir, "newer")
	older := fakeSeamark(t, dir, "older")
	described := fakeSeamark(t, dir, "described")
	sameVersion := fakeSeamark(t, dir, "same-version")
	devel := fakeSeamark(t, dir, "devel")
	unparsed := fakeSeamark(t, dir, "unparsed")
	plain := fakeSeamark(t, dir, "plain")
	require.NoError(t, os.Chmod(plain, 0o644))

	reads := stubBuildVersions(t, map[string]string{
		newer: "v0.10.0", older: "v0.8.0", described: "v0.9.0-3-gabc1234-dirty", sameVersion: "v0.9.2",
		unparsed: "dev",
	})

	cases := []struct {
		name, binary, state, version string
	}{
		{"the running binary itself", self.Path, lessoncheck.BinaryCurrent, "v0.9.2"},
		{"a copy with the same version", sameVersion, lessoncheck.BinaryCurrent, "v0.9.2"},
		{"a newer release", newer, lessoncheck.BinaryOther, "v0.10.0"},
		{"a describe suffix counts as after its tag", described, lessoncheck.BinaryOther, "v0.9.0-3-gabc1234-dirty"},
		{"a release below MinHandlerRelease", older, lessoncheck.BinaryOld, "v0.8.0"},
		{"no readable version", devel, lessoncheck.BinaryUnknown, ""},
		{"a version without a release core", unparsed, lessoncheck.BinaryUnknown, "dev"},
		{"no execute bit", plain, lessoncheck.BinaryMissing, ""},
		{"no file", filepath.Join(dir, "gone", "seamark"), lessoncheck.BinaryMissing, ""},
		{"a path relative to the environment of the client", "seamark", lessoncheck.BinaryUnknown, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := identifyBinary(c.binary, self)
			assert.Equal(t, binaryReading{state: c.state, version: c.version}, got)
		})
	}

	assert.NotContains(t, *reads, self.Path, "the same file needs no read of its content")
	assert.NotContains(t, *reads, plain, "a file without an execute bit is never opened")

	if runtime.GOOS != "windows" {
		t.Run("a FIFO is never opened", func(t *testing.T) {
			fifo := filepath.Join(dir, "fifo", "seamark")
			require.NoError(t, os.MkdirAll(filepath.Dir(fifo), 0o755))
			require.NoError(t, syscall.Mkfifo(fifo, 0o755))

			assert.Equal(t, lessoncheck.BinaryMissing, identifyBinary(fifo, self).state)
			assert.NotContains(t, *reads, fifo)
		})
	}

	t.Run("the build version comes from -ldflags, else the module version", func(t *testing.T) {
		withFlags := &debug.BuildInfo{Settings: []debug.BuildSetting{{
			Key: "-ldflags", Value: "-X github.com/seamark-dev/seamark/internal/cli.version=v0.9.1-2-gdeadbee -s",
		}}}
		withFlags.Main.Version = "v0.0.1"
		assert.Equal(t, "v0.9.1-2-gdeadbee", buildVersionOf(withFlags))

		module := &debug.BuildInfo{}
		module.Main.Version = "v0.9.3"
		assert.Equal(t, "v0.9.3", buildVersionOf(module))

		module.Main.Version = "(devel)"
		assert.Empty(t, buildVersionOf(module))

		saved := readBuildVersion
		t.Cleanup(func() { readBuildVersion = saved })

		readBuildVersion = saved
		assert.Empty(t, readBuildVersion(devel), "a file without Go build information gives no version")
	})

	t.Run("the pre-write markers start with the report constants", func(t *testing.T) {
		for _, marker := range []string{hooks.LessonGuardMarker, hooks.CodexLessonGuardMarker} {
			assert.True(t, lessoncheck.IsGuardMarker(marker), marker)
		}

		for _, marker := range []string{hooks.LessonBaselineMarker, hooks.CodexLessonBaselineMarker} {
			assert.True(t, lessoncheck.IsBaselineMarker(marker), marker)
		}
	})

	t.Run("one path per client, tool, hook stage, and context, with the hook binary", func(t *testing.T) {
		root := t.TempDir()
		installLessonHooks(t, root, ClaudeID, older, hooks.LessonChecksEnforce, hooks.LessonExplorationOn)
		installLessonHooks(t, root, CodexID, self.Path, hooks.LessonChecksCheck, "")

		claude := LessonCheckPaths(inspectionOf(t, root, ClaudeID), self)

		var keys []string
		for _, p := range claude {
			keys = append(keys, pathKey(p))
		}

		var want []string

		for _, stage := range []string{"pre-write", "post-edit"} {
			for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
				want = append(want, "claude "+stage+" "+tool+" root", "claude "+stage+" "+tool+" child")
			}
		}

		want = append(want, "claude exploration Read root", "claude exploration Read child",
			"claude  NotebookEdit root", "claude  NotebookEdit child", "claude  Bash root", "claude  Bash child")
		assert.Equal(t, want, keys)

		guard := installedPathOf(t, claude, "Write", lessoncheck.StagePreWrite, lessoncheck.ContextChild)
		assert.True(t, guard.Installed)
		assert.Equal(t, hooks.LessonGuardMarker, guard.Marker)
		assert.Equal(t, older, guard.Binary)
		assert.Equal(t, lessoncheck.BinaryOld, guard.BinaryState)
		assert.Equal(t, "v0.8.0", guard.BinaryVersion)
		assert.Equal(t, capVerification(GuardEvidence(ClaudeID, "Write", lessoncheck.ContextChild)), guard.Verification)

		codex := LessonCheckPaths(inspectionOf(t, root, CodexID), self)
		baseline := installedPathOf(t, codex, "apply_patch", lessoncheck.StagePreWrite, lessoncheck.ContextRoot)
		assert.Equal(t, hooks.CodexLessonBaselineMarker, baseline.Marker)
		assert.Equal(t, lessoncheck.BinaryCurrent, baseline.BinaryState)

		explore := installedPathOf(t, codex, "Read", lessoncheck.StageExploration, lessoncheck.ContextRoot)
		assert.False(t, explore.Installed)
		assert.Empty(t, explore.Reasons, "a path that Codex cannot run has no reason, so the report reads it as unsupported")

		report := LessonCapability(Builtin().Inspect(root), root, self)
		c, ok := report.Client(CodexID)
		require.True(t, ok)
		assert.Equal(t, lessoncheck.InstalledCheck, c.Installed)
		assert.False(t, c.HasStage(lessoncheck.StageExploration))
		assert.Contains(t, report.Uncovered, lessoncheck.UncoveredWriter{Client: CodexID, Tool: "shell", Context: lessoncheck.ContextRoot})

		claudeCap, _ := report.Client(ClaudeID)
		assert.Equal(t, lessoncheck.InstalledEnforce, claudeCap.Installed)
		assert.Equal(t, "pre-write Edit, Write, MultiEdit inactive (capability-version-old, native-deny-unverified, "+
			"native-timeout-unverified, other-binary)", claudeCap.StageText(lessoncheck.StagePreWrite))
	})

	t.Run("one report reads a hook binary of several clients once", func(t *testing.T) {
		root := t.TempDir()
		shared := fakeSeamark(t, dir, "shared")
		installLessonHooks(t, root, ClaudeID, shared, hooks.LessonChecksCheck, "")
		installLessonHooks(t, root, CodexID, shared, hooks.LessonChecksCheck, "")

		insps := Builtin().Inspect(root)
		before := len(*reads)

		report := LessonCapability(insps, root, self)
		assert.Equal(t, []string{shared}, (*reads)[before:], "the clients share one reading of the binary")

		for _, clientID := range []string{ClaudeID, CodexID} {
			c, ok := report.Client(clientID)
			require.True(t, ok, clientID)
			assert.Equal(t, lessoncheck.InstalledCheck, c.Installed, clientID)
		}
	})

	t.Run("a partial install and a narrowed matcher", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", `{"hooks": {
  "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "`+self.Path+` lessons --guard --hook"}]}],
  "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "`+self.Path+` lessons --verify --hook"}]}]
}}
`)

		paths := LessonCheckPaths(inspectionOf(t, root, ClaudeID), self)

		edit := installedPathOf(t, paths, "Edit", lessoncheck.StagePreWrite, lessoncheck.ContextRoot)
		assert.True(t, edit.Installed)
		assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapPartialInstall}, edit.Reasons,
			"PostToolUseFailure has no verify hook, so the pair is partial")

		write := installedPathOf(t, paths, "Write", lessoncheck.StagePreWrite, lessoncheck.ContextRoot)
		assert.False(t, write.Installed)
		assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapToolUncovered}, write.Reasons, "the matcher omits Write")

		post := installedPathOf(t, paths, "Write", lessoncheck.StagePostEdit, lessoncheck.ContextChild)
		assert.True(t, post.Installed)
		assert.Contains(t, post.Reasons, lessoncheck.CapPartialInstall)

		explore := installedPathOf(t, paths, "Read", lessoncheck.StageExploration, lessoncheck.ContextRoot)
		assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapNotInstalled}, explore.Reasons)
	})
}

// TestLessonCheckPathsDetectsReportOnlyWrapper proves that a definition
// that runs the pre-write hook and discards its exit status keeps every
// pre-write path inactive, with reason report-only-wrapper. The rule
// holds in either Claude Code settings file, in hooks.json, and in the
// inline [hooks] of the Codex configuration. A wrapper that passes the
// status is no such definition. The reading adds no finding, so doctor
// and status keep their existing lines.
func TestLessonCheckPathsDetectsReportOnlyWrapper(t *testing.T) {
	self := BinaryIdentity{Path: fakeSeamark(t, t.TempDir(), "self")}

	wrapper := func(command string) string {
		return `{"hooks": {"PreToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": ` +
			`"` + command + `"}]}]}}` + "\n"
	}

	discarding := self.Path + " lessons --guard --hook || true"
	passing := "/usr/bin/env " + self.Path + " lessons --guard --hook"

	preWriteReasons := func(t *testing.T, insp Inspection) [][]lessoncheck.CapReason {
		t.Helper()

		paths := LessonCheckPaths(insp, self)

		var out [][]lessoncheck.CapReason

		for _, p := range paths {
			if p.Stage == lessoncheck.StagePreWrite {
				out = append(out, p.Reasons)
			}
		}

		require.NotEmpty(t, out)

		return out
	}

	t.Run("a wrapper in either Claude Code settings file", func(t *testing.T) {
		for _, rel := range []string{".claude/settings.json", claudeLocalSettings} {
			root := t.TempDir()
			writeRel(t, root, rel, wrapper(discarding))

			bare := inspectionOf(t, t.TempDir(), ClaudeID)
			insp := inspectionOf(t, root, ClaudeID)
			require.NotNil(t, insp.LessonHooks, rel)
			assert.True(t, insp.LessonHooks.ReportOnly, rel)
			assert.Empty(t, insp.LessonHooks.Entries, "a wrapper is not an owned entry")
			assert.Equal(t, bare.Findings, insp.Findings, "%s: the reading adds no finding", rel)

			for _, reasons := range preWriteReasons(t, insp) {
				assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapNotInstalled, lessoncheck.CapReportOnlyWrapper}, reasons, rel)
			}

			report := LessonCapability([]Inspection{insp}, root, self)
			c, _ := report.Client(ClaudeID)
			assert.Contains(t, c.StageText(lessoncheck.StagePreWrite), "inactive (not-installed, report-only-wrapper)", rel)
		}
	})

	t.Run("beside an owned guard every pre-write path is inactive", func(t *testing.T) {
		root := t.TempDir()
		installLessonHooks(t, root, ClaudeID, self.Path, hooks.LessonChecksEnforce, "")
		writeRel(t, root, claudeLocalSettings, wrapper(discarding))

		report := LessonCapability(Builtin().Inspect(root), root, self)
		c, _ := report.Client(ClaudeID)

		for _, p := range c.Paths {
			if p.HookStage == lessoncheck.StagePreWrite {
				assert.Equal(t, lessoncheck.PathInactive, p.State)
				assert.Contains(t, p.Reasons, lessoncheck.CapReportOnlyWrapper)
			}
		}
	})

	t.Run("a wrapper that passes the status is not report-only", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", wrapper(passing))

		assert.Nil(t, inspectionOf(t, root, ClaudeID).LessonHooks, "no owned entry and no report-only wrapper")
	})

	t.Run("a wrapper in hooks.json or in the inline Codex hooks", func(t *testing.T) {
		codexDiscarding := self.Path + " lessons --guard --hook --client codex || true"
		codexWrapper := `{"hooks": {"PreToolUse": [{"matcher": "apply_patch", "hooks": [{"type": "command", "command": "` +
			codexDiscarding + `"}]}]}}` + "\n"

		root := t.TempDir()
		writeRel(t, root, ".codex/hooks.json", codexWrapper)

		insp := inspectionOf(t, root, CodexID)
		require.NotNil(t, insp.LessonHooks)
		assert.True(t, insp.LessonHooks.ReportOnly)

		inline := t.TempDir()
		writeRel(t, inline, ".codex/config.toml", "[[hooks.PreToolUse]]\nmatcher = \"apply_patch\"\n\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \""+codexDiscarding+"\"\n")

		insp = inspectionOf(t, inline, CodexID)
		require.NotNil(t, insp.LessonHooks)
		assert.True(t, insp.LessonHooks.ReportOnly)

		for _, reasons := range preWriteReasons(t, insp) {
			assert.Contains(t, reasons, lessoncheck.CapReportOnlyWrapper)
		}
	})
}

// TestLessonCheckPathsReportsTrustAndDisabledHooks proves the sources
// of the trust and configuration reasons. Every installed Codex path
// carries trust-unknown, because seamark cannot read the Codex trust
// record. [features] hooks = false gives hooks-disabled. A pending trust
// gives untrusted. A hook document that cannot be read gives
// document-unreadable, never not-installed. Claude Code needs no trust
// review, so its paths carry no trust reason.
func TestLessonCheckPathsReportsTrustAndDisabledHooks(t *testing.T) {
	self := BinaryIdentity{Path: fakeSeamark(t, t.TempDir(), "self")}

	t.Run("codex paths carry trust-unknown, claude paths no trust reason", func(t *testing.T) {
		root := t.TempDir()
		installLessonHooks(t, root, CodexID, self.Path, hooks.LessonChecksEnforce, "")
		installLessonHooks(t, root, ClaudeID, self.Path, hooks.LessonChecksEnforce, "")

		codex := LessonCheckPaths(inspectionOf(t, root, CodexID), self)
		for _, p := range codex {
			if p.Installed {
				assert.Contains(t, p.Reasons, lessoncheck.CapTrustUnknown, pathKey(p))
			}
		}

		claude := LessonCheckPaths(inspectionOf(t, root, ClaudeID), self)
		for _, p := range claude {
			assert.NotContains(t, p.Reasons, lessoncheck.CapTrustUnknown, pathKey(p))
		}

		report := LessonCapability(Builtin().Inspect(root), root, self)
		c, _ := report.Client(CodexID)
		post := c.Paths[slices.IndexFunc(c.Paths, func(p lessoncheck.PathCapability) bool {
			return p.HookStage == lessoncheck.StagePostEdit
		})]
		assert.Equal(t, lessoncheck.PathActiveUnverified, post.State, "trust-unknown caps the path at active-unverified")
	})

	t.Run("hooks = false in the project configuration", func(t *testing.T) {
		root := t.TempDir()
		installLessonHooks(t, root, CodexID, self.Path, hooks.LessonChecksCheck, "")
		writeRel(t, root, ".codex/config.toml", "[features]\nhooks = false\n")

		insp := inspectionOf(t, root, CodexID)
		require.NotNil(t, insp.LessonHooks)
		assert.True(t, insp.LessonHooks.HooksDisabled)

		report := LessonCapability([]Inspection{insp}, root, self)
		c, _ := report.Client(CodexID)

		for _, p := range c.Paths {
			if p.HookStage == lessoncheck.StagePreWrite || p.HookStage == lessoncheck.StagePostEdit {
				assert.Equal(t, lessoncheck.PathInactive, p.State)
				assert.Contains(t, p.Reasons, lessoncheck.CapHooksDisabled)
			}
		}
	})

	t.Run("a pending trust", func(t *testing.T) {
		root := t.TempDir()
		installLessonHooks(t, root, CodexID, self.Path, hooks.LessonChecksCheck, "")

		// No adapter sets a pending trust today, so the test sets it on the
		// inspection.
		insp := inspectionOf(t, root, CodexID)
		i := slices.IndexFunc(insp.Capabilities, func(c CapabilityInspection) bool { return c.Capability == CapabilityEdits })
		insp.Capabilities[i].Trust = TrustPending

		paths := LessonCheckPaths(insp, self)
		for _, p := range paths {
			if p.Installed {
				assert.Contains(t, p.Reasons, lessoncheck.CapUntrusted, pathKey(p))
				assert.NotContains(t, p.Reasons, lessoncheck.CapTrustUnknown, pathKey(p))
			}
		}
	})

	t.Run("a hook document that cannot be read", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".codex/hooks.json", "{not json")

		insp := inspectionOf(t, root, CodexID)
		require.NotEmpty(t, insp.HookDocumentError)

		paths := LessonCheckPaths(insp, self)
		for _, p := range paths {
			switch p.Stage {
			case lessoncheck.StagePreWrite, lessoncheck.StagePostEdit:
				assert.Equal(t, []lessoncheck.CapReason{lessoncheck.CapDocumentUnreadable}, slices.DeleteFunc(slices.Clone(p.Reasons),
					func(r lessoncheck.CapReason) bool { return r == lessoncheck.CapContentUnprojectable }), pathKey(p))
				assert.False(t, p.Installed)
			case lessoncheck.StageExploration:
				assert.Empty(t, p.Reasons, "Codex has no exploration path to read")
			}
		}
	})
}

// TestLessonCheckPathsAttributions proves the attributions, one per
// item and context. No attribution is verified, because no reviewed
// recording exists. The identity items are unsupported for the Codex
// child lane. The turn items are unsupported for a payload without a
// turn identity (prompt_id). context-reset copies the evidence of the
// resets capability. It is unsupported where no verified reset reaches
// the context.
func TestLessonCheckPathsAttributions(t *testing.T) {
	root := t.TempDir()

	for _, clientID := range []string{ClaudeID, CodexID} {
		insp := inspectionOf(t, root, clientID)

		list := attributionsOf(insp)
		require.Len(t, list, len(lessoncheck.AttributionItems)*len(lessonContexts), clientID)

		resets, _ := insp.Entry(CapabilityResets)

		for _, a := range list {
			label := clientID + " " + string(a.Item) + " " + string(a.Context)

			assert.NotEqual(t, lessoncheck.AttrVerified, a.State, "%s: no reviewed recording exists", label)

			if a.Item == lessoncheck.ItemContextReset {
				assert.Equal(t, capVerification(resets.Verification), a.Verification, "%s copies the resets evidence", label)
			} else {
				assert.Equal(t, capVerification(TimingEvidence(clientID, a.Item, a.Context)), a.Verification, label)
			}

			unsupported := a.Item == lessoncheck.ItemContextReset && (clientID == CodexID || a.Context == lessoncheck.ContextChild) ||
				clientID == CodexID && a.Context == lessoncheck.ContextChild &&
					(a.Item == lessoncheck.ItemMatchIdentity || a.Item == lessoncheck.ItemLaneIdentity)

			want := lessoncheck.AttrUnverified
			if unsupported {
				want = lessoncheck.AttrUnsupported
			}

			assert.Equal(t, want, a.State, label)
		}
	}

	t.Run("timing evidence is pending without a client version", func(t *testing.T) {
		e := TimingEvidence(ClaudeID, lessoncheck.ItemTurnBoundary, lessoncheck.ContextRoot)
		assert.Equal(t, VerificationPending, e.Level)
		assert.Empty(t, e.ClientVersion, "the reviewed recording sets the version it ran against")
		assert.NotEmpty(t, e.Surface)
		require.NoError(t, e.Validate())

		assert.Equal(t, VerificationEvidence{}, TimingEvidence(CodexID, lessoncheck.ItemLaneIdentity, lessoncheck.ContextChild))
		assert.Equal(t, VerificationEvidence{}, TimingEvidence("other", lessoncheck.ItemTurnBoundary, lessoncheck.ContextRoot))
	})

	t.Run("a payload without prompt_id gives no turn items", func(t *testing.T) {
		const id = "no-turn"

		payloadIdentities[id] = map[lessoncheck.PathContext][]lessoncheck.AttributionItem{
			lessoncheck.ContextRoot:  {lessoncheck.ItemMatchIdentity, lessoncheck.ItemLaneIdentity},
			lessoncheck.ContextChild: {lessoncheck.ItemMatchIdentity, lessoncheck.ItemLaneIdentity},
		}
		t.Cleanup(func() { delete(payloadIdentities, id) })

		insp := Inspection{ClientID: id}
		assert.Empty(t, LessonCheckPaths(insp, BinaryIdentity{}), "a client without lesson-check hooks has no path")

		for _, a := range attributionsOf(insp) {
			switch a.Item {
			case lessoncheck.ItemTurnBoundary, lessoncheck.ItemModelBoundary, lessoncheck.ItemContextReset:
				assert.Equal(t, lessoncheck.AttrUnsupported, a.State, "%s %s", a.Item, a.Context)
			default:
				assert.Equal(t, lessoncheck.AttrUnverified, a.State, "%s %s", a.Item, a.Context)
			}
		}

		// The report carries the attributions of a client without
		// lesson-check hooks, because the delivery report reads them for
		// the advisory hooks too.
		report := LessonCapabilityOf([]Inspection{insp}, nil, nil, BinaryIdentity{})

		c, ok := report.Client(id)
		require.True(t, ok, id)
		assert.Equal(t, attributionsOf(insp), c.Attributions)
	})

	t.Run("the report copies the attributions unchanged", func(t *testing.T) {
		insps := Builtin().Inspect(root)
		report := LessonCapability(insps, root, BinaryIdentity{})

		for _, insp := range insps {
			attributions := attributionsOf(insp)
			if len(attributions) == 0 {
				continue
			}

			c, ok := report.Client(insp.ClientID)
			require.True(t, ok, insp.ClientID)
			assert.Equal(t, attributions, c.Attributions)
		}
	})
}
