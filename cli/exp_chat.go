package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/agent/agentcontextconfig"
	"github.com/coder/coder/v2/cli/cliui"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/codersdk/agentsdk"
	"github.com/coder/serpent"
)

func (r *RootCmd) chatCommand() *serpent.Command {
	return &serpent.Command{
		Use:   "chat",
		Short: "Manage agent chats",
		Long:  "Commands for interacting with chats from within a workspace.",
		Handler: func(i *serpent.Invocation) error {
			return i.Command.HelpHandler(i)
		},
		Children: []*serpent.Command{
			r.chatContextCommand(),
		},
	}
}

func (r *RootCmd) chatContextCommand() *serpent.Command {
	return &serpent.Command{
		Use:   "context",
		Short: "Manage chat context",
		Long: "Inspect, refresh, add, or clear the workspace context (instruction " +
			"files and skills) for a chat.",
		Handler: func(i *serpent.Invocation) error {
			return i.Command.HelpHandler(i)
		},
		Children: []*serpent.Command{
			r.chatContextShowCommand(),
			r.chatContextRefreshCommand(),
			r.chatContextAddCommand(),
			r.chatContextClearCommand(),
		},
	}
}

// chatContextResourceRow is the table view of a pinned context resource.
type chatContextResourceRow struct {
	Source string `table:"source,default_sort"`
	Kind   string `table:"kind"`
	Size   int64  `table:"size bytes"`
	Skill  string `table:"skill"`
}

// chatContextChangeRow is the table view of one source-level context change.
type chatContextChangeRow struct {
	Status string `table:"status,default_sort"`
	Kind   string `table:"kind"`
	Source string `table:"source"`
	Skill  string `table:"skill"`
}

func (r *RootCmd) chatContextShowCommand() *serpent.Command {
	var outputFormat string
	cmd := &serpent.Command{
		Use:   "show <chat>",
		Short: "Show a chat's pinned workspace context and any drift",
		Long: "Display the workspace context a chat is pinned to (instruction files " +
			"and skills), whether it has drifted from the agent's latest snapshot, " +
			"and the per-source changes when it has.",
		Middleware: serpent.Chain(serpent.RequireNArgs(1)),
		Options: serpent.OptionSet{{
			Name:          "output",
			Flag:          "output",
			FlagShorthand: "o",
			Default:       "text",
			Description:   "Output format. Supported values: text, json.",
			Value:         serpent.EnumOf(&outputFormat, "text", "json"),
		}},
		Handler: func(inv *serpent.Invocation) error {
			ctx := inv.Context()
			client, err := r.InitClient(inv)
			if err != nil {
				return err
			}
			chatID, err := uuid.Parse(inv.Args[0])
			if err != nil {
				return xerrors.Errorf("invalid chat ID %q: %w", inv.Args[0], err)
			}

			exp := codersdk.NewExperimentalClient(client)
			chat, err := exp.GetChat(ctx, chatID)
			if err != nil {
				return xerrors.Errorf("get chat: %w", err)
			}

			if outputFormat == "json" {
				// Emit the context object directly; it is null when the chat
				// has no pinned context yet.
				out, err := json.MarshalIndent(chat.Context, "", "  ")
				if err != nil {
					return xerrors.Errorf("marshal chat context: %w", err)
				}
				_, _ = fmt.Fprintln(inv.Stdout, string(out))
				return nil
			}
			return renderChatContextText(inv.Stdout, chat)
		},
	}
	return cmd
}

