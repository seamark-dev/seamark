package lessonreport

import "github.com/seamark-dev/seamark/internal/lessoncheck"

// checkIndex reads the check stream in its own units. A pin version never
// borrows counts from another version of the same identity.
type checkIndex struct {
	reading CheckReading
	pins    map[versionKey]PinCheckReading
}

// readChecks makes one reverse pass. The set of later completions lets
// each deny read its outcome without a scan of the rest of the stream.
func readChecks(in Inputs) *checkIndex {
	c := &checkIndex{
		reading: CheckReading{
			State: checkAuditSource(in).State, Operations: map[string]int{},
			Outcomes: map[string]int{}, UnknownByReason: map[string]int{},
		},
		pins: map[versionKey]PinCheckReading{},
	}
	c.reading.InactiveEnforcement = inactiveEnforcement(in.Capability)

	if c.reading.State != StateRead {
		return c
	}

	later := map[string]bool{}

	for i := len(in.CheckAudit.Records) - 1; i >= 0; i-- {
		r := in.CheckAudit.Records[i]

		switch r.Kind {
		case lessoncheck.AuditVerify, lessoncheck.AuditPostEdit, lessoncheck.AuditGuard:
			c.reading.Operations[r.Kind]++
		default:
			continue
		}

		deny := r.Kind == lessoncheck.AuditGuard && r.Guard != nil && r.Guard.Decision == "deny"
		seen := map[versionKey]bool{}

		for _, p := range r.Pins {
			c.reading.PinEvaluations++
			c.reading.Outcomes[string(p.Outcome)]++

			if p.Outcome == lessoncheck.OutcomeUnknown && p.Reason != "" {
				c.reading.UnknownByReason[string(p.Reason)]++
			}

			key := versionKey{p.PinIdentity, p.PinVersion}
			pin := c.pin(key)
			pin.PinEvaluations++
			pin.Outcomes[string(p.Outcome)]++

			// Duplicate entries evaluate twice, but one guard run requests
			// at most one deny for a version.
			if deny && !seen[key] {
				pin.RequestedDenies++
				seen[key] = true
			}

			c.pins[key] = pin
		}

		if r.Kind == lessoncheck.AuditGuard && r.Guard != nil {
			c.reading.GuardAttempts.add(r.Guard, r.MatchSHA != "" && later[r.MatchSHA])
		}

		if r.Kind == lessoncheck.AuditPostEdit && r.MatchSHA != "" {
			later[r.MatchSHA] = true
		}
	}

	return c
}

// pin returns independent maps so a caller cannot change the index.
func (c *checkIndex) pin(key versionKey) PinCheckReading {
	p := c.pins[key]
	p.Outcomes = copyMap(p.Outcomes)

	return p
}

// add partitions each requested deny by the later execution evidence.
// A verified path alone does not prove that its reply prevented a write.
func (g *GuardAttempts) add(d *lessoncheck.GuardDecision, executed bool) {
	if d.Decision == "allow" {
		g.Allowed++

		return
	}

	if d.Decision != "deny" {
		return
	}

	g.RequestedDenies++
	g.VerifiedNative += boolInt(d.NativeBlocking == lessoncheck.LevelVerified)
	g.ReplyNotWritten += boolInt(!d.ReplyWritten)

	if executed {
		g.ExecutedAfterDeny++
	} else {
		g.NoExecutionObserved++
		g.Prevented += boolInt(d.ReplyWritten && d.NativeBlocking == lessoncheck.LevelVerified)
	}
}

// inactiveEnforcement counts pins, not clients. Two inactive clients of
// one pin still describe one requested pin whose enforcement is inactive.
func inactiveEnforcement(capability *lessoncheck.CapabilityReport) int {
	if capability == nil {
		return 0
	}

	pins := map[[2]string]bool{}

	for _, p := range capability.Pins {
		for _, client := range p.Clients {
			if client.Badge == lessoncheck.BadgeRequestedInactive {
				pins[[2]string{p.PinRef, p.PinVersion}] = true
			}
		}
	}

	return len(pins)
}
