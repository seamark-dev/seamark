package bench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// SchemaSyncInstanceID is the stable selector used by the benchmark CLI.
	SchemaSyncInstanceID = "python-ts-schema-sync-v1"
	// SchemaSyncRule is the treatment lesson's stable identity.
	SchemaSyncRule = "sync-generated-api-client"
	// SchemaSyncScopingFamily binds the trigger-scoped and repair-scoped
	// variants into one matched factorial experiment.
	SchemaSyncScopingFamily = "python-ts-schema-sync-scoping-v1"

	schemaSyncTask = `Expose each workspace's billing currency in ` + "`GET /api/workspaces/{id}`" + ` responses. ` +
		`Return it as ` + "`billingCurrency`" + `, sourced from ` +
		"`Workspace.billing_currency`" + `. Keep the change minimal and add or update tests as appropriate.`

	schemaSyncLessonYAML = `pin:
  - rule: ` + SchemaSyncRule + `
    region: server
    note: After changing a public response schema, run make sync-api and commit web/src/api/generated.ts; backend tests do not validate this generated contract.
`

	schemaSyncPlaceboYAML = `pin:
  - rule: follow-service-conventions
    region: server
    note: Keep response construction explicit, preserve the existing wire-name style, and prefer focused tests that match the neighboring service modules.
`
)

// SchemaSyncInstance models a recurring owner-only contract: backend API
// changes require an explicit generated-client refresh that ordinary backend
// tests do not enforce. The repository is synthetic and deterministic, but the
// workflow is the same one used by mixed Python/TypeScript monorepos.
func SchemaSyncInstance() Instance {
	return Instance{
		ID:               SchemaSyncInstanceID,
		Rule:             SchemaSyncRule,
		Task:             schemaSyncTask,
		LessonYAML:       schemaSyncLessonYAML,
		PlaceboYAML:      schemaSyncPlaceboYAML,
		Generate:         GenerateSchemaSyncFixture,
		Judge:            JudgeSchemaSync,
		ApplyGold:        applySchemaSyncGold,
		ApplyNaive:       applySchemaSyncNaive,
		JudgeVersion:     "python-ts-schema-sync-v1",
		ComparisonFamily: SchemaSyncScopingFamily,
		ProtocolInstance: SchemaSyncInstanceID,
		sourceFile:       "schema_sync_fixture.go",
		Checks: []Command{
			{
				Name: "python3",
				Args: []string{"-c", `import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else "python-ts-schema-sync-v1 requires Python 3.10+")`},
			},
			{Name: "make", Args: []string{"test"}},
			{Name: "python3", Args: []string{"-m", "compileall", "-q", "server", "tools"}},
		},
		ExploreFiles: []string{
			"server/schema.py", "server/presenters.py", "server/domain.py",
			"server/routes.py", "server/app.py",
			"tools/sync_api.py", "web/src/api/generated.ts", "Makefile",
			"tests/test_presenters.py", "README.md", "web/src/api/client.ts",
		},
	}
}

// GenerateSchemaSyncFixture creates a small mixed-language monorepo whose git
// history contains one earlier backend-only schema change and its follow-up
// generated-client fix. The current checkout is healthy and carries no
// Seamark treatment files.
func GenerateSchemaSyncFixture(dir string) error {
	return generateRepository(dir, schemaSyncSteps())
}

// JudgeSchemaSync first verifies the requested backend behavior, then derives
// the expected TypeScript client independently of the agent-editable generator.
func JudgeSchemaSync(dir string) (Verdict, error) {
	taskPass, err := runJudgeCommand(dir, "python3", "-c", schemaSyncTaskProbe)
	if err != nil {
		return Verdict{}, err
	}

	if !taskPass {
		return Verdict{
			Notes: "billingCurrency is missing from the public schema or response",
		}, nil
	}

	syncPass, err := runJudgeCommand(dir, "python3", "-c", schemaSyncInvariantProbe)
	if err != nil {
		return Verdict{}, err
	}

	if !syncPass {
		return Verdict{
			TaskDone: true,
			Notes:    "backend response complete; generated TypeScript client is stale",
		}, nil
	}

	return Verdict{
		TaskDone: true,
		Avoided:  true,
		Notes:    "backend response complete; generated TypeScript client is synchronized",
	}, nil
}

func applySchemaSyncNaive(dir string) error {
	for rel, content := range map[string]string{
		"server/schema.py":     schemaSyncSchemaGold,
		"server/presenters.py": schemaSyncPresentersGold,
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			return err
		}
	}

	return nil
}

func applySchemaSyncGold(dir string) error {
	if err := applySchemaSyncNaive(dir); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultSetupTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", "tools/sync_api.py")
	cmd.Dir = dir
	cmd.Env = agentEnvironment(dir)
	cmd.WaitDelay = processWaitDelay
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("generate schema-sync client timed out after %s", defaultSetupTimeout)
		}

		return fmt.Errorf("generate schema-sync client: %w\n%s", err, out)
	}

	return nil
}

