package htmlreport

import "sort"

// The hotspot map is laid out in two passes, and deliberately not with
// one algorithm. Directories are squarified — for a handful of boxes it
// gives good aspect ratios and a stable reading order (biggest at the
// top left). Files inside a directory are laid out as proportional
// strips instead: squarifying five items produced a tail of 990x2
// slivers that nothing could label, and for a box that small,
// predictable beats optimal.

// Map viewBox, in the abstract units the SVG scales from.
const (
	mapWidth  = 1000.0
	mapHeight = 520.0
)

// Layout tunables. minStrip is the smallest strip that can still carry
// a filename a reader can tell apart from its neighbour — measured, not
// guessed: at 52px two files in the same directory both truncated to
// "journ…". Fewer files per directory is the other half of that trade;
// a fifth cell only ever arrived at the floor width anyway, so it cost
// legibility without showing any more proportion.
const (
	boxPad      = 4.0
	boxHeading  = 17.0
	minStrip    = 64.0
	maxDirs     = 6
	maxDirFiles = 4
)

// Box is a laid-out rectangle in the map's coordinate space. Its fields
// are exported because the template draws directly from them — the SVG
// is emitted by html/template, not assembled as markup here, so every
// value it carries stays escaped.
type Box struct {
	Key        string
	X, Y, W, H float64
}

// weighted is one item competing for area.
type weighted struct {
	key    string
	weight float64
}

// byWeight sorts items biggest first, ties broken by key so the same
// index always produces the same map.
func byWeight(items []weighted) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].weight != items[j].weight {
			return items[i].weight > items[j].weight
		}

		return items[i].key < items[j].key
	})
}

// squarify lays items out to fill the given rectangle, favouring cells
// near square. The classic algorithm: grow a row while its worst aspect
// ratio improves, then commit it along the rectangle's shorter side and
// recurse into what is left. Items must be sorted biggest first.
func squarify(items []weighted, x, y, w, h float64) []Box {
	var out []Box

	if w <= 0 || h <= 0 {
		return nil
	}

	// Work in area units: scaling once up front means a row's summed
	// weight is directly the area it occupies.
	total := 0.0

	for _, it := range items {
		if it.weight > 0 {
			total += it.weight
		}
	}

	if total <= 0 {
		return nil
	}

	scale := (w * h) / total
	queue := make([]weighted, 0, len(items))

	for _, it := range items {
		if it.weight > 0 {
			queue = append(queue, weighted{it.key, it.weight * scale})
		}
	}

	var row []weighted

	for len(queue) > 0 {
		side := min(w, h)
		candidate := append(append([]weighted{}, row...), queue[0])

		if len(row) == 0 || worst(candidate, side) <= worst(row, side) {
			row, queue = candidate, queue[1:]

			continue
		}

		out = append(out, placeRow(row, &x, &y, &w, &h)...)
		row = nil
	}

	return append(out, placeRow(row, &x, &y, &w, &h)...)
}

// worst returns the least square aspect ratio in a row laid along side —
// the quantity squarify minimises. Lower is better; 1.0 is a square.
func worst(row []weighted, side float64) float64 {
	if len(row) == 0 || side <= 0 {
		return 0
	}

	sum, mx, mn := 0.0, row[0].weight, row[0].weight

	for _, it := range row {
		sum += it.weight
		mx, mn = max(mx, it.weight), min(mn, it.weight)
	}

	if sum <= 0 || mn <= 0 {
		return 0
	}

	return max((side*side*mx)/(sum*sum), (sum*sum)/(side*side*mn))
}

