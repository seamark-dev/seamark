package bench

// SchemaSyncCochangeInstanceID selects the schema-sync task on a history
// whose commits carry the trigger and companion files together.
const SchemaSyncCochangeInstanceID = "python-ts-schema-sync-cochange-v1"

// Trigger and companion of the schema-sync workflow instance.
const (
	schemaSyncTrigger   = "server/schema.py"
	schemaSyncCompanion = "web/src/api/generated.ts"
)

// SchemaSyncCochangeInstance is SchemaSyncInstance with one difference: its
// history. The base fixture never commits server/schema.py together with
// web/src/api/generated.ts, so co-change mining (two shared commits at least)
// finds no pair and `change_set` cannot name the companion. This variant
// grows the API in four commits that update the schema and the generated
// client together, commits the tests for two of them separately, keeps the
// backend-only mistake and its fix commit, and ends at exactly the base tree.
// The generated client is then the strongest partner of the schema among the
// files the task does not plan: the first cohort showed that a companion
// listed below the test files, at the mining floor, is read as noise.
func SchemaSyncCochangeInstance() WorkflowInstance {
	return WorkflowInstance{
		Instance: workflowVariant(SchemaSyncInstance(), SchemaSyncCochangeInstanceID,
			"schema_sync_cochange_fixture.go", generateSchemaSyncCochangeFixture),
		Trigger:   schemaSyncTrigger,
		Companion: schemaSyncCompanion,
	}
}

func generateSchemaSyncCochangeFixture(dir string) error {
	return generateRepository(dir, schemaSyncCochangeSteps())
}

// schemaSyncCochangeSteps writes twelve commits. Commits 2, 4, 6, and 8
// change the schema and the generated client together; commits 5 and 7 add
// the tests for two of those changes on their own, so the tests share fewer
// commits with the schema than the client does; commit 9 repeats the
// mistake the lesson describes and commit 10 fixes it; commits 3, 11, and 12
// touch unrelated files so the window is not only the pair. The final tree
// equals the base fixture's final tree, so the shared judges, patches, and
// checks apply unchanged.
func schemaSyncCochangeSteps() []step {
	return []step{
		{
			message: "Scaffold workspace service",
			files: map[string]string{
				".gitignore":                        schemaSyncGitignore,
				"Makefile":                          schemaSyncMakefile,
				"README.md":                         schemaSyncCochangeREADMEV0,
				"server/__init__.py":                "",
				"server/auth.py":                    schemaSyncAuth,
				"server/database.py":                schemaSyncDatabase,
				"server/domain.py":                  schemaSyncDomain,
				"server/pagination.py":              schemaSyncCochangePaginationV0,
				"web/package.json":                  schemaSyncPackageJSON,
				"web/src/workspaces/permissions.ts": schemaSyncPermissions,
			},
		},
		{
			message: "Add workspace summary API and web client",
			files: map[string]string{
				"server/app.py":              schemaSyncApp,
				"server/presenters.py":       schemaSyncCochangePresentersV0,
				"server/routes.py":           schemaSyncRoutes,
				"server/schema.py":           schemaSyncCochangeSchemaV0,
				"tests/test_presenters.py":   schemaSyncCochangeTestsV0,
				"tools/sync_api.py":          schemaSyncGenerator,
				"web/src/api/client.ts":      schemaSyncClient,
				"web/src/api/generated.ts":   schemaSyncCochangeGeneratedV0,
				"web/src/workspaces/card.ts": schemaSyncCochangeCardV0,
			},
		},
		{
			message: "Add workspace search helpers",
			files: map[string]string{
				"server/search.py": schemaSyncSearch,
			},
		},
		{
			message: "Expose workspace display name in summary responses",
			files: map[string]string{
				"server/presenters.py":          schemaSyncPresentersV1,
				"server/schema.py":              schemaSyncSchemaV1,
				"web/src/api/generated.ts":      schemaSyncGeneratedV1,
				"web/src/workspaces/card.ts":    schemaSyncCardV1,
				"web/src/workspaces/filters.ts": schemaSyncFilters,
			},
		},
		{
			message: "Cover the display name in summary tests",
			files: map[string]string{
				"tests/test_presenters.py": schemaSyncTestsV1,
			},
		},
		{
			message: "Expose the workspace owner in summary responses",
			files: map[string]string{
				"server/presenters.py":     schemaSyncCochangePresentersOwner,
				"server/schema.py":         schemaSyncCochangeSchemaOwner,
				"web/src/api/generated.ts": schemaSyncCochangeGeneratedOwner,
			},
		},
		{
			message: "Cover the owner in summary tests",
			files: map[string]string{
				"tests/test_presenters.py": schemaSyncCochangeTestsOwner,
			},
		},
		{
			message: "Stop exposing owner ids in summary responses",
			files: map[string]string{
				"server/presenters.py":     schemaSyncPresentersV1,
				"server/schema.py":         schemaSyncSchemaV1,
				"tests/test_presenters.py": schemaSyncTestsV1,
				"web/src/api/generated.ts": schemaSyncGeneratedV1,
			},
		},
		{
			message: "Expose workspace region in summary responses",
			files: map[string]string{
				"server/presenters.py":     schemaSyncPresentersV2,
				"server/schema.py":         schemaSyncSchemaV2,
				"tests/test_presenters.py": schemaSyncTestsV2,
			},
		},
		{
			message: "fix: refresh web types after workspace schema change",
			files: map[string]string{
				"web/src/api/generated.ts":   schemaSyncGeneratedV2,
				"web/src/workspaces/card.ts": schemaSyncCardV2,
			},
		},
		{
			message: "Clamp page sizes to the documented range",
			files: map[string]string{
				"server/pagination.py": schemaSyncPagination,
			},
		},
		{
			message: "Document the server package layout",
			files: map[string]string{
				"README.md": schemaSyncREADME,
			},
		},
	}
}

