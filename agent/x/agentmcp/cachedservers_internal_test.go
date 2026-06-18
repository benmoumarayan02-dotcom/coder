package agentmcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"cdr.dev/slog/v3"
	"cdr.dev/slog/v3/sloggers/slogtest"
	"github.com/coder/coder/v2/agent/agentexec"
	"github.com/coder/coder/v2/testutil"
)

// TestManager_CachedServers verifies the non-blocking CachedServers
// accessor reports every configured server, including one that failed
// to connect (which never enters m.servers and would otherwise be
// invisible to the workspace-context snapshot), and that a closed
// manager reports no servers.
func TestManager_CachedServers(t *testing.T) {
	t.Parallel()

	ctx := testutil.Context(t, testutil.WaitLong)
	logger := slogtest.Make(t, nil).Leveled(slog.LevelDebug)
	dir := t.TempDir()

	m := NewManager(ctx, logger, agentexec.DefaultExecer, nil)
	m.MarkStartupSettled()
	t.Cleanup(func() { _ = m.Close() })

	// CachedServers never blocks and is empty before the first reload.
	require.Empty(t, m.CachedServers())

	// One healthy server (re-exec fake) plus one whose binary does
	// not exist, so its connect fails.
	_, good := fakeMCPServerConfig(t, "good")
	bad := mcpServerEntry{Command: "/nonexistent/agentmcp-binary"}
	configPath := writeMCPConfig(t, dir, map[string]mcpServerEntry{
		"good": good,
		"bad":  bad,
	})

	// Reload succeeds: per-server connect failures are logged and
	// swallowed, not fatal.
	require.NoError(t, m.Reload(ctx, []string{configPath}))

	byName := make(map[string]ServerStatus)
	for _, s := range m.CachedServers() {
		byName[s.Name] = s
	}
	require.Len(t, byName, 2, "both configured servers must be reported")

	gotGood, ok := byName["good"]
	require.True(t, ok)
	require.True(t, gotGood.Connected)
	require.Empty(t, gotGood.Err)
	require.Len(t, gotGood.Tools, 1, "connected server exposes its tools")
	require.True(t, strings.HasPrefix(gotGood.Tools[0].Name, "good"+ToolNameSep))

	gotBad, ok := byName["bad"]
	require.True(t, ok, "a server that failed to connect must still be reported")
	require.False(t, gotBad.Connected)
	require.NotEmpty(t, gotBad.Err, "failed server must carry a connect error")
	require.Empty(t, gotBad.Tools)

	// After Close the manager reports no servers.
	require.NoError(t, m.Close())
	require.Empty(t, m.CachedServers())
}
