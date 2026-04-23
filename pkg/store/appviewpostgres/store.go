package appviewpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/restayway/openbooking/pkg/appview"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`create table if not exists appview_checkpoints (name text primary key, relay_sequence bigint not null)`,
		`create table if not exists appview_listings (provider_did text not null, rkey text not null, payload jsonb not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_offers (provider_did text not null, rkey text not null, payload jsonb not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_availability (provider_did text not null, rkey text not null, listing_uri text not null, rate_plan_uri text not null, payload jsonb not null, primary key (provider_did, rkey))`,
		`create table if not exists appview_reservations (
			provider_did text not null,
			intent_id text not null,
			status text not null,
			offer_uri text not null,
			booking_reference text not null default '',
			reject_code text not null default '',
			envelope_hash text not null default '',
			updated_at timestamptz not null,
			primary key (provider_did, intent_id)
		)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetCheckpoint(ctx context.Context, name string) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `select relay_sequence from appview_checkpoints where name = $1`, name).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *Store) SetCheckpoint(ctx context.Context, name string, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into appview_checkpoints (name, relay_sequence)
		values ($1, $2)
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
		_, err := tx.ExecContext(ctx, `delete from `+table+` where provider_did = $1 and rkey = $2`, providerDID, op.Record.ID.RKey)
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into `+table+` (provider_did, rkey, payload) values ($1, $2, $3)
		on conflict (provider_did, rkey) do update set payload = excluded.payload`, providerDID, op.Record.ID.RKey, op.Record.Value)
	return err
}

func (s *Store) applyAvailability(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	if op.Action == protocol.OperationDelete {
		_, err := tx.ExecContext(ctx, `delete from appview_availability where provider_did = $1 and rkey = $2`, providerDID, op.Record.ID.RKey)
		return err
	}
	var availability lexicon.Availability
	if err := json.Unmarshal(op.Record.Value, &availability); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		insert into appview_availability (provider_did, rkey, listing_uri, rate_plan_uri, payload)
		values ($1, $2, $3, $4, $5)
		on conflict (provider_did, rkey) do update set listing_uri = excluded.listing_uri, rate_plan_uri = excluded.rate_plan_uri, payload = excluded.payload
	`, providerDID, op.Record.ID.RKey, availability.ListingURI, availability.RatePlanURI, op.Record.Value)
	return err
}

func (s *Store) applyReservationCommit(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var commit lexicon.ReservationCommit
	if err := json.Unmarshal(op.Record.Value, &commit); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		insert into appview_reservations (provider_did, intent_id, status, offer_uri, booking_reference, envelope_hash, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (provider_did, intent_id) do update
		set status = excluded.status, offer_uri = excluded.offer_uri, booking_reference = excluded.booking_reference, envelope_hash = excluded.envelope_hash, updated_at = excluded.updated_at
	`, providerDID, commit.IntentID, "confirmed", commit.OfferURI, commit.BookingReference, commit.EnvelopeHash, commit.CommittedAt)
	return err
}

func (s *Store) applyReservationReject(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var reject lexicon.ReservationReject
	if err := json.Unmarshal(op.Record.Value, &reject); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		insert into appview_reservations (provider_did, intent_id, status, offer_uri, reject_code, updated_at)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (provider_did, intent_id) do update
		set status = excluded.status, offer_uri = excluded.offer_uri, reject_code = excluded.reject_code, updated_at = excluded.updated_at
	`, providerDID, reject.IntentID, "rejected", reject.OfferURI, reject.Code, reject.RejectedAt)
	return err
}

func (s *Store) applyReservationCancel(ctx context.Context, tx *sql.Tx, op protocol.Operation, providerDID string) error {
	var cancel lexicon.ReservationCancel
	if err := json.Unmarshal(op.Record.Value, &cancel); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		update appview_reservations
		set status = 'cancelled', updated_at = $3
		where provider_did = $1 and booking_reference = $2
	`, providerDID, cancel.BookingReference, cancel.CancelledAt)
	return err
}

func (s *Store) ListListings(ctx context.Context, providerDID string) ([]lexicon.Listing, error) {
	rows, err := s.db.QueryContext(ctx, `select payload from appview_listings where provider_did = $1 order by rkey`, providerDID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var listings []lexicon.Listing
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var listing lexicon.Listing
		if err := json.Unmarshal(raw, &listing); err != nil {
			return nil, err
		}
		listings = append(listings, listing)
	}
	return listings, rows.Err()
}

func (s *Store) GetOffer(ctx context.Context, providerDID, rkey string) (*lexicon.OfferQuote, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `select payload from appview_offers where provider_did = $1 and rkey = $2`, providerDID, rkey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var offer lexicon.OfferQuote
	if err := json.Unmarshal(raw, &offer); err != nil {
		return nil, err
	}
	return &offer, nil
}

func (s *Store) GetAvailability(ctx context.Context, providerDID, listingURI, ratePlanURI string) (*lexicon.Availability, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
		select payload from appview_availability where provider_did = $1 and listing_uri = $2 and rate_plan_uri = $3
		order by rkey desc limit 1
	`, providerDID, listingURI, ratePlanURI).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var availability lexicon.Availability
	if err := json.Unmarshal(raw, &availability); err != nil {
		return nil, err
	}
	return &availability, nil
}

func (s *Store) GetReservation(ctx context.Context, providerDID, intentID string) (*appview.ReservationView, error) {
	var reservation appview.ReservationView
	err := s.db.QueryRowContext(ctx, `
		select provider_did, intent_id, status, offer_uri, booking_reference, reject_code, envelope_hash, updated_at
		from appview_reservations where provider_did = $1 and intent_id = $2
	`, providerDID, intentID).Scan(
		&reservation.ProviderDID,
		&reservation.IntentID,
		&reservation.Status,
		&reservation.OfferURI,
		&reservation.BookingReference,
		&reservation.RejectCode,
		&reservation.EnvelopeHash,
		&reservation.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}
