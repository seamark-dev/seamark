package bench

// ExportRegistryCochangeInstanceID selects the export-registry task on a
// history whose commits carry the preview API and the worker registry
// together.
const ExportRegistryCochangeInstanceID = "go-export-registry-cochange-v1"

// Trigger and companion of the export-registry workflow instance.
const (
	exportRegistryTrigger   = "internal/export/preview.go"
	exportRegistryCompanion = "internal/worker/registry.go"
)

// ExportRegistryCochangeInstance is ExportRegistryInstance with one
// difference: its history. The base fixture registers each new format only
// in a separate fix commit, so the pair never reaches two shared commits.
// This variant adds the preview API and the registry in one commit, repeats
// the preview-only mistake for CSV, fixes it, and then adds JSON to both
// sides in one commit. The final tree equals the base fixture's final tree.
func ExportRegistryCochangeInstance() WorkflowInstance {
	return WorkflowInstance{
		Instance: workflowVariant(ExportRegistryInstance(), ExportRegistryCochangeInstanceID,
			"export_registry_cochange_fixture.go", generateExportRegistryCochangeFixture),
		Trigger:   exportRegistryTrigger,
		Companion: exportRegistryCompanion,
	}
}

func generateExportRegistryCochangeFixture(dir string) error {
	return generateRepository(dir, exportRegistryCochangeSteps())
}

// exportRegistryCochangeSteps writes six commits. Commits 2 and 5 change the
// preview switch and the worker registry together; commit 3 adds a preview
// format without registering it and commit 4 fixes that. The final tree
// equals the base fixture's final tree.
func exportRegistryCochangeSteps() []step {
	return []step{
		{
			message: "Scaffold export service",
			files: map[string]string{
				".gitignore":               exportRegistryGitignore,
				"go.mod":                   exportRegistryGoMod,
				"README.md":                exportRegistryREADME,
				"cmd/exportd/main.go":      exportRegistryMain,
				"internal/export/model.go": exportRegistryModel,
			},
		},
		{
			message: "Add export preview API and worker registry",
			files: map[string]string{
				"internal/export/preview.go":       exportRegistryCochangePreviewV0,
				"internal/export/preview_test.go":  exportRegistryCochangePreviewTestV0,
				"internal/worker/jobs.go":          exportRegistryJobs,
				"internal/worker/registry.go":      exportRegistryCochangeWorkerV0,
				"internal/worker/registry_test.go": exportRegistryCochangeWorkerTestV0,
			},
		},
		{
			message: "Add CSV export previews",
			files: map[string]string{
				"internal/export/csv.go":          exportRegistryCSV,
				"internal/export/preview.go":      exportRegistryPreviewV1,
				"internal/export/preview_test.go": exportRegistryPreviewTestV1,
			},
		},
		{
			message: "fix: register CSV formatter for queued exports",
			files: map[string]string{
				"internal/worker/registry.go":      exportRegistryWorkerV1,
				"internal/worker/registry_test.go": exportRegistryWorkerTestV1,
			},
		},
		{
			message: "Add JSON export previews and queued exports",
			files: map[string]string{
				"internal/export/json.go":          exportRegistryJSON,
				"internal/export/preview.go":       exportRegistryPreviewV2,
				"internal/export/preview_test.go":  exportRegistryPreviewTestV2,
				"internal/worker/registry.go":      exportRegistryWorkerV2,
				"internal/worker/registry_test.go": exportRegistryWorkerTestV2,
			},
		},
		{
			message: "Add queue priority normalization",
			files: map[string]string{
				"internal/worker/priority.go": exportRegistryPriority,
			},
		},
	}
}

// The first preview API knows no format. The base fixture starts one step
// later, so these constants exist only in the co-change history.
const exportRegistryCochangePreviewV0 = `package export

import "fmt"

func Preview(format string, rows []Row) (string, error) {
	return "", fmt.Errorf("unsupported export format %q", format)
}
`

const exportRegistryCochangePreviewTestV0 = `package export

import "testing"

func TestPreviewRejectsUnknownFormat(t *testing.T) {
	if _, err := Preview("xml", nil); err == nil {
		t.Fatal("unknown format must be rejected")
	}
}
`

const exportRegistryCochangeWorkerV0 = `package worker

import "example.com/exporter/internal/export"

var formatters = map[string]export.Formatter{}

func Formatter(name string) (export.Formatter, bool) {
	formatter, ok := formatters[name]

	return formatter, ok
}
`

const exportRegistryCochangeWorkerTestV0 = `package worker

import "testing"

func TestUnknownFormatNotRegistered(t *testing.T) {
	if _, ok := Formatter("xml"); ok {
		t.Fatal("unknown format must not be registered")
	}
}
`
