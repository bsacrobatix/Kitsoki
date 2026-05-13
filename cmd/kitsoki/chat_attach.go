// chat_attach.go — implements `kitsoki chat attach` and
// `kitsoki chat detach`, the v1 (no-frame) PTY entry-points for
// proposal §4.2 / §6.6.
//
// The interesting lifecycle (lock acquire, tmux spawn, pty_sessions
// row, heartbeat) lives in internal/chatattach so the TUI's
// in-meta-mode `/attach` slash command shares the same code. This
// file is just the CLI argument-wiring surface around chatattach.Run.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/chatattach"
	"kitsoki/internal/chats"
	"kitsoki/internal/tmux"
)

// claudeBinForAttach resolves the binary path used when spawning the
// in-tmux `claude --resume` process. Honours $KITSOKI_CLAUDE_BIN so
// the CLI test path can substitute a fake.
func claudeBinForAttach() string {
	if bin := os.Getenv("KITSOKI_CLAUDE_BIN"); bin != "" {
		return bin
	}
	return "claude"
}

func chatAttachCmd() *cobra.Command {
	var (
		dbPath         string
		workspace      string
		permissionMode string
	)
	cmd := &cobra.Command{
		Use:   "attach <chat-id>",
		Short: "Open the chat in an interactive tmux-hosted claude session",
		Long: `Acquire the chat lock, ensure a tmux session is running ` + "`" + `claude --resume <claude-session-id>` + "`" + ` for this chat, and hand the terminal to it.

While attached, the chat lock is heartbeat'd every few seconds so cross-host observers can tell the attachment is live. When you detach (tmux prefix+d) the chat transitions to pty_background — the tmux session keeps running with claude inside; re-attach later to pick up the conversation.

Exit codes:
  0   normal detach or graceful tmux exit
  1   generic error (chat not found, claude binary missing, etc.)
  75  EX_TEMPFAIL: another process holds the chat lock`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			chatID := strings.TrimPrefix(args[0], chatattach.TmuxSessionPrefix)

			s, err := openSessionStore(dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			cs, err := chats.NewStore(s.DB())
			if err != nil {
				return fmt.Errorf("open chat store: %w", err)
			}

			ctx := cmd.Context()
			if _, err := cs.Get(ctx, chatID); err != nil {
				if errors.Is(err, chats.ErrChatNotFound) {
					return fmt.Errorf("chat %q not found", chatID)
				}
				return fmt.Errorf("get chat: %w", err)
			}

			tmuxClient, err := tmux.New(tmux.DefaultSocketPath())
			if err != nil {
				return fmt.Errorf("tmux init: %w", err)
			}

			opts := chatattach.Options{
				ChatID:         chatID,
				Store:          cs,
				Tmux:           tmuxClient,
				Workspace:      workspace,
				PermissionMode: permissionMode,
				ClaudeBin:      claudeBinForAttach(),
				Stderr:         cmd.ErrOrStderr(),
			}

			runTmux := func(sessionName string) error {
				return tmuxClient.AttachStreaming(ctx, sessionName)
			}

			runErr := chatattach.Run(ctx, opts, runTmux)
			if errors.Is(runErr, chats.ErrChatBusy) {
				fmt.Fprintln(cmd.ErrOrStderr(), runErr.Error())
				return errTempFail
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "chat SQLite database (default: $XDG_DATA_HOME/kitsoki/sessions.db)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "cwd for the claude pane (--cwd equivalent)")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "default", "claude --permission-mode value: default|acceptEdits|bypassPermissions|plan")
	return cmd
}

// ─── kitsoki chat detach ──────────────────────────────────────────────────────

func chatDetachCmd() *cobra.Command {
	var (
		dbPath string
		mode   string
	)
	cmd := &cobra.Command{
		Use:   "detach <chat-id>",
		Short: "Record the chat-detach state (storage-only; usually called from tmux bindings)",
		Long: `Update the chat_pty_sessions row to reflect a detach intent.

This subcommand does not interact with tmux; it is meant to be invoked from inner-tmux bindings (or from automation) immediately before the actual tmux kill / detach. Modes:

  background  — flip the row to pty_background (tmux stays alive; default for prefix+d).
  headless    — remove the row (chat returns to idle; queued drives can run headless).
  stop        — remove the row (chat returns to idle; queued drives stay pending).

Both 'headless' and 'stop' are storage-equivalent in v1 — they differ only in the intent recorded against the chat lock's audit trail. Phase E will wire the inner-tmux config that pairs each mode with the matching tmux kill-session invocation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			chatID := strings.TrimPrefix(args[0], chatattach.TmuxSessionPrefix)

			cs, cleanup, err := openChatStore(dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			ctx := cmd.Context()
			switch mode {
			case "background":
				p, err := cs.DetachPTY(ctx, chatID)
				if errors.Is(err, chats.ErrNoPTYSession) {
					return fmt.Errorf("no pty session for chat %q", chatID)
				}
				if err != nil {
					return fmt.Errorf("detach: %w", err)
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"chat_id":      chatID,
					"mode":         string(p.Mode),
					"tmux_session": p.TmuxSession,
				})
			case "headless", "stop":
				err := cs.RemovePTY(ctx, chatID)
				if errors.Is(err, chats.ErrNoPTYSession) {
					return fmt.Errorf("no pty session for chat %q", chatID)
				}
				if err != nil {
					return fmt.Errorf("remove pty: %w", err)
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"chat_id": chatID,
					"mode":    mode,
					"removed": true,
				})
			default:
				return fmt.Errorf("invalid --mode %q (want background|headless|stop)", mode)
			}
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "chat SQLite database (default: $XDG_DATA_HOME/kitsoki/sessions.db)")
	cmd.Flags().StringVar(&mode, "mode", "background", "detach mode: background|headless|stop")
	return cmd
}

// keep exec imported; runtime-only reference in case future surfaces
// in this file gain shellouts.
var _ = exec.Command
var _ = context.Background