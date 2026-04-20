package appview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

type ReservationView struct {
	ProviderDID      string    `json:"providerDid"`
	IntentID         string    `json:"intentId"`
	Status           string    `json:"status"`
	OfferURI         string    `json:"offerUri"`
	BookingReference string    `json:"bookingReference,omitempty"`
	RejectCode       string    `json:"rejectCode,omitempty"`
	EnvelopeHash     string    `json:"envelopeHash,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type Store interface {
	EnsureSchema(ctx context.Context) error
	GetCheckpoint(ctx context.Context, name string) (uint64, error)
	SetCheckpoint(ctx context.Context, name string, sequence uint64) error
	ApplyEnvelope(ctx context.Context, envelope protocol.RelayCommitEnvelope) error
	ListListings(ctx context.Context, providerDID string) ([]lexicon.Listing, error)
	GetOffer(ctx context.Context, providerDID, rkey string) (*lexicon.OfferQuote, error)
	GetAvailability(ctx context.Context, providerDID, listingURI, ratePlanURI string) (*lexicon.Availability, error)
	GetReservation(ctx context.Context, providerDID, intentID string) (*ReservationView, error)
	Ping(ctx context.Context) error
}

type Service struct {
	store    Store
	relayURL string
	client   *http.Client
}

func NewService(ctx context.Context, store Store, relayURL string) (*Service, error) {
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return &Service{
		store:    store,
		relayURL: strings.TrimRight(relayURL, "/"),
		client:   &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func (s *Service) SyncReplay(ctx context.Context) error {
	for {
		after, err := s.store.GetCheckpoint(ctx, "relay")
		if err != nil {
			return err
		}
		events, err := s.fetchEvents(ctx, after, 200)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, event := range events {
			if err := s.store.ApplyEnvelope(ctx, event); err != nil {
				return err
			}
			if err := s.store.SetCheckpoint(ctx, "relay", event.RelaySequence); err != nil {
				return err
			}
		}
	}
}

func (s *Service) ApplyLiveEvent(ctx context.Context, event protocol.RelayCommitEnvelope) error {
	checkpoint, err := s.store.GetCheckpoint(ctx, "relay")
	if err != nil {
		return err
	}
	if event.RelaySequence <= checkpoint {
		return nil
	}
	if err := s.store.ApplyEnvelope(ctx, event); err != nil {
		return err
	}
	return s.store.SetCheckpoint(ctx, "relay", event.RelaySequence)
}

func (s *Service) LatestRelaySequence(ctx context.Context) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.relayURL+"/v1/status", nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("relay status request failed: %s", resp.Status)
	}
	var status struct {
		LatestRelaySequence uint64 `json:"latestRelaySequence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return 0, err
	}
	return status.LatestRelaySequence, nil
}

func (s *Service) Checkpoint(ctx context.Context) (uint64, error) {
	return s.store.GetCheckpoint(ctx, "relay")
}

func (s *Service) ListListings(ctx context.Context, providerDID string) ([]lexicon.Listing, error) {
	return s.store.ListListings(ctx, providerDID)
}

func (s *Service) Offer(ctx context.Context, providerDID, rkey string) (*lexicon.OfferQuote, error) {
	return s.store.GetOffer(ctx, providerDID, rkey)
}

func (s *Service) Availability(ctx context.Context, providerDID, listingURI, ratePlanURI string) (*lexicon.Availability, error) {
	return s.store.GetAvailability(ctx, providerDID, listingURI, ratePlanURI)
}

func (s *Service) Reservation(ctx context.Context, providerDID, intentID string) (*ReservationView, error) {
	return s.store.GetReservation(ctx, providerDID, intentID)
}

func (s *Service) fetchEvents(ctx context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error) {
	query := url.Values{}
	query.Set("after_sequence", fmt.Sprintf("%d", after))
	query.Set("limit", fmt.Sprintf("%d", limit))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.relayURL+"/v1/events?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("relay replay request failed: %s", resp.Status)
	}
	var events []protocol.RelayCommitEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}
