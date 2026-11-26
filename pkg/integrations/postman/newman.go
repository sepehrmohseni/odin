package postman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// NewmanRunner handles running Newman tests for Postman collections
type NewmanRunner struct {
	logger     *logrus.Logger
	repository NewmanRepository
	newmanPath string // Path to newman CLI executable
}

// NewmanRepository interface for persisting test results
type NewmanRepository interface {
	SaveTestResult(ctx context.Context, result *NewmanResult) error
	GetTestResult(ctx context.Context, id string) (*NewmanResult, error)
	GetTestHistory(ctx context.Context, collectionID string, limit int) ([]*NewmanResult, error)
	GetLatestTestResult(ctx context.Context, collectionID string) (*NewmanResult, error)
}

// NewmanRunOptions configures a Newman test run
type NewmanRunOptions struct {
	EnvironmentID    string            // Postman environment ID
	EnvironmentVars  map[string]string // Additional environment variables
	IterationCount   int               // Number of iterations to run
	DelayRequest     int               // Delay between requests (ms)
	Timeout          int               // Request timeout (ms)
	TimeoutRequest   int               // Individual request timeout (ms)
	TimeoutScript    int               // Script timeout (ms)
	BailOnError      bool              // Stop on first error
	SuppressExitCode bool              // Don't exit with error code
	Color            bool              // Enable colored output
	Insecure         bool              // Skip TLS verification (newman --insecure); off by default
	Reporters        []string          // Reporter types (cli, json, html)
	ReporterOptions  map[string]string // Reporter-specific options
}

// NewNewmanRunner creates a new Newman runner
func NewNewmanRunner(repository NewmanRepository, logger *logrus.Logger) *NewmanRunner {
	return &NewmanRunner{
		logger:     logger,
		repository: repository,
		newmanPath: "newman", // Assumes newman is in PATH
	}
}

// NewNewmanRunnerWithPath creates a new Newman runner with custom newman path
func NewNewmanRunnerWithPath(repository NewmanRepository, newmanPath string, logger *logrus.Logger) *NewmanRunner {
	return &NewmanRunner{
		logger:     logger,
		repository: repository,
		newmanPath: newmanPath,
	}
}

// RunCollection runs Newman tests for a collection
func (n *NewmanRunner) RunCollection(ctx context.Context, collectionID, serviceName string, collection *PostmanCollection, options *NewmanRunOptions) (*NewmanResult, error) {
	logger := n.logger.WithFields(logrus.Fields{
		"collection_id": collectionID,
		"service_name":  serviceName,
	})

	logger.Info("Starting Newman test run")

	startTime := time.Now()

	// Create result record
	result := &NewmanResult{
		CollectionID: collectionID,
		ServiceName:  serviceName,
		Status:       "running",
		RunAt:        startTime,
	}

	// The collection, the environment values and the report live in a private temporary
	// directory. (The collection used to be "-", which Newman does not read, environment values
	// were on the command line for any local user to see, and the JSON report, which Newman
	// writes to a file, was looked for on standard output.)
	dir, err := os.MkdirTemp("", "odin-newman-")
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, fmt.Errorf("newman: %w", err)
	}
	defer os.RemoveAll(dir)
	files, err := writeNewmanFiles(dir, collection, options)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, fmt.Errorf("newman: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, runTimeout(options))
	defer cancel()
	output, exitCode, err := n.executeNewman(runCtx, n.buildNewmanArgs(files, options))
	duration := time.Since(startTime)
	result.Duration = duration.Milliseconds()

	switch {
	case err != nil:
		result.Status = "failed"
		result.Error = err.Error()
		logger.WithError(err).Error("Newman execution failed")
	case exitCode != 0:
		result.Status = "failed"
		result.Error = fmt.Sprintf("newman exited with code %d", exitCode)
		logger.WithField("exit_code", exitCode).Warn("Newman test failures detected")
	default:
		result.Status = "passed"
		logger.Info("Newman tests passed")
	}

	report, readErr := os.ReadFile(files.report)
	if readErr != nil {
		report = output // no report: keep what Newman printed
	}
	if err := n.parseNewmanOutput(report, result); err != nil {
		logger.WithError(err).Warn("Failed to parse Newman output")
	}

	// Save result
	if err := n.repository.SaveTestResult(ctx, result); err != nil {
		logger.WithError(err).Error("Failed to save test result")
		// Don't return error, result is still valid
	}

	logger.WithFields(logrus.Fields{
		"status":   result.Status,
		"duration": duration,
		"passed":   result.Summary.Passed,
		"failed":   result.Summary.Failed,
	}).Info("Newman test run completed")

	return result, nil
}

type newmanFiles struct {
	collection, environment, report string
}

