//go:build unix

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests in this file hold the borders of the state lock in one edit
// event. The lock covers the read of the delivered set, the emit, and the
// commit. It goes before the audit append, so a slow log never makes another
// hook fail the lock. Delivery has no seam at these borders, so the tests
// watch the real lock from a second holder.

// lockDeadline bounds each wait of these tests. A pass never reaches it.
const lockDeadline = 5 * time.Second

// firstHold returns a lease of dc as soon as the lock is free. The loop has no
// sleep, because it must reach the lock before the next step of the holder.
func firstHold(root string, dc reviews.DeliveryContext) (*reviews.HookDeliveryLease, error) {
	deadline := time.Now().Add(lockDeadline)

	for {
		lease, err := reviews.OpenContextDelivery(root, dc)
		if err == nil || time.Now().After(deadline) {
			return lease, err
		}
	}
}

// waitForAuditAppend returns when a goroutine waits inside the open of the
// firing log. A FIFO at the log path holds that open until a reader comes.
// No file shows this wait, so the test reads the stacks.
func waitForAuditAppend(t *testing.T) {
	t.Helper()

	stacks := make([]byte, 1<<22)

	require.Eventually(t, func() bool {
		return bytes.Contains(stacks[:runtime.Stack(stacks, true)], []byte("reviews.openAuditLog"))
	}, lockDeadline, time.Millisecond, "the event reaches the audit append")
}

// TestDeliverCommitsInsideTheLockHold starts a second holder inside the emit.
// With a commit outside the hold, that holder reads the state before the
// commit, and a reset then loses to the commit. So the delivered pins stay
// hidden after a compaction.
func TestDeliverCommitsInsideTheLockHold(t *testing.T) {
	root := workspace(t)
	st := lessonStore(t, root)
	cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
		"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n")
	version := cfg.PinVersion(cfg.Pin[0])
	dc := reviews.DeliveryContext{ClientID: integration.ClaudeID, ReceiverID: receiverOf("session-1")}

	type holder struct {
		delivered  bool
		generation uint64
		err        error
	}

	held := make(chan struct{})
	result := make(chan holder, 1)

	emit := func(string) error {
		go func() {
			lease, err := firstHold(root, dc)
			if err != nil {
				result <- holder{err: err}

				return
			}

			close(held)

			got := holder{delivered: lease.PinDelivered(version)}
			got.err = lease.Close()

			reset, err := reviews.ResetContextGeneration(root, dc)
			got.generation, got.err = reset, errors.Join(got.err, err)
			result <- got
		}()

		// The emit takes this long on purpose, so a lock that goes before the
		// emit shows here.
		select {
		case <-held:
			t.Error("a second holder took the lock during the emit")
		case <-time.After(30 * time.Millisecond):
		}

		return nil
	}

	req := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "m1", "prompt-1", "api/handler.go"))

	out, err := Deliver(context.Background(), st, req, emit)
	require.NoError(t, err)
	require.Equal(t, StatusEmitted, out.Status)

	select {
	case got := <-result:
		require.NoError(t, got.err)
		assert.True(t, got.delivered, "the state holds the pin when the lock goes")
		assert.Equal(t, uint64(2), got.generation, "the reset runs after the commit")
	case <-time.After(lockDeadline):
		require.FailNow(t, "the second holder got no lock")
	}

	var next capture

	again, err := Deliver(context.Background(), st, req, next.emit)
	require.NoError(t, err)
	assert.Equal(t, StatusEmitted, again.Status, "the reset restores the pin")
	assert.Equal(t, uint64(2), again.Generation)
	require.Len(t, next.advice, 1)
	assert.Contains(t, next.advice[0], "Validate request payloads at the edge.")
}

// TestDeliverReleasesTheLockBeforeTheAuditAppend stops each path that writes a
// record inside the open of the log, and takes the lock from there. A reader
// then lets the append finish, so the test also sees the record of that path.
func TestDeliverReleasesTheLockBeforeTheAuditAppend(t *testing.T) {
	failed := errors.New("reply failed")

	cases := []struct {
		name string
		// prior runs one event before the stop, on a plain log.
		prior  bool
		emit   func(string) error
		status reviews.DeliveryStatus
		// want is the result of the event: nil, or the reply error.
		want error
	}{
		{name: "an emitted event", emit: func(string) error { return nil }, status: reviews.DeliveryInjected},
		{name: "a suppressed event", prior: true, emit: mustNotEmit, status: reviews.DeliverySuppressedRepeat},
		{
			name: "a failed reply", emit: func(string) error { return failed },
			status: reviews.DeliveryEvaluation, want: failed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := workspace(t)
			st := lessonStore(t, root)
			cfg := writeConfig(t, root, "hook_delivery: once-per-context\npin:\n"+
				"  - {rule: boundary-validation, region: api, note: Validate request payloads at the edge.}\n")

			req := claudeRequest(root, cfg, claudeEvent(root, "session-1", "", "m1", "prompt-1", "api/handler.go"))
			req.RecordEvaluations = true

			path := reviews.AuditLogPath(root)

			if tc.prior {
				var got capture

				_, err := Deliver(context.Background(), st, req, got.emit)
				require.NoError(t, err)
				require.NoError(t, os.Remove(path))
			}

			require.NoError(t, syscall.Mkfifo(path, 0o600))

			done := make(chan error, 1)

			go func() {
				_, err := Deliver(context.Background(), st, req, tc.emit)
				done <- err
			}()

			waitForAuditAppend(t)

			// One attempt is enough: the event waits at the open, so its lock
			// state cannot change before the read below.
			other := reviews.DeliveryContext{ClientID: integration.ClaudeID, ReceiverID: "another-session"}

			lease, lockErr := reviews.OpenContextDelivery(root, other)
			if lockErr == nil {
				require.NoError(t, lease.Close())
			}

			// The read comes before any check, so a failed check leaves no
			// goroutine at the open.
			log, err := os.ReadFile(path)
			require.NoError(t, err)

			require.ErrorIs(t, <-done, tc.want)
			require.NoError(t, lockErr, "the lock is free during the append")

			var rec reviews.Firing

			require.NoError(t, json.NewDecoder(bytes.NewReader(log)).Decode(&rec))
			assert.Equal(t, tc.status, rec.Delivery)
		})
	}
}
