// Package htmlreport renders the index as one self-contained HTML page:
// the human half of seamark. Agents read the compact text reports over
// MCP; a person auditing what the learning layer has concluded needs to
// see it all at once — what is pending a decision, what has been pinned
// twice in different words, where fixes concentrate, and every raw
// lesson behind those. The page is a snapshot, written to a file, with
// no server and no external assets.
//
// Everything it displays is untrusted text — review comments, commit
// subjects, and model-written proposals — so the page is built through
// html/template with auto-escaping, including the SVG map: geometry is
// computed here, markup is emitted there. Nothing in this package
// produces template.HTML.
package htmlreport

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Display caps. Each one is reported in the page when it bites — a
// truncated list that looks complete is worse than a long one.
const (
	maxCards      = 14   // proposal cards, pending first
	maxLessonRows = 600  // rows in the lessons table
	maxEvidence   = 420  // characters of one finding's body
	maxNoteDigest = 110  // characters of a note in the duplicate list
	churnScanned  = 400  // busiest files considered for the map
	minMapFiles   = 6    // below this, fall back to churn alone
	densityWindow = 20   // commits per file behind the fix-density colour
	labelCharPx   = 6.1  // monospace character width at the cell font size
	labelMinW     = 46.0 // a cell narrower than this gets no label
	labelMinH     = 19.0 // ...nor shorter than this
	metricMinH    = 32.0 // the fixes line needs a little more room still
)

// Report is one rendered page: the view model, fully resolved, so the
// template only ranges and prints.
type Report struct {
	Repo       string
	IndexState string
	Generated  string
	Stale      bool

	Stats   []Stat
	Sources string // the evidence mix behind every proposal

	Cards       []Card
	CardsTotal  int
	Pending     int
	Duplicates  []Duplicate
	AppliedPins int
	Redundant   int

	Groups    []Group
	Cells     []Cell
	MapWidth  float64
	MapHeight float64
	// FilesWithHistory is every file the index has commits for. The map
	// draws a fraction of them, and says which fraction: a picture that
	// silently omits most of the repo reads as the whole repo.
	FilesWithHistory int

	Lessons      []LessonRow
	LessonsTotal int
}

// Stat is one headline number.
type Stat struct {
	Value int
	Label string
}

// Card is a distilled proposal awaiting — or having received — a
// decision, with the evidence it cited.
type Card struct {
	ID      int64
	Rule    string
	Region  string
	Note    string
	Agent   string
	Status  string
	Pending bool
	// Cited is how many findings the agent cited when the pattern was
	// distilled — a fact of the proposal record, true forever.
	// Retrieved is how many of those the index can still show: mining
	// windows move on, and a pin applied months ago may cite comments
	// that have since aged out. When they differ the page says so
	// rather than presenting the shortfall as the evidence.
	Cited     int
	Retrieved int
	Events    int
	// Evidence health (RFC-002 §7), recomputed at report time: the
	// confidence tier with its facts, the prompt era when the proposal
	// predates the current recurrence rules, and the regions today's
	// inference would assign when they differ from the stored ones.
	Tier       string
	Facts      string
	Era        string
	RegionsNow string
	// Scope is the trigger-scope advisory sentence (RFC-004): the note
	// and co-change history point outside the pin's regions, so
	// delivery may miss the trigger site. Same Line() the ledger prints.
	Scope string
	// Blocked reports confirmed triggers that cannot become delivery scopes —
	// no drift and no advisory would otherwise mention them.
	Blocked  string
	Evidence []Evidence
	// Outcome is the passive loop's verdict sentence,
	// present only for measured applied pins.
	Outcome string
	// NotLanding styles the outcome as the class that needs action.
	NotLanding bool
}

// Evidence is one finding behind a proposal, as displayed.
type Evidence struct {
	Source string // review | fix:conventional | ...
	Kind   string // the source's provider, for the badge colour
	Path   string
	Body   string
	URL    string // "" unless it is a plain http(s) link
}

// Duplicate is a set of applied pins that restate one theme.
type Duplicate struct {
	Pins []DuplicatePin
}

// DuplicatePin is one member of such a set.
type DuplicatePin struct {
	ID     int64
	Rule   string
	Digest string
}

