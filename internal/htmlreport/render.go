package htmlreport

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
)

// The page ships inside the binary: a report must be one file a person
// can mail, attach to a pull request, or open on a machine with no
// network — which rules out a stylesheet or script fetched at view time.
//
//go:embed report.html.tmpl
var pageSource string

// Coordinate helpers, the only arithmetic the template does. Geometry
// is computed in Go; these exist so the SVG can place a label relative
// to its own box without the view model carrying a field per glyph.
var funcs = template.FuncMap{
	// px formats a coordinate. One decimal is below display resolution
	// and keeps the file stable: the same index renders the same bytes.
	"px": func(v float64) string { return fmt.Sprintf("%.1f", v) },
	// at offsets a coordinate — the inset of a label inside its box.
	"at": func(v, delta float64) string { return fmt.Sprintf("%.1f", v+delta) },
	// side is a box's drawn extent, a pixel short on each edge so
	// neighbouring cells read as separate tiles rather than one slab.
	"side": func(v float64) string { return fmt.Sprintf("%.1f", max(v-2, 0)) },
	// plural counts things in English. Every number on this page is a
	// count of evidence, and "1 findings" reads as a bug in the tool.
	"plural": func(n int, word string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, word)
		}

		return fmt.Sprintf("%d %ss", n, word)
	},
}

// page is parsed once: a template that fails to compile is a bug in
// this package, not a runtime condition a caller can act on.
var page = template.Must(template.New("report").Funcs(funcs).Parse(pageSource))

// Render writes the report as a self-contained HTML page. Every value
// passes through html/template's contextual escaping — the report shows
// text written by reviewers, by commit authors, and by a language
// model, none of which may become markup.
func Render(w io.Writer, r *Report) error {
	if err := page.Execute(w, r); err != nil {
		return fmt.Errorf("render report: %w", err)
	}

	return nil
}