// writeNewmanFiles writes the collection and, if there are any, the environment values in
// Postman's environment format, readable only by this user.
func writeNewmanFiles(dir string, collection *PostmanCollection, options *NewmanRunOptions) (newmanFiles, error) {
	f := newmanFiles{
		collection: filepath.Join(dir, "collection.json"),
		report:     filepath.Join(dir, "report.json"),
	}
	data, err := json.Marshal(collection)
	if err != nil {
		return f, fmt.Errorf("encoding the collection: %w", err)
	}
	if err := os.WriteFile(f.collection, data, 0o600); err != nil {
		return f, err
	}
	if options != nil && len(options.EnvironmentVars) > 0 {
		type value struct {
			Key     string `json:"key"`
			Value   string `json:"value"`
			Enabled bool   `json:"enabled"`
		}
		env := struct {
			Name   string  `json:"name"`
			Values []value `json:"values"`
		}{Name: "odin"}
		keys := make([]string, 0, len(options.EnvironmentVars))
		for k := range options.EnvironmentVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env.Values = append(env.Values, value{Key: k, Value: options.EnvironmentVars[k], Enabled: true})
		}
		data, err := json.Marshal(env)
		if err != nil {
			return f, err
		}
		f.environment = filepath.Join(dir, "environment.json")
		if err := os.WriteFile(f.environment, data, 0o600); err != nil {
			return f, err
		}
	}
	return f, nil
}

// runTimeout bounds a whole run: Newman's own --timeout plus a minute, or 15 minutes.
func runTimeout(options *NewmanRunOptions) time.Duration {
	if options != nil && options.Timeout > 0 {
		return time.Duration(options.Timeout)*time.Millisecond + time.Minute
	}
	return 15 * time.Minute
}

// buildNewmanArgs builds the Newman command line. The JSON reporter always runs, exporting to
// files.report, so the result can be read.
func (n *NewmanRunner) buildNewmanArgs(files newmanFiles, options *NewmanRunOptions) []string {
	if options == nil {
		options = &NewmanRunOptions{}
	}
	args := []string{"run", files.collection}
	if files.environment != "" {
		args = append(args, "--environment", files.environment)
	}
	if options.IterationCount > 0 {
		args = append(args, "-n", strconv.Itoa(options.IterationCount))
	}
	if options.DelayRequest > 0 {
		args = append(args, "--delay-request", strconv.Itoa(options.DelayRequest))
	}
	if options.Timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(options.Timeout))
	}
	if options.TimeoutRequest > 0 {
		args = append(args, "--timeout-request", strconv.Itoa(options.TimeoutRequest))
	}
	if options.TimeoutScript > 0 {
		args = append(args, "--timeout-script", strconv.Itoa(options.TimeoutScript))
	}
	if options.BailOnError {
		args = append(args, "--bail")
	}
	if options.SuppressExitCode {
		args = append(args, "--suppress-exit-code")
	}
	// Only when asked for: the old option NoInsecureSSL ("don't allow insecure") added
	// --insecure, turning certificate checks off (AUDIT SEC-26).
	if options.Insecure {
		args = append(args, "--insecure")
	}
	if !options.Color {
		args = append(args, "--no-color")
	}
	reporters := []string{"json"}
	for _, r := range options.Reporters {
		if r != "json" && r != "" {
			reporters = append(reporters, r)
		}
	}
	args = append(args, "-r", strings.Join(reporters, ","), "--reporter-json-export", files.report)
	return args
}

// executeNewman executes the Newman CLI command
func (n *NewmanRunner) executeNewman(ctx context.Context, args []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, n.newmanPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	n.logger.WithField("command", cmd.String()).Debug("Executing Newman command")

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, 0, fmt.Errorf("newman execution error: %w", err)
		}
	}

	// Combine stdout and stderr
	output := stdout.Bytes()
	if stderr.Len() > 0 {
		n.logger.WithField("stderr", stderr.String()).Debug("Newman stderr output")
	}

	return output, exitCode, nil
}

