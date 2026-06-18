package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/coder/coder/v2/agent/agentcontext"
	"github.com/coder/coder/v2/agent/x/agentmcp"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
)

// mcpContextProvider adapts the agent's MCP manager to the
// agentcontext.MCPProvider seam. It reads the manager's per-server
// health snapshot (never blocking) and turns each server into one
// KindMCPServer resource, so live MCP servers and their tools appear
// in the workspace-context snapshot alongside instruction files and
// skills. Servers that failed to connect surface as non-OK resources
// so they are no longer silently dropped.
type mcpContextProvider struct {
	// cachedServers returns the current per-server MCP snapshot
	// without blocking. It is *agentmcp.Manager.CachedServers in
	// production.
	cachedServers func() []agentmcp.ServerStatus
}

// MCPResources implements agentcontext.MCPProvider. It must never block;
// the resolver calls it on every re-resolve.
func (p mcpContextProvider) MCPResources() []agentcontext.Resource {
	if p.cachedServers == nil {
		return nil
	}
	return buildMCPServerResources(p.cachedServers())
}

// buildMCPServerResources turns a per-server MCP snapshot into one
// KindMCPServer resource per server. Servers are emitted in name order,
// and tools within a server in name order, so the resource ID list and
// content hashes are deterministic across resolves.
//
// A connected server that exposes at least one tool becomes a
// StatusOK resource carrying its tools. A server that failed to connect
// becomes a StatusUnreadable resource carrying the connection error, so
// it appears in the snapshot's issues instead of vanishing. A connected
// server with no tools yet is skipped until its tools arrive (a later
// re-resolve, driven by onToolsChanged, surfaces it). A server's
// .mcp.json entry still appears separately as a KindMCPConfig resource.
func buildMCPServerResources(servers []agentmcp.ServerStatus) []agentcontext.Resource {
	if len(servers) == 0 {
		return nil
	}
	sorted := slices.Clone(servers)
	slices.SortFunc(sorted, func(a, b agentmcp.ServerStatus) int {
		return strings.Compare(a.Name, b.Name)
	})

	resources := make([]agentcontext.Resource, 0, len(sorted))
	for _, s := range sorted {
		if s.Name == "" {
			continue
		}
		if !s.Connected {
			errMsg := s.Err
			if errMsg == "" {
				errMsg = "failed to connect"
			}
			resources = append(resources, agentcontext.Resource{
				ID:          resourceID(agentcontext.KindMCPServer, s.Name),
				Kind:        agentcontext.KindMCPServer,
				Source:      s.Name,
				Name:        s.Name,
				Status:      agentcontext.StatusUnreadable,
				Error:       errMsg,
				ContentHash: hashMCPServerError(s.Name, errMsg),
			})
			continue
		}
		if len(s.Tools) == 0 {
			continue
		}
		serverTools := slices.Clone(s.Tools)
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
			ID:          resourceID(agentcontext.KindMCPServer, s.Name),
			Kind:        agentcontext.KindMCPServer,
			Source:      s.Name,
			Name:        s.Name,
			Status:      agentcontext.StatusOK,
			ContentHash: hashMCPServer(s.Name, converted),
			Tools:       converted,
		})
	}
	if len(resources) == 0 {
		return nil
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

// hashMCPServerError produces a deterministic content hash for a
// failed-to-connect server. The "unreadable" discriminator keeps a
// failed server's hash distinct from an OK server's, so a server that
// transitions between connected and failed (or whose error text
// changes) flips the aggregate hash and re-pins dirty chats.
func hashMCPServerError(server, errMsg string) [32]byte {
	h := sha256.New()
	writeHashField(h, "unreadable")
	writeHashField(h, server)
	writeHashField(h, errMsg)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// writeHashField writes a length-prefixed field so adjacent fields cannot
// be confused by concatenation (e.g. "ab"+"c" vs "a"+"bc").
func writeHashField(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:%s", len(s), s)
}
