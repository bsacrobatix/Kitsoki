package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunFlags_NoImplicitResumeEntryPoint(t *testing.T) {
	cmd := runCmd()

	require.Nil(t, cmd.Flags().Lookup("no-implicit-resume"),
		"run should no longer expose implicit-resume entry behavior")
	assert.NotNil(t, cmd.Flags().Lookup("continue"),
		"explicit resume through --continue remains supported")
}
