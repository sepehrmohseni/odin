package openapi_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"odin/pkg/config"
	"odin/pkg/openapi"
	"odin/pkg/service"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func quiet() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func services() []*service.Config {
	return []*service.Config{
		{Name: "users", BasePath: "/api/users", Targets: []string{"http://10.0.0.7:8080"}, Authentication: true,
			RateLimit: 100, RateLimitWindow: time.Minute, Protocol: "http"},
		{Name: "graph", BasePath: "/graphql", Targets: []string{"http://graph:4000"}, Protocol: "graphql"},
		{Name: "rpc", BasePath: "/rpc", Targets: []string{"grpc:9000"}, Protocol: "grpc"},
		{Name: "public-files", BasePath: "/files/", Targets: []string{"http://files"}, Protocol: "http"},
	}
}

func TestGenerateDescribesOnlyWhatTheGatewayKnows(t *testing.T) {
	spec := openapi.Generate(openapi.Options{}, services())

	assert.Equal(t, "Odin API Gateway", spec.Info.Title)
	assert.Contains(t, spec.Paths, "/api/users")
	assert.Contains(t, spec.Paths, "/api/users/{path}")
	assert.Contains(t, spec.Paths, "/files")
	assert.Contains(t, spec.Paths, "/files/{path}")
	assert.Contains(t, spec.Paths, "/graphql")
	assert.NotContains(t, spec.Paths, "/rpc", "gRPC is not described by OpenAPI")
	assert.NotContains(t, spec.Paths, "/api/users/{id}", "no invented CRUD operations")

	users := spec.Paths["/api/users/{path}"]
	for _, op := range []*openapi.Operation{users.Get, users.Post, users.Put, users.Patch, users.Delete} {
		require.NotNil(t, op)
		assert.Empty(t, op.Parameters, "no invented limit/offset parameters")
		assert.Equal(t, []map[string][]string{{"bearerAuth": {}}}, op.Security)
		assert.Contains(t, op.Responses, "401")
		assert.Contains(t, op.Responses, "429")
		assert.Contains(t, op.Responses["429"].Headers, "Retry-After")
		assert.Contains(t, op.Responses, "default")
	}
	require.Len(t, users.Parameters, 1)
	assert.Equal(t, "path", users.Parameters[0].Name)

	files := spec.Paths["/files"].Get
	assert.Empty(t, files.Security)
	assert.NotContains(t, files.Responses, "401")
	assert.NotContains(t, files.Responses, "429")

	graph := spec.Paths["/graphql"]
	require.NotNil(t, graph.Post)
	require.NotNil(t, graph.Post.RequestBody)
	assert.Nil(t, graph.Delete)

	assert.Equal(t, "bearer", spec.Components.SecuritySchemes["bearerAuth"].Scheme)

	data, err := spec.JSON()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "10.0.0.7", "targets are internal and never published")

	var ids = map[string]bool{}
	for _, item := range spec.Paths {
		for _, op := range []*openapi.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				assert.False(t, ids[op.OperationID], "duplicate operationId %s", op.OperationID)
				ids[op.OperationID] = true
			}
		}
	}
}

func TestGeneratedDocumentIsStableAndRealYAML(t *testing.T) {
	a, err := openapi.Generate(openapi.Options{}, services()).JSON()
	require.NoError(t, err)
	svcs := services()
	svcs[0], svcs[3] = svcs[3], svcs[0]
	b, err := openapi.Generate(openapi.Options{}, svcs).JSON()
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b))

	y, err := openapi.Generate(openapi.Options{}, services()).YAML()
	require.NoError(t, err)
	assert.False(t, json.Valid(y), "YAML output used to be JSON")
	var v map[string]interface{}
	require.NoError(t, yaml.Unmarshal(y, &v))
	assert.Equal(t, "3.0.3", v["openapi"])
}

func TestBuildWritesTheGeneratedDocument(t *testing.T) {
	out := filepath.Join(t.TempDir(), "openapi.yaml")
	doc, err := openapi.Build(config.OpenAPIConfig{Enabled: true, OutputPath: out, Title: "Shop"}, services(), quiet())
	require.NoError(t, err)

	written, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, string(doc.YAML), string(written))
	assert.Contains(t, string(doc.JSON), `"title": "Shop"`)
}

func TestBuildServesAHandWrittenDocument(t *testing.T) {
	off := false
	dir := t.TempDir()

	src := filepath.Join(dir, "api.yaml")
	require.NoError(t, os.WriteFile(src, []byte("openapi: 3.0.3\ninfo: {title: Mine, version: '2'}\npaths: {}\n"), 0o600))
	doc, err := openapi.Build(config.OpenAPIConfig{AutoGenerate: &off, OutputPath: src}, services(), quiet())
	require.NoError(t, err)
	var v map[string]interface{}
	require.NoError(t, json.Unmarshal(doc.JSON, &v))
	assert.Equal(t, "Mine", v["info"].(map[string]interface{})["title"])

	_, err = openapi.Build(config.OpenAPIConfig{AutoGenerate: &off}, services(), quiet())
	assert.ErrorContains(t, err, "outputPath")

	notSpec := filepath.Join(dir, "x.json")
	require.NoError(t, os.WriteFile(notSpec, []byte(`{"hello": 1}`), 0o600))
	_, err = openapi.Build(config.OpenAPIConfig{AutoGenerate: &off, OutputPath: notSpec}, services(), quiet())
	assert.ErrorContains(t, err, "not an OpenAPI document")
}
