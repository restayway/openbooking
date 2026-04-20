package appview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"openbooking/pkg/lexicon"
	"openbooking/pkg/protocol"
)

type memoryStore struct {
	checkpoint   uint64
	listings     []lexicon.Listing
	offer        *lexicon.OfferQuote
	availability *lexicon.Availability
	reservation  *ReservationView
}

func (m *memoryStore) EnsureSchema(context.Context) error { return nil }
func (m *memoryStore) Ping(context.Context) error         { return nil }
func (m *memoryStore) GetCheckpoint(context.Context, string) (uint64, error) {
	return m.checkpoint, nil
}
func (m *memoryStore) SetCheckpoint(_ context.Context, _ string, sequence uint64) error {
	m.checkpoint = sequence
	return nil
}
func (m *memoryStore) ApplyEnvelope(_ context.Context, envelope protocol.RelayCommitEnvelope) error {
	for _, op := range envelope.Commit.Ops {
		switch op.Record.ID.NSID {
		case lexicon.NSIDCatalogListing:
			m.listings = []lexicon.Listing{{Name: "Harbor", Currency: "EUR", LocationRef: "geo:1,1"}}
		case lexicon.NSIDOfferQuote:
			m.offer = &lexicon.OfferQuote{ListingURI: "listing", RatePlanURI: "rate"}
		case lexicon.NSIDInventoryAvail:
			m.availability = &lexicon.Availability{ListingURI: "listing", RatePlanURI: "rate", AvailableUnits: 2}
		case lexicon.NSIDReserveCommit:
			m.reservation = &ReservationView{ProviderDID: envelope.Commit.ActorDID, IntentID: "intent-1", Status: "confirmed"}
		}
	}
	return nil
}
func (m *memoryStore) ListListings(context.Context, string) ([]lexicon.Listing, error) {
	return m.listings, nil
}
func (m *memoryStore) GetOffer(context.Context, string, string) (*lexicon.OfferQuote, error) {
	return m.offer, nil
}
func (m *memoryStore) GetAvailability(context.Context, string, string, string) (*lexicon.Availability, error) {
	return m.availability, nil
}
func (m *memoryStore) GetReservation(context.Context, string, string) (*ReservationView, error) {
	return m.reservation, nil
}

func TestAppViewHTTP(t *testing.T) {
	store := &memoryStore{
		listings:     []lexicon.Listing{{Name: "Harbor", Currency: "EUR", LocationRef: "geo:1,1"}},
		offer:        &lexicon.OfferQuote{ListingURI: "listing", RatePlanURI: "rate", ExpiresAt: time.Now()},
		availability: &lexicon.Availability{ListingURI: "listing", RatePlanURI: "rate", AvailableUnits: 2},
		reservation:  &ReservationView{ProviderDID: "did:web:provider.example", IntentID: "intent-1", Status: "confirmed"},
	}
	service, err := NewService(context.Background(), store, "http://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/providers/did:web:provider.example/reservations/intent-1", nil)
	NewHandler(service).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
