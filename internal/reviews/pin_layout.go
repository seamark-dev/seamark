package reviews

// PinForRegions returns the pin that a writer adds to lessons.yaml for a
// rule, a note, and a region set. The region fields follow one layout.
// Region holds the first region, or "*" for an empty set. Regions holds
// the set only when it has more than one region.
//
// Region carries the first region for readers that predate region sets,
// so such a reader gets a narrower scope than "*". The set keeps its
// order, because the first region is the one those readers see.
//
// The apply renderer (distill.RenderPins), the manual pin renderer
// (pinfile.RenderPin), and the region splices of the pin editor use it.
// An applied pin and an authored pin thus get the same region keys.
func PinForRegions(rule, note string, set []string) PinRule {
	pin := PinRule{Rule: rule, Region: "*", Note: note}

	if len(set) > 0 {
		pin.Region = set[0]
	}

	if len(set) > 1 {
		pin.Regions = set
	}

	return pin
}