// Group is a directory box on the hotspot map. Files is how many the
// directory holds, not how many are drawn — the label carries the
// difference so a four-cell box does not read as a four-file directory.
type Group struct {
	Box
	Label string
	Files int
}

// Cell is one file on the hotspot map.
type Cell struct {
	Box
	File    string
	Scope   string // what clicking it filters the lessons table by
	Label   string // truncated to what the cell can show; "" when it cannot
	Metric  string // the fix count line; "" when the cell is too short
	Fill    string
	Ink     string
	Tooltip string
}

// LessonRow is one mined lesson in the explorer table.
type LessonRow struct {
	Occurrences int
	Region      string
	Reviewer    string
	Symptom     string
	Search      string // lowercased region + symptom, for the text filter
	// Scopes are the map scopes this lesson belongs to, space-separated.
	// Region membership is decided here rather than in the page, so
	// clicking a cell cannot disagree with the count in its tooltip.
	Scopes string
}

// Generate builds the report from the index and writes the page.
func Generate(w io.Writer, st *store.Store, root string, now time.Time) error {
	r, err := Build(st, root, now)
	if err != nil {
		return err
	}

	return Render(w, r)
}

// Build assembles the view model. now is a parameter rather than read
// from the clock so a test can assert the whole page byte for byte.
func Build(st *store.Store, root string, now time.Time) (*Report, error) {
	findings, err := st.AllFindings()
	if err != nil {
		return nil, err
	}

	lessons, err := st.AllLessons(0)
	if err != nil {
		return nil, err
	}

	proposals, err := allProposals(st)
	if err != nil {
		return nil, err
	}

	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	firings, err := reviews.ReadFirings(root)
	if err != nil {
		return nil, err
	}

	var applied []model.Proposal

	for _, p := range proposals {
		if p.Status == model.ProposalApplied {
			applied = append(applied, p)
		}
	}

	readings, err := outcome.Gather(st, cfg, applied, firings)
	if err != nil {
		return nil, err
	}

	// The same trigger-scope audit the ledger prints, from the same
	// helper, over the cards' own evidence universe — the surfaces
	// cannot disagree. With a fallback config every applied pin reads
	// as pruned and skips; pending rows still audit.
	byID := make(map[int64]model.Finding, len(findings))
	for _, f := range findings {
		byID[f.ID] = f
	}

	// Only the visible cards need store work: allProposals sorted the
	// list exactly the way buildCards truncates it, and the ledger on
	// a mature corpus holds several times maxCards rows.
	visible := proposals
	if len(visible) > maxCards {
		visible = visible[:maxCards]
	}

	scopes, err := distill.AuditScopes(st, cfg, root, visible, byID)
	if err != nil {
		return nil, err
	}

	// One recompute for every "regions now" reader (see
	// distill.RecomputeRegions): the cards must show the same current
	// set the ledger and --retarget use. Blocked triggers ride along —
	// a confirmed miss with no drift line must not vanish from the
	// page.
	regionsNow := make(map[int64][]string, len(visible))
	blocked := make(map[int64]string)

	for _, p := range visible {
		if p.Status != model.ProposalProposed && p.Status != model.ProposalApplied {
			continue
		}

		var living []model.Finding

		for _, id := range p.Members {
			if f, ok := byID[id]; ok {
				living = append(living, f)
			}
		}

		if len(living) == 0 {
			continue
		}

		recomputed, facts, err := distill.RecomputeRegions(st, root, p, living)
		if err != nil {
			return nil, err
		}

		regionsNow[p.ID] = recomputed

		for _, f := range facts {
			if line := f.BlockedLine(); line != "" {
				if blocked[p.ID] != "" {
					blocked[p.ID] += "; "
				}

				blocked[p.ID] += line
			}
		}
	}

	// One churn query serves both the headline count and the map, so the
	// two can never describe different sets of files.
	churn, err := st.FileChurn(0)
	if err != nil {
		return nil, err
	}

	state, _ := st.GetMeta("indexed_state")
	current := index.WorkspaceState(root)

	r := &Report{
		Repo:       repoName(root),
		IndexState: prefix(state, 12),
		Generated:  now.Format("2006-01-02 15:04"),
		// A report that outlives the terminal it was generated from
		// must carry its own freshness warning: unreadable state is not
		// evidence of staleness, so an unknown current state stays quiet.
		Stale:            state != "" && current != "" && current != state,
		Sources:          sourceMix(findings),
		MapWidth:         mapWidth,
		MapHeight:        mapHeight,
		FilesWithHistory: len(churn),
	}

	r.buildCards(proposals, byID, readings, scopes, regionsNow, blocked, root, now)
	r.buildDuplicates(proposals)

	// The map first: it decides which scopes exist, and each lesson row
	// records the ones it belongs to so clicking a cell filters by
	// region rather than by text that happens to contain the word.
	if err := r.buildMap(st, churn, lessons); err != nil {
		return nil, err
	}

	r.buildLessons(lessons)

	notLanding := 0

	for _, rd := range readings {
		if rd.Verdict == outcome.VerdictNotLanding {
			notLanding++
		}
	}

	r.Stats = []Stat{
		{len(lessons), "lessons"},
		{len(findings), "findings"},
		{r.Pending, "pending proposals"},
		{r.CardsTotal - r.Pending, "decided"},
		{len(readings), "pins measured"},
		{notLanding, "not landing"},
		{len(churn), "files with history"},
	}

	return r, nil
}

