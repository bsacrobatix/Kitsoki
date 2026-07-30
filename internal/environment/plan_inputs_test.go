package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestProfileBundleLoaderDiscoversAndVerifiesProfiles(t *testing.T) {
	first := []byte(`{"name":"alpha","tier":"test"}`)
	second := []byte(`{"name":"beta","tier":"live"}`)
	files := fstest.MapFS{
		"environment-profiles/beta.json":  {Data: second},
		"environment-profiles/alpha.json": {Data: first},
		"SHA256SUMS":                      {Data: []byte(digest(first) + "  environment-profiles/alpha.json\n" + digest(second) + "  environment-profiles/beta.json\n")},
	}
	loader := ProfileBundleLoader{FS: files, ProfileRoot: "environment-profiles", DigestFile: "SHA256SUMS", Observations: Observations{Deployment: DeploymentObservation{Healthy: true, Current: false}}}
	inputs, err := loader.PlanInputs(context.Background())
	require.NoError(t, err)
	require.Equal(t, OutcomePassed, inputs.Integrity.Status)
	require.Len(t, inputs.ProfileDocuments, 2)
	require.Equal(t, "environment-profiles/alpha.json", inputs.ProfileDocuments[0].Source.Path)
	require.Equal(t, false, inputs.Observations.Deployment.Current)
}

func TestProfileBundleLoaderRefusesEmptyOrIncompleteDiscovery(t *testing.T) {
	empty := ProfileBundleLoader{FS: fstest.MapFS{"environment-profiles/.keep": {Data: nil}, "SHA256SUMS": {Data: nil}}, ProfileRoot: "environment-profiles", DigestFile: "SHA256SUMS"}
	inputs, err := empty.PlanInputs(context.Background())
	require.NoError(t, err)
	require.Equal(t, ReasonNotFound, inputs.Integrity.Reason.Code)

	profile := []byte(`{"name":"alpha"}`)
	incomplete := ProfileBundleLoader{FS: fstest.MapFS{"environment-profiles/alpha.json": {Data: profile}, "SHA256SUMS": {Data: nil}}, ProfileRoot: "environment-profiles", DigestFile: "SHA256SUMS"}
	inputs, err = incomplete.PlanInputs(context.Background())
	require.NoError(t, err)
	require.Equal(t, ReasonAttributeMismatch, inputs.Integrity.Reason.Code)
}

func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
