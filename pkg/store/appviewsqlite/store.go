package appviewsqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	_ "modernc.org/sqlite"

	"github.com/restayway/openbooking/pkg/appview"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

// Store implements appview.Store backed by SQLite (modernc.org/sqlite).
type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	if dsn == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)\u0026_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`create table if not exists appview_checkpoints (name text primary key, relay_sequence bigint not null)`,
		`create table if not exists appview_listings (provider_did text not null, rkey text not null, payload text not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_offers (provider_did text not null, rkey text not null, payload text not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_availability (provider_did text not null, rkey text not null, listing_uri text not null collate nocase, rate_plan_uri text not null collate nocase, payload text not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_reservations (
			provider_did text not null,
			intent_id text not null,
			status text not null,
			offer_uri text not null,
			booking_reference text not null default '',
			reject_code text not null default '',
			envelope_hash text not null default '',
			updated_at timestamp not null,
			primary key (provider_did, intent_id)
		)`,
		`create index if not exists idx_reservations_booking_ref on appview_reservations(provider_did, booking_reference)`,
		`create index if not exists idx_availability_listing on appview_availability(provider_did, listing_uri, rate_plan_uri)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetCheckpoint(ctx context.Context, name string) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `select relay_sequence from appview_checkpoints where name = ?`, name).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *Store) SetCheckpoint(ctx context.Context, name string, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into appview_checkpoints (name, relay_sequence) values (?, ?)
		on conflict (name) do update set relay_sequence = excluded.relay_sequence
	`, name, sequence)
	return err
}

func (s *Store) ApplyEnvelope(ctx context.Context, envelope protocol.RelayCommitEnvelope) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, op := range envelope.Commit.Ops {
		switch op.Record.ID.NSID {
		case lexicon.NSIDCatalogListing:
			err = s.applyUpsertOrDelete(ctx, tx, op, "appview_listings", envelope.Commit.ActorDID)
		case lexicon.NSIDOfferQuote:
			err = s.applyUpsertOrDelete(ctx, tx, op, "appview_offers", envelope.Commit.ActorDID)
		case lexicon.NSIDInventoryAvail:
			err = s.applyAvailability(ctx, tx, op, envelope.Commit.ActorDID)
		case lexicon.NSIDReserveCommit:
			err = s.applyReservationCommit(ctx, tx, op, envelope.Commit.ActorDID)
		case lexicon.NSIDReserveReject:
			err = s.applyReservationReject(ctx, tx, op, envelope.Commit.ActorDID)
		case lexicon.NSIDReserveCancel:
			err = s.applyReservationCancel(ctx, tx, op, envelope.Commit.ActorDID)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) applyUpsertOrDelete(ctx context.Context, tx *sql.Tx, op protocol.Operation, table, providerDID string) error {
	if op.Action == protocol.OperationDelete {
		_, err := tx.ExecContext(ctx, `delete from `+table+` where provider_did = ? and rkey = ?`, providerDID, op.Record.ID.RKey)
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into `+table+` (provider_did, rkey, payload) values (?, ?, ?) on conflict (provider_did, rkey) do update set payload = excluded.payload`, providerDID, op.Record.ID.RKey, string(op.Record.Value))
	return err
}

func (s *Store) applyAvailability(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	if op.Action == protocol.OperationDelete {
		_, err := tx.ExecContext(ctx, `delete from appview_availability where provider_did = ? and rkey = ?`, providerDID, op.Record.ID.RKey)
		return err
	}
	var a lexicon.Availability
	if err := json.Unmarshal(op.Record.Value, &a); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into appview_availability (provider_did, rkey, listing_uri, rate_plan_uri, payload) values (?, ?, ?, ?, ?)
		on conflict (provider_did, rkey) do update set listing_uri = excluded.listing_uri, rate_plan_uri = excluded.rate_plan_uri, payload = excluded.payload`,
		providerDID, op.Record.ID.RKey, a.ListingURI, a.RatePlanURI, string(op.Record.Value))
	return err
}

func (s *Store) applyReservationCommit(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var rc lexicon.ReservationCommit
	if err := json.Unmarshal(op.Record.Value, &rc); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into appview_reservations (provider_did, intent_id, status, offer_uri, booking_reference, envelope_hash, updated_at)
		values (?, ?, ?, ?, ?, ?, ?) on conflict (provider_did, intent_id) do update
		set status = excluded.status, offer_uri = excluded.offer_uri, booking_reference = excluded.booking_reference, envelope_hash = excluded.envelope_hash, updated_at = excluded.updated_at`,
		providerDID, rc.IntentID, "confirmed", rc.OfferURI, rc.BookingReference, rc.EnvelopeHash, rc.CommittedAt)
	return err
}

func (s *Store) applyReservationReject(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var rj lexicon.ReservationReject
	if err := json.Unmarshal(op.Record.Value, &rj); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into appview_reservations (provider_did, intent_id, status, offer_uri, reject_code, updated_at)
		values (?, ?, ?, ?, ?, ?) on conflict (provider_did, intent_id) do update
		set status = excluded.status, offer_uri = excluded.offer_uri, reject_code = excluded.reject_code, updated_at = excluded.updated_at`,
		providerDID, rj.IntentID, "rejected", rj.OfferURI, rj.Code, rj.RejectedAt)
	return err
}

func (s *Store) applyReservationCancel(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var c lexicon.ReservationCancel
	if err := json.Unmarshal(op.Record.Value, &c); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `update appview_reservations set status = 'cancelled', updated_at = ? where provider_did = ? and booking_reference = ?`, c.CancelledAt, providerDID, c.BookingReference)
	return err
}

func (s *Store) ListListings(ctx context.Context, providerDID string) ([]lexicon.Listing, error) {
	rows, err := s.db.QueryContext(ctx, `select payload from appview_listings where provider_did = ? order by rkey`, providerDID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var listings []lexicon.Listing
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var l lexicon.Listing
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			return nil, err
		}
		listings = append(listings, l)
	}
	return listings, rows.Err()
}

func (s *Store) GetOffer(ctx context.Context, providerDID, rkey string) (*lexicon.OfferQuote, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select payload from appview_offers where provider_did = ? and rkey = ?`, providerDID, rkey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o lexicon.OfferQuote
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) GetAvailability(ctx context.Context, providerDID, listingURI, ratePlanURI string) (*lexicon.Availability, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `
		select payload from appview_availability where provider_did = ? and listing_uri = ? and rate_plan_uri = ?
		order by rkey desc limit 1`, providerDID, listingURI, ratePlanURI).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a lexicon.Availability
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) GetReservation(ctx context.Context, providerDID, intentID string) (*appview.ReservationView, error) {
	var r appview.ReservationView
	err := s.db.QueryRowContext(ctx, `
		select provider_did, intent_id, status, offer_uri, booking_reference, reject_code, envelope_hash, updated_at
		from appview_reservations where provider_did = ? and intent_id = ?`, providerDID, intentID).Scan(
		&r.ProviderDID, &r.IntentID, &r.Status, &r.OfferURI, &r.BookingReference, &r.RejectCode, &r.EnvelopeHash, &r.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
