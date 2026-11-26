// Package ai detects traffic anomalies per service: it learns each service's normal request
// rate, server error share and latency, and reports windows that fall far outside them.
//
// Every request to a service is counted in the current window (Middleware). At the end of each
// window (Options.Interval) the analyzer compares each service's window with its baseline, an
// exponentially weighted mean and variance of earlier windows, and records an anomaly when a
// metric is more than Threshold standard deviations away. A deviation that continues is one
// ongoing anomaly, not one per window. Anomalies can be sent to a webhook and, when an analysis
// service is configured, get a written explanation.
package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Anomaly types.
const (
	TypeTrafficSpike   = "traffic_spike"
	TypeTrafficDrop    = "traffic_drop"
	TypeErrorSpike     = "error_spike"
	TypeLatencySpike   = "latency_spike"
	TypeClientDominant = "dominant_client"
)

// Severity levels, lowest first.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Options configure an Analyzer. Zero values get defaults.
type Options struct {
	Interval   time.Duration // window length (default 1m)
	Window     time.Duration // how far back the baseline reaches, roughly (default 1h)
	Threshold  float64       // z-score that counts as anomalous (default 3)
	MinSamples int           // windows needed before a baseline is used (default 10)
	Tags       map[string]string
	MaxStored  int // anomalies kept in memory (default 500)
}

// Anomaly is a deviation from a service's baseline.
type Anomaly struct {
	ID            string            `json:"id" bson:"_id"`
	Service       string            `json:"service" bson:"service"`
	Type          string            `json:"type" bson:"type"`
	Severity      string            `json:"severity" bson:"severity"`
	Score         float64           `json:"score" bson:"score"` // 0-100
	Description   string            `json:"description" bson:"description"`
	Observed      float64           `json:"observed" bson:"observed"`
	Expected      float64           `json:"expected" bson:"expected"`
	ZScore        float64           `json:"zScore" bson:"z_score"`
	FirstSeen     time.Time         `json:"firstSeen" bson:"first_seen"`
	LastSeen      time.Time         `json:"lastSeen" bson:"last_seen"`
	Windows       int               `json:"windows" bson:"windows"` // consecutive windows it lasted
	Resolved      bool              `json:"resolved" bson:"resolved"`
	FalsePositive bool              `json:"falsePositive" bson:"false_positive"`
	Explanation   string            `json:"explanation,omitempty" bson:"explanation,omitempty"`
	Suggestions   []string          `json:"suggestions,omitempty" bson:"suggestions,omitempty"`
	Tags          map[string]string `json:"tags,omitempty" bson:"tags,omitempty"`
}

// Baseline is what the analyzer has learned about a service.
type Baseline struct {
	Service        string    `json:"service"`
	Samples        int       `json:"samples"` // windows seen
	Ready          bool      `json:"ready"`   // enough samples to detect anomalies
	RequestRate    Stat      `json:"requestRate"`
	ServerErrorPct Stat      `json:"serverErrorPct"`
	LatencyMs      Stat      `json:"latencyMs"`
	Updated        time.Time `json:"updated"`
}

// Stat is an exponentially weighted mean and standard deviation.
type Stat struct {
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stdDev"`
	v      float64
}

func (s *Stat) add(x, alpha float64, first bool) {
	if first {
		s.Mean, s.v = x, 0
	} else {
		d := x - s.Mean
		s.Mean += alpha * d
		s.v = (1 - alpha) * (s.v + alpha*d*d)
	}
	s.StdDev = math.Sqrt(s.v)
}

type window struct {
	requests, clientErrors, serverErrors int64
	latencySumMs                         float64
	clients                              map[string]int64
}

// Store persists anomalies; nil keeps them in memory only.
type Store interface {
	SaveAnomaly(ctx context.Context, a *Anomaly) error
}

// Notifier is told about new anomalies and escalations (see Webhook).
type Notifier interface {
	Notify(ctx context.Context, a *Anomaly) error
}

// Explainer writes an explanation for an anomaly (see AnalysisService).
type Explainer interface {
	Explain(ctx context.Context, a *Anomaly, b *Baseline) (explanation string, suggestions []string, err error)
}

// Analyzer collects traffic and detects anomalies.
type Analyzer struct {
	opts      Options
	alpha     float64
	logger    *logrus.Logger
	store     Store
	notifier  Notifier
	explainer Explainer
	now       func() time.Time

	mu        sync.Mutex
	current   map[string]*window
	baselines map[string]*Baseline
	anomalies []*Anomaly          // newest last
	ongoing   map[string]*Anomaly // service/type -> unresolved anomaly
	stop      chan struct{}
	done      chan struct{}
	saves     chan Anomaly // to the store, in order (see persistLocked)
}

