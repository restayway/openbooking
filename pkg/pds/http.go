package pds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restayway/openbooking/pkg/auth"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/privdata"
)

func NewHandler(service *Service) http.Handler {
	return NewHandlerWithAuth(service, nil)
}

// NewHandlerWithAuth returns the PDS handler optionally wrapped with signature verification.
func NewHandlerWithAuth(service *Service, verifier *auth.Verifier) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"actorDid": service.Config().ActorDID,
			"role":     service.Config().Role,
		})
	})

	mux.HandleFunc("GET /v1/repo/head", func(w http.ResponseWriter, r *http.Request) {
		head := service.Head()
		if head == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"actorDid": service.Config().ActorDID,
				"head":     nil,
			})
			return
		}
		writeJSON(w, http.StatusOK, head)
	})

	mux.HandleFunc("GET /v1/records/", func(w http.ResponseWriter, r *http.Request) {
		nsid, rkey, ok := splitRecordPath(strings.TrimPrefix(r.URL.Path, "/v1/records/"))
		if !ok {
			writeError(w, http.StatusBadRequest, "expected /v1/records/{nsid}/{rkey}")
			return
		}
		record, err := service.GetRecord(r.Context(), nsid, rkey)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, record)
	})

	mux.HandleFunc("GET /v1/commits", func(w http.ResponseWriter, r *http.Request) {
		afterRevision, err := parseUintQuery(r, "after_revision")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		limit, err := parseIntQuery(r, "limit", 100)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		commits, err := service.ListCommits(r.Context(), afterRevision, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, commits)
	})

	mux.HandleFunc("POST /v1/records", func(w http.ResponseWriter, r *http.Request) {
		var input RecordWriteInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		commit, record, err := service.PutRecord(r.Context(), input, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"commit": commit,
			"record": record,
		})
	})

	mux.HandleFunc("DELETE /v1/records/", func(w http.ResponseWriter, r *http.Request) {
		nsid, rkey, ok := splitRecordPath(strings.TrimPrefix(r.URL.Path, "/v1/records/"))
		if !ok {
			writeError(w, http.StatusBadRequest, "expected /v1/records/{nsid}/{rkey}")
			return
		}
		commit, err := service.DeleteRecord(r.Context(), nsid, rkey, time.Now().UTC())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, commit)
	})

	mux.HandleFunc("POST /v1/intents", func(w http.ResponseWriter, r *http.Request) {
		var intent lexicon.ReservationIntent
		if err := decodeJSON(r, &intent); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := service.ProcessIntent(r.Context(), intent, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		status := http.StatusCreated
		if result.Duplicate {
			status = http.StatusOK
		}
		writeJSON(w, status, result)
	})

	mux.HandleFunc("POST /v1/cancels", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var cancel lexicon.ReservationCancel
		if err := decodeJSON(r, &cancel); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := service.ProcessCancel(r.Context(), requesterDID, cancel, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		status := http.StatusCreated
		if result.Duplicate {
			status = http.StatusOK
		}
		writeJSON(w, status, result)
	})

	mux.HandleFunc("GET /v1/private/profile", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		bundle, err := service.GetPrivateProfile(r.Context(), requesterDID)
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})

	mux.HandleFunc("PUT /v1/private/profile", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var input PrivateProfileInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		bundle, err := service.PutPrivateProfile(r.Context(), requesterDID, input, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})

	mux.HandleFunc("PUT /v1/private/reservations/", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		intentID := strings.TrimPrefix(r.URL.Path, "/v1/private/reservations/")
		if intentID == "" {
			writeError(w, http.StatusBadRequest, "intent id is required")
			return
		}
		var input ReservationLinkInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		link, err := service.PutReservationLink(r.Context(), requesterDID, intentID, input, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, link)
	})

	mux.HandleFunc("GET /v1/private/reservations/", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		intentID := strings.TrimPrefix(r.URL.Path, "/v1/private/reservations/")
		if intentID == "" {
			writeError(w, http.StatusBadRequest, "intent id is required")
			return
		}
		bundle, err := service.GetPrivateReservation(r.Context(), requesterDID, intentID, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})

	mux.HandleFunc("POST /v1/private/consents", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var input ConsentGrantInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		grant, err := service.CreateConsentGrant(r.Context(), requesterDID, input, time.Now().UTC())
		if err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, grant)
	})

	mux.HandleFunc("DELETE /v1/private/consents/", func(w http.ResponseWriter, r *http.Request) {
		requesterDID, err := requesterDIDFromHeader(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		grantID := strings.TrimPrefix(r.URL.Path, "/v1/private/consents/")
		if grantID == "" {
			writeError(w, http.StatusBadRequest, "grant id is required")
			return
		}
		if err := service.RevokeConsentGrant(r.Context(), requesterDID, grantID, time.Now().UTC()); err != nil {
			writeError(w, statusForError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})

	if verifier != nil {
		return verifier.Middleware(mux)
	}
	return mux
}

func RunOutboxPublisher(ctx context.Context, interval time.Duration, service *Service) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := service.PublishPendingOutbox(ctx, 50); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func splitRecordPath(path string) (string, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	nsid := strings.Join(parts[:len(parts)-1], ".")
	rkey := parts[len(parts)-1]
	if nsid == "" || rkey == "" {
		return "", "", false
	}
	return nsid, rkey, true
}

func parseUintQuery(r *http.Request, key string) (uint64, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseIntQuery(r *http.Request, key string, fallback int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func statusForError(err error) int {
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound
	}
	switch {
	case strings.Contains(err.Error(), "unsupported nsid"),
		strings.Contains(err.Error(), "unsupported private nsid"),
		strings.Contains(err.Error(), "required"),
		strings.Contains(err.Error(), "mismatch"),
		strings.Contains(err.Error(), "unknown field"),
		strings.Contains(err.Error(), "invalid"):
		return http.StatusBadRequest
	case strings.Contains(err.Error(), "authorization required"),
		strings.Contains(err.Error(), "active consent grant required"):
		return http.StatusForbidden
	case strings.Contains(err.Error(), "provider role"):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func requesterDIDFromHeader(r *http.Request) (string, error) {
	// Prefer authenticated DID from context when auth middleware is enabled.
	if actor := auth.ActorDIDFromContext(r.Context()); actor != "" {
		return actor, nil
	}
	// Fallback to plain header when auth middleware is not mounted.
	value := strings.TrimSpace(r.Header.Get("X-OpenBooking-Actor-DID"))
	if value == "" {
		return "", errors.New("X-OpenBooking-Actor-DID header is required")
	}
	return value, nil
}

var _ = privdata.ScopeReservationRead
