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
// fixes it by introducing the version namespace, and then bumps the namespace
// together with the next response-shape change. The final tree equals the
// base fixture's final tree.
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

// cacheVersionCochangeSteps writes six commits. Commits 2 and 5 change the
// presenter and the cache module together; commit 3 changes the cached shape
// without a namespace bump and commit 4 fixes it. The final tree equals the
// base fixture's final tree.
func cacheVersionCochangeSteps() []step {
	return []step{
		{
			message: "Scaffold workspace service",
			files: map[string]string{
				".gitignore":         cacheVersionGitignore,
				"Makefile":           cacheVersionMakefile,
				"README.md":          cacheVersionREADME,
				"server/__init__.py": "",
			},
		},
		{
			message: "Add cached workspace summary API",
			files: map[string]string{
				"server/app.py":            cacheVersionApp,
				"server/cache.py":          cacheVersionCochangeCacheV0,
				"server/domain.py":         cacheVersionCochangeDomainV0,
				"server/presenters.py":     cacheVersionCochangePresentersV0,
				"server/routes.py":         cacheVersionRoutes,
				"tests/test_presenters.py": cacheVersionCochangeTestsV0,
				"tests/test_routes.py":     cacheVersionCochangeRouteTestsV0,
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
			message: "fix: invalidate cached workspace summaries",
			files: map[string]string{
				"server/cache.py": cacheVersionCacheV1,
			},
		},
		{
			message: "Expose workspace region in summaries",
			files: map[string]string{
				"server/cache.py":          cacheVersionCacheV2,
				"server/domain.py":         cacheVersionDomainV2,
				"server/presenters.py":     cacheVersionPresentersV2,
				"tests/test_presenters.py": cacheVersionTestsV2,
				"tests/test_routes.py":     cacheVersionRouteTestsV2,
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
