/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// fitness-runner is a lightweight Kubernetes Job image that executes an ADL
// fitness test against a crew's discussion endpoint.
//
// Environment variables:
//
//	DISCUSSION_ENDPOINT  — full URL to the crew's discussion gateway
//	TEST_FILE            — path to the mounted ADL file (e.g. /tests/discussion-health.adl)
//	JOB_NAME             — name of the owning Job (for annotation patching)
//	POD_NAMESPACE        — namespace (from downward API)
//	CREWFITNESS_NAME     — name of the owning CrewFitness CR
//
// Exit codes:
//
//	0 - the crew was reached and answered; the scenario ran to completion and its
//	    assertion results (pass OR fail) were recorded on the Job annotation
//	1 - the run could not obtain a crew answer (fatal setup error, gateway
//	    unreachable, or no synthesis before the deadline). This is a transient/
//	    infra failure, so the owning Job's backoffLimit retries it rather than
//	    recording a spurious zero. Assertion pass/fail does NOT affect the exit
//	    code; a real answer that fails its assertions is a completed measurement.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// fitnessResultAnnotation is the annotation key written to the owning Job.
	fitnessResultAnnotation = "kubemoot.ai/fitness-result"

	// defaultMaxDuration is the fallback if DEFINE CONST MAX_DURATION is absent.
	defaultMaxDuration = 120 * time.Second

	// defaultReadinessTimeout bounds the wait for the discussion gateway to be ready.
	defaultReadinessTimeout = 120 * time.Second

	// constQuestion and constMaxDuration are the ADL constants that hold the
	// question put to the crew and the longest the discussion may run.
	constQuestion    = "QUESTION"
	constMaxDuration = "MAX_DURATION"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := requireEnv("DISCUSSION_ENDPOINT")
	testFile := requireEnv("TEST_FILE")
	jobName := requireEnv("JOB_NAME")
	namespace := requireEnv("POD_NAMESPACE")

	fmt.Printf("[fitness-runner] endpoint=%s test=%s job=%s namespace=%s\n",
		endpoint, testFile, jobName, namespace)

	ft, err := loadFitnessTest(testFile)
	if err != nil {
		return err
	}

	maxDuration := parseMaxDuration(ft.Constants[constMaxDuration])
	fmt.Printf("[fitness-runner] max_duration=%s question=%q\n",
		maxDuration, ft.Constants[constQuestion])

	ctx := context.Background()

	// Readiness gate: wait for the discussion gateway to be ready before testing.
	client := newHTTPClient()
	if err := waitForReady(ctx, client, endpoint, readinessTimeout(os.Getenv)); err != nil {
		return fmt.Errorf("readiness gate: %w", err)
	}

	// Run the test
	outcome := RunFitnessTest(ctx, ft, endpoint, maxDuration)
	results := outcome.Assertions

	// Print results to stdout for debugging
	printResults(results)

	// An unanswered run is a transient/infra failure, not a measurement: the crew
	// was never reached or never synthesized an answer. Do NOT record results or a
	// transcript for it (that would freeze a spurious zero into the suite); return
	// an error so the Job's backoffLimit retries the scenario. Only after the
	// retries are exhausted does the operator record it as an Error.
	if !outcome.Answered {
		return fmt.Errorf("no crew answer obtained (gateway unreachable or no synthesis before deadline); " +
			"retrying via Job backoffLimit")
	}

	// Patch the Job annotation with the results
	resultJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	if err := patchJobAnnotation(ctx, namespace, jobName, string(resultJSON)); err != nil {
		// The annotation is the only channel the operator reads results from. Now
		// that an answered run exits 0 (Job Complete), a missing annotation makes
		// the operator derive Passed from zero assertions, so a silently-dropped
		// patch would turn a real Failed into a spurious Passed. Fail the run so
		// the Job retries and records the result instead of losing it.
		return fmt.Errorf("patch job annotation with results: %w", err)
	}

	// Capture the full discussion transcript to NATS Object Store (suite
	// iterations only; best-effort — never fails the run). Drill-down data for
	// the merged Fitness dashboard.
	maybeWriteTranscript(outcome, os.Getenv)

	// The scenario ran to completion. Exit 0 regardless of assertion pass/fail:
	// the pass/fail verdict lives in the recorded results, and a Job retry must
	// NOT re-run a scenario that already produced a real answer.
	fmt.Println(verdict(results))
	return nil
}

