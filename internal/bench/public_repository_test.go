package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicRepositoryPrepareAndGenerate(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	require.NoError(t, generateRepository(origin, []step{{
		message: "base",
		files: map[string]string{
			"module/go.mod":     "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
			"module/main.go":    "package app\n\nimport _ \"example.com/dep\"\n",
			"module/dep/go.mod": "module example.com/dep\n\ngo 1.25\n",
			"module/dep/dep.go": "package dep\n",
		},
	}}))
	commit := fixtureHead(origin)

	require.NoError(t, runPreparationCommand(context.Background(), filepath.Join(origin, "module"),
		"go", "mod", "vendor"))
	vendorSHA, err := treeSHA256(filepath.Join(origin, "module", "vendor"))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(origin, "module", "vendor")))

	cacheRoot := filepath.Join(root, "cache")
	t.Setenv(publicBenchmarkCacheEnv, cacheRoot)
	spec := publicRepositorySpec{
		InstanceID: "test-public-instance",
		CacheKey:   "test-public-cache",
		URL:        origin,
		Commit:     commit,
		ModuleDir:  "module",
		VendorSHA:  vendorSHA,
	}

	prepared, err := spec.prepare(context.Background())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cacheRoot, spec.CacheKey), prepared)
	require.NoError(t, spec.validateCache(prepared))

	generated := filepath.Join(root, "generated")
	require.NoError(t, spec.generate(generated))
	assert.Equal(t, commit, fixtureHead(generated))
	assert.FileExists(t, filepath.Join(generated, "module", "vendor", "example.com", "dep", "dep.go"))

	status, err := exec.Command("git", "-C", generated, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, status)
	originURL, err := exec.Command("git", "-C", generated, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.Equal(t, origin+"\n", string(originURL))
}

func TestTreeSHA256BindsPathsAndContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("beta"), 0o644))

	first, err := treeSHA256(root)
	require.NoError(t, err)
	second, err := treeSHA256(root)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "b.txt"), []byte("changed"), 0o644))
	changed, err := treeSHA256(root)
	require.NoError(t, err)
	assert.NotEqual(t, first, changed)

	renamedPath := filepath.Join(root, "nested", "renamed.txt")
	require.NoError(t, os.Rename(filepath.Join(root, "nested", "b.txt"), renamedPath))
	renamed, err := treeSHA256(root)
	require.NoError(t, err)
	assert.NotEqual(t, changed, renamed, "moving unchanged content must change the tree digest")
}

func TestPreparationEnvironmentIgnoresHostGitConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_GLOBAL", "/host/gitconfig")
	t.Setenv("GIT_CONFIG_COUNT", "1")

	env := environmentByKey(t, preparationEnvironment())
	assert.Equal(t, "1", env["GIT_CONFIG_NOSYSTEM"])
	assert.Equal(t, os.DevNull, env["GIT_CONFIG_GLOBAL"])
	assert.NotContains(t, env, "GIT_CONFIG_COUNT")
}
