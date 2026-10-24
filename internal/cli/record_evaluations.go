package cli

import "os"

// recordEvaluationsEnv names the switch for two audit record kinds of the
// lesson hooks: the evaluation record of a hook run that emits nothing,
// and the reset record of a context reset. The native recording script
// sets it for its paid runs, because a timing replay reads those records.
// Without the switch the audit log keeps its usual size.
const recordEvaluationsEnv = "SEAMARK_RECORD_EVALUATIONS"

// recordEvaluations reports whether the hook process runs with the switch
// on. Only the exact value "1" turns it on, so an empty or mistyped value
// keeps the default. The hook reads the switch once per run and passes the
// result to the delivery package, which reads no environment.
func recordEvaluations() bool {
	return os.Getenv(recordEvaluationsEnv) == "1"
}