// allProposals returns every proposal in every state, newest first —
// the report shows the whole ledger, not just what is open.
func allProposals(st *store.Store) ([]model.Proposal, error) {
	var out []model.Proposal

	for _, status := range []string{
		model.ProposalProposed, model.ProposalApplied,
		model.ProposalDismissed, model.ProposalSuperseded,
	} {
		ps, err := st.Proposals(status)
		if err != nil {
			return nil, err
		}

		out = append(out, ps...)
	}

	// Pending first, then newest — the queue is what the reader came for.
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Status == model.ProposalProposed, out[j].Status == model.ProposalProposed; a != b {
			return a
		}

		return out[i].ID > out[j].ID
	})

	return out, nil
}

// buildCards renders the decision queue: pending proposals first, then
// the most recent decisions, each with the findings it cited. byID is
// the evidence universe the caller also feeds the scope audit.
func (r *Report) buildCards(
	proposals []model.Proposal,
	byID map[int64]model.Finding,
	readings map[int64]outcome.Reading,
	scopes map[int64]distill.ScopeAdvisory,
	regionsNow map[int64][]string,
	blocked map[int64]string,
	root string,
	now time.Time,
) {

	r.CardsTotal = len(proposals)

	for _, p := range proposals {
		if p.Status == model.ProposalProposed {
			r.Pending++
		}
	}

	if len(proposals) > maxCards {
		proposals = proposals[:maxCards]
	}

	for _, p := range proposals {
		var cited []model.Finding

		for _, id := range p.Members {
			if f, ok := byID[id]; ok {
				cited = append(cited, f)
			}
		}

		// Cited comes from the proposal record, not from the lookup:
		// findings are re-mined on a moving window while decided
		// proposals persist forever, so an old applied pin's evidence
		// can age out of the index. Counting only what is still there
		// would report a well-founded pin as resting on nothing.
		card := Card{
			ID: p.ID, Rule: clean(p.Rule), Region: regionLabel(p),
			Note: clean(p.Note), Agent: clean(p.Agent), Status: p.Status,
			Pending: p.Status == model.ProposalProposed,
			Cited:   len(p.Members), Retrieved: len(cited),
			Events: distill.CountEvents(cited),
		}

		// The same re-judgment the --proposals ledger prints: this page
		// is where the human decides, so the decision-relevant facts
		// must be on the card, not only in the terminal.
		if p.Status == model.ProposalProposed || p.Status == model.ProposalApplied {
			tier, facts := confidence.Assess(p, byID, root, now)
			card.Tier = tier.String()
			card.Facts = clean(facts.Line())

			if strings.HasSuffix(p.Agent, "/v1") {
				card.Era = "distilled under prompt v1, before the same-PR-counts-once rule"
			}

			if recomputed, ok := regionsNow[p.ID]; ok {
				if reviews.NewPinKey("x", "", recomputed) != reviews.NewPinKey("x", "", p.RegionSet()) {
					card.RegionsNow = clean(strings.Join(recomputed, ", "))
					if card.RegionsNow == "" {
						card.RegionsNow = "*"
					}
				}
			}

			// The passive-loop verdict; readings only contains measured
			// applied pins, so no status check is needed here.
			if rd, ok := readings[p.ID]; ok {
				card.Outcome = clean(rd.Line())
				card.NotLanding = rd.Verdict == outcome.VerdictNotLanding
			}

			// The trigger-scope advisory; scopes only contains flagged
			// pending and applied rows, same rule.
			if adv, ok := scopes[p.ID]; ok {
				card.Scope = clean(adv.Line())
			}

			card.Blocked = clean(blocked[p.ID])
		}

		for _, f := range cited {
			provider, _, _ := strings.Cut(f.Source, ":")
			card.Evidence = append(card.Evidence, Evidence{
				Source: clean(f.Source), Kind: clean(provider), Path: clean(f.Path),
				Body: shorten(cleanBlock(f.Body), maxEvidence), URL: safeURL(f.URL),
			})
		}

		r.Cards = append(r.Cards, card)
	}
}

