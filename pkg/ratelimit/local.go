package ratelimit

import (
	"math"
	"sync"
	"time"
)

// localStore keeps rate-limit state in memory, for a single gateway instance. (Without Redis
// the limiter used to allow every request.) Idle keys are swept as the map grows, so memory is
// bounded by the number of clients active within one window.
type localStore struct {
	mu      sync.Mutex
	windows map[string]*windowState
	buckets map[string]*bucketState
	ops     int
}

type windowState struct {
	start    time.Time // start of the current fixed window
	count    int
	previous int // count of the previous window, for the sliding estimate
	window   time.Duration
}

type bucketState struct {
	tokens float64
	last   time.Time
	window time.Duration
}

func newLocalStore() *localStore {
	return &localStore{
		windows: make(map[string]*windowState),
		buckets: make(map[string]*bucketState),
	}
}

// fixedWindow counts requests in consecutive windows of length window.
func (s *localStore) fixedWindow(key string, window time.Duration, now time.Time) (count int, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	start := now.Truncate(window)
	w, ok := s.windows[key]
	if !ok || !w.start.Equal(start) {
		prev := 0
		if ok && w.start.Add(window).Equal(start) {
			prev = w.count
		}
		w = &windowState{start: start, previous: prev, window: window}
		s.windows[key] = w
	}
	w.count++
	return w.count, start.Add(window)
}

// slidingWindow estimates the count over the last window by weighting the previous fixed
// window by how much of it still overlaps (the usual sliding-window-counter approximation).
func (s *localStore) slidingWindow(key string, limit int, window time.Duration, now time.Time) (estimate int, allowed bool, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	start := now.Truncate(window)
	w, ok := s.windows[key]
	if !ok || !w.start.Equal(start) {
		prev := 0
		if ok && w.start.Add(window).Equal(start) {
			prev = w.count
		}
		w = &windowState{start: start, previous: prev, window: window}
		s.windows[key] = w
	}
	elapsed := float64(now.Sub(start)) / float64(window)
	weighted := float64(w.previous)*(1-elapsed) + float64(w.count)
	if weighted+1 > float64(limit) {
		// Rejected requests do not count, or a client over its limit would never recover.
		return int(math.Ceil(weighted)), false, start.Add(window)
	}
	w.count++
	return int(math.Ceil(weighted + 1)), true, start.Add(window)
}

// tokenBucket refills limit tokens per window, up to burst, and takes one per request.
func (s *localStore) tokenBucket(key string, limit, burst int, window time.Duration, now time.Time) (remaining int, allowed bool, reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	rate := float64(limit) / float64(window) // tokens per nanosecond
	b, ok := s.buckets[key]
	if !ok {
		b = &bucketState{tokens: float64(burst), last: now, window: window}
		s.buckets[key] = b
	}
	b.tokens = math.Min(float64(burst), b.tokens+float64(now.Sub(b.last))*rate)
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / rate)
		return 0, false, now.Add(wait)
	}
	b.tokens--
	return int(b.tokens), true, now.Add(time.Duration((float64(burst) - b.tokens) / rate))
}

// sweepLocked drops idle entries every so often.
func (s *localStore) sweepLocked(now time.Time) {
	s.ops++
	if s.ops < 1024 && len(s.windows)+len(s.buckets) < 10000 {
		return
	}
	s.ops = 0
	for k, w := range s.windows {
		if now.Sub(w.start) > 2*w.window {
			delete(s.windows, k)
		}
	}
	for k, b := range s.buckets {
		if now.Sub(b.last) > b.window {
			delete(s.buckets, k)
		}
	}
}
