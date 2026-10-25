package lessoncheck

import (
	"context"
	"errors"
)

// budget bounds one evaluation with a context. The hook handlers give
// the guard budget or the post-edit budget, and limit names it. The
// zero value is unbounded, so Evaluate and Verify keep their results.
//
// An evaluation that the budget ends must never read as a pass. Every
// checker then reports the cause as unknown, so an enforce pin denies
// (contract K8, step 2).
type budget struct {
	ctx   context.Context
	limit string
}

// ended reports whether the budget is over, and the cause that the
// results then carry: resource-limit with the limit name for a
// deadline, and interrupted for a cancellation.
func (b budget) ended() (reasonHit, bool) {
	if b.ctx == nil {
		return reasonHit{}, false
	}

	switch err := b.ctx.Err(); {
	case err == nil:
		return reasonHit{}, false
	case errors.Is(err, context.Canceled):
		return reasonHit{reason: ReasonInterrupted}, true
	default:
		return reasonHit{reason: ReasonResourceLimit, limit: b.limit}, true
	}
}

// runDiff is the diff that diffLinesWithin runs. Only tests replace it:
// a test blocks or panics the diff to prove the budget bound and the
// panic hand-over without a wall-clock limit.
var runDiff = diffLines

// diffResult is the outcome of one diffLines call that runs apart from
// its caller.
type diffResult struct {
	added    []addedLine
	panicked any
}

// diffLinesWithin runs diffLines and gives up when the budget ends.
// It returns the cause of the end instead of the added lines.
//
// The difflib matcher has no cancellation point, and its cost grows
// with the product of the repeats of a line. So the diff runs in its
// own goroutine, and the caller stops waiting when the budget ends.
// The abandoned goroutine ends with the process: the hook handlers
// exit right after the decision. A bounded diff gives the same lines
// as diffLines, because the work is the same.
//
// A panic of the diff goes back to the caller, so the recovery of
// DecideGuard (contract K8, step 7) still sees it.
func diffLinesWithin(b budget, before, after []string) ([]addedLine, reasonHit) {
	if b.ctx == nil {
		return runDiff(before, after), reasonHit{}
	}

	// An ended budget starts no work.
	if cause, ended := b.ended(); ended {
		return nil, cause
	}

	done := make(chan diffResult, 1)

	go func() {
		var res diffResult

		defer func() {
			res.panicked = recover()
			done <- res
		}()

		res.added = runDiff(before, after)
	}()

	select {
	case res := <-done:
		if res.panicked != nil {
			panic(res.panicked)
		}

		return res.added, reasonHit{}
	case <-b.ctx.Done():
		cause, _ := b.ended()

		return nil, cause
	}
}
