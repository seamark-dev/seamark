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
// the preview-only mistake for CSV, fixes it, then moves the preview switch
// and the registry together three more times: TSV added, TSV dropped, JSON
// added. The tests for two of those changes are committed on their own, so
// the registry is the strongest partner of the preview switch among the
// files the task does not plan. The final tree equals the base fixture's
// final tree.
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

// exportRegistryCochangeSteps writes twelve commits. Commits 2, 6, 8, and 9
// change the preview switch and the worker registry together; commit 4 adds
// a preview format without registering it and commit 5 fixes that; commits
// 7 and 10 add tests on their own; commits 3, 11, and 12 touch unrelated
// files. The TSV formatter lives in csv.go while it exists, so no commit has
// to delete a file. The final tree equals the base fixture's final tree.
func exportRegistryCochangeSteps() []step {
	return []step{
		{
			message: "Scaffold export service",
			files: map[string]string{
				".gitignore":               exportRegistryGitignore,
				"go.mod":                   exportRegistryGoMod,
				"README.md":                exportRegistryCochangeREADMEV0,
				"cmd/exportd/main.go":      exportRegistryMain,
				"internal/export/model.go": exportRegistryModel,
			},
		},
		{
			message: "Add export preview API and worker registry",
			files: map[string]string{
				"internal/export/preview.go":       exportRegistryCochangePreviewV0,
				"internal/export/preview_test.go":  exportRegistryCochangePreviewTestV0,
				"internal/worker/jobs.go":          exportRegistryCochangeJobsV0,
				"internal/worker/registry.go":      exportRegistryCochangeWorkerV0,
				"internal/worker/registry_test.go": exportRegistryCochangeWorkerTestV0,
			},
		},
		{
			message: "Add queue priority normalization",
			files: map[string]string{
				"internal/worker/priority.go": exportRegistryPriority,
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
			message: "Add TSV export previews and queued exports",
			files: map[string]string{
				"internal/export/csv.go":      exportRegistryCochangeCSVWithTSV,
				"internal/export/preview.go":  exportRegistryCochangePreviewTSV,
				"internal/worker/registry.go": exportRegistryCochangeWorkerTSV,
			},
		},
		{
			message: "Cover TSV previews and queued exports",
			files: map[string]string{
				"internal/export/preview_test.go":  exportRegistryCochangePreviewTestTSV,
				"internal/worker/registry_test.go": exportRegistryCochangeWorkerTestTSV,
			},
		},
		{
			message: "Drop TSV exports",
			files: map[string]string{
				"internal/export/csv.go":           exportRegistryCSV,
				"internal/export/preview.go":       exportRegistryPreviewV1,
				"internal/export/preview_test.go":  exportRegistryPreviewTestV1,
				"internal/worker/registry.go":      exportRegistryWorkerV1,
				"internal/worker/registry_test.go": exportRegistryWorkerTestV1,
			},
		},
		{
			message: "Add JSON export previews and queued exports",
			files: map[string]string{
				"internal/export/json.go":     exportRegistryJSON,
				"internal/export/preview.go":  exportRegistryPreviewV2,
				"internal/worker/registry.go": exportRegistryWorkerV2,
			},
		},
		{
			message: "Cover JSON previews and queued exports",
			files: map[string]string{
				"internal/export/preview_test.go":  exportRegistryPreviewTestV2,
				"internal/worker/registry_test.go": exportRegistryWorkerTestV2,
			},
		},
		{
			message: "Document the worker pipeline",
			files: map[string]string{
				"README.md": exportRegistryREADME,
			},
		},
		{
			message: "Report unsupported queued formats by name",
			files: map[string]string{
				"internal/worker/jobs.go": exportRegistryJobs,
			},
		},
	}
}

// The scaffold's README and the first job runner are one commit short of the
// base fixture's, so two unrelated commits can finish them later.
const exportRegistryCochangeREADMEV0 = `# Export service

The preview endpoint renders small exports synchronously.
`

const exportRegistryCochangeJobsV0 = `package worker

import (
	"errors"

	"example.com/exporter/internal/export"
)

func Render(format string, rows []export.Row) (string, error) {
	formatter, ok := Formatter(format)
	if !ok {
		return "", errors.New("unsupported queued export format")
	}

	return formatter(rows)
}
`

// TSV was supported for one release and dropped: two more commits that move
// the preview switch and the registry together.
const exportRegistryCochangeCSVWithTSV = `package export

import (
	"encoding/csv"
	"strings"
	"strconv"
)

func FormatCSV(rows []Row) (string, error) {
	var out strings.Builder
	w := csv.NewWriter(&out)
	if err := w.Write([]string{"name", "total"}); err != nil {
		return "", err
	}
	for _, row := range rows {
		if err := w.Write([]string{row.Name, strconv.Itoa(row.Total)}); err != nil {
			return "", err
		}
	}
	w.Flush()

	return out.String(), w.Error()
}

func FormatTSV(rows []Row) (string, error) {
	var out strings.Builder
	out.WriteString("name\ttotal\n")
	for _, row := range rows {
		out.WriteString(row.Name + "\t" + strconv.Itoa(row.Total) + "\n")
	}

	return out.String(), nil
}
`

const exportRegistryCochangePreviewTSV = `package export

import "fmt"

func Preview(format string, rows []Row) (string, error) {
	switch format {
	case "csv":
		return FormatCSV(rows)
	case "tsv":
		return FormatTSV(rows)
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}
`

const exportRegistryCochangeWorkerTSV = `package worker

import "example.com/exporter/internal/export"

var formatters = map[string]export.Formatter{
	"csv": export.FormatCSV,
	"tsv": export.FormatTSV,
}

func Formatter(name string) (export.Formatter, bool) {
	formatter, ok := formatters[name]

	return formatter, ok
}
`

const exportRegistryCochangePreviewTestTSV = `package export

import "testing"

func TestPreviewCSV(t *testing.T) {
	got, err := Preview("csv", []Row{{Name: "Alpha", Total: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "name,total\nAlpha,12\n" {
		t.Fatalf("CSV preview = %q", got)
	}
}

func TestPreviewTSV(t *testing.T) {
	got, err := Preview("tsv", []Row{{Name: "Alpha", Total: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "name\ttotal\nAlpha\t12\n" {
		t.Fatalf("TSV preview = %q", got)
	}
}
`

const exportRegistryCochangeWorkerTestTSV = `package worker

import "testing"

func TestKnownFormatsRegistered(t *testing.T) {
	for _, name := range []string{"csv", "tsv"} {
		if _, ok := Formatter(name); !ok {
			t.Fatalf("%s formatter is not registered", name)
		}
	}
}
`

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