// The scaffold's README and pagination helper are one commit short of the
// base fixture's, so two unrelated commits can finish them later.
const schemaSyncCochangeREADMEV0 = `# Relay Desk

Relay Desk is a small workspace service with a Python 3.10+ backend and
TypeScript web application.

## Development

Run backend tests with:

    make test
`

const schemaSyncCochangePaginationV0 = `def page_window(page: int, size: int) -> tuple[int, int]:
    page = max(page, 1)
    return (page - 1) * size, size
`

// The owner id was exposed for one release and withdrawn: two more commits
// that move the schema and the generated client together.
const schemaSyncCochangeSchemaOwner = `# Public response schemas consumed by the API encoder.
SCHEMAS = {
    "WorkspaceSummary": (
        ("id", "string"),
        ("displayName", "string"),
        ("ownerId", "string"),
    ),
}
`

const schemaSyncCochangePresentersOwner = `from server.domain import Workspace


def workspace_summary(workspace: Workspace) -> dict[str, str]:
    return {
        "id": workspace.id,
        "displayName": workspace.display_name,
        "ownerId": workspace.owner_id,
    }
`

const schemaSyncCochangeGeneratedOwner = `// Code generated by tools/sync_api.py. DO NOT EDIT.

export interface WorkspaceSummary {
  id: string;
  displayName: string;
  ownerId: string;
}
`

const schemaSyncCochangeTestsOwner = `import unittest

from server.app import get_workspace
from server.domain import Workspace


class WorkspaceSummaryTests(unittest.TestCase):
    def test_exposes_identity_and_owner(self) -> None:
        workspace = Workspace("ws-1", "Northwind", "eu-west", "EUR", "user-1")
        payload = get_workspace(workspace)
        self.assertEqual(payload["id"], "ws-1")
        self.assertEqual(payload["displayName"], "Northwind")
        self.assertEqual(payload["ownerId"], "user-1")


if __name__ == "__main__":
    unittest.main()
`

// The first API version exposes only the identifier. The base fixture starts
// one step later, so these constants exist only in the co-change history.
const schemaSyncCochangeSchemaV0 = `# Public response schemas consumed by the API encoder.
SCHEMAS = {
    "WorkspaceSummary": (
        ("id", "string"),
    ),
}
`

const schemaSyncCochangePresentersV0 = `from server.domain import Workspace


def workspace_summary(workspace: Workspace) -> dict[str, str]:
    return {
        "id": workspace.id,
    }
`

const schemaSyncCochangeTestsV0 = `import unittest

from server.app import get_workspace
from server.domain import Workspace


class WorkspaceSummaryTests(unittest.TestCase):
    def test_exposes_identifier(self) -> None:
        workspace = Workspace("ws-1", "Northwind", "eu-west", "EUR", "user-1")
        payload = get_workspace(workspace)
        self.assertEqual(payload["id"], "ws-1")


if __name__ == "__main__":
    unittest.main()
`

const schemaSyncCochangeGeneratedV0 = `// Code generated by tools/sync_api.py. DO NOT EDIT.

export interface WorkspaceSummary {
  id: string;
}
`

const schemaSyncCochangeCardV0 = `import type { WorkspaceSummary } from "../api/generated";

export function workspaceTitle(workspace: WorkspaceSummary): string {
  return workspace.id;
}
`
