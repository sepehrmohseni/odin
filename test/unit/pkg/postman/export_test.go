package postman_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"odin/pkg/config"
	"odin/pkg/integrations/postman"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memRepo is an in-memory SyncRepository.
type memRepo struct {
	mu      sync.Mutex
	records map[string]*postman.SyncRecord
}

func (m *memRepo) SaveSyncRecord(_ context.Context, r *postman.SyncRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string]*postman.SyncRecord{}
	}
	m.records[r.ID] = r
	return nil
}
func (m *memRepo) GetSyncRecord(context.Context, string) (*postman.SyncRecord, error) {
	return nil, nil
}
func (m *memRepo) GetSyncHistory(context.Context, string, int) ([]*postman.SyncRecord, error) {
	return nil, nil
}
func (m *memRepo) GetLastSync(context.Context, string, postman.SyncDirection) (*postman.SyncRecord, error) {
	return nil, nil
}
func (m *memRepo) UpdateSyncStatus(context.Context, string, postman.SyncStatus, error) error {
	return nil
}

// fakePostman records the write requests the client makes.
type fakePostman struct {
	mu     sync.Mutex
	writes []string // "PUT /collections/col-1 <body>"
}

func (f *fakePostman) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.writes = append(f.writes, r.Method+" "+r.URL.Path+" "+string(body))
	f.mu.Unlock()
	_, _ = io.WriteString(w, `{"collection":{"id":"col-1","uid":"col-1","name":"users"}}`)
}

func newEngine(t *testing.T, services ...config.ServiceConfig) (*postman.SyncEngine, *fakePostman) {
	t.Helper()
	fake := &fakePostman{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	client := postman.NewClient("test-key", logger)
	client.SetBaseURL(srv.URL)

	engine := postman.NewSyncEngine(client, &postman.IntegrationConfig{}, &memRepo{}, logger)
	engine.SetServiceLookup(func(name string) (*config.ServiceConfig, bool) {
		for i := range services {
			if services[i].Name == name {
				return &services[i], true
			}
		}
		return nil, false
	})
	return engine, fake
}

func TestExportNeverOverwritesWithPlaceholder(t *testing.T) {
	engine, fake := newEngine(t)

	err := engine.SyncCollection(context.Background(), "col-1", "not-a-service", postman.SyncDirectionExport)
	assert.ErrorContains(t, err, "not configured")
	assert.Empty(t, fake.writes, "the collection must not be overwritten (it used to get a placeholder service)")
}

func TestExportSendsTheRealService(t *testing.T) {
	engine, fake := newEngine(t, config.ServiceConfig{
		Name:     "users",
		BasePath: "/api/users",
		Targets:  []string{"http://users.internal:8081"},
	})

	_ = engine.SyncCollection(context.Background(), "col-1", "users", postman.SyncDirectionExport)
	require.Len(t, fake.writes, 1)
	assert.True(t, strings.HasPrefix(fake.writes[0], "PUT /collections/col-1 "), fake.writes[0])
	assert.Contains(t, fake.writes[0], "users.internal:8081", "the export carries the configured target")
}
