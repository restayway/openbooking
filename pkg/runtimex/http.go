package runtimex

import "net/http"

func ComposeHandler(base http.Handler, registry *Registry, ready func(Snapshot) (bool, map[string]any)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", registry.HealthHandler())
	mux.Handle("/readyz", registry.ReadyHandler(ready))
	mux.Handle("/metrics", registry.MetricsHandler())
	mux.Handle("/", base)
	return mux
}
