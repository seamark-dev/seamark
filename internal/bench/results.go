package bench

// PriorCostFor only pools rows from the same immutable run fingerprint. An
// estimate made from another model, task, or runtime is worse than no estimate
// because it creates false confidence before a paid run.
func PriorCostFor(path, fingerprint string) (rows int, meanInput int64, meanCost float64, ok bool) {
	rs, err := ReadRows(path)
	if err != nil || len(rs) == 0 {
		return 0, 0, 0, false
	}

	var inputTotal, count int64
	var costTotal float64

	for _, row := range rs {
		if fingerprint != "" && row.Fingerprint != fingerprint {
			continue
		}
		if row.SchemaVersion >= 2 && (!row.Valid || !row.PairValid) {
			continue
		}

		input := row.ContextTokens
		if input == 0 {
			// Schema v1 called the combined fresh/cache count InputTokens.
			input = row.InputTokens
		}
		if input == 0 && row.CostUSD == 0 {
			continue
		}

		inputTotal += input
		costTotal += row.CostUSD
		count++
	}

	if count == 0 {
		return 0, 0, 0, false
	}

	return int(count), inputTotal / count, costTotal / float64(count), true
}
