package chatd

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"golang.org/x/xerrors"

	"cdr.dev/slog/v3"
	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/codersdk"
)

// maxContextChangeContentBytes caps each side of an instruction-file change so
// the single-chat GET response stays bounded. The agent push admits resource
// bodies up to 256KiB; for the on-read diff we surface at most this many bytes
// per side, truncated on a rune boundary.
const maxContextChangeContentBytes = 64 * 1024

// ContextDetail computes the chat's pinned context resource list and, when the
// chat has drifted, the per-source change set against the agent's latest
// pushed snapshot. It is read-only and intended for the single-chat GET
// handler; list and watch payloads omit this detail to stay lightweight.
//
// resources lists the chat's full pinned inventory (instruction files, skills,
// and MCP configs/servers); changes is nil unless the chat is dirty (and has a
// resolvable agent), so the second read is only paid for when it can differ.
func (server *Server) ContextDetail(
	ctx context.Context,
	chat database.Chat,
) (resources []codersdk.ChatContextResource, changes []codersdk.ChatContextResourceChange, err error) {
	pinned, err := server.db.ListChatContextResourcesByChatID(ctx, chat.ID)
	if err != nil {
		return nil, nil, xerrors.Errorf("list chat context resources: %w", err)
	}
	resources = pinnedContextResources(pinned)

	if !chat.ContextDirtySince.Valid || !chat.AgentID.Valid {
		return resources, nil, nil
	}
	snapshot, err := server.db.ListWorkspaceAgentContextResources(ctx, chat.AgentID.UUID)
	if err != nil {
		return nil, nil, xerrors.Errorf("list workspace agent context resources: %w", err)
	}
	changes = diffContextResources(pinned, snapshot)
	server.logger.Debug(ctx, "computed chat context detail",
		slog.F("chat_id", chat.ID),
		slog.F("resource_count", len(resources)),
		slog.F("change_count", len(changes)),
	)
	return resources, changes, nil
}

// pinnedContextResources converts a chat's pinned context rows into the
// metadata-only resource list reported on the chat. It surfaces the full
// pinned inventory the user can act on, each stamped with its Status:
//
//   - OK instruction files with non-empty (sanitized) content, OK skills with
//     a name, and OK MCP configs/servers (mcp_server carries its tools).
//   - Non-OK rows (invalid, unreadable, oversize, excluded) of a tracked kind,
//     carrying Status and Error so the UI can explain why the resource was
//     dropped from the prompt instead of silently omitting it. Their
//     body-specific fields are empty.
//
// OK-but-empty instruction files, OK skills with no name, and untracked kinds
// (reserved plugin/hook/subagent/command) are skipped. Input order (source ASC
// from the query) is preserved.
func pinnedContextResources(resources []database.ChatContextResource) []codersdk.ChatContextResource {
	var out []codersdk.ChatContextResource
	for _, r := range resources {
		kind, ok := contextResourceKind(r.BodyKind)
		if !ok {
			continue
		}
		if r.Status != database.WorkspaceAgentContextResourceStatusOk {
			// Surface the failure (with its reason) rather than dropping it
			// silently; the body is empty for non-OK rows.
			out = append(out, codersdk.ChatContextResource{
				Source:    r.Source,
				Kind:      kind,
				SizeBytes: r.SizeBytes,
				Status:    codersdk.ChatContextResourceStatus(r.Status),
				Error:     r.Error,
			})
			continue
		}
		switch r.BodyKind {
		case database.WorkspaceAgentContextBodyKindInstructionFile:
			body, decoded := decodeInstructionFileBody(r.Body)
			if !decoded || SanitizePromptText(string(body.GetContent())) == "" {
				continue
			}
			out = append(out, codersdk.ChatContextResource{
				Source:    r.Source,
				Kind:      kind,
				SizeBytes: r.SizeBytes,
				Status:    codersdk.ChatContextResourceStatusOK,
			})
		case database.WorkspaceAgentContextBodyKindSkill:
			body, decoded := decodeSkillMetaBody(r.Body)
			if !decoded || body.GetName() == "" {
				continue
			}
			out = append(out, codersdk.ChatContextResource{
				Source:           r.Source,
				Kind:             kind,
				SizeBytes:        r.SizeBytes,
				Status:           codersdk.ChatContextResourceStatusOK,
				SkillName:        body.GetName(),
				SkillDescription: body.GetDescription(),
			})
		case database.WorkspaceAgentContextBodyKindMcpConfig:
			out = append(out, codersdk.ChatContextResource{
				Source:    r.Source,
				Kind:      kind,
				SizeBytes: r.SizeBytes,
				Status:    codersdk.ChatContextResourceStatusOK,
			})
		case database.WorkspaceAgentContextBodyKindMcpServer:
			out = append(out, codersdk.ChatContextResource{
				Source:    r.Source,
				Kind:      kind,
				SizeBytes: r.SizeBytes,
				Status:    codersdk.ChatContextResourceStatusOK,
				McpTools:  mcpToolsFromServerBody(r.Source, r.Body),
			})
		}
	}
	return out
}

// contextResourceSide is the subset of a context resource row needed to diff
// one source across the pinned copy and the agent snapshot.
type contextResourceSide struct {
	kind        database.WorkspaceAgentContextBodyKind
	body        json.RawMessage
	contentHash []byte
}

