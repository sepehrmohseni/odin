package postman_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"odin/pkg/integrations/postman"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memResults struct{ saved []*postman.NewmanResult }

func (m *memResults) SaveTestResult(_ context.Context, r *postman.NewmanResult) error {
	m.saved = append(m.saved, r)
	return nil
}
func (m *memResults) GetTestResult(context.Context, string) (*postman.NewmanResult, error) {
	return nil, nil
}
func (m *memResults) GetTestHistory(context.Context, string, int) ([]*postman.NewmanResult, error) {
	return nil, nil
}
func (m *memResults) GetLatestTestResult(context.Context, string) (*postman.NewmanResult, error) {
	return nil, nil
}

// fakeNewman stands in for the newman CLI: it records its arguments and the files it was given,
// and writes a JSON report where --reporter-json-export says, as Newman does.
const fakeNewman = `#!/bin/sh
echo "$@" > "$NEWMAN_ARGS"
[ -f "$2" ] && cp "$2" "$NEWMAN_ARGS.collection"
while [ $# -gt 0 ]; do
  case "$1" in
    --environment) shift; cp "$1" "$NEWMAN_ARGS.environment" ;;
    --reporter-json-export) shift; report="$1" ;;
  esac
  shift
done
cat > "$report" <<'JSON'
{"run":{"stats":{"assertions":{"total":3,"failed":1},"requests":{"total":2},"iterations":{"total":1}},
 "executions":[{"item":{"name":"list"},"assertions":[{"assertion":"status is 200"}]},
               {"item":{"name":"create"},"assertions":[{"assertion":"status is 201","error":{"name":"AssertionError","message":"expected 500 to equal 201"}}]}]}}
JSON
exit 1
`

// SEC-26 and the runner's plumbing: the collection and environment go to files (not "-" and not
// the command line), the JSON report is read from its file, and --insecure appears only when
// asked for.
func TestNewmanRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "newman")
	require.NoError(t, os.WriteFile(bin, []byte(fakeNewman), 0o700)) //nolint:gosec // a test executable
	argsFile := filepath.Join(dir, "args")
	t.Setenv("NEWMAN_ARGS", argsFile)

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	repo := &memResults{}
	runner := postman.NewNewmanRunnerWithPath(repo, bin, logger)
	coll := &postman.PostmanCollection{Info: postman.CollectionInfo{Name: "orders"}}

	result, err := runner.RunCollection(context.Background(), "c1", "orders", coll, &postman.NewmanRunOptions{
		EnvironmentVars: map[string]string{"apiKey": "s3cret-token"},
	})
	require.NoError(t, err)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.NotContains(t, string(args), "--insecure", "TLS is verified unless asked otherwise")
	assert.NotContains(t, string(args), "s3cret-token", "secrets are not on the command line")
	assert.Contains(t, string(args), "-r json --reporter-json-export")
	collection, err := os.ReadFile(argsFile + ".collection")
	require.NoError(t, err, "the collection was written to a file")
	assert.Contains(t, string(collection), `"orders"`)
	env, err := os.ReadFile(argsFile + ".environment")
	require.NoError(t, err)
	assert.Contains(t, string(env), `"key":"apiKey","value":"s3cret-token"`)

	assert.Equal(t, "failed", result.Status, "newman exited 1")
	assert.Equal(t, 3, result.Summary.Total)
	assert.Equal(t, 1, result.Summary.Failed)
	require.Len(t, result.Results, 2)
	assert.Equal(t, "expected 500 to equal 201", result.Results[1].Error)
	require.Len(t, repo.saved, 1)

	_, err = runner.RunCollection(context.Background(), "c1", "orders", coll, &postman.NewmanRunOptions{Insecure: true})
	require.NoError(t, err)
	args, _ = os.ReadFile(argsFile)
	assert.True(t, strings.Contains(string(args), "--insecure"), "only when asked for")
}
