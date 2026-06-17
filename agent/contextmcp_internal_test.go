package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/agent/agentcontext"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
)

func TestBuildMCPServerResources(t *testing.T) {
	t.Parallel()

	t.Run("Empty", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, buildMCPServerResources(nil))
		require.Nil(t, buildMCPServerResources([]workspacesdk.MCPToolInfo{}))
	})

	t.Run("GroupsByServerSortedWithTools", func(t *testing.T) {
		t.Parallel()
		tools := []workspacesdk.MCPToolInfo{
			{ServerName: "github", Name: "github__search", Description: "Search"},
			{ServerName: "fs", Name: "fs__read", Description: "Read", Schema: map[string]any{"type": "object"}},
			{ServerName: "github", Name: "github__create", Description: "Create"},
			// Dropped: a tool with no server cannot be grouped.
			{ServerName: "", Name: "orphan"},
		}
		got := buildMCPServerResources(tools)
		require.Len(t, got, 2)

		// Servers are emitted in name order: fs, then github.
		require.Equal(t, "fs", got[0].Source)
		require.Equal(t, "fs", got[0].Name)
		require.Equal(t, agentcontext.KindMCPServer, got[0].Kind)
		require.Equal(t, "mcp_server:fs", got[0].ID)
		require.Equal(t, agentcontext.StatusOK, got[0].Status)
		require.NotEqual(t, [32]byte{}, got[0].ContentHash)
		require.Len(t, got[0].Tools, 1)
		require.Equal(t, "fs__read", got[0].Tools[0].Name)
		require.Equal(t, map[string]any{"type": "object"}, got[0].Tools[0].InputSchema)

		require.Equal(t, "github", got[1].Source)
		require.Len(t, got[1].Tools, 2)
		// Tools within a server are sorted by name: create, then search.
		require.Equal(t, "github__create", got[1].Tools[0].Name)
		require.Equal(t, "github__search", got[1].Tools[1].Name)
	})

	t.Run("ContentHashStableAndToolSensitive", func(t *testing.T) {
		t.Parallel()
		base := []workspacesdk.MCPToolInfo{
			{ServerName: "fs", Name: "fs__read", Description: "Read"},
		}
		h1 := buildMCPServerResources(base)[0].ContentHash
		// Identical input is hashed identically.
		require.Equal(t, h1, buildMCPServerResources(base)[0].ContentHash)
		// A description change flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]workspacesdk.MCPToolInfo{
			{ServerName: "fs", Name: "fs__read", Description: "Read files"},
		})[0].ContentHash)
		// Adding a tool flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]workspacesdk.MCPToolInfo{
			{ServerName: "fs", Name: "fs__read", Description: "Read"},
			{ServerName: "fs", Name: "fs__write", Description: "Write"},
		})[0].ContentHash)
		// A schema change flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]workspacesdk.MCPToolInfo{
			{ServerName: "fs", Name: "fs__read", Description: "Read", Schema: map[string]any{"type": "object"}},
		})[0].ContentHash)
	})

	t.Run("ProviderDelegates", func(t *testing.T) {
		t.Parallel()
		// A nil cache source yields no resources rather than panicking.
		require.Nil(t, mcpContextProvider{}.MCPResources())

		p := mcpContextProvider{cachedTools: func() []workspacesdk.MCPToolInfo {
			return []workspacesdk.MCPToolInfo{{ServerName: "fs", Name: "fs__read"}}
		}}
		got := p.MCPResources()
		require.Len(t, got, 1)
		require.Equal(t, "fs", got[0].Source)
	})
}
