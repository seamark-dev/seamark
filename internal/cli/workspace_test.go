package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// These tests pin how each command finds and opens the index database.
// They held before the commands used internal/workspace, and they must
// keep holding after it. They cover the error texts and the commands
// that accept a missing index. They also cover the migrating opens, the
// --db rules, and the recorded repo_root (A0 defect D15).

// noIndexText is the refusal of every command that needs an index run.
// It repeats the text of errNoIndex, so a change to that text fails here.
const noIndexText = "no index found; run `seamark index` first"

// recordedRoot is a repo_root value that names no directory. A root
// equal to it can only come from the database meta.
const recordedRoot = "/nonexistent/recorded-root"

// gitWorkspace makes a git repository with a nested directory. A -C
// value of the nested directory shows whether a command widens it to
// the git top level.
func gitWorkspace(t *testing.T) (top, sub string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	top = t.TempDir()

	out, err := exec.Command("git", "init", "-q", top).CombinedOutput()
	require.NoError(t, err, string(out))

	sub = filepath.Join(top, "pkg", "inner")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	return top, sub
}

// writeIndexDB creates a database at path with the current schema and
// sets the meta values. A schema_version value stamps another version,
// because store.Open accepts any meta key.
func writeIndexDB(t *testing.T, path string, meta map[string]string) {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	for k, v := range meta {
		require.NoError(t, st.SetMeta(k, v))
	}

	require.NoError(t, st.Close())
}

// writeGarbageDB writes a file at path that SQLite cannot read.
func writeGarbageDB(t *testing.T, path string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("this is not a SQLite database, only text bytes"), 0o644))
}

// writeUnreadableDB writes an indexed database that this user cannot
// open. The test skips as root, because root ignores the file mode.
func writeUnreadableDB(t *testing.T, path string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root reads a file with mode 0000")
	}

	writeIndexDB(t, path, map[string]string{store.MetaIndexedAt: "1"})
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
}

// schemaMeta returns the meta value that stamps the supported schema
// version plus delta.
func schemaMeta(delta int) map[string]string {
	return map[string]string{store.MetaSchemaVersion: strconv.Itoa(store.SupportedSchema() + delta)}
}

// storeOpenErr returns the error text of store.Open for path. A command
// that opens through store.Open must return this text unchanged. The
// cases that use it leave the file unchanged on a failed open.
func storeOpenErr(t *testing.T, path string) string {
	t.Helper()

	st, err := store.Open(path)
	if err == nil {
		_ = st.Close()
	}

	require.Error(t, err, "the case expects store.Open to fail")

	return err.Error()
}

// assertSchemaSupported checks that a migrating open upgraded the file.
func assertSchemaSupported(t *testing.T, path string) {
	t.Helper()

	v, err := store.ProbeVersion(path)
	require.NoError(t, err)
	assert.Equal(t, store.SupportedSchema(), v, "store.Open migrates an older schema")
}

// assertSameDir compares two directory paths after symlink resolution.
// The macOS temporary directory is a symbolic link, and git prints its
// target. A path that does not exist is compared as written.
func assertSameDir(t *testing.T, want, got string) {
	t.Helper()

	w, errW := filepath.EvalSymlinks(want)
	g, errG := filepath.EvalSymlinks(got)

	if errW != nil || errG != nil {
		assert.Equal(t, want, got)

		return
	}

	assert.Equal(t, w, g)
}

// resolvedPaths returns the database path and the root that the index
// commands (openIndex, doctor, state) derive from the options.
func resolvedPaths(t *testing.T, opts *options) (dbPath, root string) {
	t.Helper()

	ws, err := resolveWorkspace(opts)
	require.NoError(t, err)

	return ws.DBPath, ws.Root
}