// placeRow commits a row against the shorter side of the remaining
// rectangle and shrinks that rectangle by what the row consumed. The
// rectangle is passed by pointer because consuming it is the point.
func placeRow(row []weighted, x, y, w, h *float64) []Box {
	if len(row) == 0 {
		return nil
	}

	sum := 0.0
	for _, it := range row {
		sum += it.weight
	}

	if sum <= 0 || *w <= 0 || *h <= 0 {
		return nil
	}

	out := make([]Box, 0, len(row))
	offset := 0.0

	if *w >= *h {
		// Wide remainder: the row becomes a column at the left edge.
		colWidth := min(sum / *h, *w)

		for _, it := range row {
			cell := (it.weight / sum) * *h
			out = append(out, Box{it.key, *x, *y + offset, colWidth, cell})
			offset += cell
		}

		*x += colWidth
		*w -= colWidth

		return out
	}

	// Tall remainder: the row becomes a band across the top.
	rowHeight := min(sum / *w, *h)

	for _, it := range row {
		cell := (it.weight / sum) * *w
		out = append(out, Box{it.key, *x + offset, *y, cell, rowHeight})
		offset += cell
	}

	*y += rowHeight
	*h -= rowHeight

	return out
}

// strips lays items along the rectangle's long axis in proportion to
// their weight, guaranteeing every drawn strip at least minStrip along
// that axis — a cell too small to label is worse than an absent one,
// because it still claims the reader's attention. Items that do not fit
// above the floor are dropped, biggest kept; dropped reports how many,
// so the caller can say so rather than quietly showing a partial
// picture. Items must be sorted biggest first.
func strips(items []weighted, x, y, w, h float64) (boxes []Box, dropped int) {
	if w <= 0 || h <= 0 {
		return nil, len(items)
	}

	var live []weighted

	for _, it := range items {
		if it.weight > 0 {
			live = append(live, it)
		}
	}

	if len(live) == 0 {
		return nil, 0
	}

	// Columns when the box is wide, rows when it is tall.
	vertical := w >= h

	span := h
	if vertical {
		span = w
	}

	// How many strips this span can carry legibly. None, when the box
	// itself is narrower than one strip.
	fits := int(span / minStrip)
	if fits < len(live) {
		dropped, live = len(live)-fits, live[:max(fits, 0)]
	}

	if len(live) == 0 {
		return nil, dropped
	}

	sizes := allocate(live, span)
	offset := 0.0

	for i, it := range live {
		size := sizes[i]

		// The last strip absorbs the rounding remainder, so the strips
		// close the box exactly and no seam shows at the edge.
		if i == len(live)-1 {
			size = span - offset
		}

		if vertical {
			boxes = append(boxes, Box{it.key, x + offset, y, size, h})
		} else {
			boxes = append(boxes, Box{it.key, x, y + offset, w, size})
		}

		offset += size
	}

	return boxes, dropped
}

// allocate splits span between items in proportion to weight, raising
// any share that lands under minStrip up to the floor and taking the
// difference from the rest. Callers guarantee the items fit
// (len(items)*minStrip <= span), which is what makes this terminate:
// with k items already floored the remainder still covers the rest at
// the floor, so each pass either floors nobody and returns, or floors
// at least one of a strictly shrinking set.
func allocate(items []weighted, span float64) []float64 {
	sizes := make([]float64, len(items))
	floored := make([]bool, len(items))

	for {
		free, weight := span, 0.0

		for i, it := range items {
			if floored[i] {
				free -= minStrip

				continue
			}

			weight += it.weight
		}

		changed := false

		for i, it := range items {
			if floored[i] {
				sizes[i] = minStrip

				continue
			}

			sizes[i] = free
			if weight > 0 {
				sizes[i] = (it.weight / weight) * free
			}

			if sizes[i] < minStrip {
				floored[i], changed = true, true
			}
		}

		if !changed {
			return sizes
		}
	}
}

// inset returns the area inside a directory box that its files may use:
// padded on every side, with room at the top for the directory label.
func inset(b Box) (x, y, w, h float64) {
	return b.X + boxPad, b.Y + boxHeading,
		max(b.W-2*boxPad, 1), max(b.H-boxHeading-boxPad, 1)
}
