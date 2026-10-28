package htmlreport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// TestGenerateDeliveryCardBesideOutcome builds the page beside delivery
// records of two current pins. This build reads no capture archive, so
// no card gets a delivery line and the page keeps its legacy bytes. The
// card field stays escaped text, and a canceled context changes nothing.
func TestGenerateDeliveryCardBesideOutcome(t *testing.T) {
	st, root := seed(t)
	pins := `pin:
  - rule: error-context
    region: api
    note: Wrap returned errors with context describing the failed operation.
  - rule: error-context-wrapping
    region: api
    note: Wrap returned errors with context describing the failed operation.
  - rule: ascii-quotes
    region: api
    note: Use ASCII quotes.
`
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	configPath := filepath.Join(root, ".seamark", "lessons.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(pins), 0o644))
	cfg, err := reviews.ParseConfig([]byte(pins))
	require.NoError(t, err)
	dismissed := model.Proposal{Signature: "delivery-dismissed", Rule: "ascii-quotes", Region: "api", Status: model.ProposalDismissed}
	require.NoError(t, storetest.InsertProposal(st, &dismissed))

	req := lessonreport.Request{Root: root, SeamarkVersion: "test-release",
		PinRef: "invalid-caller-selection", Applied: []model.Proposal{{ID: 999}},
		Now: func() time.Time { require.FailNow(t, "Build owns the fixed clock"); return time.Time{} }}
	generate := func(req lessonreport.Request) string {
		var b strings.Builder
		require.NoError(t, Generate(context.Background(), &b, st, root, clock, req))

		return b.String()
	}
	legacy := generate(lessonreport.Request{})
	assert.Equal(t, legacy, generate(req), "no audit adds neither a line nor markup")

	// Two current applied pins have delivery records. The reading of
	// each stays unavailable, because this build reads the audit only.
	var audit strings.Builder
	for _, pin := range cfg.Pin[:2] {
		record, err := json.Marshal(reviews.Firing{
			TS: clock.Format(time.RFC3339), SchemaVersion: reviews.AuditSchemaVersion,
			Delivery: reviews.DeliveryInjected, SessionSHA: "fixture-session", Client: integration.ClaudeID,
			Pins: []reviews.FiringPin{{Identity: reviews.PinIdentityDigest(reviews.NewPinKey(pin.Rule, pin.Region, nil)),
				Version: cfg.PinVersion(pin), Delivery: reviews.PinInjected}},
		})
		require.NoError(t, err)
		audit.Write(record)
		audit.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(reviews.AuditLogPath(root), []byte(audit.String()), 0o644))
	legacy = generate(lessonreport.Request{})
	r, err := Build(context.Background(), st, root, clock, req)
	require.NoError(t, err)
	require.NotEmpty(t, r.Cards)

	for _, card := range r.Cards {
		assert.Empty(t, card.Delivery)
	}

	page := generate(req)
	assert.Equal(t, legacy, page, "outcome bytes and card order stay unchanged")
	assert.NotContains(t, page, `<p class="health delivery">`)

	// The delivery field remains ordinary untrusted template text, even
	// if a caller supplies markup rather than a fixed Compact line.
	r.Cards[0].Delivery = `<script>alert("capture")</script> & <payload>`
	var escaped strings.Builder
	require.NoError(t, Render(&escaped, r))
	assert.Contains(t, escaped.String(), `&lt;script&gt;alert(&#34;capture&#34;)&lt;/script&gt; &amp; &lt;payload&gt;`)
	assert.NotContains(t, escaped.String(), `<script>alert("capture")</script>`)

	// Cancellation belongs to the optional delivery reading, not to the
	// existing outcome page or its error channel.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var cancelled strings.Builder
	require.NoError(t, Generate(ctx, &cancelled, st, root, clock, req))
	assert.Equal(t, legacy, cancelled.String())
}
