package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gwerrors "odin/pkg/errors"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Middleware counts every request that was routed to a service. It belongs before the
// service routes, so it sees the service name the router sets, and the status a returned
// error will become.
func Middleware(a *Analyzer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			service, _ := c.Get("service_name").(string)
			if service == "" {
				return err
			}
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				status = gwerrors.StatusCodeFromError(err)
			}
			a.Record(service, status, time.Since(start), c.RealIP())
			return err
		}
	}
}

// Webhook posts each new anomaly as JSON. The body has a "text" line (which Slack-compatible
// webhooks display) and the anomaly itself.
type Webhook struct {
	URL         string
	MinSeverity string // default medium
	Client      *http.Client
}

// Notify sends a if it is at least MinSeverity.
func (w *Webhook) Notify(ctx context.Context, a *Anomaly) error {
	minSev := w.MinSeverity
	if minSev == "" {
		minSev = SeverityMedium
	}
	if rank(a.Severity) < rank(minSev) {
		return nil
	}
	body, err := json.Marshal(map[string]interface{}{
		"text":    fmt.Sprintf("[%s] %s: %s", strings.ToUpper(a.Severity), a.Service, a.Description),
		"anomaly": a,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %d", resp.StatusCode)
	}
	return nil
}

// AnalysisService asks an analysis service (such as deployments/grok) to explain an anomaly:
// POST {URL}/analyze with a prompt and the figures (no client addresses), answered with
// {"response": "...", "suggestions": ["..."]}.
type AnalysisService struct {
	URL    string
	Client *http.Client
}

// Explain implements Explainer.
func (s *AnalysisService) Explain(ctx context.Context, a *Anomaly, b *Baseline) (string, []string, error) {
	payload := map[string]interface{}{
		"prompt": fmt.Sprintf("An API gateway detected a %s on the service %q: %s. Explain likely causes and what to check.",
			strings.ReplaceAll(a.Type, "_", " "), a.Service, a.Description),
		"max_tokens": 400,
		"context": map[string]interface{}{
			"service":  a.Service,
			"type":     a.Type,
			"severity": a.Severity,
			"observed": a.Observed,
			"expected": a.Expected,
			"z_score":  a.ZScore,
			"baseline": map[string]interface{}{
				"request_rate":     b.RequestRate.Mean,
				"server_error_pct": b.ServerErrorPct.Mean,
				"latency_ms":       b.LatencyMs.Mean,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+"/analyze", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("analysis service answered %d", resp.StatusCode)
	}
	var out struct {
		Response    string   `json:"response"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("analysis service: %w", err)
	}
	if out.Response == "" && len(out.Suggestions) == 0 {
		return "", nil, errors.New("analysis service returned nothing")
	}
	return out.Response, out.Suggestions, nil
}

// MongoStore keeps anomalies in the ai_anomalies collection, removed after the retention period.
type MongoStore struct {
	coll *mongo.Collection
}

// NewMongoStore prepares the collection and its expiry index.
func NewMongoStore(ctx context.Context, db *mongo.Database, retention time.Duration) (*MongoStore, error) {
	coll := db.Collection("ai_anomalies")
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "last_seen", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(retention.Seconds()))}, //nolint:gosec // days fit in int32
		{Keys: bson.D{{Key: "service", Value: 1}, {Key: "first_seen", Value: -1}}},
	})
	if err != nil {
		return nil, fmt.Errorf("ai_anomalies indexes: %w", err)
	}
	return &MongoStore{coll: coll}, nil
}

// SaveAnomaly inserts or replaces an anomaly.
func (m *MongoStore) SaveAnomaly(ctx context.Context, a *Anomaly) error {
	_, err := m.coll.ReplaceOne(ctx, bson.M{"_id": a.ID}, a, options.Replace().SetUpsert(true))
	return err
}

// Recent returns the newest anomalies, to show them again after a restart.
func (m *MongoStore) Recent(ctx context.Context, limit int64) ([]*Anomaly, error) {
	cur, err := m.coll.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "first_seen", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var out []*Anomaly
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Restore adds anomalies from before a restart (newest first, as Recent returns them). They
// are history: none is treated as ongoing.
func (a *Analyzer) Restore(list []*Anomaly) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		a.anomalies = append(a.anomalies, list[i])
	}
	if len(a.anomalies) > a.opts.MaxStored {
		a.anomalies = a.anomalies[len(a.anomalies)-a.opts.MaxStored:]
	}
}
