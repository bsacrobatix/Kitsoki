package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitsokimcp "kitsoki/internal/mcp"
)

// dialAsk sends one request to the listener socket and reads one response,
// mirroring what the mcp-operator-ask grandchild does on the wire.
func dialAsk(t *testing.T, sock string, req kitsokimcp.OperatorAskRequest) kitsokimcp.OperatorAskResponse {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	defer conn.Close()
	payload, _ := json.Marshal(req)
	_, err = conn.Write(append(payload, '\n'))
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)
	var resp kitsokimcp.OperatorAskResponse
	require.NoError(t, json.Unmarshal(line, &resp))
	return resp
}

func TestOperatorAskListener_RoundTrip(t *testing.T) {
	fake := &fakePrompter{answers: map[string]any{"Which env?": "prod"}}
	l, err := startOperatorAskListener(context.Background(), fake, "sess-9", time.Minute)
	require.NoError(t, err)
	defer l.close()

	resp := dialAsk(t, l.sockPath, kitsokimcp.OperatorAskRequest{
		Questions: []kitsokimcp.OperatorAskQuestion{{
			Question:    "Which env?",
			Header:      "Env",
			Options:     []kitsokimcp.OperatorAskOption{{Label: "prod"}, {Label: "staging"}},
			MultiSelect: false,
		}},
	})
	require.Empty(t, resp.Error)
	assert.Equal(t, "prod", resp.Answers["Which env?"])

	// The prompter saw the mapped host-facing question + session id.
	assert.Equal(t, "sess-9", fake.gotSession)
	require.Len(t, fake.gotQuestions, 1)
	assert.Equal(t, "Env", fake.gotQuestions[0].Header)
	require.Len(t, fake.gotQuestions[0].Options, 2)
	assert.Equal(t, "prod", fake.gotQuestions[0].Options[0].Label)
}

func TestOperatorAskListener_PrompterErrorBecomesErrorFrame(t *testing.T) {
	fake := &fakePrompter{err: errors.New("operator cancelled")}
	l, err := startOperatorAskListener(context.Background(), fake, "s", time.Minute)
	require.NoError(t, err)
	defer l.close()

	resp := dialAsk(t, l.sockPath, kitsokimcp.OperatorAskRequest{
		Questions: []kitsokimcp.OperatorAskQuestion{{Question: "q", Options: []kitsokimcp.OperatorAskOption{{Label: "a"}}}},
	})
	assert.Equal(t, "operator cancelled", resp.Error)
	assert.Nil(t, resp.Answers)
}

func TestOperatorAskListener_CloseRemovesSocket(t *testing.T) {
	l, err := startOperatorAskListener(context.Background(), &fakePrompter{}, "s", time.Minute)
	require.NoError(t, err)
	_, statErr := os.Stat(l.sockPath)
	require.NoError(t, statErr, "socket should exist while listening")
	l.close()
	_, statErr = os.Stat(l.sockPath)
	require.True(t, os.IsNotExist(statErr), "socket should be removed after close")
}

func TestOperatorAskListener_CtxCancelUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l, err := startOperatorAskListener(ctx, &fakePrompter{}, "s", time.Minute)
	require.NoError(t, err)
	defer l.close()
	sock := l.sockPath
	cancel()
	// AfterFunc closes the listener; a subsequent dial should fail promptly.
	require.Eventually(t, func() bool {
		_, derr := net.Dial("unix", sock)
		return derr != nil
	}, time.Second, 10*time.Millisecond, "listener must stop accepting after ctx cancel")
}

func TestAttachOperatorAsk_NoopWhenNoOperator(t *testing.T) {
	ctx := context.Background()
	inArgs := []string{"-p", "--model", "x"}
	inTools := []string{"Read"}
	outArgs, outTools, cleanup, err := attachOperatorAsk(ctx, inArgs, inTools)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()
	assert.Equal(t, inArgs, outArgs, "args unchanged with no operator")
	assert.Equal(t, inTools, outTools, "tools unchanged with no operator")
	assert.NotContains(t, outArgs, "--mcp-config")
	assert.NotContains(t, outTools, operatorAskToolName)
}

func TestAttachOperatorAsk_WiresToolWhenInteractive(t *testing.T) {
	ctx := WithKitsokiSessionID(
		WithOperatorPrompter(context.Background(), &fakePrompter{}),
		"sess-42",
	)
	outArgs, outTools, cleanup, err := attachOperatorAsk(ctx, []string{"-p"}, []string{"Read"})
	require.NoError(t, err)
	defer cleanup()

	assert.Contains(t, outTools, operatorAskToolName, "the ask tool must be allowlisted")
	assert.Contains(t, outArgs, "--mcp-config")
	assert.Contains(t, outArgs, "--append-system-prompt")

	// The MCP config tempfile points at mcp-operator-ask with a --socket.
	cfgIdx := indexOfStr(outArgs, "--mcp-config")
	require.GreaterOrEqual(t, cfgIdx, 0)
	cfgPath := outArgs[cfgIdx+1]
	raw, readErr := os.ReadFile(cfgPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(raw), operatorAskServerName)
	assert.Contains(t, string(raw), "mcp-operator-ask")
	assert.Contains(t, string(raw), "--socket")

	// The system clause names the tool.
	scIdx := indexOfStr(outArgs, "--append-system-prompt")
	require.GreaterOrEqual(t, scIdx, 0)
	assert.Contains(t, outArgs[scIdx+1], operatorAskToolName)

	// cleanup removes the config file.
	cleanup()
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr), "cleanup must remove the MCP config tempfile")
}

func indexOfStr(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
