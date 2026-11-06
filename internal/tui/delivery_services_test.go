package tui

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestDeliveryFakeServices(t *testing.T) {
	f := newFake()
	report, err := f.Delivery(lessonreport.Request{PinRef: "unscripted"})
	require.NoError(t, err)
	assert.Equal(t, lessonreport.Schema, report.Schema)
	assert.Equal(t, lessonreport.CaptureUnavailable, report.Capture.State)
	assert.Empty(t, report.Pins)
	assert.Empty(t, report.Historical)
	assert.Nil(t, report.Expanded)

	data, err := json.Marshal(report)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "null")
	assert.Equal(t, 1, f.readCount("Delivery"))
	assert.Empty(t, f.called(), "a delivery read never counts as a mutation")

	req := lessonreport.Request{PinRef: "cccccccc", Limits: lessonreport.Limits{MaxAuditBytes: 12}}
	selectionErr := &reviews.PinRefError{Kind: reviews.PinRefMissing, Ref: req.PinRef}
	f.delivery = func(got lessonreport.Request) (lessonreport.Report, error) {
		assert.Equal(t, req.PinRef, got.PinRef)
		assert.Equal(t, req.Limits, got.Limits)

		return report, selectionErr
	}

	got, err := f.Delivery(req)
	assert.Equal(t, report, got)
	assert.ErrorIs(t, err, selectionErr)
	assert.Equal(t, 2, f.readCount("Delivery"))
	assert.Empty(t, f.called())
}
