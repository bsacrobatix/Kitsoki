package main

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/webauth"
	"kitsoki/internal/webconfig"
)

func TestWebInviteCmd_CreatesInviteAndPrintsLink(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	cmd := webInviteCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"Ana", "--admin", "--db", dbPath, "--base-url", "https://kitsoki.example.com"})
	require.NoError(t, cmd.Execute())

	printed := out.String()
	assert.Contains(t, printed, `Invitation for "Ana" (role: admin)`)
	assert.Contains(t, printed, "https://kitsoki.example.com/auth/invite?code=")

	u, err := url.Parse(strings.TrimSpace(lastLine(printed)))
	require.NoError(t, err)
	code := u.Query().Get("code")
	require.NotEmpty(t, code)

	store, closeDB, err := webauth.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = closeDB() }()

	inv, err := store.LookupInvite(context.Background(), code)
	require.NoError(t, err)
	assert.Equal(t, "Ana", inv.Name)
	assert.Equal(t, webauth.RoleAdmin, inv.Role)

	invites, err := store.ListInvites(context.Background())
	require.NoError(t, err)
	require.Len(t, invites, 1)
	assert.NotEqual(t, code, invites[0].ID, "the plaintext code must never be recoverable from storage")
}

func TestWebInviteCmd_List(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	create := webInviteCmd()
	create.SetOut(&bytes.Buffer{})
	create.SetArgs([]string{"Bob", "--db", dbPath})
	require.NoError(t, create.Execute())

	list := webInviteCmd()
	out := &bytes.Buffer{}
	list.SetOut(out)
	list.SetArgs([]string{"--list", "--db", dbPath})
	require.NoError(t, list.Execute())
	assert.Contains(t, out.String(), "Bob")
	assert.Contains(t, out.String(), "pending")
}

func TestWebInviteCmd_RequiresNameWithoutList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cmd := webInviteCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--db", dbPath})
	assert.Error(t, cmd.Execute())
}

func TestResolveAuthRequired(t *testing.T) {
	cases := []struct {
		name string
		auth *webconfig.AuthConfig
		addr string
		want bool
	}{
		{"nil config, loopback", nil, "127.0.0.1:7777", false},
		{"nil config, public", nil, "0.0.0.0:7777", true},
		{"mode off, public", &webconfig.AuthConfig{Mode: "off"}, "0.0.0.0:7777", false},
		{"mode required, loopback", &webconfig.AuthConfig{Mode: "required"}, "127.0.0.1:7777", true},
		{"mode auto, loopback", &webconfig.AuthConfig{Mode: "auto"}, "127.0.0.1:7777", false},
		{"mode auto, public", &webconfig.AuthConfig{Mode: "auto"}, ":7777", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, resolveAuthRequired(c.auth, c.addr))
		})
	}
}

func TestBuildWebAuth_RequiredWithoutCredsFailsFast(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	_, _, err := buildWebAuth(&webconfig.AuthConfig{Mode: "required"}, "0.0.0.0:7777", dbPath, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id")
}

func TestBuildWebAuth_OffReturnsNilManager(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	mgr, closeFn, err := buildWebAuth(nil, "127.0.0.1:7777", dbPath, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Nil(t, mgr)
	assert.Nil(t, closeFn)
}

func TestBuildWebAuth_RequiredWithCredsBuildsManager(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	cfg := &webconfig.AuthConfig{
		Mode:   "required",
		GitHub: webconfig.GitHubAuthConfig{ClientID: "cid", ClientSecret: "secret"},
	}
	mgr, closeFn, err := buildWebAuth(cfg, "0.0.0.0:7777", dbPath, &bytes.Buffer{})
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, closeFn)
	_ = closeFn()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
