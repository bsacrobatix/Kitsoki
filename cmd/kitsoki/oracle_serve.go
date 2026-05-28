// oracle_serve.go — implements `kitsoki oracle-serve` (Phase 6).
//
// Starts a long-running JSON-RPC daemon that listens on a unix socket and
// dispatches incoming oracle calls to the five oracle handlers in-process.
// Callers (validator subprocesses, Python scripts, CI tooling) connect once
// and reuse the socket for the lifetime of the acceptance loop, avoiding
// per-call subprocess overhead.
//
// Socket path:
//   - --socket <path> flag
//   - KITSOKI_ORACLE_SOCK env var
//   - default: /tmp/kitsoki-oracle-<pid>.sock
//
// JSON-RPC protocol (newline-delimited, over the unix socket):
//
//	Request:     {"jsonrpc":"2.0","id":<n>,"method":"oracle.<verb>","params":{…}}
//	Notification: {"jsonrpc":"2.0","method":"oracle.event","params":{…}}
//	Response:    {"jsonrpc":"2.0","id":<n>,"result":{…}}  or  {"jsonrpc":"2.0","id":<n>,"error":{"code":-32000,"message":"…"}}
//
// Streaming (§5.2):
//
//	Each request may generate zero or more notification frames (no "id" field)
//	before the final response frame (with "id" matching the request). Clients
//	read lines until they see a frame with the matching "id".
//
// The "params" object carries the same keys as the CLI flags plus an optional
// "parent_session_id" field for trace continuity. Each RPC call propagates
// parent_session_id per-call via context (not os.Setenv) so concurrent clients
// each see their own session ID.
//
// The CLI auto-delegates to this daemon when KITSOKI_ORACLE_SOCK is set.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/host"
)

// rpcWriteDeadline is the per-write timeout applied to every enc.Encode call on
// the unix socket. Prevents a slow/stopped client from blocking the dispatch
// goroutine indefinitely (and backing up claude's stdout).
const rpcWriteDeadline = 5 * time.Second

// rpcDefaultTimeoutTask is the per-call timeout for oracle.task. Tasks may run
// long-lived agents, so the budget is generous.
const rpcDefaultTimeoutTask = 5 * time.Minute

// rpcDefaultTimeoutOther is the per-call timeout for all non-task verbs.
const rpcDefaultTimeoutOther = 60 * time.Second