// buildDuplicates audits the applied pins against each other: which of
// them say the same thing in different words. seamark never edits
// lessons.yaml unasked, so this reports and points at `--prune`.
func (r *Report) buildDuplicates(proposals []model.Proposal) {
	var applied []model.Proposal

	for _, p := range proposals {
		if p.Status == model.ProposalApplied {
			applied = append(applied, p)
		}
	}

	r.AppliedPins = len(applied)

	for _, cluster := range distill.Clusters(applied) {
		dup := Duplicate{}

		for _, p := range cluster {
			dup.Pins = append(dup.Pins, DuplicatePin{
				ID: p.ID, Rule: clean(p.Rule),
				Digest: shorten(clean(p.Note), maxNoteDigest),
			})
		}

		r.Redundant += len(cluster) - 1
		r.Duplicates = append(r.Duplicates, dup)
	}
}

// buildLessons fills the explorer table with every mined lesson,
// one-offs included: this is the raw material, not the filtered view
// the agent surfaces get. Each row also records which of the map's
// scopes it belongs to — see LessonRow.Scopes.
func (r *Report) buildLessons(lessons []model.Lesson) {
	r.LessonsTotal = len(lessons)

	if len(lessons) > maxLessonRows {
		lessons = lessons[:maxLessonRows]
	}

	// The scopes the map can filter by, in the order the cells were
	// drawn, deduplicated: several files in a directory share one.
	var scopes []string

	seen := map[string]bool{}

	for _, c := range r.Cells {
		if !seen[c.Scope] {
			seen[c.Scope] = true

			scopes = append(scopes, c.Scope)
		}
	}

	for _, l := range lessons {
		row := LessonRow{
			Occurrences: l.Occurrences, Region: clean(l.Region),
			Reviewer: clean(l.Reviewer), Symptom: clean(l.Symptom),
		}
		row.Search = strings.ToLower(row.Region + " " + row.Symptom)

		var applies []string

		for _, scope := range scopes {
			if LessonInScope(l.Region, scope) {
				applies = append(applies, scope)
			}
		}

		// Space-separated because regions are paths and cannot contain
		// spaces; the page splits on it.
		row.Scopes = strings.Join(applies, " ")

		r.Lessons = append(r.Lessons, row)
	}
}

// LessonInScope reports whether a lesson mined for lessonRegion belongs
// to the area named by scope: the region itself, anything beneath it,
// and the ancestors that govern it — the same "area" the lesson ledger
// and `expand lessons:<dir>` use. An empty region is repo-wide and so
// belongs to every scope.
//
// It exists because the page filters client-side, and a filter that
// matched substrings would put a `web` lesson mentioning "API" under
// scope `api` while hiding the repo-wide lesson that really governs it.
// Deciding membership here keeps one implementation of the rule; the
// page only reads the answer back off each row.
func LessonInScope(lessonRegion, scope string) bool {
	if lessonRegion == "" {
		return true
	}

	return reviews.RegionMatches(lessonRegion, scope) ||
		reviews.RegionMatches(scope, lessonRegion)
}

