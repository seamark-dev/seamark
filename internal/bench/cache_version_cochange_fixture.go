package bench

// CacheVersionCochangeInstanceID selects the cache-version task on a history
// whose commits carry the presenter and the cache namespace together.
const CacheVersionCochangeInstanceID = "python-cache-version-cochange-v1"

// Trigger and companion of the cache-version workflow instance. The task
// also edits server/domain.py; the presenter is the file whose change alters
// the cached response shape, which is what the lesson is about.
const (
	cacheVersionTrigger   = "server/presenters.py"
	cacheVersionCompanion = "server/cache.py"
)

// CacheVersionCochangeInstance is CacheVersionInstance with one difference:
// its history. The base fixture bumps the cache namespace only in a separate
// fix commit, so the pair never reaches two shared commits. This variant
// introduces the cached API in one commit, repeats the backend-only mistake,
// fixes it by introducing the version namespace with a commit message that
// says why, then moves the presenter and the cache module together three
// more times: a plan-tier field with its bump, the revert of that field,
// and the region field with its bump. The tests for two of those changes
// are committed on their own, so the cache module is the strongest partner
// of the presenter among the files the task does not plan. The final tree
// equals the base fixture's final tree.
func CacheVersionCochangeInstance() WorkflowInstance {
	return WorkflowInstance{
		Instance: workflowVariant(CacheVersionInstance(), CacheVersionCochangeInstanceID,
			"cache_version_cochange_fixture.go", generateCacheVersionCochangeFixture),
		Trigger:   cacheVersionTrigger,
		Companion: cacheVersionCompanion,
	}
}

func generateCacheVersionCochangeFixture(dir string) error {
	return generateRepository(dir, cacheVersionCochangeSteps())
}

// cacheVersionCochangeSteps writes twelve commits. Commits 2, 6, 8, and 9
// change the presenter and the cache module together; commit 4 changes the
// cached shape without a namespace bump and commit 5 fixes it; commits 7
// and 10 add tests on their own; commits 3, 11, and 12 touch unrelated
// files. The revert in commit 8 is what a real history records when a field
// is withdrawn: it restores the namespace version, so the region bump that
// follows lands on the base fixture's final value. The final tree equals
// the base fixture's final tree.
func cacheVersionCochangeSteps() []step {
	return []step{
		{
			message: "Scaffold workspace service",
			files: map[string]string{
				".gitignore":         cacheVersionGitignore,
				"Makefile":           cacheVersionMakefile,
				"README.md":          cacheVersionCochangeREADMEV0,
				"server/__init__.py": "",
			},
		},
		{
			message: "Add cached workspace summary API",
			files: map[string]string{
				"server/app.py":            cacheVersionCochangeAppV0,
				"server/cache.py":          cacheVersionCochangeCacheV0,
				"server/domain.py":         cacheVersionCochangeDomainV0,
				"server/presenters.py":     cacheVersionCochangePresentersV0,
				"server/routes.py":         cacheVersionRoutes,
				"tests/test_presenters.py": cacheVersionCochangeTestsV0,
				"tests/test_routes.py":     cacheVersionCochangeRouteTestsV0,
			},
		},
		{
			message: "Add workspace search normalization",
			files: map[string]string{
				"server/search.py": cacheVersionSearch,
			},
		},
		{
			message: "Expose workspace name in summaries",
			files: map[string]string{
				"server/domain.py":         cacheVersionDomainV1,
				"server/presenters.py":     cacheVersionPresentersV1,
				"tests/test_presenters.py": cacheVersionTestsV1,
				"tests/test_routes.py":     cacheVersionRouteTestsV1,
			},
		},
		{
			message: "fix: version the summary cache namespace so shape changes evict old entries",
			files: map[string]string{
				"server/cache.py": cacheVersionCacheV1,
			},
		},
		{
			message: "Expose the workspace plan tier in summaries",
			files: map[string]string{
				"server/cache.py":      cacheVersionCacheV2,
				"server/domain.py":     cacheVersionCochangeDomainTier,
				"server/presenters.py": cacheVersionCochangePresentersTier,
			},
		},
		{
			message: "Cover the plan tier in presenter tests",
			files: map[string]string{
				"tests/test_presenters.py": cacheVersionCochangeTestsTier,
			},
		},
		{
			message: "Revert \"Expose the workspace plan tier in summaries\"",
			files: map[string]string{
				"server/cache.py":          cacheVersionCacheV1,
				"server/domain.py":         cacheVersionDomainV1,
				"server/presenters.py":     cacheVersionPresentersV1,
				"tests/test_presenters.py": cacheVersionTestsV1,
			},
		},
		{
			message: "Expose workspace region in summaries",
			files: map[string]string{
				"server/cache.py":      cacheVersionCacheV2,
				"server/domain.py":     cacheVersionDomainV2,
				"server/presenters.py": cacheVersionPresentersV2,
			},
		},
		{
			message: "Cover the region in presenter and route tests",
			files: map[string]string{
				"tests/test_presenters.py": cacheVersionTestsV2,
				"tests/test_routes.py":     cacheVersionRouteTestsV2,
			},
		},
		{
			message: "Document how to run the suite",
			files: map[string]string{
				"README.md": cacheVersionREADME,
			},
		},
		{
			message: "Reuse one cache instance per process",
			files: map[string]string{
				"server/app.py": cacheVersionApp,
			},
		},
	}
}

