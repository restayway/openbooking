package runtimex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Component struct {
	Healthy bool      `json:"healthy"`
	Detail  string    `json:"detail,omitempty"`
	Updated time.Time `json:"updatedAt"`
}

type Snapshot struct {
	Components map[string]Component `json:"components"`
	Counters   map[string]uint64    `json:"counters"`
	Gauges     map[string]int64     `json:"gauges"`
}

type Registry struct {
	mu         sync.RWMutex
	components map[string]Component
	counters   sync.Map
	gauges     sync.Map
}

func NewRegistry() *Registry {
	return &Registry{components: map[string]Component{}}
}

func (r *Registry) SetComponent(name string, healthy bool, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.components[name] = Component{
		Healthy: healthy,
		Detail:  detail,
		Updated: time.Now().UTC(),
	}
}

func (r *Registry) AddCounter(name string, delta uint64) {
	r.counter(name).Add(delta)
}

func (r *Registry) SetGauge(name string, value int64) {
	r.gauge(name).Store(value)
}

func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	components := make(map[string]Component, len(r.components))
	for key, value := range r.components {
		components[key] = value
	}
	r.mu.RUnlock()

	counters := map[string]uint64{}
	r.counters.Range(func(key, value any) bool {
		counters[key.(string)] = value.(*atomic.Uint64).Load()
		return true
	})
	gauges := map[string]int64{}
	r.gauges.Range(func(key, value any) bool {
		gauges[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	return Snapshot{Components: components, Counters: counters, Gauges: gauges}
}

func (r *Registry) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, r.Snapshot())
	}
}

func (r *Registry) ReadyHandler(ready func(Snapshot) (bool, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snapshot := r.Snapshot()
		ok, payload := ready(snapshot)
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, payload)
	}
}

func (r *Registry) MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		snapshot := r.Snapshot()
		for name, value := range snapshot.Counters {
			fmt.Fprintf(w, "# TYPE %s counter\n%s %d\n", name, name, value)
		}
		for name, value := range snapshot.Gauges {
			fmt.Fprintf(w, "# TYPE %s gauge\n%s %d\n", name, name, value)
		}
		for name, component := range snapshot.Components {
			healthy := 0
			if component.Healthy {
				healthy = 1
			}
			metric := sanitizeMetric(name)
			fmt.Fprintf(w, "# TYPE component_%s gauge\ncomponent_%s %d\n", metric, metric, healthy)
		}
	}
}

type RetryConfig struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func RunRetriable(ctx context.Context, logger *slog.Logger, registry *Registry, component string, cfg RetryConfig, fn func(context.Context) error) {
	backoff := cfg.InitialBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	maxBackoff := cfg.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		registry.SetComponent(component, true, "")
		err := fn(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		registry.SetComponent(component, false, err.Error())
		registry.AddCounter(component+"_restarts_total", 1)
		if logger != nil {
			logger.Warn("worker failed; retrying", "component", component, "error", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sanitizeMetric(name string) string {
	out := make([]rune, 0, len(name))
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			out = append(out, ch)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func (r *Registry) counter(name string) *atomic.Uint64 {
	if value, ok := r.counters.Load(name); ok {
		return value.(*atomic.Uint64)
	}
	ptr := &atomic.Uint64{}
	actual, _ := r.counters.LoadOrStore(name, ptr)
	return actual.(*atomic.Uint64)
}

func (r *Registry) gauge(name string) *atomic.Int64 {
	if value, ok := r.gauges.Load(name); ok {
		return value.(*atomic.Int64)
	}
	ptr := &atomic.Int64{}
	actual, _ := r.gauges.LoadOrStore(name, ptr)
	return actual.(*atomic.Int64)
}