// loadFitnessTest reads and parses the ADL fitness test at path.
func loadFitnessTest(path string) (fitnessscript.FitnessTest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fitnessscript.FitnessTest{}, fmt.Errorf("read test file %q: %w", path, err)
	}
	ft := fitnessscript.ParseFitnessTest(string(raw))
	fmt.Printf("[fitness-runner] description=%q assertions=%d\n",
		ft.Description, len(ft.Assertions))
	return ft, nil
}

// readinessTimeout is READINESS_TIMEOUT in whole seconds when it is a positive
// integer, else defaultReadinessTimeout.
func readinessTimeout(getenv func(string) string) time.Duration {
	if rt, err := strconv.Atoi(getenv("READINESS_TIMEOUT")); err == nil && rt > 0 {
		return time.Duration(rt) * time.Second
	}
	return defaultReadinessTimeout
}

// verdict is the closing RESULT line: PASSED only when every assertion passed.
func verdict(results []AssertionResult) string {
	for _, r := range results {
		if !r.Passed {
			return "[fitness-runner] RESULT: FAILED (answer recorded; not retried)"
		}
	}
	return "[fitness-runner] RESULT: PASSED"
}

// requireEnv returns the value of an environment variable or exits with an error.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		fmt.Fprintf(os.Stderr, "FATAL: required environment variable %q is not set\n", key)
		os.Exit(1)
	}
	return val
}

// parseMaxDuration converts a MAX_DURATION constant value like "90 seconds" or "2m"
// into a time.Duration. Falls back to defaultMaxDuration on parse failure.
func parseMaxDuration(raw string) time.Duration {
	if raw == "" {
		return defaultMaxDuration
	}

	// Try standard Go duration strings first (e.g. "2m30s", "120s")
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}

	// Handle "<N> seconds" format from ADL
	parts := strings.Fields(strings.ToLower(raw))
	for i, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			// Look at the next word for the unit
			if i+1 < len(parts) {
				unit := parts[i+1]
				switch {
				case strings.HasPrefix(unit, "second"):
					return time.Duration(n) * time.Second
				case strings.HasPrefix(unit, "minute"):
					return time.Duration(n) * time.Minute
				case strings.HasPrefix(unit, "hour"):
					return time.Duration(n) * time.Hour
				}
			}
			// Bare number — assume seconds
			return time.Duration(n) * time.Second
		}
	}

	fmt.Fprintf(os.Stderr, "[fitness-runner] WARNING: cannot parse MAX_DURATION %q, using default\n", raw)
	return defaultMaxDuration
}

// printResults writes assertion results as formatted JSON to stdout.
func printResults(results []AssertionResult) {
	fmt.Println("[fitness-runner] assertion results:")
	for i, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Printf("  [%d] %s | %s | %s\n", i+1, status, r.Raw, r.Message)
	}

	jsonBytes, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println("[fitness-runner] JSON:")
	fmt.Println(string(jsonBytes))
}

// patchJobAnnotation patches the kubemoot.ai/fitness-result annotation on the Job
// using the in-cluster Kubernetes client.
func patchJobAnnotation(ctx context.Context, namespace, jobName, resultJSON string) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create k8s client: %w", err)
	}

	// Escape the JSON value for embedding in the patch JSON
	escapedJSON, err := json.Marshal(resultJSON)
	if err != nil {
		return fmt.Errorf("escape result JSON: %w", err)
	}

	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%s}}}`,
		fitnessResultAnnotation, string(escapedJSON))

	_, err = clientset.BatchV1().Jobs(namespace).Patch(
		ctx,
		jobName,
		types.MergePatchType,
		[]byte(patch),
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch job %s/%s: %w", namespace, jobName, err)
	}

	fmt.Printf("[fitness-runner] patched job %s/%s with %d assertion result(s)\n",
		namespace, jobName, len(resultJSON))
	return nil
}
