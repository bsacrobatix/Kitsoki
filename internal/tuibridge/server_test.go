package tuibridge

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialTest connects to the bridge and returns a func that reads accumulated
// output frames until the given substring appears (or the deadline expires).
func dialTest(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/pty"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func waitForOutput(t *testing.T, conn *websocket.Conn, want string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var buf bytes.Buffer
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read (buffered so far: %q): %v", buf.String(), err)
		}
		buf.Write(data)
		if strings.Contains(buf.String(), want) {
			return buf.String()
		}
	}
}

// TestServer_EchoesKeystrokes verifies the core pty<->websocket bridge: bytes
// written to the websocket reach the spawned process's stdin, and the
// process's stdout reaches the websocket, byte-for-byte (modulo the tty's own
// line echo).
func TestServer_EchoesKeystrokes(t *testing.T) {
	srv := New(Options{Command: []string{"/bin/cat"}})
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	conn := dialTest(t, ts)
	ctx := context.Background()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("hello-bridge\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForOutput(t, conn, "hello-bridge", 5*time.Second)
}

// TestServer_Resize verifies the JSON resize control frame actually resizes
// the pty before the spawned process reads it, by racing a resize frame
// against a shell that reports its terminal size once a line of input
// arrives — websocket frame ordering on one connection guarantees the resize
// is applied before the triggering input is delivered.
func TestServer_Resize(t *testing.T) {
	srv := New(Options{Command: []string{"/bin/sh", "-c", "read _; stty size"}})
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	conn := dialTest(t, ts)
	ctx := context.Background()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("go\n")); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	waitForOutput(t, conn, "40 120", 5*time.Second)
}
