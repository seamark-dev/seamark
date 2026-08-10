package bench

import (
	"os"
	"path/filepath"
)

const (
	// CacheVersionInstanceID is the stable selector used by the benchmark CLI.
	CacheVersionInstanceID = "python-cache-version-v1"
	// CacheVersionRule is the treatment lesson's stable identity.
	CacheVersionRule = "bump-cached-response-version"

	cacheVersionTask = `Expose each workspace's locale in ` + "`GET /api/workspaces/{id}`" + ` responses. ` +
		`Return it as ` + "`locale`" + `, sourced from ` + "`Workspace.locale`" +
		`. Keep the change minimal and add or update tests as appropriate.`

	cacheVersionLessonYAML = `pin:
  - rule: ` + CacheVersionRule + `
    region: server
    note: When a cached workspace response shape changes, bump WORKSPACE_SUMMARY_VERSION in server/cache.py; old entries survive deploys and presenter tests bypass the cache.
`

	cacheVersionPlaceboYAML = `pin:
  - rule: keep-presenters-explicit
    region: server
    note: Keep public response construction explicit and preserve the neighboring snake_case wire-name convention.
`
)

// CacheVersionInstance models an owner-only compatibility rule: changing a
// cached response shape requires a namespace bump even though the presenter
// and its public tests pass without one.
func CacheVersionInstance() Instance {
	return Instance{
		ID:           CacheVersionInstanceID,
		Rule:         CacheVersionRule,
		Task:         cacheVersionTask,
		LessonYAML:   cacheVersionLessonYAML,
		PlaceboYAML:  cacheVersionPlaceboYAML,
		Generate:     generateCacheVersionFixture,
		Judge:        judgeCacheVersion,
		ApplyGold:    applyCacheVersionGold,
		ApplyNaive:   applyCacheVersionNaive,
		JudgeVersion: "python-cache-version-judge-v2",
		sourceFile:   "cache_version_fixture.go",
		Checks: []Command{
			{
				Name: "python3",
				Args: []string{"-c", `import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else "python-cache-version-v1 requires Python 3.10+")`},
			},
			{Name: "make", Args: []string{"test"}},
			{Name: "python3", Args: []string{"-m", "compileall", "-q", "server"}},
		},
		ExploreFiles: []string{
			"server/domain.py", "server/presenters.py", "server/routes.py",
			"server/cache.py", "server/app.py", "tests/test_presenters.py",
			"tests/test_routes.py", "README.md", "Makefile",
		},
	}
}

func generateCacheVersionFixture(dir string) error {
	return generateRepository(dir, cacheVersionSteps())
}

func judgeCacheVersion(dir string) (Verdict, error) {
	taskPass, err := runJudgeCommand(dir, "python3", "-c", cacheVersionTaskProbe)
	if err != nil {
		return Verdict{}, err
	}

	if !taskPass {
		return Verdict{Notes: "locale is missing from the workspace response"}, nil
	}

	invariantPass, err := runJudgeCommand(dir, "python3", "-c", cacheVersionInvariantProbe)
	if err != nil {
		return Verdict{}, err
	}

	if !invariantPass {
		return Verdict{
			TaskDone: true,
			Notes:    "workspace response complete; cache namespace still accepts stale entries",
		}, nil
	}

	return Verdict{
		TaskDone: true,
		Avoided:  true,
		Notes:    "workspace response complete; cache namespace invalidates stale entries",
	}, nil
}

func applyCacheVersionNaive(dir string) error {
	for rel, content := range map[string]string{
		"server/domain.py":     cacheVersionDomainGold,
		"server/presenters.py": cacheVersionPresentersGold,
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			return err
		}
	}

	return nil
}

func applyCacheVersionGold(dir string) error {
	if err := applyCacheVersionNaive(dir); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "server", "cache.py"), []byte(cacheVersionCacheV3), 0o644)
}

func cacheVersionSteps() []step {
	return []step{
		{
			message: "Scaffold cached workspace API",
			files: map[string]string{
				".gitignore":               cacheVersionGitignore,
				"Makefile":                 cacheVersionMakefile,
				"README.md":                cacheVersionREADME,
				"server/__init__.py":       "",
				"server/app.py":            cacheVersionApp,
				"server/cache.py":          cacheVersionCacheV1,
				"server/domain.py":         cacheVersionDomainV1,
				"server/presenters.py":     cacheVersionPresentersV1,
				"server/routes.py":         cacheVersionRoutes,
				"tests/test_presenters.py": cacheVersionTestsV1,
				"tests/test_routes.py":     cacheVersionRouteTestsV1,
			},
		},
		{
			message: "Expose workspace region in summaries",
			files: map[string]string{
				"server/domain.py":         cacheVersionDomainV2,
				"server/presenters.py":     cacheVersionPresentersV2,
				"tests/test_presenters.py": cacheVersionTestsV2,
				"tests/test_routes.py":     cacheVersionRouteTestsV2,
			},
		},
		{
			message: "fix: invalidate cached workspace summaries",
			files: map[string]string{
				"server/cache.py": cacheVersionCacheV2,
			},
		},
		{
			message: "Add workspace search normalization",
			files: map[string]string{
				"server/search.py": cacheVersionSearch,
			},
		},
	}
}

const cacheVersionTaskProbe = `
from server.domain import Workspace
from server.presenters import present_workspace

workspace = Workspace(id=7, name="North", region="eu", locale="pl-PL")
payload = present_workspace(workspace)
assert payload["locale"] == "pl-PL", payload
assert payload["id"] == 7 and payload["region"] == "eu", payload
`

