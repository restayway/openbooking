package appviewsqlite

import (
	"context"
	"testing"
	"time"

	"github.com/restayway/openbooking/pkg/appview"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

func TestSqliteStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Apply a listing
	listing, err := protocol.NewRecord(protocol.RecordID{ActorDID: "did:web:p.example", NSID: lexicon.NSIDCatalogListing, RKey: "lst-1"}, lexicon.Listing{Name: "Riverside", Currency: "USD", LocationRef: "geo:1,1"}, time.Now())
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	envelope := protocol.RelayCommitEnvelope{Commit: protocol.Commit{ActorDID: "did:web:p.example", Ops: []protocol.Operation{{Action: protocol.OperationPut, Record: listing}}}}
	if err := store.ApplyEnvelope(ctx, envelope); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Query it back
	listings, err := store.ListListings(ctx, "did:web:p.example")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 listing, got %d", len(listings))
	}
	if listings[0].Name != "Riverside" {
		t.Fatalf("unexpected name: %s", listings[0].Name)
	}

	// Commit a reservation then cancel it
	commit, _ := protocol.NewRecord(protocol.RecordID{ActorDID: "did:web:p.example", NSID: lexicon.NSIDReserveCommit, RKey: "ref-1"}, lexicon.ReservationCommit{IntentID: "ref-1", OfferURI: "ob://x/x/x", BookingReference: "ref-1", Status: "confirmed", CommittedAt: time.Now().UTC()}, time.Now())
	cancel, _ := protocol.NewRecord(protocol.RecordID{ActorDID: "did:web:p.example", NSID: lexicon.NSIDReserveCancel, RKey: "ref-1"}, lexicon.ReservationCancel{BookingReference: "ref-1", ReasonCode: "guest-request", CancelledAt: time.Now().UTC()}, time.Now())

	if err := store.ApplyEnvelope(ctx, protocol.RelayCommitEnvelope{Commit: protocol.Commit{ActorDID: "did:web:p.example", Ops: []protocol.Operation{{Action: protocol.OperationPut, Record: commit}}}}); err != nil {
		t.Fatalf("apply commit: %v", err)
	}
	if err := store.ApplyEnvelope(ctx, protocol.RelayCommitEnvelope{Commit: protocol.Commit{ActorDID: "did:web:p.example", Ops: []protocol.Operation{{Action: protocol.OperationPut, Record: cancel}}}}); err != nil {
		t.Fatalf("apply cancel: %v", err)
	}

	res, err := store.GetReservation(ctx, "did:web:p.example", "ref-1")
	if err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	if res == nil {
		t.Fatal("expected reservation")
	}
	if res.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", res.Status)
	}
}

func TestSqliteStoreCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seq, err := store.GetCheckpoint(ctx, "relay")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if seq != 0 {
		t.Fatalf("expected 0, got %d", seq)
	}
	if err := store.SetCheckpoint(ctx, "relay", 42); err != nil {
		t.Fatalf("set: %v", err)
	}
	seq, err = store.GetCheckpoint(ctx, "relay")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if seq != 42 {
		t.Fatalf("expected 42, got %d", seq)
	}
}

func TestSqliteStoreCompatibilityWithAppview(t *testing.T) {
	var _ appview.Store = (*Store)(nil)
}
