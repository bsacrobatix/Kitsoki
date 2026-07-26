package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	appgraph "kitsoki/internal/app/graph"
	"kitsoki/internal/applicationbuild"
	"kitsoki/internal/kitrepo"
)

// applicationCmd exposes the static, presentation-free parts of a story
// application. Invocation is session-routed through runstatus so CLI, web,
// JSON-RPC, and MCP can share one live registry rather than constructing
// transport-specific business behavior here.
func applicationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Inspect and drive story application contracts",
	}
	cmd.AddCommand(applicationDescribeCmd())
	cmd.AddCommand(applicationHandlersCmd())
	cmd.AddCommand(applicationGraphCmd())
	cmd.AddCommand(applicationCallCmd())
	cmd.AddCommand(applicationFeedbackCmd())
	cmd.AddCommand(applicationBuildCmd())
	cmd.AddCommand(applicationDevCmd())
	return cmd
}

func applicationFeedbackCmd() *cobra.Command {
	var (
		baseURL        string
		sessionID      string
		instruction    string
		kind           string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "feedback <semantic-ref>",
		Short: "Attach privacy-safe application context to a reviewed feedback report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionID == "" {
				return fmt.Errorf("--session-id is required")
			}
			if strings.TrimSpace(instruction) == "" {
				return fmt.Errorf("--instruction is required")
			}
			result, err := callApplicationRPC(cmd.Context(), baseURL, "runstatus.application.feedback", map[string]any{
				"session_id": sessionID, "ref": args[0], "instruction": instruction,
				"kind": kind, "idempotency_key": idempotencyKey,
			})
			if err != nil {
				return err
			}
			var response any
			if err := json.Unmarshal(result, &response); err != nil {
				return fmt.Errorf("decode application feedback receipt: %w", err)
			}
			return writeApplicationJSON(cmd, response)
		},
	}
	cmd.Flags().StringVar(&baseURL, "url", "http://127.0.0.1:8080", "live kitsoki web/daemon base URL")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "application session ID")
	cmd.Flags().StringVar(&instruction, "instruction", "", "reviewed feedback text")
	cmd.Flags().StringVar(&kind, "kind", "bug", "feedback report kind")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable retry key (derived when omitted)")
	return cmd
}

func applicationBuildCmd() *cobra.Command {
	var (
		tempRoot     string
		artifactRoot string
	)
	cmd := &cobra.Command{
		Use:   "build <app.yaml>",
		Short: "Build a content-addressed story application bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newApplicationLifecycleManager(
				args[0], tempRoot, artifactRoot, "", 0, nil,
				cmd.OutOrStdout(), cmd.ErrOrStderr(),
			)
			if err != nil {
				return err
			}
			manifest, err := manager.Build(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeApplicationJSON(cmd, manifest)
		},
	}
	cmd.Flags().StringVar(&tempRoot, "temp-root", "", "generated workspace root (default <project>/.temp/application)")
	cmd.Flags().StringVar(&artifactRoot, "artifact-root", "", "published bundle root (default <project>/.artifacts/application-builds)")
	return cmd
}

