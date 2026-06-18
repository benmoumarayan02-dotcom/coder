package aibridge

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cdr.dev/slog/v3/sloggers/slogtest"
	agplaibridge "github.com/coder/coder/v2/coderd/aibridge"
)

func TestExtractAgentFirewallHeaders(t *testing.T) {
	t.Parallel()

	logger := slogtest.Make(t, nil)
	ctx := context.Background()

	t.Run("both headers present", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSessionID, "e5f6a7b8-1234-5678-9abc-def012345678")
		req.Header.Set(agplaibridge.HeaderAgentFirewallSequenceNumber, "42")

		sessionID, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		require.NotNil(t, sessionID)
		assert.Equal(t, "e5f6a7b8-1234-5678-9abc-def012345678", *sessionID)
		require.NotNil(t, seqNumber)
		assert.Equal(t, int32(42), *seqNumber)
	})

	t.Run("no headers present", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)

		sessionID, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		assert.Nil(t, sessionID)
		assert.Nil(t, seqNumber)
	})

	t.Run("only session ID", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSessionID, "e5f6a7b8-1234-5678-9abc-def012345678")

		sessionID, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		require.NotNil(t, sessionID)
		assert.Equal(t, "e5f6a7b8-1234-5678-9abc-def012345678", *sessionID)
		assert.Nil(t, seqNumber)
	})

	t.Run("only sequence number", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSequenceNumber, "7")

		sessionID, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		assert.Nil(t, sessionID)
		require.NotNil(t, seqNumber)
		assert.Equal(t, int32(7), *seqNumber)
	})

	t.Run("sequence number zero", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSequenceNumber, "0")

		_, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		require.NotNil(t, seqNumber)
		assert.Equal(t, int32(0), *seqNumber)
	})

	t.Run("invalid sequence number is ignored", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSessionID, "e5f6a7b8-1234-5678-9abc-def012345678")
		req.Header.Set(agplaibridge.HeaderAgentFirewallSequenceNumber, "not-a-number")

		sessionID, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		require.NotNil(t, sessionID)
		assert.Equal(t, "e5f6a7b8-1234-5678-9abc-def012345678", *sessionID)
		assert.Nil(t, seqNumber, "non-numeric sequence number should be treated as absent")
	})

	t.Run("sequence number exceeding int32 range is rejected", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set(agplaibridge.HeaderAgentFirewallSequenceNumber, "2147483648") // max int32 + 1

		_, seqNumber := extractAgentFirewallHeaders(req, logger, ctx)

		assert.Nil(t, seqNumber, "out-of-range sequence number should be treated as absent")
	})
}
