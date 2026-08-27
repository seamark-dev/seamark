package bench

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// ResultSchemaVersion is the immutable contract emitted by the current
// harness and accepted by strict reporting. Version 7 adds the hook-exposure
// expectation and a shared protocol fingerprint for scoped factorial
// experiments. Strict reporting continues to accept frozen v5/v6 evidence
// without rewriting it.
const ResultSchemaVersion = 7

const oldestSupportedResultSchemaVersion = 5

// ValidateResultRow enforces the semantic contract documented by
// the versioned schemas in bench/. It is intentionally stricter than ReadRows, whose
// best-effort behavior remains useful for historical cost estimation.
func ValidateResultRow(row Row) error {
	if row.SchemaVersion < oldestSupportedResultSchemaVersion ||
		row.SchemaVersion > ResultSchemaVersion {
		return fmt.Errorf("schema_version is %d, supported versions are %d through %d",
			row.SchemaVersion, oldestSupportedResultSchemaVersion, ResultSchemaVersion)
	}

	switch {
	case row.TS == "":
		return fmt.Errorf("ts is required")
	case row.RunID == "":
		return fmt.Errorf("run_id is required")
	case row.Instance == "":
		return fmt.Errorf("instance is required")
	case row.Pin == "":
		return fmt.Errorf("pin is required")
	case !validGitOID(row.Fixture):
		return fmt.Errorf("fixture must be a lowercase SHA-1 or SHA-256 git object ID")
	case !knownArm(row.Arm):
		return fmt.Errorf("unknown arm %q", row.Arm)
	case row.Trial < 1:
		return fmt.Errorf("trial must be at least 1")
	case row.Avoided && !row.TaskDone:
		return fmt.Errorf("invariant_pass requires task_pass")
	case row.TaskDone && !row.ChecksPass:
		return fmt.Errorf("task_pass requires checks_pass")
	case row.PairValid && !row.Valid:
		return fmt.Errorf("pair_valid requires valid")
	case len(row.Checks) == 0:
		return fmt.Errorf("checks must contain at least one public validation command")
	case row.HookFirings > row.HookAuditRows:
		return fmt.Errorf("hook_firings cannot exceed hook_audit_rows")
	case row.SchemaVersion < 7 && row.Valid &&
		(row.Arm == ArmHookOn || row.Arm == ArmPlacebo) && row.HookFirings == 0:
		return fmt.Errorf("valid treatment rows require a matching hook firing")
	case row.Valid && (row.Arm == ArmHookOff || row.Arm == ArmFileOnly) && row.HookFirings != 0:
		return fmt.Errorf("valid control rows cannot contain treatment hook firings")
	case row.InputTokens < 0 || row.CacheReadTokens < 0 || row.CacheCreationTokens < 0 ||
		row.ContextTokens < 0 || row.OutputTokens < 0:
		return fmt.Errorf("token counts must not be negative")
	case row.HookFirings < 0 || row.HookAuditRows < 0 || row.HookMatches < 0 ||
		row.HookInjections < 0 || row.HookRepeated < 0 || row.HookSuppressed < 0 ||
		row.HookContextBytes < 0 || row.Turns < 0 || row.PermissionDenials < 0:
		return fmt.Errorf("event counts must not be negative")
	case row.CostUSD < 0 || math.IsNaN(row.CostUSD) || math.IsInf(row.CostUSD, 0) || row.DurationMS < 0:
		return fmt.Errorf("cost must be finite and cost and duration must not be negative")
	case row.MaxBudgetUSD < 0 || math.IsNaN(row.MaxBudgetUSD) || math.IsInf(row.MaxBudgetUSD, 0):
		return fmt.Errorf("maximum budget must be finite and not negative")
	}

	if row.SchemaVersion >= 6 {
		switch {
		case row.HookDelivery != HookDeliveryAlways && row.HookDelivery != HookDeliveryOncePerContext:
			return fmt.Errorf("hook_delivery must be %q or %q", HookDeliveryAlways, HookDeliveryOncePerContext)
		case row.HookMatches > row.HookAuditRows:
			return fmt.Errorf("hook_matches cannot exceed hook_audit_rows")
		case row.HookMatches != row.HookInjections+row.HookSuppressed:
			return fmt.Errorf("hook_matches must equal hook_injections plus hook_suppressed")
		case row.HookRepeated > row.HookInjections:
			return fmt.Errorf("hook_repeated_injections cannot exceed hook_injections")
		case row.HookDelivery == HookDeliveryAlways && row.HookSuppressed != 0:
			return fmt.Errorf("hook_suppressed must be zero when hook_delivery is %q", HookDeliveryAlways)
		case row.HookFirings != row.HookInjections:
			return fmt.Errorf("hook_firings must equal hook_injections in schema v6 and later")
		case row.SchemaVersion == 6 && row.Valid &&
			(row.Arm == ArmHookOn || row.Arm == ArmPlacebo) && row.HookInjections == 0:
			return fmt.Errorf("valid treatment rows require a matching hook injection")
		case row.Valid && (row.Arm == ArmHookOff || row.Arm == ArmFileOnly) &&
			(row.HookMatches != 0 || row.HookInjections != 0 || row.HookRepeated != 0 ||
				row.HookSuppressed != 0 || row.HookContextBytes != 0):
			return fmt.Errorf("valid control rows cannot contain hook delivery intensity")
		}
	}

	if row.SchemaVersion >= 7 {
		hooked := row.Arm == ArmHookOn || row.Arm == ArmPlacebo
		switch {
		case hooked && row.HookExposure != HookExposureRequired && row.HookExposure != HookExposureOptional:
			return fmt.Errorf("hooked rows require hook_exposure %q or %q",
				HookExposureRequired, HookExposureOptional)
		case !hooked && row.HookExposure != HookExposureNone:
			return fmt.Errorf("control rows require hook_exposure %q", HookExposureNone)
		case hooked && row.Valid && row.HookExposure == HookExposureRequired && row.HookInjections == 0:
			return fmt.Errorf("valid required-exposure rows require a matching hook injection")
		case !validSHA256(row.ProtocolFingerprint):
			return fmt.Errorf("protocol_fingerprint must be a lowercase SHA-256")
		case row.ComparisonFamily != strings.TrimSpace(row.ComparisonFamily):
			return fmt.Errorf("comparison_family must be trimmed")
		}
	}

	if row.InputTokens > math.MaxInt64-row.CacheReadTokens ||
		row.InputTokens+row.CacheReadTokens > math.MaxInt64-row.CacheCreationTokens {
		return fmt.Errorf("token component sum overflows int64")
	}

	for _, usage := range row.ModelUsage {
		if usage.InputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheCreationTokens < 0 ||
			usage.OutputTokens < 0 || usage.CostUSD < 0 || math.IsNaN(usage.CostUSD) ||
			math.IsInf(usage.CostUSD, 0) {
			return fmt.Errorf("model usage must contain non-negative tokens and finite non-negative cost")
		}
	}

	for _, check := range row.Checks {
		if check.Command == "" {
			return fmt.Errorf("check command is required")
		}

		if check.Pass && check.TimedOut {
			return fmt.Errorf("a timed-out check cannot pass")
		}
	}

	if checksPass(row.Checks) != row.ChecksPass {
		return fmt.Errorf("checks_pass does not match individual check results")
	}

	if _, err := time.Parse(time.RFC3339, row.TS); err != nil {
		return fmt.Errorf("ts: %w", err)
	}

	for _, field := range []struct{ name, value string }{
		{"task_sha256", row.TaskSHA},
		{"fingerprint", row.Fingerprint},
	} {
		if !validSHA256(field.value) {
			return fmt.Errorf("%s must be a lowercase SHA-256", field.name)
		}
	}

	if row.SeamarkSHA != "" && !validSHA256(row.SeamarkSHA) {
		return fmt.Errorf("seamark_sha256 must be a lowercase SHA-256")
	}

	for _, field := range []struct{ name, value string }{
		{"transcript_sha256", row.TranscriptSHA},
		{"stderr_sha256", row.StderrSHA},
		{"patch_sha256", row.PatchSHA},
	} {
		if field.value != "" && !validSHA256(field.value) {
			return fmt.Errorf("%s must be a lowercase SHA-256", field.name)
		}
	}
	for _, artifact := range []struct{ name, path, digest string }{
		{"transcript", row.Transcript, row.TranscriptSHA},
		{"stderr", row.StderrLog, row.StderrSHA},
		{"patch", row.Patch, row.PatchSHA},
	} {
		if (artifact.path == "") != (artifact.digest == "") {
			return fmt.Errorf("%s path and SHA-256 must be present together", artifact.name)
		}
	}

	contextTokens := row.InputTokens + row.CacheReadTokens + row.CacheCreationTokens
	if row.ContextTokens != contextTokens {
		return fmt.Errorf("context_tokens is %d, want component sum %d", row.ContextTokens, contextTokens)
	}

	return nil
}

func validSHA256(value string) bool {
	return validLowerHex(value, 64)
}

func validGitOID(value string) bool {
	return validLowerHex(value, 40) || validLowerHex(value, 64)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}

	decoded, err := hex.DecodeString(value)

	return err == nil && len(decoded) == length/2 && value == fmt.Sprintf("%x", decoded)
}

// PriorCostFor only pools rows from the same immutable run fingerprint. An
// estimate made from another model, task, or runtime is worse than no estimate
// because it creates false confidence before a paid run.
func PriorCostFor(path, fingerprint string) (rows int, meanInput int64, meanCost float64, ok bool) {
	if fingerprint == "" {
		return 0, 0, 0, false
	}

	rs, err := ReadRows(path)
	if err != nil || len(rs) == 0 {
		return 0, 0, 0, false
	}

	var inputTotal, count int64
	var costTotal float64

	for _, row := range rs {
		if row.Fingerprint != fingerprint {
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
