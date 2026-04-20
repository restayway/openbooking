package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func NewHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := service.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		after, err := parseUint(r, "after_sequence")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		limit, err := parseInt(r, "limit", 100)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		events, err := service.ListEvents(r.Context(), after, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, events)
	})
	mux.HandleFunc("GET /v1/actors/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/head") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		actorDID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/actors/"), "/head")
		head, err := service.ActorHead(r.Context(), actorDID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if head == nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, head)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func parseUint(r *http.Request, key string) (uint64, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseInt(r *http.Request, key string, fallback int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

var _ = errors.New
