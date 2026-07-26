package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/testrunner"
)

const flowEvidenceMaxFixtureBytes = 8 * 1024 * 1024

// testrunnerFlowEvidenceRunner invokes the in-process deterministic flow API.
// It never constructs a command or subprocess.
type testrunnerFlowEvidenceRunner struct {
	importResolver app.ImportResolver
}

func newTestrunnerFlowEvidenceRunner(resolver app.ImportResolver) host.FlowEvidenceRunner {
	return &testrunnerFlowEvidenceRunner{importResolver: resolver}
}

func (r *testrunnerFlowEvidenceRunner) RunFlowEvidence(
	ctx context.Context,
	suite host.FlowEvidenceSuite,
	limits host.FlowEvidenceLimits,
) (host.FlowEvidenceSuiteResult, error) {
	if limits.MaxRuns < 1 || limits.MaxEvidenceBytes < 1 {
		return host.FlowEvidenceSuiteResult{}, fmt.Errorf(
			"flow evidence runner: positive run and evidence limits are required",
		)
	}
	runCount, err := preflightFlowEvidenceSuite(suite.FlowGlob, limits.MaxRuns)
	if err != nil {
		return host.FlowEvidenceSuiteResult{}, err
	}
	if runCount == 0 {
		return host.FlowEvidenceSuiteResult{
			SuiteID: suite.ID,
			Passed:  false,
			Error:   "declared suite contains no flow fixtures",
		}, nil
	}
	report, err := testrunner.RunFlows(ctx, suite.AppPath, suite.FlowGlob, testrunner.FlowOptions{
		ImportResolver:    r.importResolver,
		DeterministicOnly: true,
	})
	if err != nil {
		return host.FlowEvidenceSuiteResult{}, err
	}
	actual := report.Passed + report.Failed
	if actual != runCount || len(report.Results) != actual {
		return host.FlowEvidenceSuiteResult{}, fmt.Errorf(
			"flow evidence runner: planned %d runs but runner returned %d",
			runCount, actual,
		)
	}
	result := host.FlowEvidenceSuiteResult{
		SuiteID:  suite.ID,
		Passed:   report.Failed == 0 && actual > 0,
		RunCount: actual,
		Runs:     make([]host.FlowEvidenceRunResult, 0, actual),
	}
	failureCount := 0
	for index, flow := range report.Results {
		run := host.FlowEvidenceRunResult{
			Ref:    flowEvidenceRunRef(suite.ID, flow.File, index),
			Passed: flow.Passed,
		}
		for _, turn := range flow.Turns {
			for _, failure := range turn.Failures {
				failureCount++
				if failureCount > 200 {
					return host.FlowEvidenceSuiteResult{}, fmt.Errorf(
						"flow evidence runner: failure count exceeds 200; refusing to truncate",
					)
				}
				run.Failures = append(run.Failures, failure)
			}
		}
		result.Runs = append(result.Runs, run)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return host.FlowEvidenceSuiteResult{}, fmt.Errorf("flow evidence runner: encode result: %w", err)
	}
	if len(encoded) > limits.MaxEvidenceBytes {
		return host.FlowEvidenceSuiteResult{}, fmt.Errorf(
			"flow evidence runner: result is %d bytes, exceeds %d; refusing to truncate",
			len(encoded), limits.MaxEvidenceBytes,
		)
	}
	return result, nil
}

func preflightFlowEvidenceSuite(glob string, maxRuns int) (int, error) {
	files, err := testrunner.ExpandGlobList(glob)
	if err != nil {
		return 0, fmt.Errorf("flow evidence runner: resolve declared suite: %w", err)
	}
	totalBytes := int64(0)
	runCount := 0
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return 0, fmt.Errorf("flow evidence runner: stat declared fixture: %w", err)
		}
		totalBytes += info.Size()
		if totalBytes > flowEvidenceMaxFixtureBytes {
			return 0, fmt.Errorf(
				"flow evidence runner: fixture input exceeds %d bytes; refusing to truncate",
				flowEvidenceMaxFixtureBytes,
			)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return 0, fmt.Errorf("flow evidence runner: read declared fixture: %w", err)
		}
		for _, document := range strings.Split(string(raw), "\n---") {
			document = strings.TrimSpace(document)
			if document == "" {
				continue
			}
			var header struct {
				TestKind string `yaml:"test_kind"`
			}
			if err := yaml.Unmarshal([]byte(document), &header); err != nil {
				return 0, fmt.Errorf("flow evidence runner: parse declared fixture: %w", err)
			}
			if header.TestKind != "flow" {
				continue
			}
			runCount++
			if runCount > maxRuns {
				return 0, fmt.Errorf(
					"flow evidence runner: suite declares more than %d runs; refusing to truncate",
					maxRuns,
				)
			}
		}
	}
	return runCount, nil
}

func flowEvidenceRunRef(suiteID, file string, index int) string {
	sum := sha256.Sum256([]byte(suiteID + "\x00" + file + "\x00" + strconv.Itoa(index)))
	return "flow:" + hex.EncodeToString(sum[:12])
}