func TestCLIWorkspaceResolveRules(t *testing.T) {
	top, sub := gitWorkspace(t)

	t.Run("the default database widens -C to the git top level", func(t *testing.T) {
		dbPath, root := resolvedPaths(t, &options{workspace: sub})

		assertSameDir(t, top, root)
		assert.Equal(t, store.DefaultPath(root), dbPath)
	})

	t.Run("an empty -C means the current directory", func(t *testing.T) {
		t.Chdir(sub)

		dbPath, root := resolvedPaths(t, &options{})

		assertSameDir(t, top, root)
		assert.Equal(t, store.DefaultPath(root), dbPath)
	})

	t.Run("--db keeps the -C directory as the root and the path as given", func(t *testing.T) {
		dbPath, root := resolvedPaths(t, &options{workspace: sub, dbPath: filepath.Join("rel", "x.db")})

		assert.Equal(t, sub, root, "with --db the root is the absolute -C directory, not the top level")
		assert.Equal(t, filepath.Join("rel", "x.db"), dbPath, "a relative --db stays relative")
	})

	t.Run("a missing -C directory is no error", func(t *testing.T) {
		missing := filepath.Join(top, "missing")

		dbPath, root := resolvedPaths(t, &options{workspace: missing})

		assert.Equal(t, missing, root)
		assert.Equal(t, store.DefaultPath(missing), dbPath)
		assert.NoDirExists(t, missing, "resolution creates nothing")
	})

	t.Run("a regular file as -C is no error", func(t *testing.T) {
		file := filepath.Join(top, "file.txt")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

		dbPath, root := resolvedPaths(t, &options{workspace: file})

		assert.Equal(t, file, root)
		assert.Equal(t, store.DefaultPath(file), dbPath)
	})
}

// openCase is one database state for openIndex or openHookIndex. The
// workspace is a git repository, and -C names its nested directory
// unless setup says otherwise.
type openCase struct {
	name string
	// setup builds the state and returns the command options.
	setup func(t *testing.T, top, sub string) *options
	// err returns the exact error text; nil means that the open succeeds.
	err func(t *testing.T, top, sub string) string
	// root returns the root that a successful open returns.
	root func(top, sub string) string
	// after checks the files after the open.
	after func(t *testing.T, top, sub string)
}

// defaultDB returns the options for -C sub and the default database.
func defaultDB(_ *testing.T, _, sub string) *options {
	return &options{workspace: sub}
}

// noIndexErr is the err function of a refusal with noIndexText.
func noIndexErr(*testing.T, string, string) string { return noIndexText }

// defaultOpenErr is the err function of a failure that store.Open
// reports for the default database.
func defaultOpenErr(t *testing.T, top, _ string) string {
	return storeOpenErr(t, store.DefaultPath(top))
}

// topRoot and recorded are root functions.
func topRoot(top, _ string) string { return top }

func recorded(string, string) string { return recordedRoot }

func TestCLIWorkspaceOpenIndexCases(t *testing.T) {
	cases := []openCase{
		{
			name:  "no database file is refused and nothing is created",
			setup: defaultDB,
			err:   noIndexErr,
			after: func(t *testing.T, top, sub string) {
				assert.NoDirExists(t, filepath.Join(top, store.DefaultDir))
				assert.NoDirExists(t, filepath.Join(sub, store.DefaultDir))
			},
		},
		{
			name: "a database path that cannot be stat'ed is refused as missing",
			setup: func(t *testing.T, top, sub string) *options {
				file := filepath.Join(top, "file.txt")
				require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

				return &options{workspace: sub, dbPath: filepath.Join(file, "index.db")}
			},
			err: noIndexErr,
		},
		{
			name: "a missing -C directory is refused as missing",
			setup: func(_ *testing.T, top, _ string) *options {
				return &options{workspace: filepath.Join(top, "missing")}
			},
			err: noIndexErr,
			after: func(t *testing.T, top, _ string) {
				assert.NoDirExists(t, filepath.Join(top, "missing"))
			},
		},
		{
			name: "a database without an index run is refused",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), nil)

				return defaultDB(t, top, sub)
			},
			err: noIndexErr,
		},
		{
			name: "an older database without an index run is migrated and refused",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), schemaMeta(-1))

				return defaultDB(t, top, sub)
			},
			err: noIndexErr,
			after: func(t *testing.T, top, _ string) {
				assertSchemaSupported(t, store.DefaultPath(top))
			},
		},
		{
			name: "the recorded repo_root replaces the -C root",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), map[string]string{
					store.MetaIndexedAt: "1", store.MetaRepoRoot: recordedRoot,
				})

				return defaultDB(t, top, sub)
			},
			root: recorded,
		},
		{
			name: "without a recorded repo_root the root is the git top level",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), map[string]string{store.MetaIndexedAt: "1"})

				return defaultDB(t, top, sub)
			},
			root: topRoot,
		},
		{
			name: "with --db and no recorded repo_root the root is the -C directory",
			setup: func(t *testing.T, _, sub string) *options {
				db := filepath.Join(t.TempDir(), "elsewhere.db")
				writeIndexDB(t, db, map[string]string{store.MetaIndexedAt: "1"})

				return &options{workspace: sub, dbPath: db}
			},
			root: func(_, sub string) string { return sub },
		},
		{
			name: "an older indexed database is migrated and opened with its recorded repo_root",
			setup: func(t *testing.T, top, sub string) *options {
				meta := schemaMeta(-1)
				meta[store.MetaIndexedAt] = "1"
				meta[store.MetaRepoRoot] = recordedRoot
				writeIndexDB(t, store.DefaultPath(top), meta)

				return defaultDB(t, top, sub)
			},
			root: recorded,
			after: func(t *testing.T, top, _ string) {
				assertSchemaSupported(t, store.DefaultPath(top))
			},
		},
		{
			name: "a newer database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				meta := schemaMeta(+1)
				meta[store.MetaIndexedAt] = "1"
				writeIndexDB(t, store.DefaultPath(top), meta)

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
		{
			name: "a file that is not a database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				writeGarbageDB(t, store.DefaultPath(top))

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
		{
			name: "an unreadable database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				writeUnreadableDB(t, store.DefaultPath(top))

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, sub := gitWorkspace(t)
			opts := tc.setup(t, top, sub)

			st, root, err := openIndex(opts)

			if tc.err != nil {
				require.Error(t, err)
				assert.Nil(t, st)
				assert.Equal(t, tc.err(t, top, sub), err.Error())
			} else {
				require.NoError(t, err)
				require.NotNil(t, st)
				require.NoError(t, st.Close())
				assertSameDir(t, tc.root(top, sub), root)
			}

			if tc.after != nil {
				tc.after(t, top, sub)
			}
		})
	}
}

