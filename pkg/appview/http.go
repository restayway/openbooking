package appview

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/providers/", func(w http.ResponseWriter, r *http.Request) {
		parts := splitPath(r.URL.Path)
		if len(parts) < 4 || parts[0] != "v1" || parts[1] != "providers" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		providerDID := parts[2]
		switch {
		case len(parts) == 4 && parts[3] == "listings":
			listings, err := service.ListListings(r.Context(), providerDID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, listings)
		case len(parts) == 5 && parts[3] == "offers":
			offer, err := service.Offer(r.Context(), providerDID, parts[4])
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if offer == nil {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeJSON(w, http.StatusOK, offer)
		case len(parts) == 4 && parts[3] == "availability":
			availability, err := service.Availability(r.Context(), providerDID, r.URL.Query().Get("listing_uri"), r.URL.Query().Get("rate_plan_uri"))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if availability == nil {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeJSON(w, http.StatusOK, availability)
		case len(parts) == 5 && parts[3] == "reservations":
			reservation, err := service.Reservation(r.Context(), providerDID, parts[4])
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if reservation == nil {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			writeJSON(w, http.StatusOK, reservation)
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	})
	return mux
}

func splitPath(path string) []string {
	var out []string
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
