package relaysqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"

	"github.com/restayway/openbooking/pkg/protocol"
	"github.com/restayway/openbooking/pkg/relay"
)

// Ensure compile-time compatibility with relay.Store interface.
var _ relay.Store = (*Store)(nil)

type Store struct{ db *sql.DB }

func Open(dsn string) (*Store, error) {
	if dsn == "" { dsn = ":memory:" }
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)\u0026_pragma=journal_mode(WAL)")
	if err != nil { return nil, err }
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`create table if not exists relay_events (relay_sequence integer primary key autoincrement, relay_did text not null, source_subject text not null, source_sequence bigint not null, validated_at timestamp not null, commit_json text not null)`,
		`create unique index if not exists idx_relay_events_actor_rev on relay_events (json_extract(commit_json, '$.actorDID'), json_extract(commit_json, '$.revision'))`,
		`create table if not exists actor_heads (actor_did text primary key, commit_json text not null, updated_at timestamp not null)`,
		`create table if not exists consumer_checkpoints (consumer_name text primary key, subject text not null, last_sequence bigint not null default 0, updated_at timestamp not null)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil { return err }
	}
	return nil
}

func (s *Store) SaveEvent(ctx context.Context, envelope protocol.RelayCommitEnvelope) (bool, uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return false, 0, err }
	defer func() { if err != nil { _ = tx.Rollback() } }()

	commitPayload, _ := json.Marshal(envelope.Commit)
	var actorDID = envelope.Commit.ActorDID
	var revision = envelope.Commit.Revision

	var existing uint64
	err = tx.QueryRowContext(ctx, `select relay_sequence from relay_events where json_extract(commit_json, '$.actorDID') = ? and json_extract(commit_json, '$.revision') = ?`, actorDID, revision).Scan(&existing)
	if err == nil { return false, existing, tx.Commit() }
	if !errors.Is(err, sql.ErrNoRows) { return false, 0, err }

	res, err := tx.ExecContext(ctx, `insert into relay_events (relay_did, source_subject, source_sequence, validated_at, commit_json) values (?, ?, ?, ?, ?)`, envelope.RelayDID, envelope.SourceSubject, envelope.SourceSequence, envelope.ValidatedAt, string(commitPayload))
	if err != nil { return false, 0, err }
	seq, _ := res.LastInsertId()

	_, err = tx.ExecContext(ctx, `insert into actor_heads (actor_did, commit_json, updated_at) values (?, ?, ?) on conflict (actor_did) do update set commit_json = excluded.commit_json, updated_at = excluded.updated_at`, actorDID, string(commitPayload), time.Now().UTC())
	if err != nil { return false, 0, err }

	return true, uint64(seq), tx.Commit()
}

func (s *Store) ActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select commit_json from actor_heads where actor_did = ?`, actorDID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) { return nil, nil }
	if err != nil { return nil, err }
	var c protocol.Commit
	if err := json.Unmarshal([]byte(raw), &c); err != nil { return nil, err }
	return &c, nil
}

func (s *Store) LastActorCommit(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	return s.ActorHead(ctx, actorDID)
}

func (s *Store) ListEventsAfter(ctx context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error) {
	rows, err := s.db.QueryContext(ctx, `select relay_did, relay_sequence, source_subject, source_sequence, validated_at, commit_json from relay_events where relay_sequence > ? order by relay_sequence asc limit ?`, after, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var events []protocol.RelayCommitEnvelope
	for rows.Next() {
		var e protocol.RelayCommitEnvelope
		var raw string
		if err := rows.Scan(&e.RelayDID, &e.RelaySequence, &e.SourceSubject, &e.SourceSequence, &e.ValidatedAt, &raw); err != nil { return nil, err }
		if err := json.Unmarshal([]byte(raw), &e.Commit); err != nil { return nil, err }
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) LastRelaySequence(ctx context.Context) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(relay_sequence), 0) from relay_events`).Scan(&seq)
	return seq, err
}

func (s *Store) GetConsumerCheckpoint(ctx context.Context, consumer string) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `select last_sequence from consumer_checkpoints where consumer_name = ?`, consumer).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) { return 0, nil }
	return seq, err
}

func (s *Store) SetConsumerCheckpoint(ctx context.Context, consumer string, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `insert into consumer_checkpoints (consumer_name, subject, last_sequence, updated_at) values (?, ?, ?, ?) on conflict (consumer_name) do update set last_sequence = excluded.last_sequence, updated_at = excluded.updated_at`, consumer, "", seq, time.Now().UTC())
	return err
}

func (s *Store) SetConsumerSubjectCheckpoint(ctx context.Context, consumer, subject string, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `insert into consumer_checkpoints (consumer_name, subject, last_sequence, updated_at) values (?, ?, ?, ?) on conflict (consumer_name) do update set subject = excluded.subject, last_sequence = excluded.last_sequence, updated_at = excluded.updated_at`, consumer, subject, seq, time.Now().UTC())
	return err
}

// GetActorHead returns the latest known commit for an actor (alias to ActorHead).
func (s *Store) GetActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	return s.ActorHead(ctx, actorDID)
}

// LastConsumerCheckpoint returns the latest consumer checkpoint across all consumers.
func (s *Store) LastConsumerCheckpoint(ctx context.Context) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(last_sequence), 0) from consumer_checkpoints`).Scan(&seq)
	return seq, err
}