func TestCLIWorkspaceOpenHookIndexCases(t *testing.T) {
	cases := []openCase{
		{
			name:  "no database file gets an in-memory index and creates nothing",
			setup: defaultDB,
			root:  topRoot,
			after: func(t *testing.T, top, sub string) {
				assert.NoDirExists(t, filepath.Join(top, store.DefaultDir))
				assert.NoDirExists(t, filepath.Join(sub, store.DefaultDir))
			},
		},
		{
			name: "a missing -C directory gets an in-memory index and creates nothing",
			setup: func(_ *testing.T, top, _ string) *options {
				return &options{workspace: filepath.Join(top, "missing")}
			},
			root: func(top, _ string) string { return filepath.Join(top, "missing") },
			after: func(t *testing.T, top, _ string) {
				assert.NoDirExists(t, filepath.Join(top, "missing"))
			},
		},
		{
			name: "a database path that cannot be stat'ed fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				file := filepath.Join(top, "file.txt")
				require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

				return &options{workspace: sub, dbPath: filepath.Join(file, "index.db")}
			},
			err: func(t *testing.T, top, _ string) string {
				return storeOpenErr(t, filepath.Join(top, "file.txt", "index.db"))
			},
		},
		{
			name: "a database without an index run opens with its recorded repo_root",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), map[string]string{store.MetaRepoRoot: recordedRoot})

				return defaultDB(t, top, sub)
			},
			root: recorded,
		},
		{
			name: "without a recorded repo_root the root is the git top level",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), nil)

				return defaultDB(t, top, sub)
			},
			root: topRoot,
		},
		{
			name: "with --db the -C directory still widens to the git top level",
			setup: func(t *testing.T, _, sub string) *options {
				db := filepath.Join(t.TempDir(), "elsewhere.db")
				writeIndexDB(t, db, nil)

				return &options{workspace: sub, dbPath: db}
			},
			root: topRoot,
		},
		{
			name: "an older database is migrated and opened with its recorded repo_root",
			setup: func(t *testing.T, top, sub string) *options {
				meta := schemaMeta(-1)
				meta[store.MetaRepoRoot] = recordedRoot
				writeIndexDB(t, store.DefaultPath(top), meta)

				return defaultDB(t, top, sub)
			},
			root: recorded,
			after: func(t *testing.T, top, _ string) {
				assertSchemaSupported(t, store.DefaultPath(top))
			},
		},
		{
			name: "a newer database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				writeIndexDB(t, store.DefaultPath(top), schemaMeta(+1))

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
		{
			name: "a file that is not a database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				writeGarbageDB(t, store.DefaultPath(top))

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
		{
			name: "an unreadable database fails with the store.Open error",
			setup: func(t *testing.T, top, sub string) *options {
				writeUnreadableDB(t, store.DefaultPath(top))

				return defaultDB(t, top, sub)
			},
			err: defaultOpenErr,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, sub := gitWorkspace(t)
			opts := tc.setup(t, top, sub)

			st, root, err := openHookIndex(opts)

			if tc.err != nil {
				require.Error(t, err)
				assert.Nil(t, st)
				assert.Equal(t, tc.err(t, top, sub), err.Error())
			} else {
				require.NoError(t, err)
				require.NotNil(t, st)

				_, err := st.Stats()
				require.NoError(t, err, "the returned store answers queries")
				require.NoError(t, st.Close())
				assertSameDir(t, tc.root(top, sub), root)
			}

			if tc.after != nil {
				tc.after(t, top, sub)
			}
		})
	}
}

