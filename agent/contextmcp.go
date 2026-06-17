package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/coder/coder/v2/agent/agentcontext"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
)

// mcpContextProvider adapts the agent's MCP manager to the
// agentcontext.MCPProvider seam. It reads the manager's cached tool
// list (never blocking) and groups it into one KindMCPServer resource
// per server, so live MCP servers and their tools appear in the
// workspace-context snapshot alongside instruction files and skills.
type mcpContextProvider struct {
	// cachedTools returns the current MCP tool cache without blocking.
	// It is *agentmcp.Manager.CachedTools in production.
	cachedTools func() []workspacesdk.MCPToolInfo
}

// MCPResources implements agentcontext.MCPProvider. It must never block;
// the resolver calls it on every re-resolve.
func (p mcpContextProvider) MCPResources() []agentcontext.Resource {
	if p.cachedTools == nil {
		return nil
	}
	return buildMCPServerResources(p.cachedTools())
}

// buildMCPServerResources groups a flat MCP tool list by server name and
// returns one KindMCPServer resource per server. Servers are emitted in
// name order, and tools within a server in name order, so the resource ID
// list and content hashes are deterministic across resolves. Only servers
// that expose at least one tool are surfaced; a server's .mcp.json entry
// still appears separately as a KindMCPConfig resource.
func buildMCPServerResources(tools []workspacesdk.MCPToolInfo) []agentcontext.Resource {
	if len(tools) == 0 {
		return nil
	}
	byServer := make(map[string][]workspacesdk.MCPToolInfo)
	for _, t := range tools {
		if t.ServerName == "" {
			continue
		}
		byServer[t.ServerName] = append(byServer[t.ServerName], t)
	}
	servers := make([]string, 0, len(byServer))
	for name := range byServer {
		servers = append(servers, name)
	}
	slices.Sort(servers)

	resources := make([]agentcontext.Resource, 0, len(servers))
	for _, server := range servers {
		serverTools := byServer[server]
		slices.SortFunc(serverTools, func(a, b workspacesdk.MCPToolInfo) int {
			return strings.Compare(a.Name, b.Name)
		})
		converted := make([]agentcontext.MCPTool, 0, len(serverTools))
		for _, t := range serverTools {
			converted = append(converted, agentcontext.MCPTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Schema,
			})
		}
		resources = append(resources, agentcontext.Resource{
			ID:          resourceID(agentcontext.KindMCPServer, server),
			Kind:        agentcontext.KindMCPServer,
			Source:      server,
			Name:        server,
			Status:      agentcontext.StatusOK,
			ContentHash: hashMCPServer(server, converted),
			Tools:       converted,
		})
	}
	return resources
}

// resourceID mirrors agentcontext's unexported ID scheme
// ("<kind>:<source>") so MCP server resources sort and dedup
// consistently with filesystem-resolved resources.
func resourceID(kind agentcontext.ResourceKind, source string) string {
	return kind.String() + ":" + source
}

// hashMCPServer produces a deterministic content hash over a server's
// identity and full tool set (name, description, and input schema) so any
// tool-set change flips the snapshot's aggregate hash and re-pins dirty
// chats. The schema is encoded with encoding/json, which sorts map keys.
func hashMCPServer(server string, tools []agentcontext.MCPTool) [32]byte {
	h := sha256.New()
	writeHashField(h, server)
	for _, t := range tools {
		writeHashField(h, t.Name)
		writeHashField(h, t.Description)
		if len(t.InputSchema) > 0 {
			if schema, err := json.Marshal(t.InputSchema); err == nil {
				writeHashField(h, string(schema))
			}
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// writeHashField writes a length-prefixed field so adjacent fields cannot
// be confused by concatenation (e.g. "ab"+"c" vs "a"+"bc").
func writeHashField(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:%s", len(s), s)
}