// buildMap lays out the hotspot map: directories sized by the history
// they carry, files inside them coloured by fix density. churn arrives
// busiest-first; only its head is considered, since nothing below that
// could win area on a map of two dozen cells.
func (r *Report) buildMap(st *store.Store, churn []store.FileChurn, lessons []model.Lesson) error {
	if len(churn) > churnScanned {
		churn = churn[:churnScanned]
	}

	files, err := mapCandidates(st, churn)
	if err != nil {
		return err
	}

	// Group by directory: a flat map of fifty cells was unreadable, and
	// the directory is how a person orients in a repo.
	byDir := map[string][]store.FileChurn{}
	for _, f := range files {
		byDir[path.Dir(f.File)] = append(byDir[path.Dir(f.File)], f)
	}

	dirs := make([]weighted, 0, len(byDir))

	for dir, fs := range byDir {
		total := 0.0
		for _, f := range fs {
			total += float64(f.Commits)
		}

		dirs = append(dirs, weighted{dir, total})
	}

	byWeight(dirs)

	if len(dirs) > maxDirs {
		dirs = dirs[:maxDirs]
	}

	for _, box := range squarify(dirs, 0, 0, mapWidth, mapHeight) {
		fs := byDir[box.Key]
		shown := fs

		if len(shown) > maxDirFiles {
			shown = shown[:maxDirFiles]
		}

		items := make([]weighted, len(shown))
		for i, f := range shown {
			items[i] = weighted{f.File, float64(f.Commits)}
		}

		x, y, w, h := inset(box)

		// Strips may still refuse a file the box has no legible room
		// for; the group label's file count is what tells the reader
		// the box holds more than it draws.
		boxes, _ := strips(items, x, y, w, h)

		r.Groups = append(r.Groups, Group{
			Box: box, Label: dirLabel(box.Key), Files: len(fs),
		})

		for _, cellBox := range boxes {
			cell, err := r.cell(st, cellBox, lessons)
			if err != nil {
				return err
			}

			r.Cells = append(r.Cells, cell)
		}
	}

	return nil
}

// mapCandidates picks the files worth drawing: the code seamark parsed,
// tests excluded, busiest first. A repo whose languages seamark cannot
// parse yet would map to nothing, so it falls back to raw history —
// better an approximate map than an empty one.
func mapCandidates(st *store.Store, churn []store.FileChurn) ([]store.FileChurn, error) {
	symbols, err := st.FileSymbolCounts()
	if err != nil {
		return nil, err
	}

	var parsed, fallback []store.FileChurn

	for _, f := range churn {
		// Test churn points attention at the wrong files — the same
		// reason why/orient collapse test callers.
		if model.IsTestPath(f.File) {
			continue
		}

		if symbols[f.File] > 0 {
			parsed = append(parsed, f)
		}

		fallback = append(fallback, f)
	}

	if len(parsed) >= minMapFiles {
		return parsed, nil
	}

	return fallback, nil
}

// cell turns one laid-out box into a drawable file cell: heat colour,
// tooltip, and labels sized to what actually fits.
func (r *Report) cell(st *store.Store, box Box, lessons []model.Lesson) (Cell, error) {
	decisions, err := st.DecisionsForFile(box.Key, densityWindow)
	if err != nil {
		return Cell{}, err
	}

	// The same membership rule the rows record, so the number in the
	// tooltip is exactly what clicking the cell shows.
	scope := report.LessonScope(box.Key)
	inScope := 0

	for _, l := range lessons {
		if LessonInScope(l.Region, scope) {
			inScope++
		}
	}

	fixCount, commits := report.FixCount(decisions), len(decisions)
	density := "too little history to judge"
	ratio := 0.0

	// Below the shared floor a ratio is noise, not a hotspot, so the
	// cell stays calm rather than colouring on one unlucky commit.
	if commits >= report.MinFixDensityHistory {
		ratio = float64(fixCount) / float64(commits)
		density = fmt.Sprintf("%d of the last %d commits were fixes", fixCount, commits)
	}

	fill, ink := heat(ratio)
	cell := Cell{
		Box: box, File: box.Key, Scope: scope, Fill: fill, Ink: ink,
		Tooltip: fmt.Sprintf("%s\n%s · %d lessons in %s", box.Key, density, inScope, scope),
	}

	if box.W > labelMinW && box.H > labelMinH {
		cell.Label = shorten(path.Base(box.Key), int((box.W-12)/labelCharPx))

		if box.H > metricMinH && commits >= report.MinFixDensityHistory {
			cell.Metric = fmt.Sprintf("%d/%d fixes", fixCount, commits)
		}
	}

	return cell, nil
}

