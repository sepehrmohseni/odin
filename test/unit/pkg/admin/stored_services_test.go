package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"odin/pkg/admin"
	"odin/pkg/config"
	"odin/pkg/mongodb"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memServices is an in-memory stand-in for MongoDB's services collection. Methods the
// stored-services API does not use are left to the embedded (nil) interface.
type memServices struct {
	mongodb.Repository
	mu   sync.Mutex
	docs map[string]*mongodb.ServiceDocument
}

func (m *memServices) CreateService(_ context.Context, d *mongodb.ServiceDocument) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d.ID = d.Name
	m.docs[d.Name] = d
	return nil
}

func (m *memServices) GetServiceByName(_ context.Context, name string) (*mongodb.ServiceDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.docs[name]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("service not found: %s", name)
}

func (m *memServices) ListServices(_ context.Context, _ *bool) ([]*mongodb.ServiceDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*mongodb.ServiceDocument, 0, len(m.docs))
	for _, d := range m.docs {
		out = append(out, d)
	}
	return out, nil
}

func (m *memServices) UpdateService(_ context.Context, id string, d *mongodb.ServiceDocument) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[id] = d
	return nil
}

func (m *memServices) DeleteService(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return fmt.Errorf("not found")
	}
	delete(m.docs, id)
	return nil
}

func TestStoredServicesAPI(t *testing.T) {
	repo := &memServices{docs: map[string]*mongodb.ServiceDocument{}}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port: 8080\n"), 0o600))
	e := echo.New()
	h := admin.New(baseConfig(), cfgPath, quietLogger())
	served := []config.ServiceConfig{{Name: "billing", BasePath: "/api/billing", Targets: []string{"http://billing:8080"}}}
	h.SetStoredServices(mongodb.NewServiceAdapter(repo, quietLogger()), served)
	h.Register(e)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", basic("root", "s3cret-pass"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"billing","basePath":"/api/billing","targets":["http://billing:8080"],"timeout":"5s"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "after a restart")
	assert.Equal(t, http.StatusConflict, call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"billing","basePath":"/x","targets":["http://b"]}`).Code)

	// The admin's own validation applies.
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"../x","basePath":"/x","targets":["http://b"]}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"x","basePath":"x","targets":["http://b"]}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"x","basePath":"/x","targets":["ftp://b"]}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPost, "/admin/api/mongodb/services", `{"name":"x","basePath":"/x","targets":["http://b"],"timeout":"soon"}`).Code)

	rec = call(http.MethodGet, "/admin/api/mongodb/services", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Services []struct {
			Name    string `json:"name"`
			Timeout string `json:"timeout"`
			Served  bool   `json:"served"`
		} `json:"services"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Services, 1)
	assert.Equal(t, "5s", list.Services[0].Timeout)
	assert.True(t, list.Services[0].Served, "the gateway loaded it at startup")

	assert.Equal(t, http.StatusOK, call(http.MethodPut, "/admin/api/mongodb/services/billing", `{"basePath":"/api/billing","targets":["http://billing-2:8080"]}`).Code)
	assert.Equal(t, []string{"http://billing-2:8080"}, repo.docs["billing"].Targets)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPut, "/admin/api/mongodb/services/billing", `{"name":"other","basePath":"/x","targets":["http://b"]}`).Code)

	assert.Equal(t, http.StatusOK, call(http.MethodDelete, "/admin/api/mongodb/services/billing", "").Code)
	assert.Equal(t, http.StatusNotFound, call(http.MethodDelete, "/admin/api/mongodb/services/billing", "").Code)

	// Stored services show on the dashboard, marked, without the config file's edit buttons.
	rec = call(http.MethodGet, "/admin/dashboard", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<code>/api/billing</code>")
	assert.Contains(t, rec.Body.String(), ">MongoDB</span>")
	assert.NotContains(t, rec.Body.String(), `href="/admin/services/billing"`)
}