func schemaSyncSteps() []step {
	return []step{
		{
			message: "Scaffold workspace API and web client",
			files: map[string]string{
				".gitignore":                        schemaSyncGitignore,
				"Makefile":                          schemaSyncMakefile,
				"README.md":                         schemaSyncREADME,
				"server/__init__.py":                "",
				"server/app.py":                     schemaSyncApp,
				"server/auth.py":                    schemaSyncAuth,
				"server/database.py":                schemaSyncDatabase,
				"server/domain.py":                  schemaSyncDomain,
				"server/pagination.py":              schemaSyncPagination,
				"server/presenters.py":              schemaSyncPresentersV1,
				"server/routes.py":                  schemaSyncRoutes,
				"server/schema.py":                  schemaSyncSchemaV1,
				"tests/test_presenters.py":          schemaSyncTestsV1,
				"tools/sync_api.py":                 schemaSyncGenerator,
				"web/package.json":                  schemaSyncPackageJSON,
				"web/src/api/client.ts":             schemaSyncClient,
				"web/src/api/generated.ts":          schemaSyncGeneratedV1,
				"web/src/workspaces/card.ts":        schemaSyncCardV1,
				"web/src/workspaces/filters.ts":     schemaSyncFilters,
				"web/src/workspaces/permissions.ts": schemaSyncPermissions,
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
			message: "Add workspace search helpers",
			files: map[string]string{
				"server/search.py": schemaSyncSearch,
			},
		},
	}
}

const schemaSyncTaskProbe = `
from server.domain import Workspace
from server.app import get_workspace
from server.schema import SCHEMAS

fields = dict(SCHEMAS["WorkspaceSummary"])
assert fields.get("billingCurrency") == "string"

workspace = Workspace(
    id="ws-7",
    display_name="Northwind",
    region="eu-central",
    billing_currency="EUR",
    owner_id="user-1",
)
payload = get_workspace(workspace)
assert payload.get("billingCurrency") == "EUR"
`

// schemaSyncInvariantProbe deliberately does not call the repository's
// generator: the agent is allowed to edit that file, so hidden scoring derives
// the expected client independently from the public schema source.
const schemaSyncInvariantProbe = `
from pathlib import Path
from server.schema import SCHEMAS

lines = [
    "// Code generated by tools/sync_api.py. DO NOT EDIT.",
    "",
]
for name, fields in SCHEMAS.items():
    lines.append(f"export interface {name} {{")
    for field, kind in fields:
        lines.append(f"  {field}: {kind};")
    lines.extend(("}", ""))

expected = "\n".join(lines)
actual = Path("web/src/api/generated.ts").read_text()
assert actual == expected
`

const schemaSyncGitignore = `__pycache__/
*.pyc
.seamark/
.claude/
`

const schemaSyncMakefile = `.PHONY: test sync-api check-api-sync

test:
	python3 -m unittest discover -s tests

sync-api:
	python3 tools/sync_api.py

check-api-sync:
	python3 tools/sync_api.py --check
`

const schemaSyncREADME = `# Relay Desk

Relay Desk is a small workspace service with a Python 3.10+ backend and
TypeScript web application.

## Development

Run backend tests with:

    make test

The server package owns authentication, persistence, pagination, public schema
descriptions, and response presenters. The web package consumes the public API.
`

const schemaSyncDomain = `from dataclasses import dataclass


@dataclass(frozen=True)
class Workspace:
    id: str
    display_name: str
    region: str
    billing_currency: str
    owner_id: str
`

const schemaSyncSchemaV1 = `# Public response schemas consumed by the API encoder.
SCHEMAS = {
    "WorkspaceSummary": (
        ("id", "string"),
        ("displayName", "string"),
    ),
}
`

const schemaSyncSchemaV2 = `# Public response schemas consumed by the API encoder.
SCHEMAS = {
    "WorkspaceSummary": (
        ("id", "string"),
        ("displayName", "string"),
        ("region", "string"),
    ),
}
`

const schemaSyncSchemaGold = `# Public response schemas consumed by the API encoder.
SCHEMAS = {
    "WorkspaceSummary": (
        ("id", "string"),
        ("displayName", "string"),
        ("region", "string"),
        ("billingCurrency", "string"),
    ),
}
`

const schemaSyncPresentersV1 = `from server.domain import Workspace


def workspace_summary(workspace: Workspace) -> dict[str, str]:
    return {
        "id": workspace.id,
        "displayName": workspace.display_name,
    }
`

const schemaSyncPresentersV2 = `from server.domain import Workspace


def workspace_summary(workspace: Workspace) -> dict[str, str]:
    return {
        "id": workspace.id,
        "displayName": workspace.display_name,
        "region": workspace.region,
    }
`

const schemaSyncPresentersGold = `from server.domain import Workspace


def workspace_summary(workspace: Workspace) -> dict[str, str]:
    return {
        "id": workspace.id,
        "displayName": workspace.display_name,
        "region": workspace.region,
        "billingCurrency": workspace.billing_currency,
    }
`

const schemaSyncTestsV1 = `import unittest

from server.app import get_workspace
from server.domain import Workspace


class WorkspaceSummaryTests(unittest.TestCase):
    def test_exposes_identity(self) -> None:
        workspace = Workspace("ws-1", "Northwind", "eu-west", "EUR", "user-1")
        payload = get_workspace(workspace)
        self.assertEqual(payload["id"], "ws-1")
        self.assertEqual(payload["displayName"], "Northwind")


if __name__ == "__main__":
    unittest.main()
`

const schemaSyncTestsV2 = `import unittest

from server.app import get_workspace
from server.domain import Workspace


class WorkspaceSummaryTests(unittest.TestCase):
    def test_exposes_identity_and_region(self) -> None:
        workspace = Workspace("ws-1", "Northwind", "eu-west", "EUR", "user-1")
        payload = get_workspace(workspace)
        self.assertEqual(payload["id"], "ws-1")
        self.assertEqual(payload["displayName"], "Northwind")
        self.assertEqual(payload["region"], "eu-west")


if __name__ == "__main__":
    unittest.main()
`

const schemaSyncGenerator = `#!/usr/bin/env python3
from __future__ import annotations

import argparse
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from server.schema import SCHEMAS


def render() -> str:
    lines = [
        "// Code generated by tools/sync_api.py. DO NOT EDIT.",
        "",
    ]
    for name, fields in SCHEMAS.items():
        lines.append(f"export interface {name} {{")
        for field, kind in fields:
            lines.append(f"  {field}: {kind};")
        lines.extend(("}", ""))
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    output = ROOT / "web" / "src" / "api" / "generated.ts"
    expected = render()
    if args.check:
        current = output.read_text() if output.exists() else ""
        if current != expected:
            print("generated API client is stale; run make sync-api", file=sys.stderr)
            return 1
        return 0

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(expected)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
`

const schemaSyncGeneratedV1 = `// Code generated by tools/sync_api.py. DO NOT EDIT.

export interface WorkspaceSummary {
  id: string;
  displayName: string;
}
`

const schemaSyncGeneratedV2 = `// Code generated by tools/sync_api.py. DO NOT EDIT.

export interface WorkspaceSummary {
  id: string;
  displayName: string;
  region: string;
}
`

const schemaSyncApp = `from server.domain import Workspace
from server.presenters import workspace_summary
from server.schema import SCHEMAS


def encode_response(schema_name: str, payload: dict[str, str]) -> dict[str, str]:
    allowed = {name for name, _ in SCHEMAS[schema_name]}
    return {name: value for name, value in payload.items() if name in allowed}


def get_workspace(workspace: Workspace) -> dict[str, str]:
    return encode_response("WorkspaceSummary", workspace_summary(workspace))
`

const schemaSyncAuth = `def can_view_workspace(user_id: str, owner_id: str, is_admin: bool = False) -> bool:
    return is_admin or user_id == owner_id
`

const schemaSyncRoutes = `from server.app import get_workspace


ROUTES = {
    ("GET", "/api/workspaces/{id}"): get_workspace,
}
`

const schemaSyncDatabase = `from server.domain import Workspace


class WorkspaceRepository:
    def __init__(self) -> None:
        self._items: dict[str, Workspace] = {}

    def save(self, workspace: Workspace) -> None:
        self._items[workspace.id] = workspace

    def get(self, workspace_id: str) -> Workspace | None:
        return self._items.get(workspace_id)
`

const schemaSyncPagination = `def page_window(page: int, size: int) -> tuple[int, int]:
    page = max(page, 1)
    size = min(max(size, 1), 100)
    return (page - 1) * size, size
`

const schemaSyncSearch = `from server.domain import Workspace


def matches_query(workspace: Workspace, query: str) -> bool:
    needle = query.casefold().strip()
    return not needle or needle in workspace.display_name.casefold()
`

const schemaSyncPackageJSON = `{
  "name": "relay-desk-web",
  "private": true,
  "type": "module"
}
`

const schemaSyncClient = `import type { WorkspaceSummary } from "./generated";

export async function fetchWorkspace(id: string): Promise<WorkspaceSummary> {
  const response = await fetch("/api/workspaces/" + id);
  if (!response.ok) throw new Error("workspace request failed: " + response.status);
  return response.json() as Promise<WorkspaceSummary>;
}
`

const schemaSyncCardV1 = `import type { WorkspaceSummary } from "../api/generated";

export function workspaceTitle(workspace: WorkspaceSummary): string {
  return workspace.displayName;
}
`

const schemaSyncCardV2 = `import type { WorkspaceSummary } from "../api/generated";

export function workspaceTitle(workspace: WorkspaceSummary): string {
  return workspace.displayName + " · " + workspace.region;
}
`

const schemaSyncFilters = `import type { WorkspaceSummary } from "../api/generated";

export function matchesWorkspace(workspace: WorkspaceSummary, query: string): boolean {
  return workspace.displayName.toLocaleLowerCase().includes(query.toLocaleLowerCase());
}
`

const schemaSyncPermissions = `export type WorkspaceRole = "owner" | "editor" | "viewer";

export function canEdit(role: WorkspaceRole): boolean {
  return role === "owner" || role === "editor";
}
`