// diffContextResources compares a chat's pinned context against the agent's
// latest snapshot, by source, and returns the changes among prompt kinds
// (instruction files and skills). A source present on both sides with an equal
// content hash is unchanged and omitted; a differing hash is modified;
// pinned-only is removed; snapshot-only is added. Output is ordered by source.
func diffContextResources(
	pinned []database.ChatContextResource,
	snapshot []database.WorkspaceAgentContextResource,
) []codersdk.ChatContextResourceChange {
	pinnedBySource := make(map[string]contextResourceSide, len(pinned))
	for _, r := range pinned {
		pinnedBySource[r.Source] = contextResourceSide{kind: r.BodyKind, body: r.Body, contentHash: r.ContentHash}
	}
	snapshotBySource := make(map[string]contextResourceSide, len(snapshot))
	sources := make([]string, 0, len(pinned)+len(snapshot))
	for _, r := range pinned {
		sources = append(sources, r.Source)
	}
	for _, r := range snapshot {
		if _, ok := pinnedBySource[r.Source]; !ok {
			sources = append(sources, r.Source)
		}
		snapshotBySource[r.Source] = contextResourceSide{kind: r.BodyKind, body: r.Body, contentHash: r.ContentHash}
	}
	sort.Strings(sources)

	var changes []codersdk.ChatContextResourceChange
	for _, source := range sources {
		pinnedSide, hasPinned := pinnedBySource[source]
		snapshotSide, hasSnapshot := snapshotBySource[source]
		switch {
		case hasPinned && hasSnapshot:
			if bytes.Equal(pinnedSide.contentHash, snapshotSide.contentHash) {
				continue
			}
			if change, ok := buildResourceChange(source, codersdk.ChatContextResourceChangeStatusModified, &pinnedSide, &snapshotSide); ok {
				changes = append(changes, change)
			}
		case hasPinned:
			if change, ok := buildResourceChange(source, codersdk.ChatContextResourceChangeStatusRemoved, &pinnedSide, nil); ok {
				changes = append(changes, change)
			}
		case hasSnapshot:
			if change, ok := buildResourceChange(source, codersdk.ChatContextResourceChangeStatusAdded, nil, &snapshotSide); ok {
				changes = append(changes, change)
			}
		}
	}
	return changes
}

// buildResourceChange assembles a change entry for one source. The reported
// kind comes from the side that exists now (snapshot for added/modified,
// pinned for removed); ok is false only for kinds chatd does not track. An
// instruction-file change carries the sanitized, capped bodies of whichever
// sides are present; a skill change carries the identifying name and
// description; MCP config/server changes carry only source, kind, and status.
func buildResourceChange(
	source string,
	status codersdk.ChatContextResourceChangeStatus,
	pinned, snapshot *contextResourceSide,
) (codersdk.ChatContextResourceChange, bool) {
	current := snapshot
	if current == nil {
		current = pinned
	}
	kind, ok := contextResourceKind(current.kind)
	if !ok {
		return codersdk.ChatContextResourceChange{}, false
	}

	change := codersdk.ChatContextResourceChange{
		Source: source,
		Kind:   kind,
		Status: status,
	}
	switch kind {
	case codersdk.ChatContextResourceKindInstructionFile:
		if pinned != nil {
			change.OldContent = cappedInstructionContent(pinned.body)
		}
		if snapshot != nil {
			change.NewContent = cappedInstructionContent(snapshot.body)
		}
	case codersdk.ChatContextResourceKindSkill:
		// Removed skills exist only on the pinned side; otherwise the snapshot
		// identifies what a refresh would adopt.
		identity := snapshot
		if identity == nil {
			identity = pinned
		}
		if body, decoded := decodeSkillMetaBody(identity.body); decoded {
			change.SkillName = body.GetName()
			change.SkillDescription = body.GetDescription()
		}
	}
	return change, true
}

// contextResourceKind maps a database body kind to the codersdk kind reported
// on the chat. ok is false only for kinds chatd does not track yet (the
// reserved plugin/hook/subagent/command kinds), which are omitted from the
// resource list and change set.
func contextResourceKind(kind database.WorkspaceAgentContextBodyKind) (codersdk.ChatContextResourceKind, bool) {
	switch kind {
	case database.WorkspaceAgentContextBodyKindInstructionFile:
		return codersdk.ChatContextResourceKindInstructionFile, true
	case database.WorkspaceAgentContextBodyKindSkill:
		return codersdk.ChatContextResourceKindSkill, true
	case database.WorkspaceAgentContextBodyKindMcpConfig:
		return codersdk.ChatContextResourceKindMCPConfig, true
	case database.WorkspaceAgentContextBodyKindMcpServer:
		return codersdk.ChatContextResourceKindMCPServer, true
	default:
		return "", false
	}
}

// cappedInstructionContent decodes, sanitizes, and length-caps an instruction
// file body for display in a change diff. It returns "" when the body is not a
// decodable instruction file (e.g. a non-OK snapshot with an empty body).
func cappedInstructionContent(body json.RawMessage) string {
	decoded, ok := decodeInstructionFileBody(body)
	if !ok {
		return ""
	}
	return truncateUTF8(SanitizePromptText(string(decoded.GetContent())), maxContextChangeContentBytes)
}

// truncateUTF8 returns s truncated to at most n bytes without splitting a
// multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