// NewAnalyzer creates an analyzer; Start begins the windows.
func NewAnalyzer(opts Options, logger *logrus.Logger) *Analyzer {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Window <= 0 {
		opts.Window = time.Hour
	}
	if opts.Threshold <= 0 {
		opts.Threshold = 3
	}
	if opts.MinSamples <= 0 {
		opts.MinSamples = 10
	}
	if opts.MaxStored <= 0 {
		opts.MaxStored = 500
	}
	alpha := float64(opts.Interval) / float64(opts.Window)
	alpha = math.Max(0.01, math.Min(0.5, alpha))
	return &Analyzer{
		opts:      opts,
		alpha:     alpha,
		logger:    logger,
		now:       time.Now,
		current:   map[string]*window{},
		baselines: map[string]*Baseline{},
		ongoing:   map[string]*Anomaly{},
	}
}

// SetStore persists anomalies (MongoDB); SetNotifier sends them (webhook); SetExplainer has them
// explained (analysis service). Call before Start.
func (a *Analyzer) SetStore(s Store) {
	a.store = s
	a.saves = make(chan Anomaly, 1024)
	go a.saveLoop()
}
func (a *Analyzer) SetNotifier(n Notifier)   { a.notifier = n }
func (a *Analyzer) SetExplainer(e Explainer) { a.explainer = e }

// Record counts one request to service.
func (a *Analyzer) Record(service string, status int, latency time.Duration, client string) {
	if service == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.current[service]
	if w == nil {
		w = &window{clients: map[string]int64{}}
		a.current[service] = w
	}
	w.requests++
	w.latencySumMs += float64(latency.Microseconds()) / 1000
	switch {
	case status >= 500:
		w.serverErrors++
	case status >= 400:
		w.clientErrors++
	}
	if _, ok := w.clients[client]; ok || len(w.clients) < 5000 {
		w.clients[client]++
	}
}

// Start closes a window every Interval until Stop.
func (a *Analyzer) Start() {
	a.stop, a.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(a.done)
		t := time.NewTicker(a.opts.Interval)
		defer t.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-t.C:
				a.Analyze()
			}
		}
	}()
}

// Stop ends the windows.
func (a *Analyzer) Stop() {
	if a.stop != nil {
		close(a.stop)
		<-a.done
	}
}

// Close is Stop, for the gateway's shutdown list.
func (a *Analyzer) Close() error {
	a.Stop()
	return nil
}

// Analyze closes the current window: each service is compared with its baseline, then added
// to it. It is called every Interval; tests call it directly.
func (a *Analyzer) Analyze() []*Anomaly {
	a.mu.Lock()
	windows := a.current
	a.current = map[string]*window{}
	seconds := a.opts.Interval.Seconds()
	now := a.now()

	// A known service with no requests at all had a window of zero.
	for name := range a.baselines {
		if _, ok := windows[name]; !ok {
			windows[name] = &window{clients: map[string]int64{}}
		}
	}

	var found []*Anomaly
	names := make([]string, 0, len(windows))
	for name := range windows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := windows[name]
		b := a.baselines[name]
		if b == nil {
			b = &Baseline{Service: name}
			a.baselines[name] = b
		}
		rate := float64(w.requests) / seconds
		errPct, latency := 0.0, 0.0
		if w.requests > 0 {
			errPct = 100 * float64(w.serverErrors) / float64(w.requests)
			latency = w.latencySumMs / float64(w.requests)
		}

		var flagged []*Anomaly
		if b.Ready {
			flagged = a.detect(name, w, b, rate, errPct, latency, seconds)
		}
		if d := dominantClient(w); d != nil {
			d.Service = name
			flagged = append(flagged, d)
		}
		active := map[string]bool{}
		for _, an := range flagged {
			active[an.Type] = true
			found = append(found, a.track(an, now))
		}
		// A deviation that is over resolves its ongoing anomaly (and the stored copy).
		for key, an := range a.ongoing {
			if an.Service == name && !active[an.Type] {
				an.Resolved = true
				delete(a.ongoing, key)
				a.persistLocked(an)
			}
		}

		// Learn from the window, except metrics that were anomalous (so an outage does not
		// become the new normal).
		first := b.Samples == 0
		if !active[TypeTrafficSpike] && !active[TypeTrafficDrop] {
			b.RequestRate.add(rate, a.alpha, first)
		}
		if w.requests > 0 && !active[TypeErrorSpike] {
			b.ServerErrorPct.add(errPct, a.alpha, first)
		}
		if w.requests > 0 && !active[TypeLatencySpike] {
			b.LatencyMs.add(latency, a.alpha, first)
		}
		b.Samples++
		b.Ready = b.Samples >= a.opts.MinSamples
		b.Updated = now
	}
	a.mu.Unlock()

	for _, an := range found {
		if an.Windows == 1 {
			a.publish(an)
		}
	}
	return found
}

