package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/agent/agentcontext"
	"github.com/coder/coder/v2/agent/x/agentmcp"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
)

func TestBuildMCPServerResources(t *testing.T) {
	t.Parallel()

	t.Run("Empty", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, buildMCPServerResources(nil))
		require.Nil(t, buildMCPServerResources([]agentmcp.ServerStatus{}))
	})

	t.Run("GroupsByServerSortedWithTools", func(t *testing.T) {
		t.Parallel()
		servers := []agentmcp.ServerStatus{
			{Name: "github", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "github__search", Description: "Search"},
				{Name: "github__create", Description: "Create"},
			}},
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "Read", Schema: map[string]any{"type": "object"}},
			}},
			// Dropped: a server with no name cannot be addressed.
			{Name: "", Connected: true, Tools: []workspacesdk.MCPToolInfo{{Name: "orphan"}}},
		}
		got := buildMCPServerResources(servers)
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

	t.Run("ConnectedWithoutToolsSkipped", func(t *testing.T) {
		t.Parallel()
		// A connected server that has not yet reported any tools is
		// not surfaced; a later re-resolve picks it up once tools
		// arrive.
		require.Nil(t, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: true},
		}))
	})

	t.Run("FailedServerSurfacesAsIssue", func(t *testing.T) {
		t.Parallel()
		got := buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "broken", Connected: false, Err: "initialize \"broken\": exec: no such file"},
		})
		require.Len(t, got, 1)
		require.Equal(t, agentcontext.KindMCPServer, got[0].Kind)
		require.Equal(t, "broken", got[0].Source)
		require.Equal(t, "broken", got[0].Name)
		require.Equal(t, "mcp_server:broken", got[0].ID)
		require.Equal(t, agentcontext.StatusUnreadable, got[0].Status)
		require.Equal(t, "initialize \"broken\": exec: no such file", got[0].Error)
		require.Empty(t, got[0].Tools)
		require.NotEqual(t, [32]byte{}, got[0].ContentHash)
	})

	t.Run("FailedServerWithoutErrorGetsDefault", func(t *testing.T) {
		t.Parallel()
		got := buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "broken", Connected: false},
		})
		require.Len(t, got, 1)
		require.Equal(t, agentcontext.StatusUnreadable, got[0].Status)
		require.Equal(t, "failed to connect", got[0].Error)
	})

	t.Run("ContentHashStableAndToolSensitive", func(t *testing.T) {
		t.Parallel()
		base := []agentmcp.ServerStatus{
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "Read"},
			}},
		}
		h1 := buildMCPServerResources(base)[0].ContentHash
		// Identical input is hashed identically.
		require.Equal(t, h1, buildMCPServerResources(base)[0].ContentHash)
		// A description change flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "Read files"},
			}},
		})[0].ContentHash)
		// Adding a tool flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "Read"},
				{Name: "fs__write", Description: "Write"},
			}},
		})[0].ContentHash)
		// A schema change flips the hash.
		require.NotEqual(t, h1, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "Read", Schema: map[string]any{"type": "object"}},
			}},
		})[0].ContentHash)
	})

	t.Run("FailedServerHashErrorSensitive", func(t *testing.T) {
		t.Parallel()
		h1 := buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: false, Err: "boom"},
		})[0].ContentHash
		// The error text participates in the hash so a changed error
		// re-pins dirty chats.
		require.NotEqual(t, h1, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: false, Err: "different"},
		})[0].ContentHash)
		// A failed server hashes differently from a connected one, so
		// the connected->failed transition flips the aggregate hash.
		require.NotEqual(t, h1, buildMCPServerResources([]agentmcp.ServerStatus{
			{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{
				{Name: "fs__read", Description: "boom"},
			}},
		})[0].ContentHash)
	})

	t.Run("ProviderDelegates", func(t *testing.T) {
		t.Parallel()
		// A nil cache source yields no resources rather than panicking.
		require.Nil(t, mcpContextProvider{}.MCPResources())

		p := mcpContextProvider{cachedServers: func() []agentmcp.ServerStatus {
			return []agentmcp.ServerStatus{
				{Name: "fs", Connected: true, Tools: []workspacesdk.MCPToolInfo{{Name: "fs__read"}}},
				{Name: "broken", Connected: false, Err: "nope"},
			}
		}}
		got := p.MCPResources()
		require.Len(t, got, 2)
		// Emitted in name order: broken (failed), then fs (ok).
		require.Equal(t, "broken", got[0].Source)
		require.Equal(t, agentcontext.StatusUnreadable, got[0].Status)
		require.Equal(t, "fs", got[1].Source)
		require.Equal(t, agentcontext.StatusOK, got[1].Status)
	})
}