func renderChatContextText(out io.Writer, chat codersdk.Chat) error {
	if chat.Context == nil {
		_, _ = fmt.Fprintf(out, "Chat %s has no pinned workspace context.\n", chat.ID)
		return nil
	}
	cc := chat.Context

	status := "clean"
	if cc.Dirty {
		status = "drifted"
		if cc.DirtySince != nil {
			status = fmt.Sprintf("drifted (since %s)", cc.DirtySince.Format(time.RFC3339))
		}
	}
	_, _ = fmt.Fprintf(out, "Context for chat %s\n", chat.ID)
	_, _ = fmt.Fprintf(out, "  Status: %s\n", status)
	if cc.Error != "" {
		_, _ = fmt.Fprintf(out, "  Error:  %s\n", cc.Error)
	}

	resourceRows := make([]chatContextResourceRow, 0, len(cc.Resources))
	for _, res := range cc.Resources {
		resourceRows = append(resourceRows, chatContextResourceRow{
			Source: res.Source,
			Kind:   string(res.Kind),
			Size:   res.SizeBytes,
			Skill:  res.SkillName,
		})
	}
	_, _ = fmt.Fprintf(out, "\nPinned resources (%d)\n", len(resourceRows))
	if len(resourceRows) == 0 {
		_, _ = fmt.Fprintln(out, "  (none)")
	} else {
		tbl, err := cliui.DisplayTable(resourceRows, "source", nil)
		if err != nil {
			return xerrors.Errorf("render resources: %w", err)
		}
		_, _ = fmt.Fprintln(out, tbl)
	}

	if !cc.Dirty {
		return nil
	}

	changeRows := make([]chatContextChangeRow, 0, len(cc.Changes))
	for _, change := range cc.Changes {
		changeRows = append(changeRows, chatContextChangeRow{
			Status: string(change.Status),
			Kind:   string(change.Kind),
			Source: change.Source,
			Skill:  change.SkillName,
		})
	}
	_, _ = fmt.Fprintf(out, "\nChanges vs latest snapshot (%d)\n", len(changeRows))
	if len(changeRows) == 0 {
		_, _ = fmt.Fprintln(out, "  (none)")
	} else {
		tbl, err := cliui.DisplayTable(changeRows, "status", nil)
		if err != nil {
			return xerrors.Errorf("render changes: %w", err)
		}
		_, _ = fmt.Fprintln(out, tbl)
	}
	_, _ = fmt.Fprintf(out, "Run 'coder chat context refresh %s' to adopt the latest context.\n", chat.ID)
	return nil
}

func (r *RootCmd) chatContextRefreshCommand() *serpent.Command {
	cmd := &serpent.Command{
		Use:   "refresh <chat>",
		Short: "Refresh a chat's workspace context to the latest snapshot",
		Long: "Re-pin a chat to the workspace agent's latest context snapshot and " +
			"clear the drift marker. The chat's next turn uses the refreshed context.",
		Middleware: serpent.Chain(serpent.RequireNArgs(1)),
		Handler: func(inv *serpent.Invocation) error {
			ctx := inv.Context()
			client, err := r.InitClient(inv)
			if err != nil {
				return err
			}
			chatID, err := uuid.Parse(inv.Args[0])
			if err != nil {
				return xerrors.Errorf("invalid chat ID %q: %w", inv.Args[0], err)
			}

			exp := codersdk.NewExperimentalClient(client)
			chat, err := exp.RefreshChatContext(ctx, chatID)
			if err != nil {
				return xerrors.Errorf("refresh chat context: %w", err)
			}

			_, _ = fmt.Fprintf(inv.Stdout, "Refreshed context for chat %s.\n", chatID)
			if chat.Context != nil && chat.Context.Error != "" {
				_, _ = fmt.Fprintf(inv.Stdout, "Snapshot reported an error: %s\n", chat.Context.Error)
			}
			return nil
		},
	}
	return cmd
}

