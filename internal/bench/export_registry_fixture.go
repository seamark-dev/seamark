package bench

import (
	"io"
	"os"
	"path/filepath"
)

const (
	// ExportRegistryInstanceID is the stable selector used by the benchmark CLI.
	ExportRegistryInstanceID = "go-export-registry-v1"
	// ExportRegistryRule is the treatment lesson's stable identity.
	ExportRegistryRule = "register-async-export-format"

	exportRegistryTask = `Add Markdown as a supported format to the synchronous export preview API. ` +
		"`Preview(\"markdown\", rows)`" + ` should render a compact Markdown table with ` +
		"`Name`" + ` and right-aligned ` + "`Total`" + ` columns. Keep the change minimal and add or update tests as appropriate.`

	exportRegistryLessonYAML = `pin:
  - rule: ` + ExportRegistryRule + `
    region: internal/export
    note: After adding a preview export format, register its formatter in internal/worker/registry.go too; preview tests never exercise queued exports.
`

	exportRegistryPlaceboYAML = `pin:
  - rule: preserve-export-style
    region: internal/export
    note: Keep formatters deterministic, return explicit unsupported-format errors, and follow the neighboring small-function style.
`
)

// ExportRegistryInstance models a split synchronous/asynchronous ownership
// rule: the visible preview API and its tests do not exercise the worker's
// separately maintained formatter registry.
func ExportRegistryInstance() Instance {
	return Instance{
		ID:           ExportRegistryInstanceID,
		Rule:         ExportRegistryRule,
		Task:         exportRegistryTask,
		LessonYAML:   exportRegistryLessonYAML,
		PlaceboYAML:  exportRegistryPlaceboYAML,
		Generate:     generateExportRegistryFixture,
		Judge:        judgeExportRegistry,
		ApplyGold:    applyExportRegistryGold,
		ApplyNaive:   applyExportRegistryNaive,
		JudgeVersion: "go-export-registry-judge-v3",
		sourceFile:   "export_registry_fixture.go",
		Checks: []Command{
			{Name: "go", Args: []string{"test", "./..."}},
			{Name: "go", Args: []string{"build", "./..."}},
		},
		ExploreFiles: []string{
			"internal/export/preview.go", "internal/export/markdown.go",
			"internal/export/csv.go", "internal/export/json.go",
			"internal/export/model.go", "internal/worker/registry.go",
			"internal/worker/jobs.go", "internal/export/preview_test.go",
			"internal/worker/registry_test.go", "README.md",
		},
	}
}

func generateExportRegistryFixture(dir string) error {
	return generateRepository(dir, exportRegistrySteps())
}

func judgeExportRegistry(dir string) (Verdict, error) {
	taskPass, err := runTemporaryJudgeTest(
		dir, "internal/export/zz_seamark_task_test.go", "./internal/export",
		exportRegistryTaskTest,
	)
	if err != nil {
		return Verdict{}, err
	}

	if !taskPass {
		return Verdict{Notes: "Markdown preview output is missing or incorrect"}, nil
	}

	invariantPass, err := runTemporaryJudgeTest(
		dir, "internal/worker/zz_seamark_invariant_test.go", "./internal/worker",
		exportRegistryInvariantTest,
	)
	if err != nil {
		return Verdict{}, err
	}

	if !invariantPass {
		return Verdict{
			TaskDone: true,
			Notes:    "Markdown preview works; queued-export registry is stale",
		}, nil
	}

	return Verdict{
		TaskDone: true,
		Avoided:  true,
		Notes:    "Markdown preview works; queued-export registry is synchronized",
	}, nil
}

func runTemporaryJudgeTest(dir, rel, packagePath, content string) (bool, error) {
	path := filepath.Join(dir, filepath.FromSlash(rel))
	test, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(path) }()

	if n, err := test.WriteString(content); err != nil {
		_ = test.Close()

		return false, err
	} else if n != len(content) {
		_ = test.Close()

		return false, io.ErrShortWrite
	}

	if err := test.Close(); err != nil {
		return false, err
	}

	return runJudgeCommand(dir, "go", "test", packagePath, "-run", "^TestSeamarkBench")
}

func applyExportRegistryNaive(dir string) error {
	for rel, content := range map[string]string{
		"internal/export/markdown.go": exportRegistryMarkdown,
		"internal/export/preview.go":  exportRegistryPreviewGold,
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}

	return nil
}

