package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/integration"
)

// TestMain clears the recording switch before the tests run. The native
// recording script or a developer shell can export it. With the switch on,
// the hook tests find evaluation and reset records that they do not expect.
// A test that needs the switch sets it with t.Setenv, which clears it again
// after that test.
//
// The hook fixtures also name /opt/bin/seamark, which a test machine does
// not need to hold. The inspection reads it as a hook that can start.
func TestMain(m *testing.M) {
	if err := os.Unsetenv(recordEvaluationsEnv); err != nil {
		panic(err)
	}

	integration.TreatHookBinaryAsPresent("/opt/bin/seamark")

	os.Exit(m.Run())
}

// TestRecordEvaluationsNeedsTheExactValue proves that only the value "1"
// turns on the recording switch. Any other value keeps the audit log at
// its usual size, so a mistyped value never grows the log of a user.
func TestRecordEvaluationsNeedsTheExactValue(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"", false},
		{"0", false},
		{"true", false},
		{" 1", false},
		{"yes", false},
	} {
		t.Run(c.value, func(t *testing.T) {
			t.Setenv(recordEvaluationsEnv, c.value)

			assert.Equal(t, c.want, recordEvaluations())
		})
	}
}
