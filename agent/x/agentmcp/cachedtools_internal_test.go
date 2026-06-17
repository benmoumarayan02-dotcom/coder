package agentmcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cdr.dev/slog/v3"
	"cdr.dev/slog/v3/sloggers/slogtest"
	"github.com/coder/coder/v2/agent/agentexec"
	"github.com/coder/coder/v2/testutil"
)

// TestManager_CachedToolsAndOnToolsChanged verifies the non-blocking
// CachedTools accessor returns an independent copy of the cache and that a
// reload which writes the cache fires the onToolsChanged hook (used to
// re-resolve the workspace-context snapshot).
func TestManager_CachedToolsAndOnToolsChanged(t *testing.T) {
	t.Parallel()

	ctx := testutil.Context(t, testutil.WaitLong)
	logger := slogtest.Make(t, nil).Leveled(slog.LevelDebug)
	dir := t.TempDir()

	m := NewManager(ctx, logger, agentexec.DefaultExecer, nil)
	m.MarkStartupSettled()
	t.Cleanup(func() { _ = m.Close() })

	// CachedTools never blocks and is empty before the first reload.
	require.Empty(t, m.CachedTools())

	changed := make(chan struct{}, 8)
	m.SetOnToolsChanged(func() { changed <- struct{}{} })

	_, entry := fakeMCPServerConfig(t, "srv")
	configPath := writeMCPConfig(t, dir, map[string]mcpServerEntry{"srv": entry})

	tools, err := m.Tools(ctx, []string{configPath})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	// The reload that produced the tools fired the change hook.
	testutil.RequireReceive(ctx, t, changed)

	// CachedTools reflects the same set, returned as an independent copy
	// so callers cannot mutate the manager's cache.
	cached := m.CachedTools()
	require.Len(t, cached, 1)
	require.Equal(t, tools[0].Name, cached[0].Name)
	cached[0].Name = "mutated"
	require.NotEqual(t, "mutated", m.CachedTools()[0].Name)
}