func TestCLIWorkspaceStateExportCases(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, top string) []string
		// err returns the exact error text; nil means that export succeeds.
		err   func(t *testing.T, top string) string
		after func(t *testing.T, top string)
	}{
		{
			name:  "no database file is refused",
			setup: func(*testing.T, string) []string { return nil },
			err:   func(*testing.T, string) string { return noIndexText },
		},
		{
			name: "a database path that cannot be stat'ed is refused as missing",
			setup: func(t *testing.T, top string) []string {
				file := filepath.Join(top, "file.txt")
				require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

				return []string{"--db", filepath.Join(file, "index.db")}
			},
			err: func(*testing.T, string) string { return noIndexText },
		},
		{
			name: "a database without an index run is exported",
			setup: func(t *testing.T, top string) []string {
				writeIndexDB(t, store.DefaultPath(top), nil)

				return nil
			},
		},
		{
			name: "an older database is migrated and exported",
			setup: func(t *testing.T, top string) []string {
				writeIndexDB(t, store.DefaultPath(top), schemaMeta(-1))

				return nil
			},
			after: func(t *testing.T, top string) {
				assertSchemaSupported(t, store.DefaultPath(top))
			},
		},
		{
			name: "a file that is not a database fails with the store.Open error",
			setup: func(t *testing.T, top string) []string {
				writeGarbageDB(t, store.DefaultPath(top))

				return nil
			},
			err: func(t *testing.T, top string) string { return storeOpenErr(t, store.DefaultPath(top)) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, _ := gitWorkspace(t)
			args := append([]string{"-C", top}, tc.setup(t, top)...)

			out, err := run(t, append(args, "state", "export")...)

			if tc.err != nil {
				require.Error(t, err)
				assert.Empty(t, out)
				assert.Equal(t, tc.err(t, top), err.Error())
			} else {
				require.NoError(t, err)
				assert.Contains(t, out, "seamark_state_version")
			}

			if tc.after != nil {
				tc.after(t, top)
			}
		})
	}
}

func TestCLIWorkspaceCheckWithAMissingDirectory(t *testing.T) {
	// check resolves -C without a directory test. The index walk then
	// names the missing directory.
	missing := filepath.Join(t.TempDir(), "missing")

	out, err := run(t, "-C", missing, "check")
	require.Error(t, err)

	assert.Empty(t, out)
	assert.Equal(t, fmt.Sprintf("index: walk %s: lstat %s: no such file or directory", missing, missing), err.Error())
	assert.NoDirExists(t, missing)
}

func TestCLIWorkspaceMCPWithAMissingDirectory(t *testing.T) {
	// The server resolves -C without a directory test, so it starts and
	// ends cleanly at the end of its input.
	missing := filepath.Join(t.TempDir(), "missing")

	out, _, err := runIn(t, "", "-C", missing, "mcp")
	require.NoError(t, err)

	assert.Empty(t, out)
	assert.NoDirExists(t, missing)
}

func TestCLIWorkspaceDoctorPrintsTheDBFlagAsGiven(t *testing.T) {
	top, _ := gitWorkspace(t)
	writeIndexDB(t, store.DefaultPath(top), map[string]string{store.MetaIndexedAt: "1"})
	t.Chdir(top)

	rel := filepath.Join(store.DefaultDir, store.DefaultDBName)

	// doctor exits non-zero for a failed check; the JSON is still whole.
	out, _ := run(t, "-C", top, "--db", rel, "doctor", "--json")

	var report struct {
		Checks []struct{ Name, Detail string } `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	var detail string

	for _, c := range report.Checks {
		if c.Name == "index" {
			detail = c.Detail
		}
	}

	assert.Equal(t, fmt.Sprintf("%s (schema v%d)", rel, store.SupportedSchema()), detail,
		"doctor prints a relative --db as given")
}