// oracleServeCmd returns the `kitsoki oracle-serve` command.
func oracleServeCmd() *cobra.Command {
	var socketPath string

	cmd := &cobra.Command{
		Use:   "oracle-serve",
		Short: "Start the oracle JSON-RPC daemon on a unix socket (Phase 6)",
		Long: `Start a long-running JSON-RPC server that handles oracle calls over a unix
socket. Validator subprocesses and Python scripts connect to the socket once
and reuse it, avoiding per-call subprocess overhead.

Socket resolution (first wins):
  --socket <path>        explicit flag
  KITSOKI_ORACLE_SOCK    environment variable
  /tmp/kitsoki-oracle-<pid>.sock  default

JSON-RPC methods: oracle.extract, oracle.decide, oracle.ask, oracle.task,
oracle.converse. Each takes the same parameters as the corresponding CLI
subcommand plus an optional "parent_session_id" field.

Streaming (§5.2): each request may produce zero or more notification frames
before the final response. Notifications have no "id" field:
  {"jsonrpc":"2.0","method":"oracle.event","params":{...}}
The final response carries the request id:
  {"jsonrpc":"2.0","id":<n>,"result":{...}}

The server exports KITSOKI_ORACLE_SOCK to stderr on startup so callers can
read it:
  kitsoki oracle-serve &
  export KITSOKI_ORACLE_SOCK=$(<path read from stderr>)

Stops on SIGTERM or SIGINT.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if socketPath == "" {
				socketPath = os.Getenv("KITSOKI_ORACLE_SOCK")
			}
			if socketPath == "" {
				socketPath = filepath.Join(os.TempDir(),
					"kitsoki-oracle-"+strconv.Itoa(os.Getpid())+".sock")
			}
			os.Setenv("KITSOKI_ORACLE_SOCK", socketPath)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()

			return runOracleServe(ctx, socketPath, cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "unix socket path (default: $KITSOKI_ORACLE_SOCK or /tmp/kitsoki-oracle-<pid>.sock)")
	return cmd
}

// runOracleServe starts the unix socket listener and processes requests until
// ctx is cancelled.
//
// Race-free startup (M11): the function first attempts to dial the socket. If
// an existing server is already listening, it aborts with an error. If the dial
// fails (no such file / connection refused), it is safe to remove the stale
// socket file and bind a fresh listener.
func runOracleServe(ctx context.Context, socketPath string, logOut io.Writer) error {
	// M11: try dialling first. If something already listens, fail fast.
	if probe, err := net.Dial("unix", socketPath); err == nil {
		probe.Close()
		return fmt.Errorf("oracle-serve: another oracle-serve is already running at %s", socketPath)
	}
	// Safe to remove any stale socket file.
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("oracle-serve: listen %q: %w", socketPath, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	fmt.Fprintf(logOut, "kitsoki: oracle-serve listening on %s\n", socketPath)
	fmt.Fprintf(logOut, "kitsoki: export KITSOKI_ORACLE_SOCK=%s\n", socketPath)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				break
			}
			fmt.Fprintf(logOut, "kitsoki: oracle-serve accept error: %v\n", acceptErr)
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			handleOracleConn(ctx, c)
		}(conn)
	}
	wg.Wait()
	return nil
}

// rpcRequest is an incoming JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
}

// rpcResponse is an outgoing JSON-RPC 2.0 response (with id).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcNotification is an outgoing JSON-RPC 2.0 notification (no id).
// Sent for streaming progress events before the final response.
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleOracleConn reads newline-delimited JSON-RPC requests from conn and
// writes responses. One goroutine per connection; the connection is closed
// when the client disconnects or ctx is cancelled.
//
// N4 robustness:
//   - A background goroutine watches ctx.Done() and closes the conn so any
//     in-flight enc.Encode returns an error and the dispatch goroutine unwinds.
//   - Every enc.Encode call is guarded by a write deadline (rpcWriteDeadline)
//     so a slow client cannot block the dispatch goroutine indefinitely.
//   - Each dispatch call runs under a per-call context with a verb-specific
//     timeout (5 min for task, 60 s for all other verbs), overridable via
//     "timeout_seconds" in the RPC params.
func handleOracleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// N4: close conn when the parent context is cancelled so any in-flight
	// write (enc.Encode) unblocks immediately.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	enc := json.NewEncoder(conn)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	// encodeWithDeadline sets a write deadline before every enc.Encode call
	// and clears it on success so the next read doesn't inherit the deadline.
	encodeWithDeadline := func(v any) error {
		if err := conn.SetWriteDeadline(time.Now().Add(rpcWriteDeadline)); err != nil {
			return err
		}
		if err := enc.Encode(v); err != nil {
			return err
		}
		return conn.SetWriteDeadline(time.Time{})
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			resp := rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			}
			_ = encodeWithDeadline(resp)
			continue
		}

		// N4: derive a per-call context with a verb-specific timeout.
		// The caller may override via "timeout_seconds" in params.
		callTimeout := rpcDefaultTimeoutOther
		if req.Method == "oracle.task" {
			callTimeout = rpcDefaultTimeoutTask
		}
		if ts, ok := req.Params["timeout_seconds"]; ok {
			switch v := ts.(type) {
			case float64:
				if v > 0 {
					callTimeout = time.Duration(v) * time.Second
				}
			case int:
				if v > 0 {
					callTimeout = time.Duration(v) * time.Second
				}
			}
		}
		callCtx, callCancel := context.WithTimeout(connCtx, callTimeout)

		// Wire a per-connection notifier so the handler can emit streaming
		// notifications before the final response. Write deadline is set on
		// each notification to prevent a slow client from stalling dispatch.
		notify := func(params any) {
			n := rpcNotification{
				JSONRPC: "2.0",
				Method:  "oracle.event",
				Params:  params,
			}
			_ = encodeWithDeadline(n)
		}
		result, handlerErr := dispatchOracleRPC(callCtx, req.Method, req.Params, notify)
		callCancel()

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		if handlerErr != nil {
			resp.Error = &rpcError{Code: -32000, Message: handlerErr.Error()}
		} else if result.Error != "" {
			resp.Error = &rpcError{Code: -32000, Message: result.Error}
		} else {
			resp.Result = result.Data
		}
		_ = encodeWithDeadline(resp)
	}
}

// dispatchOracleRPC routes a JSON-RPC method to the appropriate oracle handler.
// The notify callback, when called, sends an oracle.event notification frame to
// the client before the final response — implementing §5.2 server-streaming.
// parent_session_id is threaded into the handler context per-call (not via
// os.Setenv) so concurrent clients each see their own session ID.
func dispatchOracleRPC(ctx context.Context, method string, params map[string]any, notify func(any)) (host.Result, error) {
	if params == nil {
		params = map[string]any{}
	}
	// C3: propagate parent_session_id per-call via context, not via os.Setenv.
	if sid, _ := params["parent_session_id"].(string); sid != "" {
		ctx = host.WithKitsokiSessionID(ctx, sid)
	}

	// H6: wire a StreamSink that converts oracle stream events into
	// JSON-RPC notification frames written to this connection.
	ctx = host.WithStreamSink(ctx, &rpcStreamSink{notify: notify})

	switch method {
	case "oracle.extract":
		return host.OracleExtractHandler(ctx, params)
	case "oracle.decide":
		return host.OracleDecideHandler(ctx, params)
	case "oracle.ask":
		return host.OracleAskHandler(ctx, params)
	case "oracle.task":
		return host.OracleTaskHandler(ctx, params)
	case "oracle.converse":
		return host.OracleConverseHandler(ctx, params)
	default:
		return host.Result{}, fmt.Errorf("oracle-serve: unknown method %q; valid methods: oracle.extract, oracle.decide, oracle.ask, oracle.task, oracle.converse", method)
	}
}

// rpcStreamSink implements host.StreamSink by converting StreamEvents into
// JSON-RPC notification frames sent over the connection before the final
// response. This is the §5.2 streaming implementation.
type rpcStreamSink struct {
	notify func(any)
}

func (s *rpcStreamSink) OnStreamEvent(ctx context.Context, ev host.StreamEvent) {
	if s.notify == nil {
		return
	}
	params := map[string]any{
		"type": ev.Type,
	}
	if ev.Subtype != "" {
		params["subtype"] = ev.Subtype
	}
	if ev.Tool != "" {
		params["tool"] = ev.Tool
	}
	if ev.Preview != "" {
		params["preview"] = ev.Preview
	}
	if ev.SessionID != "" {
		params["session_id"] = ev.SessionID
	}
	if ev.IsResult && ev.CostUSD != 0 {
		params["total_cost_usd"] = ev.CostUSD
	}
	s.notify(params)
}

// oracleRPCCall sends one JSON-RPC request to the unix socket at sockPath,
// reads streaming notifications followed by the final response, and writes
// the result to w. Used by the CLI auto-delegation path (KITSOKI_ORACLE_SOCK).
func oracleRPCCall(ctx context.Context, w io.Writer, sockPath, method string, params map[string]any) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("oracle: connect to socket %q: %w (is oracle-serve running?)", sockPath, err)
	}
	defer conn.Close()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("oracle: marshal RPC request: %w", err)
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		return fmt.Errorf("oracle: write RPC request: %w", err)
	}

	// Read lines until we see a response frame (has "id" field matching ours).
	// Notification frames (no "id") are logged and discarded at the CLI level.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		// Peek at the "id" field to distinguish notification vs response.
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("oracle: parse frame: %w", err)
		}
		if frame.Method == "oracle.event" {
			// Notification frame — log the event preview and continue reading.
			var evParams map[string]any
			if len(frame.Params) > 0 {
				_ = json.Unmarshal(frame.Params, &evParams)
			}
			if evParams != nil {
				preview, _ := evParams["preview"].(string)
				evType, _ := evParams["type"].(string)
				if preview != "" {
					slog.InfoContext(ctx, "oracle.event", "type", evType, "preview", preview)
				}
			}
			continue
		}
		// Response frame.
		if frame.Error != nil {
			return fmt.Errorf("%s", frame.Error.Message)
		}
		res := host.Result{Data: map[string]any{}}
		if len(frame.Result) > 0 {
			var m map[string]any
			if err := json.Unmarshal(frame.Result, &m); err == nil {
				res.Data = m
			}
		}
		return writeOracleResult(w, res)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("oracle: read RPC response: %w", err)
	}
	return fmt.Errorf("oracle: server closed connection without a response")
}
