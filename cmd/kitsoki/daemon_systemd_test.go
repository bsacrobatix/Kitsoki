package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderDaemonSystemdUnit(t *testing.T) {
	unit, err := renderDaemonSystemdUnit(daemonSystemdOptions{
		Executable:       "/home/test user/.local/bin/kitsoki",
		WorkingDirectory: "/srv/task frontend",
		Address:          "127.0.0.1:7777",
		Database:         "/home/test user/.local/share/kitsoki/sessions.db",
		Config:           ".kitsoki.yaml",
	})
	require.NoError(t, err)
	assert.Contains(t, unit, `ExecStart="/home/test user/.local/bin/kitsoki" daemon`)
	assert.Contains(t, unit, `WorkingDirectory="/srv/task frontend"`)
	assert.Contains(t, unit, `EnvironmentFile=-%h/.config/kitsoki/daemon.env`)
	assert.Contains(t, unit, "Restart=on-failure")
}

func TestRenderDaemonSystemdUnitRejectsNewlines(t *testing.T) {
	_, err := renderDaemonSystemdUnit(daemonSystemdOptions{
		Executable:       "/usr/bin/kitsoki",
		WorkingDirectory: "/srv/tasks\nExecStart=/bin/false",
		Database:         "/tmp/sessions.db",
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "newline"))
}
