package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type daemonSystemdOptions struct {
	Executable       string
	WorkingDirectory string
	Address          string
	Database         string
	Config           string
}

func daemonInstallSystemdCmd() *cobra.Command {
	var (
		unitPath string
		working  string
		addr     string
		dbPath   string
		config   string
		dryRun   bool
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "install-systemd",
		Short: "Install a systemd user unit for the Kitsoki daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve kitsoki executable: %w", err)
			}
			if strings.Contains(filepath.Clean(exe), string(filepath.Separator)+"go-build") {
				return fmt.Errorf("install-systemd requires an installed Kitsoki executable; run make install, then invoke kitsoki daemon install-systemd")
			}
			if working == "" {
				working, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve working directory: %w", err)
				}
			}
			if dbPath == "" {
				dbPath = defaultDBPath()
			}
			unit, err := renderDaemonSystemdUnit(daemonSystemdOptions{
				Executable:       exe,
				WorkingDirectory: working,
				Address:          addr,
				Database:         dbPath,
				Config:           config,
			})
			if err != nil {
				return err
			}
			if dryRun {
				_, err = fmt.Fprint(cmd.OutOrStdout(), unit)
				return err
			}
			if unitPath == "" {
				configHome, err := os.UserConfigDir()
				if err != nil {
					return fmt.Errorf("resolve user config directory: %w", err)
				}
				unitPath = filepath.Join(configHome, "systemd", "user", "kitsoki-daemon.service")
			}
			if prior, readErr := os.ReadFile(unitPath); readErr == nil && string(prior) != unit && !force {
				return fmt.Errorf("%s already exists with different content; rerun with --force after review", unitPath)
			} else if readErr != nil && !os.IsNotExist(readErr) {
				return fmt.Errorf("read existing unit: %w", readErr)
			}
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
				return fmt.Errorf("create systemd user directory: %w", err)
			}
			if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
				return fmt.Errorf("write systemd unit: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", unitPath)
			fmt.Fprintln(cmd.OutOrStdout(), "next: systemctl --user daemon-reload && systemctl --user enable --now kitsoki-daemon")
			return nil
		},
	}
	cmd.Flags().StringVar(&unitPath, "unit-path", "", "unit output path (default: user systemd config directory)")
	cmd.Flags().StringVar(&working, "working-directory", "", "project directory the daemon serves (default: current directory)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7777", "daemon HTTP listen address")
	cmd.Flags().StringVar(&dbPath, "db", "", "persistent SQLite path (default: Kitsoki data directory)")
	cmd.Flags().StringVar(&config, "config", ".kitsoki.yaml", "project web/daemon config path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the unit without writing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing different unit")
	return cmd
}

func renderDaemonSystemdUnit(opts daemonSystemdOptions) (string, error) {
	for label, value := range map[string]string{
		"executable": opts.Executable, "working directory": opts.WorkingDirectory,
		"address": opts.Address, "database": opts.Database, "config": opts.Config,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("%s contains a newline", label)
		}
	}
	if opts.Executable == "" || opts.WorkingDirectory == "" || opts.Database == "" {
		return "", fmt.Errorf("executable, working directory, and database are required")
	}
	args := []string{
		systemdQuote(opts.Executable), "daemon",
		"--addr", systemdQuote(opts.Address),
		"--db", systemdQuote(opts.Database),
		"--config", systemdQuote(opts.Config),
	}
	return fmt.Sprintf(`[Unit]
Description=Kitsoki durable job daemon
After=network.target

[Service]
Type=simple
WorkingDirectory=%s
EnvironmentFile=-%%h/.config/kitsoki/daemon.env
ExecStart=%s
Restart=on-failure
RestartSec=2
TimeoutStopSec=15

[Install]
WantedBy=default.target
`, systemdPath(opts.WorkingDirectory), strings.Join(args, " ")), nil
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

// systemdPath escapes a path for directives such as WorkingDirectory that do
// not apply command-line quote parsing. Percent is doubled to prevent specifier
// expansion; other non-portable bytes use systemd's C-style hex escape.
func systemdPath(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '%':
			out.WriteString("%%")
		case c == '/' || c == '.' || c == '_' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, `\x%02x`, c)
		}
	}
	return out.String()
}