func applicationDevCmd() *cobra.Command {
	var (
		tempRoot     string
		artifactRoot string
		backendURL   string
		vitePort     int
		noBackend    bool
		flowPath     string
		hostCassette string
	)
	cmd := &cobra.Command{
		Use:   "dev <app.yaml>",
		Short: "Run the Kitsoki backend and Vite application development loop",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var backend []string
			if !noBackend {
				executable, err := os.Executable()
				if err != nil {
					return fmt.Errorf("resolve kitsoki executable: %w", err)
				}
				address, err := applicationBackendAddress(backendURL)
				if err != nil {
					return err
				}
				storyPath, err := filepath.Abs(args[0])
				if err != nil {
					return err
				}
				backend = []string{
					executable, "web", "--addr", address,
					"--stories-dir", filepath.Dir(storyPath),
				}
				if flowPath != "" {
					backend = append(backend, "--flow", flowPath)
				}
				if hostCassette != "" {
					backend = append(backend, "--host-cassette", hostCassette)
				}
			}
			manager, err := newApplicationLifecycleManager(
				args[0], tempRoot, artifactRoot, backendURL, vitePort, backend,
				cmd.OutOrStdout(), cmd.ErrOrStderr(),
			)
			if err != nil {
				return err
			}
			plan, err := manager.Prepare(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "application dev: http://127.0.0.1:%d\n", plan.VitePort)
			fmt.Fprintf(cmd.OutOrStdout(), "compatibility: %s\n", plan.CompatibilityOut)
			_, err = manager.Dev(cmd.Context(), args[0])
			return err
		},
	}
	cmd.Flags().StringVar(&tempRoot, "temp-root", "", "generated workspace root (default <project>/.temp/application)")
	cmd.Flags().StringVar(&artifactRoot, "artifact-root", "", "bundle root used by the lifecycle (default <project>/.artifacts/application-builds)")
	cmd.Flags().StringVar(&backendURL, "backend-url", "http://127.0.0.1:7777", "Kitsoki JSON-RPC backend URL")
	cmd.Flags().IntVar(&vitePort, "port", 5173, "strict Vite development port")
	cmd.Flags().BoolVar(&noBackend, "no-backend", false, "connect to an already-running backend instead of starting kitsoki web")
	cmd.Flags().StringVar(&flowPath, "flow", "", "deterministic flow fixture passed to the managed backend")
	cmd.Flags().StringVar(&hostCassette, "host-cassette", "", "host cassette passed to the managed backend")
	return cmd
}

func newApplicationLifecycleManager(
	storyPath, tempRoot, artifactRoot, backendURL string,
	vitePort int,
	backendCommand []string,
	stdout, stderr io.Writer,
) (*applicationbuild.Manager, error) {
	kitsokiRoot := kitrepo.Resolve()
	if kitsokiRoot == "" {
		return nil, fmt.Errorf("cannot locate the Kitsoki source toolchain; set %s", kitrepo.EnvVar)
	}
	projectRoot, err := applicationProjectRoot(storyPath)
	if err != nil {
		return nil, err
	}
	if tempRoot == "" {
		tempRoot = filepath.Join(projectRoot, ".temp", "application")
	}
	if artifactRoot == "" {
		artifactRoot = filepath.Join(projectRoot, ".artifacts", "application-builds")
	}
	return applicationbuild.New(applicationbuild.Config{
		RepoRoot: kitsokiRoot, TempRoot: tempRoot, ArtifactRoot: artifactRoot,
		BackendURL: backendURL, VitePort: vitePort, BackendCommand: backendCommand,
	}, loadAppWithEnv, applicationbuild.OSRunner{Stdout: stdout, Stderr: stderr}, nil)
}

func applicationProjectRoot(storyPath string) (string, error) {
	absolute, err := filepath.Abs(storyPath)
	if err != nil {
		return "", err
	}
	for current := filepath.Dir(absolute); ; current = filepath.Dir(current) {
		for _, marker := range []string{".kitsoki.yaml", ".git", "go.mod"} {
			if _, statErr := os.Stat(filepath.Join(current, marker)); statErr == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Dir(absolute), nil
		}
	}
}

func applicationBackendAddress(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("--backend-url must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("--backend-url scheme must be http or https")
	}
	return parsed.Host, nil
}

