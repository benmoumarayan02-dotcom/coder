package agent_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/agent"
	"github.com/coder/coder/v2/agent/agentcontextconfig"
	"github.com/coder/coder/v2/agent/agenttest"
	agentproto "github.com/coder/coder/v2/agent/proto"
	"github.com/coder/coder/v2/codersdk/agentsdk"
	"github.com/coder/coder/v2/testutil"
)

// TestAgent_MCPServerToolsPushed verifies the end-to-end MCP push path:
// a .mcp.json in the workspace directory is discovered by the context
// resolver, agentcontext's own MCP runner connects the declared server,
// and the server's tool list is surfaced as a KindMCPServer resource in
// a PushContextState snapshot. This exercises the self-contained
// agentcontext MCP wiring (its mcpRunner and runMCPSync) end to end,
// with no dependency on the agent/x/agentmcp package.
func TestAgent_MCPServerToolsPushed(t *testing.T) {
	t.Parallel()

	// The MCP manager launches the declared server as a subprocess. Point
	// it at this test binary, which TestMain re-execs as a minimal stdio
	// MCP server when TEST_MCP_FAKE_SERVER=1.
	testBin, err := os.Executable()
	require.NoError(t, err)

	dir := t.TempDir()
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"fake": map[string]any{
				"command": testBin,
				"env":     map[string]string{"TEST_MCP_FAKE_SERVER": "1"},
			},
		},
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o600))

	//nolint:dogsled // setupAgent returns a wide tuple; we only care about the client.
	_, client, _, _, _ := setupAgent(t,
		agentsdk.Manifest{Directory: dir},
		0,
		func(_ *agenttest.Client, opts *agent.Options) {
			opts.ContextConfig = agentcontextconfig.Config{}
		},
	)

	// Wait for a push carrying the connected MCP server and its tool.
	var pushes []*agentproto.PushContextStateRequest
	require.Eventually(t, func() bool {
		pushes = client.ContextStatePushes()
		for _, push := range pushes {
			for _, r := range push.GetResources() {
				srv := r.GetMcpServer()
				if srv == nil || srv.GetServerName() != "fake" {
					continue
				}
				for _, tool := range srv.GetTools() {
					if tool.GetName() == "echo" {
						return true
					}
				}
			}
		}
		return false
	}, testutil.WaitLong, testutil.IntervalMedium,
		"expected the connected MCP server's tools to appear in a snapshot push; got %d pushes", len(pushes))

	// The MCP server resource carries OK status and the tool metadata.
	var found *agentproto.MCPServerBody
	for _, push := range pushes {
		for _, r := range push.GetResources() {
			if srv := r.GetMcpServer(); srv != nil && srv.GetServerName() == "fake" {
				if r.GetStatus() == agentproto.ContextResource_OK {
					found = srv
				}
			}
		}
	}
	require.NotNil(t, found, "a connected MCP server must push an OK resource")
	require.Len(t, found.GetTools(), 1)
	assert.Equal(t, "echo", found.GetTools()[0].GetName())
	assert.Equal(t, "echoes input", found.GetTools()[0].GetDescription())
}

// runFakeMCPServer serves a minimal MCP protocol over stdin/stdout: it
// answers initialize and advertises a single "echo" tool. The workspace
// MCP manager re-execs the test binary into this routine via TestMain
// when TEST_MCP_FAKE_SERVER=1.
func runFakeMCPServer() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		var resp any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake-server", "version": "0.0.1"},
				},
			}
		case "notifications/initialized":
			// Notifications take no response.
			continue
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "echoes input",
							"inputSchema": map[string]any{
								"type":       "object",
								"properties": map[string]any{},
							},
						},
					},
				},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			}
		}

		out, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", out)
	}
}