func (*RootCmd) chatContextAddCommand() *serpent.Command {
	var (
		dir    string
		chatID string
	)
	agentAuth := &AgentAuth{}
	cmd := &serpent.Command{
		Use:   "add",
		Short: "Add context to an active chat",
		Long: "Read instruction files and discover skills from a directory, then add " +
			"them as context to an active chat session. Multiple calls " +
			"are additive.",
		Handler: func(inv *serpent.Invocation) error {
			ctx := inv.Context()
			ctx, stop := inv.SignalNotifyContext(ctx, StopSignals...)
			defer stop()

			if dir == "" && inv.Environ.Get("CODER") != "true" {
				return xerrors.New("this command must be run inside a Coder workspace (set --dir to override)")
			}

			client, err := agentAuth.CreateClient()
			if err != nil {
				return xerrors.Errorf("create agent client: %w", err)
			}

			resolvedDir := dir
			if resolvedDir == "" {
				resolvedDir, err = os.Getwd()
				if err != nil {
					return xerrors.Errorf("get working directory: %w", err)
				}
			}
			resolvedDir, err = filepath.Abs(resolvedDir)
			if err != nil {
				return xerrors.Errorf("resolve directory: %w", err)
			}
			info, err := os.Stat(resolvedDir)
			if err != nil {
				return xerrors.Errorf("cannot read directory %q: %w", resolvedDir, err)
			}
			if !info.IsDir() {
				return xerrors.Errorf("%q is not a directory", resolvedDir)
			}

			parts := agentcontextconfig.ContextPartsFromDir(resolvedDir)
			if len(parts) == 0 {
				_, _ = fmt.Fprintln(inv.Stderr, "No context files or skills found in "+resolvedDir)
				return nil
			}

			// Resolve chat ID from flag or auto-detect.
			resolvedChatID, err := parseChatID(chatID)
			if err != nil {
				return err
			}

			resp, err := client.AddChatContext(ctx, agentsdk.AddChatContextRequest{
				ChatID: resolvedChatID,
				Parts:  parts,
			})
			if err != nil {
				return xerrors.Errorf("add chat context: %w", err)
			}

			_, _ = fmt.Fprintf(inv.Stdout, "Added %d context part(s) to chat %s\n", resp.Count, resp.ChatID)
			return nil
		},
		Options: serpent.OptionSet{
			{
				Name:        "Directory",
				Flag:        "dir",
				Description: "Directory to read context files and skills from. Defaults to the current working directory.",
				Value:       serpent.StringOf(&dir),
			},
			{
				Name:        "Chat ID",
				Flag:        "chat",
				Env:         "CODER_CHAT_ID",
				Description: "Chat ID to add context to. Auto-detected from CODER_CHAT_ID, the only active chat, or the only top-level active chat.",
				Value:       serpent.StringOf(&chatID),
			},
		},
	}
	agentAuth.AttachOptions(cmd, false)
	return cmd
}

func (*RootCmd) chatContextClearCommand() *serpent.Command {
	var chatID string
	agentAuth := &AgentAuth{}
	cmd := &serpent.Command{
		Use:   "clear",
		Short: "Clear context from an active chat",
		Long: "Soft-delete all context-file and skill messages from an active chat. " +
			"The next turn will re-fetch default context from the agent.",
		Handler: func(inv *serpent.Invocation) error {
			ctx := inv.Context()
			ctx, stop := inv.SignalNotifyContext(ctx, StopSignals...)
			defer stop()

			client, err := agentAuth.CreateClient()
			if err != nil {
				return xerrors.Errorf("create agent client: %w", err)
			}

			resolvedChatID, err := parseChatID(chatID)
			if err != nil {
				return err
			}

			resp, err := client.ClearChatContext(ctx, agentsdk.ClearChatContextRequest{
				ChatID: resolvedChatID,
			})
			if err != nil {
				return xerrors.Errorf("clear chat context: %w", err)
			}

			if resp.ChatID == uuid.Nil {
				_, _ = fmt.Fprintln(inv.Stdout, "No active chats to clear.")
			} else {
				_, _ = fmt.Fprintf(inv.Stdout, "Cleared context from chat %s\n", resp.ChatID)
			}
			return nil
		},
		Options: serpent.OptionSet{{
			Name:        "Chat ID",
			Flag:        "chat",
			Env:         "CODER_CHAT_ID",
			Description: "Chat ID to clear context from. Auto-detected from CODER_CHAT_ID, the only active chat, or the only top-level active chat.",
			Value:       serpent.StringOf(&chatID),
		}},
	}
	agentAuth.AttachOptions(cmd, false)
	return cmd
}

// parseChatID returns the chat UUID from the flag value (which
// serpent already populates from --chat or CODER_CHAT_ID). Returns
// uuid.Nil if empty (the server will auto-detect).
func parseChatID(flagValue string) (uuid.UUID, error) {
	if flagValue == "" {
		return uuid.Nil, nil
	}
	parsed, err := uuid.Parse(flagValue)
	if err != nil {
		return uuid.Nil, xerrors.Errorf("invalid chat ID %q: %w", flagValue, err)
	}
	return parsed, nil
}