func applicationCallCmd() *cobra.Command {
	var (
		baseURL        string
		sessionID      string
		inputArg       string
		routingMode    string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "call <handler>",
		Short: "Call a live story application handler through JSON-RPC",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := readApplicationInput(inputArg)
			if err != nil {
				return err
			}
			params := map[string]any{
				"handler": args[0],
				"input":   json.RawMessage(input),
			}
			if sessionID != "" {
				params["session_id"] = sessionID
			}
			if routingMode != "" {
				params["routing_mode"] = routingMode
			}
			if idempotencyKey != "" {
				params["idempotency_key"] = idempotencyKey
			}
			result, err := callApplicationRPC(cmd.Context(), baseURL, "runstatus.application.cli_call", params)
			if err != nil {
				return err
			}
			var pretty any
			if err := json.Unmarshal(result, &pretty); err != nil {
				return fmt.Errorf("decode application outcome: %w", err)
			}
			return writeApplicationJSON(cmd, pretty)
		},
	}
	cmd.Flags().StringVar(&baseURL, "url", "http://127.0.0.1:8080", "live kitsoki web/daemon base URL")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "existing application session ID (required only by session-required handlers)")
	cmd.Flags().StringVar(&inputArg, "input", "{}", "JSON object or @path to a JSON file")
	cmd.Flags().StringVar(&routingMode, "routing-mode", "", "routing pin override (off, exact, synonym, semantic, llm)")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "idempotency key required by write/external handlers")
	return cmd
}

func applicationDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <app.yaml>",
		Short: "Print the loaded application/v1 contract as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			def, err := loadAppWithEnv(args[0])
			if err != nil {
				return err
			}
			if def.Application == nil {
				return fmt.Errorf("%s does not declare application:", args[0])
			}
			return writeApplicationJSON(cmd, def.Application)
		},
	}
}

type applicationHandlerDescription struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SemanticRef string   `json:"semantic_ref"`
	Session     string   `json:"session"`
	Effect      string   `json:"effect"`
	RoutingMode string   `json:"routing_mode"`
	Outcomes    []string `json:"outcomes"`
	Expose      []string `json:"expose"`
}

func applicationHandlersCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "handlers <app.yaml>",
		Short: "Discover the story's exported typed handlers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			def, err := loadAppWithEnv(args[0])
			if err != nil {
				return err
			}
			out := make([]applicationHandlerDescription, 0)
			if def.Exports != nil {
				names := make([]string, 0, len(def.Exports.Handlers))
				for name := range def.Exports.Handlers {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					handler := def.Exports.Handlers[name]
					if handler == nil || (transport != "" && !applicationExposes(handler.Expose, transport)) {
						continue
					}
					out = append(out, applicationHandlerDescription{
						ID:          name,
						Name:        handler.Name,
						Description: handler.Description,
						SemanticRef: handler.SemanticRef,
						Session:     handler.Session,
						Effect:      string(handler.Effect),
						RoutingMode: handler.RoutingMode,
						Outcomes:    append([]string(nil), handler.Outcomes...),
						Expose:      append([]string(nil), handler.Expose...),
					})
				}
			}
			return writeApplicationJSON(cmd, out)
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "", "show only handlers exposed on this transport (jsonrpc, mcp, cli)")
	return cmd
}

func applicationGraphCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "graph <app.yaml>",
		Short: "Print the story program graph as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			def, err := loadAppWithEnv(args[0])
			if err != nil {
				return err
			}
			return writeApplicationJSON(cmd, appgraph.ProgramGraph(def, def.App.ID))
		},
	}
}

func applicationExposes(expose []string, transport string) bool {
	for _, candidate := range expose {
		if candidate == transport {
			return true
		}
	}
	return false
}

func writeApplicationJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func readApplicationInput(value string) ([]byte, error) {
	if strings.HasPrefix(value, "@") {
		path := strings.TrimPrefix(value, "@")
		if path == "" {
			return nil, fmt.Errorf("--input @path must name a file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read application input: %w", err)
		}
		value = string(raw)
	}
	raw := []byte(value)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("application input must be a JSON object: %w", err)
	}
	return raw, nil
}

func callApplicationRPC(ctx context.Context, baseURL, method string, params map[string]any) (json.RawMessage, error) {
	requestBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/rpc", bytes.NewReader(requestBody),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call application RPC: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("application RPC returned HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode application RPC: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("application RPC %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result, nil
}