// detect compares one window with the baseline. Standard deviations have floors, so a perfectly
// steady service (deviation 0) does not turn every small change into an infinite z-score, and
// minimum counts keep a handful of requests from raising alarms.
func (a *Analyzer) detect(name string, w *window, b *Baseline, rate, errPct, latency, seconds float64) []*Anomaly {
	var out []*Anomaly
	thr := a.opts.Threshold

	rs := math.Max(b.RequestRate.StdDev, math.Max(0.1*b.RequestRate.Mean, 1/seconds))
	if z := (rate - b.RequestRate.Mean) / rs; z >= thr && w.requests >= 20 {
		out = append(out, a.anomaly(name, TypeTrafficSpike, z, rate, b.RequestRate.Mean,
			fmt.Sprintf("Traffic at %.1f requests/s, usually %.1f", rate, b.RequestRate.Mean)))
	} else if z <= -thr && b.RequestRate.Mean*seconds >= 20 {
		out = append(out, a.anomaly(name, TypeTrafficDrop, z, rate, b.RequestRate.Mean,
			fmt.Sprintf("Traffic fell to %.1f requests/s, usually %.1f", rate, b.RequestRate.Mean)))
	}

	es := math.Max(b.ServerErrorPct.StdDev, 2)
	if z := (errPct - b.ServerErrorPct.Mean) / es; z >= thr && w.serverErrors >= 5 {
		an := a.anomaly(name, TypeErrorSpike, z, errPct, b.ServerErrorPct.Mean,
			fmt.Sprintf("%.1f%% of responses were server errors, usually %.1f%%", errPct, b.ServerErrorPct.Mean))
		if errPct >= 50 {
			an.Severity = SeverityCritical
		}
		out = append(out, an)
	}

	ls := math.Max(b.LatencyMs.StdDev, math.Max(0.2*b.LatencyMs.Mean, 5))
	if z := (latency - b.LatencyMs.Mean) / ls; z >= thr && w.requests >= 10 {
		out = append(out, a.anomaly(name, TypeLatencySpike, z, latency, b.LatencyMs.Mean,
			fmt.Sprintf("Average latency %.0f ms, usually %.0f ms", latency, b.LatencyMs.Mean)))
	}
	return out
}

// dominantClient flags one client sending most of a busy service's traffic while others are
// active too (a single client alone, as in a test, is not unusual).
func dominantClient(w *window) *Anomaly {
	if w.requests < 100 || len(w.clients) < 3 {
		return nil
	}
	var top string
	var n int64
	for c, k := range w.clients {
		if k > n || (k == n && c < top) {
			top, n = c, k
		}
	}
	share := float64(n) / float64(w.requests)
	if share < 0.5 {
		return nil
	}
	sev := SeverityMedium
	if share >= 0.8 {
		sev = SeverityHigh
	}
	return &Anomaly{
		Type:        TypeClientDominant,
		Severity:    sev,
		Score:       math.Min(100, share*100),
		Observed:    share * 100,
		Description: fmt.Sprintf("One client (%s) sent %.0f%% of %d requests", top, share*100, w.requests),
	}
}

func (a *Analyzer) anomaly(service, typ string, z, observed, expected float64, desc string) *Anomaly {
	thr := a.opts.Threshold
	sev := SeverityMedium
	switch abs := math.Abs(z); {
	case abs >= 3*thr:
		sev = SeverityCritical
	case abs >= 2*thr:
		sev = SeverityHigh
	}
	return &Anomaly{
		Service:     service,
		Type:        typ,
		Severity:    sev,
		Score:       math.Min(100, math.Abs(z)/thr*33),
		ZScore:      round(z, 2),
		Observed:    round(observed, 3),
		Expected:    round(expected, 3),
		Description: desc,
	}
}

// track adds an anomaly, or extends the ongoing one of the same service and type. Callers hold
// a.mu.
func (a *Analyzer) track(an *Anomaly, now time.Time) *Anomaly {
	if an.Service == "" {
		return an
	}
	key := an.Service + "/" + an.Type
	if cur, ok := a.ongoing[key]; ok {
		cur.LastSeen = now
		cur.Windows++
		cur.Observed, cur.ZScore, cur.Description = an.Observed, an.ZScore, an.Description
		if rank(an.Severity) > rank(cur.Severity) {
			cur.Severity = an.Severity
		}
		cur.Score = math.Max(cur.Score, an.Score)
		return cur
	}
	an.ID = newID()
	an.FirstSeen, an.LastSeen, an.Windows = now, now, 1
	if len(a.opts.Tags) > 0 {
		an.Tags = a.opts.Tags
	}
	a.ongoing[key] = an
	a.anomalies = append(a.anomalies, an)
	if len(a.anomalies) > a.opts.MaxStored {
		a.anomalies = a.anomalies[len(a.anomalies)-a.opts.MaxStored:]
	}
	return an
}