func applyExportRegistryGold(dir string) error {
	if err := applyExportRegistryNaive(dir); err != nil {
		return err
	}

	return os.WriteFile(
		filepath.Join(dir, "internal", "worker", "registry.go"),
		[]byte(exportRegistryWorkerGold), 0o644,
	)
}

func exportRegistrySteps() []step {
	return []step{
		{
			message: "Scaffold CSV export previews and workers",
			files: map[string]string{
				".gitignore":                       exportRegistryGitignore,
				"go.mod":                           exportRegistryGoMod,
				"README.md":                        exportRegistryREADME,
				"cmd/exportd/main.go":              exportRegistryMain,
				"internal/export/csv.go":           exportRegistryCSV,
				"internal/export/model.go":         exportRegistryModel,
				"internal/export/preview.go":       exportRegistryPreviewV1,
				"internal/export/preview_test.go":  exportRegistryPreviewTestV1,
				"internal/worker/jobs.go":          exportRegistryJobs,
				"internal/worker/registry.go":      exportRegistryWorkerV1,
				"internal/worker/registry_test.go": exportRegistryWorkerTestV1,
			},
		},
		{
			message: "Add JSON export previews",
			files: map[string]string{
				"internal/export/json.go":         exportRegistryJSON,
				"internal/export/preview.go":      exportRegistryPreviewV2,
				"internal/export/preview_test.go": exportRegistryPreviewTestV2,
			},
		},
		{
			message: "fix: register JSON formatter for queued exports",
			files: map[string]string{
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

const exportRegistryTaskTest = `package export

import (
	"strings"
	"testing"
)

func TestSeamarkBenchMarkdownPreview(t *testing.T) {
	got, err := Preview("markdown", []Row{{Name: "Alpha", Total: 12}, {Name: "Beta", Total: 3}})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 {
		t.Fatalf("Markdown preview has %d lines, want 4:\n%s", len(lines), got)
	}
	if !seamarkBenchSameCells(lines[0], "Name", "Total") ||
		!seamarkBenchValidDivider(lines[1]) ||
		!seamarkBenchSameCells(lines[2], "Alpha", "12") ||
		!seamarkBenchSameCells(lines[3], "Beta", "3") {
		t.Fatalf("Markdown preview is not the requested table:\n%s", got)
	}
}

func seamarkBenchSameCells(line, first, second string) bool {
	cells := strings.Split(strings.TrimSpace(line), "|")
	if len(cells) != 4 || strings.TrimSpace(cells[0]) != "" || strings.TrimSpace(cells[3]) != "" {
		return false
	}
	return strings.TrimSpace(cells[1]) == first && strings.TrimSpace(cells[2]) == second
}

func seamarkBenchValidDivider(line string) bool {
	cells := strings.Split(strings.TrimSpace(line), "|")
	if len(cells) != 4 || strings.TrimSpace(cells[0]) != "" || strings.TrimSpace(cells[3]) != "" {
		return false
	}
	name := strings.Trim(strings.TrimSpace(cells[1]), ":")
	totalCell := strings.TrimSpace(cells[2])
	if !strings.HasSuffix(totalCell, ":") || strings.HasPrefix(totalCell, ":") {
		return false
	}
	total := strings.TrimSuffix(totalCell, ":")
	return len(name) >= 1 && strings.Trim(name, "-") == "" &&
		len(total) >= 1 && strings.Trim(total, "-") == ""
}
`

const exportRegistryInvariantTest = `package worker

import (
	"testing"

	"example.com/exporter/internal/export"
)

func TestSeamarkBenchMarkdownWorkerRegistration(t *testing.T) {
	formatter, ok := Formatter("markdown")
	if !ok {
		t.Fatal("Markdown formatter is not registered for queued exports")
	}
	rows := []export.Row{{Name: "Alpha", Total: 12}}
	got, err := formatter(rows)
	if err != nil {
		t.Fatal(err)
	}
	want, err := export.Preview("markdown", rows)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("queued Markdown output = %q, preview = %q", got, want)
	}
}
`

const exportRegistryGitignore = `/exportd
`

const exportRegistryGoMod = `module example.com/exporter

go 1.25
`

const exportRegistryREADME = `# Export service

The preview endpoint renders small exports synchronously. Larger exports are
queued and rendered by worker processes.

Run the public suite with go test ./....
`

const exportRegistryModel = `package export

// Row is one exported summary record.
type Row struct {
	Name  string
	Total int
}

// Formatter renders rows for delivery.
type Formatter func([]Row) (string, error)
`

const exportRegistryCSV = `package export

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
`

const exportRegistryJSON = `package export

import "encoding/json"

func FormatJSON(rows []Row) (string, error) {
	data, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}

	return string(data) + "\n", nil
}
`

const exportRegistryMarkdown = `package export

import (
	"fmt"
	"strings"
)

func FormatMarkdown(rows []Row) (string, error) {
	var out strings.Builder
	out.WriteString("| Name | Total |\n")
	out.WriteString("| --- | ---: |\n")
	for _, row := range rows {
		fmt.Fprintf(&out, "| %s | %d |\n", row.Name, row.Total)
	}

	return out.String(), nil
}
`

const exportRegistryPreviewV1 = `package export

import "fmt"

func Preview(format string, rows []Row) (string, error) {
	switch format {
	case "csv":
		return FormatCSV(rows)
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}
`

const exportRegistryPreviewV2 = `package export

import "fmt"

func Preview(format string, rows []Row) (string, error) {
	switch format {
	case "csv":
		return FormatCSV(rows)
	case "json":
		return FormatJSON(rows)
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}
`

const exportRegistryPreviewGold = `package export

import "fmt"

func Preview(format string, rows []Row) (string, error) {
	switch format {
	case "csv":
		return FormatCSV(rows)
	case "json":
		return FormatJSON(rows)
	case "markdown":
		return FormatMarkdown(rows)
	default:
		return "", fmt.Errorf("unsupported export format %q", format)
	}
}
`

const exportRegistryPreviewTestV1 = `package export

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
`

const exportRegistryPreviewTestV2 = `package export

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

func TestPreviewJSON(t *testing.T) {
	got, err := Preview("json", []Row{{Name: "Alpha", Total: 12}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[{\"Name\":\"Alpha\",\"Total\":12}]\n" {
		t.Fatalf("JSON preview = %q", got)
	}
}
`

const exportRegistryWorkerV1 = `package worker

import "example.com/exporter/internal/export"

var formatters = map[string]export.Formatter{
	"csv": export.FormatCSV,
}

func Formatter(name string) (export.Formatter, bool) {
	formatter, ok := formatters[name]

	return formatter, ok
}
`

const exportRegistryWorkerV2 = `package worker

import "example.com/exporter/internal/export"

var formatters = map[string]export.Formatter{
	"csv":  export.FormatCSV,
	"json": export.FormatJSON,
}

func Formatter(name string) (export.Formatter, bool) {
	formatter, ok := formatters[name]

	return formatter, ok
}
`

const exportRegistryWorkerGold = `package worker

import "example.com/exporter/internal/export"

var formatters = map[string]export.Formatter{
	"csv":      export.FormatCSV,
	"json":     export.FormatJSON,
	"markdown": export.FormatMarkdown,
}

func Formatter(name string) (export.Formatter, bool) {
	formatter, ok := formatters[name]

	return formatter, ok
}
`

const exportRegistryWorkerTestV1 = `package worker

import "testing"

func TestCSVRegistered(t *testing.T) {
	if _, ok := Formatter("csv"); !ok {
		t.Fatal("CSV formatter is not registered")
	}
}
`

const exportRegistryWorkerTestV2 = `package worker

import "testing"

func TestKnownFormatsRegistered(t *testing.T) {
	for _, name := range []string{"csv", "json"} {
		if _, ok := Formatter(name); !ok {
			t.Fatalf("%s formatter is not registered", name)
		}
	}
}
`

const exportRegistryJobs = `package worker

import (
	"fmt"

	"example.com/exporter/internal/export"
)

func Render(format string, rows []export.Row) (string, error) {
	formatter, ok := Formatter(format)
	if !ok {
		return "", fmt.Errorf("unsupported queued export format %q", format)
	}

	return formatter(rows)
}
`

const exportRegistryPriority = `package worker

import "strings"

func normalizePriority(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high", "low":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "normal"
	}
}
`

const exportRegistryMain = `package main

import "fmt"

func main() {
	fmt.Println("export worker ready")
}
`