// The scaffold's README and the first app module are one commit short of the
// base fixture's, so two unrelated commits can finish them later.
const cacheVersionCochangeREADMEV0 = `# Workspace service

A small Python service with explicit presenters and a process-external cache.
`

const cacheVersionCochangeAppV0 = `from server.cache import MemoryCache
from server.domain import Workspace
from server.routes import get_workspace


def workspace_response(workspace: Workspace) -> dict[str, object]:
    return get_workspace(workspace, MemoryCache())
`

// The plan tier was exposed for one release and reverted: two more commits
// that move the presenter and the cache namespace together.
const cacheVersionCochangeDomainTier = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: int
    name: str
    tier: str
`

const cacheVersionCochangePresentersTier = `from server.domain import Workspace


def present_workspace(workspace: Workspace) -> dict[str, object]:
    return {
        "id": workspace.id,
        "name": workspace.name,
        "tier": workspace.tier,
    }
`

const cacheVersionCochangeTestsTier = `import unittest

from server.domain import Workspace
from server.presenters import present_workspace


class PresenterTests(unittest.TestCase):
    def test_workspace_fields(self) -> None:
        payload = present_workspace(Workspace(id=7, name="North", tier="pro"))
        self.assertEqual(payload["id"], 7)
        self.assertEqual(payload["name"], "North")
        self.assertEqual(payload["tier"], "pro")


if __name__ == "__main__":
    unittest.main()
`

// The first cached API version has no namespace version at all. The fix
// commit introduces WORKSPACE_SUMMARY_VERSION, which is why the lesson can
// point at it later.
const cacheVersionCochangeCacheV0 = `def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary:{workspace_id}"


class MemoryCache:
    def __init__(self) -> None:
        self.values: dict[str, dict[str, object]] = {}

    def get(self, key: str) -> dict[str, object] | None:
        return self.values.get(key)

    def set(self, key: str, value: dict[str, object]) -> None:
        self.values[key] = dict(value)
`

const cacheVersionCochangeDomainV0 = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: int
`

const cacheVersionCochangePresentersV0 = `from server.domain import Workspace


def present_workspace(workspace: Workspace) -> dict[str, object]:
    return {
        "id": workspace.id,
    }
`

const cacheVersionCochangeTestsV0 = `import unittest

from server.domain import Workspace
from server.presenters import present_workspace


class PresenterTests(unittest.TestCase):
    def test_workspace_fields(self) -> None:
        payload = present_workspace(Workspace(id=7))
        self.assertEqual(payload["id"], 7)


if __name__ == "__main__":
    unittest.main()
`

const cacheVersionCochangeRouteTestsV0 = `import unittest

from server.cache import MemoryCache
from server.domain import Workspace
from server.routes import get_workspace


class RouteTests(unittest.TestCase):
    def test_second_read_uses_cached_payload(self) -> None:
        cache = MemoryCache()
        workspace = Workspace(id=7)
        first = get_workspace(workspace, cache)
        second = get_workspace(workspace, cache)
        self.assertEqual(second, first)


if __name__ == "__main__":
    unittest.main()
`