// parseNewmanOutput parses Newman JSON output
func (n *NewmanRunner) parseNewmanOutput(output []byte, result *NewmanResult) error {
	if len(output) == 0 {
		return fmt.Errorf("empty newman output")
	}

	// Newman outputs JSON when using json reporter
	var newmanOutput struct {
		Collection struct {
			Name string `json:"name"`
		} `json:"collection"`
		Run struct {
			Stats struct {
				Iterations  NewmanStats `json:"iterations"`
				Items       NewmanStats `json:"items"`
				Scripts     NewmanStats `json:"scripts"`
				PreRequest  NewmanStats `json:"prerequest"`
				Requests    NewmanStats `json:"requests"`
				Tests       NewmanStats `json:"tests"`
				Assertions  NewmanStats `json:"assertions"`
				TestScripts NewmanStats `json:"testScripts"`
			} `json:"stats"`
			Failures []struct {
				Error struct {
					Message string `json:"message"`
					Test    string `json:"test"`
				} `json:"error"`
				At     string `json:"at"`
				Source struct {
					Name string `json:"name"`
				} `json:"source"`
			} `json:"failures"`
			Executions []struct {
				Item struct {
					Name string `json:"name"`
				} `json:"item"`
				Assertions []struct {
					Assertion string `json:"assertion"`
					Error     struct {
						Name    string `json:"name"`
						Message string `json:"message"`
					} `json:"error"`
				} `json:"assertions"`
				Request struct {
					Method string `json:"method"`
					URL    struct {
						Raw string `json:"raw"`
					} `json:"url"`
				} `json:"request"`
				Response struct {
					Code   int    `json:"code"`
					Status string `json:"status"`
				} `json:"response"`
			} `json:"executions"`
		} `json:"run"`
	}

	if err := json.Unmarshal(output, &newmanOutput); err != nil {
		n.logger.WithError(err).Debug("Failed to parse as Newman JSON, trying fallback")
		return n.parsePlainOutput(output, result)
	}

	// Fill in the result summary
	result.Summary = NewmanSummary{
		Total:      newmanOutput.Run.Stats.Assertions.Total,
		Passed:     newmanOutput.Run.Stats.Assertions.Total - newmanOutput.Run.Stats.Assertions.Failed,
		Failed:     newmanOutput.Run.Stats.Assertions.Failed,
		Requests:   newmanOutput.Run.Stats.Requests.Total,
		Iterations: newmanOutput.Run.Stats.Iterations.Total,
	}

	// Extract test results
	for _, exec := range newmanOutput.Run.Executions {
		testResult := NewmanTestResult{
			Name:   exec.Item.Name,
			Status: "passed",
		}

		if exec.Request.Method != "" {
			testResult.Method = exec.Request.Method
		}
		if exec.Request.URL.Raw != "" {
			testResult.URL = exec.Request.URL.Raw
		}
		if exec.Response.Code > 0 {
			testResult.ResponseCode = exec.Response.Code
			testResult.ResponseStatus = exec.Response.Status
		}

		// Check for assertion failures
		for _, assertion := range exec.Assertions {
			if assertion.Error.Name != "" || assertion.Error.Message != "" {
				testResult.Status = "failed"
				testResult.Error = assertion.Error.Message
				testResult.Assertions = append(testResult.Assertions, Assertion{
					Name:   assertion.Assertion,
					Passed: false,
					Error:  assertion.Error.Message,
				})
			} else {
				testResult.Assertions = append(testResult.Assertions, Assertion{
					Name:   assertion.Assertion,
					Passed: true,
				})
			}
		}

		result.Results = append(result.Results, testResult)
	}

	return nil
}

// parsePlainOutput attempts to parse plain text output as fallback
func (n *NewmanRunner) parsePlainOutput(output []byte, result *NewmanResult) error {
	// This is a simplified parser for plain text output
	// In production, you'd want more robust parsing
	outputStr := string(output)

	result.Output = outputStr

	// Try to extract basic stats from text output
	// Newman plain output format:
	// ┌─────────────────────────┬──────────┬──────────┐
	// │                         │ executed │   failed │
	// ├─────────────────────────┼──────────┼──────────┤
	// │              iterations │        1 │        0 │
	// │                requests │        2 │        0 │
	// │            test-scripts │        2 │        0 │
	// │      prerequest-scripts │        0 │        0 │
	// │              assertions │        4 │        0 │

	n.logger.Debug("Using simplified plain text parsing")

	// Set basic fields
	result.Summary = NewmanSummary{
		Total:  0,
		Passed: 0,
		Failed: 0,
	}

	return nil
}

// RunCollectionByID runs tests for a collection fetched from Postman
func (n *NewmanRunner) RunCollectionByID(ctx context.Context, client *Client, collectionID, serviceName string, options *NewmanRunOptions) (*NewmanResult, error) {
	// Fetch collection
	collection, err := client.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch collection: %w", err)
	}

	return n.RunCollection(ctx, collectionID, serviceName, collection, options)
}

// GetTestHistory retrieves test history for a collection
func (n *NewmanRunner) GetTestHistory(ctx context.Context, collectionID string, limit int) ([]*NewmanResult, error) {
	return n.repository.GetTestHistory(ctx, collectionID, limit)
}

// GetLatestTestResult retrieves the latest test result for a collection
func (n *NewmanRunner) GetLatestTestResult(ctx context.Context, collectionID string) (*NewmanResult, error) {
	return n.repository.GetLatestTestResult(ctx, collectionID)
}

// CheckNewmanInstalled checks if Newman CLI is installed
func (n *NewmanRunner) CheckNewmanInstalled() error {
	cmd := exec.Command(n.newmanPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("newman not found: %w (install with: npm install -g newman)", err)
	}

	n.logger.WithField("version", string(output)).Info("Newman CLI detected")
	return nil
}

// SetNewmanPath sets a custom path to the Newman executable
func (n *NewmanRunner) SetNewmanPath(path string) {
	n.newmanPath = path
}