// publish stores, explains and sends a new anomaly, in the background. It works on copies:
// the shared anomaly changes under a.mu while later windows extend it.
func (a *Analyzer) publish(an *Anomaly) {
	a.mu.Lock()
	snapshot := *an
	var b Baseline
	if bl := a.baselines[an.Service]; bl != nil {
		b = *bl
	}
	a.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if a.explainer != nil && rank(snapshot.Severity) >= rank(SeverityMedium) {
			if text, sugg, err := a.explainer.Explain(ctx, &snapshot, &b); err != nil {
				a.logger.WithError(err).Warn("Anomaly explanation failed")
			} else {
				snapshot.Explanation, snapshot.Suggestions = text, sugg
				a.mu.Lock()
				an.Explanation, an.Suggestions = text, sugg
				a.mu.Unlock()
			}
		}
		// The state now, not at detection: the anomaly may have been extended or resolved
		// while the explanation was being written.
		a.mu.Lock()
		a.persistLocked(an)
		a.mu.Unlock()
		if a.notifier != nil {
			if err := a.notifier.Notify(ctx, &snapshot); err != nil {
				a.logger.WithError(err).Warn("Failed to send anomaly alert")
			}
		}
		a.logger.WithFields(logrus.Fields{"service": snapshot.Service, "type": snapshot.Type, "severity": snapshot.Severity}).
			Warn("Traffic anomaly: " + snapshot.Description)
	}()
}

// Anomalies returns stored anomalies, newest first, that match the filter.
func (a *Analyzer) Anomalies(f Filter) []Anomaly {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Anomaly
	for i := len(a.anomalies) - 1; i >= 0; i-- {
		an := a.anomalies[i]
		if f.Service != "" && an.Service != f.Service || f.Type != "" && an.Type != f.Type ||
			f.Severity != "" && rank(an.Severity) < rank(f.Severity) ||
			f.Open && (an.Resolved || an.FalsePositive) {
			continue
		}
		out = append(out, *an)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

// Filter selects anomalies; zero fields match everything.
type Filter struct {
	Service, Type string
	Severity      string // this level or higher
	Open          bool   // neither resolved nor a false positive
	Limit         int
}

// Mark sets an anomaly resolved or a false positive. It reports whether the ID exists.
func (a *Analyzer) Mark(id string, falsePositive bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, an := range a.anomalies {
		if an.ID != id {
			continue
		}
		an.Resolved = true
		an.FalsePositive = an.FalsePositive || falsePositive
		for key, cur := range a.ongoing {
			if cur == an {
				delete(a.ongoing, key)
			}
		}
		a.persistLocked(an)
		return true
	}
	return false
}

// Baselines returns what has been learned, by service name.
func (a *Analyzer) Baselines() []Baseline {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Baseline, 0, len(a.baselines))
	for _, b := range a.baselines {
		cp := *b
		cp.RequestRate.Mean, cp.RequestRate.StdDev = round(cp.RequestRate.Mean, 3), round(cp.RequestRate.StdDev, 3)
		cp.ServerErrorPct.Mean, cp.ServerErrorPct.StdDev = round(cp.ServerErrorPct.Mean, 3), round(cp.ServerErrorPct.StdDev, 3)
		cp.LatencyMs.Mean, cp.LatencyMs.StdDev = round(cp.LatencyMs.Mean, 3), round(cp.LatencyMs.StdDev, 3)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// Settings returns the effective options.
func (a *Analyzer) Settings() Options { return a.opts }

func rank(sev string) int {
	switch sev {
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return 0
}

func round(x float64, places int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// persistLocked queues the anomaly's current state for the store; callers hold a.mu. One
// goroutine saves the queue in order, so the last save of an anomaly is its latest state. (The
// first save used to run in its own goroutine, after an explanation that can take seconds, and
// could overwrite a resolution saved in the meantime.)
func (a *Analyzer) persistLocked(an *Anomaly) {
	if a.saves == nil {
		return
	}
	select {
	case a.saves <- *an:
	default:
		a.logger.Warn("Anomaly store is behind; dropping an update")
	}
}

func (a *Analyzer) saveLoop() {
	for an := range a.saves {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := a.store.SaveAnomaly(ctx, &an); err != nil {
			a.logger.WithError(err).Warn("Failed to save anomaly")
		}
		cancel()
	}
}