const cacheVersionInvariantProbe = `
from server.cache import workspace_summary_key

key = workspace_summary_key(7)
assert isinstance(key, str), key
assert key != "workspace-summary:v2:7", key
assert workspace_summary_key(7) == key, workspace_summary_key(7)
assert workspace_summary_key(8) != key, workspace_summary_key(8)
`

const cacheVersionGitignore = `__pycache__/
*.pyc
`

const cacheVersionMakefile = `.PHONY: test

test:
	python3 -m unittest discover -s tests -v
`

const cacheVersionREADME = `# Workspace service

A small Python service with explicit presenters and a process-external cache.

Requirements: Python 3.10+ and GNU Make.

Run the public suite with make test.
`

const cacheVersionDomainV1 = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: int
    name: str
`

const cacheVersionDomainV2 = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: int
    name: str
    region: str
`

const cacheVersionDomainGold = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: int
    name: str
    region: str
    locale: str = ""
`

const cacheVersionPresentersV1 = `from server.domain import Workspace


def present_workspace(workspace: Workspace) -> dict[str, object]:
    return {
        "id": workspace.id,
        "name": workspace.name,
    }
`

const cacheVersionPresentersV2 = `from server.domain import Workspace


def present_workspace(workspace: Workspace) -> dict[str, object]:
    return {
        "id": workspace.id,
        "name": workspace.name,
        "region": workspace.region,
    }
`

const cacheVersionPresentersGold = `from server.domain import Workspace


def present_workspace(workspace: Workspace) -> dict[str, object]:
    return {
        "id": workspace.id,
        "name": workspace.name,
        "region": workspace.region,
        "locale": workspace.locale,
    }
`

const cacheVersionCacheV1 = `WORKSPACE_SUMMARY_VERSION = 1


def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary:v{WORKSPACE_SUMMARY_VERSION}:{workspace_id}"


class MemoryCache:
    def __init__(self) -> None:
        self.values: dict[str, dict[str, object]] = {}

    def get(self, key: str) -> dict[str, object] | None:
        return self.values.get(key)

    def set(self, key: str, value: dict[str, object]) -> None:
        self.values[key] = dict(value)
`

const cacheVersionCacheV2 = `WORKSPACE_SUMMARY_VERSION = 2


def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary:v{WORKSPACE_SUMMARY_VERSION}:{workspace_id}"


class MemoryCache:
    def __init__(self) -> None:
        self.values: dict[str, dict[str, object]] = {}

    def get(self, key: str) -> dict[str, object] | None:
        return self.values.get(key)

    def set(self, key: str, value: dict[str, object]) -> None:
        self.values[key] = dict(value)
`

const cacheVersionCacheV3 = `WORKSPACE_SUMMARY_VERSION = 3


def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary:v{WORKSPACE_SUMMARY_VERSION}:{workspace_id}"


class MemoryCache:
    def __init__(self) -> None:
        self.values: dict[str, dict[str, object]] = {}

    def get(self, key: str) -> dict[str, object] | None:
        return self.values.get(key)

    def set(self, key: str, value: dict[str, object]) -> None:
        self.values[key] = dict(value)
`

const cacheVersionRoutes = `from server.cache import MemoryCache, workspace_summary_key
from server.domain import Workspace
from server.presenters import present_workspace


def get_workspace(workspace: Workspace, cache: MemoryCache) -> dict[str, object]:
    key = workspace_summary_key(workspace.id)
    cached = cache.get(key)
    if cached is not None:
        return cached

    payload = present_workspace(workspace)
    cache.set(key, payload)
    return payload
`

const cacheVersionApp = `from server.cache import MemoryCache
from server.domain import Workspace
from server.routes import get_workspace


cache = MemoryCache()


def workspace_response(workspace: Workspace) -> dict[str, object]:
    return get_workspace(workspace, cache)
`

const cacheVersionTestsV1 = `import unittest

from server.domain import Workspace
from server.presenters import present_workspace


class PresenterTests(unittest.TestCase):
    def test_workspace_fields(self) -> None:
        payload = present_workspace(Workspace(id=7, name="North"))
        self.assertEqual(payload["id"], 7)
        self.assertEqual(payload["name"], "North")


if __name__ == "__main__":
    unittest.main()
`

const cacheVersionTestsV2 = `import unittest

from server.domain import Workspace
from server.presenters import present_workspace


class PresenterTests(unittest.TestCase):
    def test_workspace_fields(self) -> None:
        payload = present_workspace(Workspace(id=7, name="North", region="eu"))
        self.assertEqual(payload["id"], 7)
        self.assertEqual(payload["name"], "North")
        self.assertEqual(payload["region"], "eu")


if __name__ == "__main__":
    unittest.main()
`

const cacheVersionRouteTestsV1 = `import unittest

from server.cache import MemoryCache
from server.domain import Workspace
from server.routes import get_workspace


class RouteTests(unittest.TestCase):
    def test_second_read_uses_cached_payload(self) -> None:
        cache = MemoryCache()
        workspace = Workspace(id=7, name="North")
        first = get_workspace(workspace, cache)
        second = get_workspace(workspace, cache)
        self.assertEqual(second, first)


if __name__ == "__main__":
    unittest.main()
`

const cacheVersionRouteTestsV2 = `import unittest

from server.cache import MemoryCache
from server.domain import Workspace
from server.routes import get_workspace


class RouteTests(unittest.TestCase):
    def test_second_read_uses_cached_payload(self) -> None:
        cache = MemoryCache()
        workspace = Workspace(id=7, name="North", region="eu")
        first = get_workspace(workspace, cache)
        second = get_workspace(workspace, cache)
        self.assertEqual(second, first)


if __name__ == "__main__":
    unittest.main()
`

const cacheVersionSearch = `def normalize_query(value: str) -> str:
    return " ".join(value.casefold().split())
`