// Heat steps, darkest at the top. Solid fills with a fixed ink colour
// each: an opacity ramp looked clever and made half the labels
// unreadable.
var heatSteps = []struct {
	from      float64
	fill, ink string
}{
	{0.40, "#C4553D", "#FFF6F2"},
	{0.25, "#E8B23A", "#14181A"},
	{0.12, "#B8A44A", "#14181A"},
	{0.00, "#4B7A6B", "#F1F7F4"},
}

func heat(ratio float64) (fill, ink string) {
	for _, step := range heatSteps {
		if ratio >= step.from {
			return step.fill, step.ink
		}
	}

	// Unreachable for any real ratio (the last step starts at zero), but
	// a NaN compares false against every bound, and a cell with no fill
	// is invisible rather than merely miscoloured.
	last := heatSteps[len(heatSteps)-1]

	return last.fill, last.ink
}

// sourceMix summarises where the evidence came from, commonest first —
// the one line that says whether this repo's lessons rest on reviews,
// on fix commits, or on both.
func sourceMix(findings []model.Finding) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Source]++
	}

	sources := make([]string, 0, len(counts))
	for source := range counts {
		sources = append(sources, source)
	}

	sort.Slice(sources, func(i, j int) bool {
		if counts[sources[i]] != counts[sources[j]] {
			return counts[sources[i]] > counts[sources[j]]
		}

		return sources[i] < sources[j]
	})

	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fmt.Sprintf("%d %s", counts[source], clean(source)))
	}

	return strings.Join(parts, " · ")
}

// clean strips control characters — including newlines — from text
// that came from a reviewer, a commit, or a model. Escaping is the
// template's job; this is about text that would corrupt the page's
// layout rather than its markup.
func clean(s string) string {
	return render.Sanitize(s)
}

// cleanBlock is clean for text displayed as a block, where line breaks
// are content rather than damage: a review comment's paragraphs are
// most of what makes it readable evidence.
func cleanBlock(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}

		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, s)
}

// shorten trims to n characters with a trailing ellipsis, counting
// runes so a cut never lands inside a multi-byte character.
func shorten(s string, n int) string {
	return render.Truncate(s, n)
}

// shortenPath trims a path from the front, keeping the tail: the last
// segments of api/services/... identify it, the first ones do not.
func shortenPath(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n || n <= 1 {
		return shorten(s, n)
	}

	return "…" + string(runes[len(runes)-n+1:])
}

// prefix takes the first n runes as-is — for an index state, where an
// ellipsis would only suggest the hash was longer than it is.
func prefix(s string, n int) string {
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n])
	}

	return s
}

// dirLabel names a directory box. path.Dir spells the repository root
// ".", which reads as a mistake on a map otherwise full of real paths.
func dirLabel(dir string) string {
	if dir == "." {
		return "repository root"
	}

	return shortenPath(dir, 34)
}

// regionLabel names a proposal's area — the full region set, with the
// repo-wide star spelled the way lessons.yaml spells it.
func regionLabel(p model.Proposal) string {
	if set := p.RegionSet(); len(set) > 0 {
		return clean(strings.Join(set, ", "))
	}

	return "*"
}

// safeURL passes through plain http(s) links and drops everything else.
// html/template would neutralise a javascript: href anyway; this keeps
// the page from displaying a dead link where a provenance link belongs.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return u.String()
}

// repoName is the workspace's directory name — what a person calls this
// repository. filepath, not path: root is an operating-system path, and
// on Windows a slash-only Base returns the whole thing, putting
// C:\Users\someone\repo in the header of a report meant to be shared.
// (Everything else in this package handles repo-relative index paths,
// which are always slash-separated, and correctly uses path.)
func repoName(root string) string {
	base := filepath.Base(filepath.Clean(root))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "workspace"
	}

	return base
}
