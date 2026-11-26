package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gql(h http.Handler, query string, headers map[string]string) *httptest.ResponseRecorder {
	all := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		all[k] = v
	}
	return serve(h, "POST", "/graphql", strings.NewReader(`{"query":`+jsonString(query)+`}`), all)
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// COR-10: __typename is not introspection, client headers reach the server, and the server's
// status and body come back as they are.
func TestGraphQLPassesThroughFaithfully(t *testing.T) {
	var mu sync.Mutex
	var seenAuth, seenCustom string
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seenAuth, seenCustom = r.Header.Get("Authorization"), r.Header.Get("X-Tenant")
		mu.Unlock()
		if strings.Contains(string(body), "broken") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"message":"Cannot query field broken","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"me":{"__typename":"User","name":"alice"}}}`)
	})
	h := newGateway(t, &config.Config{
		Services: []config.ServiceConfig{{
			Name: "gql", BasePath: "/graphql", Targets: []string{be.URL}, Protocol: "graphql",
			GraphQL: &config.GraphQLConfig{EnableIntrospection: false},
		}},
	})

	rec := gql(h, "{ me { __typename name } }", map[string]string{"Authorization": "Bearer abc", "X-Tenant": "acme"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"__typename":"User"`, "__typename is allowed with introspection off")
	mu.Lock()
	assert.Equal(t, "Bearer abc", seenAuth, "client headers are forwarded")
	assert.Equal(t, "acme", seenCustom)
	mu.Unlock()

	assert.Equal(t, http.StatusForbidden, gql(h, "{ __schema { types { name } } }", nil).Code)
	assert.Equal(t, http.StatusForbidden, gql(h, `{ __type(name: "User") { name } }`, nil).Code)

	rec = gql(h, "{ broken }", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "the server's status, not 200")
	assert.Contains(t, rec.Body.String(), `"code":"GRAPHQL_VALIDATION_FAILED"`, "extensions survive")
}

func TestGraphQLQueryCaching(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"products":[]}}`)
	})
	h := newGateway(t, &config.Config{
		Services: []config.ServiceConfig{{
			Name: "gql", BasePath: "/graphql", Targets: []string{be.URL}, Protocol: "graphql",
			GraphQL: &config.GraphQLConfig{EnableQueryCaching: true, CacheTTL: time.Minute},
		}},
	})

	assert.Equal(t, "MISS", gql(h, "{ products { id } }", nil).Header().Get("X-Cache"))
	rec := gql(h, "{ products { id } }", nil)
	assert.Equal(t, "HIT", rec.Header().Get("X-Cache"))
	assert.Equal(t, `{"data":{"products":[]}}`, rec.Body.String())
	assert.EqualValues(t, 1, be.calls.Load(), "the second query was answered from the cache")

	gql(h, "{ products { id } }", map[string]string{"Authorization": "Bearer abc"})
	gql(h, "mutation { addProduct(name: \"x\") { id } }", nil)
	gql(h, "mutation { addProduct(name: \"x\") { id } }", nil)
	assert.EqualValues(t, 4, be.calls.Load(), "credentialed queries and mutations always reach the server")
}

func TestGraphQLUpstreamDown(t *testing.T) {
	h := newGateway(t, &config.Config{
		Services: []config.ServiceConfig{{
			Name: "gql", BasePath: "/graphql", Targets: []string{"http://127.0.0.1:1"}, Protocol: "graphql",
			GraphQL: &config.GraphQLConfig{},
		}},
	})
	rec := gql(h, "{ me }", nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code, "was a 500")
	assert.JSONEq(t, `{"errors":[{"message":"Bad Gateway"}]}`, rec.Body.String())
}
