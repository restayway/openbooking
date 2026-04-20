package relaypostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"openbooking/pkg/protocol"
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
		`create table if not exists relay_events (
			relay_sequence bigserial primary key,
			actor_did text not null,
			revision bigint not null,
			source_subject text not null,
			source_sequence bigint not null,
			validated_at timestamptz not null,
			envelope_json jsonb not null
		)`,
		`create unique index if not exists relay_actor_revision_idx on relay_events(actor_did, revision)`,
		`create table if not exists relay_actor_heads (
			actor_did text primary key,
			commit_json jsonb not null
		)`,
		`create table if not exists relay_consumer_checkpoints (
			consumer_name text primary key,
			last_sequence bigint not null,
			updated_at timestamptz not null
		)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LastActorCommit(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `select commit_json from relay_actor_heads where actor_did = $1`, actorDID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var commit protocol.Commit
	if err := json.Unmarshal(raw, &commit); err != nil {
		return nil, err
	}
	return &commit, nil
}

func (s *Store) SaveEvent(ctx context.Context, envelope protocol.RelayCommitEnvelope) (bool, uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	raw, err := json.Marshal(envelope)
	if err != nil {
		return false, 0, err
	}
	result, err := tx.ExecContext(ctx, `
		insert into relay_events (actor_did, revision, source_subject, source_sequence, validated_at, envelope_json)
		values ($1, $2, $3, $4, $5, $6)
	`, envelope.Commit.ActorDID, envelope.Commit.Revision, envelope.SourceSubject, envelope.SourceSequence, envelope.ValidatedAt, raw)
	if err != nil {
		if isUniqueViolation(err) {
			sequence, seqErr := s.LastRelaySequence(ctx)
			return false, sequence, seqErr
		}
		return false, 0, err
	}
	commitRaw, err := json.Marshal(envelope.Commit)
	if err != nil {
		return false, 0, err
	}
	_, err = tx.ExecContext(ctx, `
		insert into relay_actor_heads (actor_did, commit_json)
		values ($1, $2)
		on conflict (actor_did) do update set commit_json = excluded.commit_json
	`, envelope.Commit.ActorDID, commitRaw)
	if err != nil {
		return false, 0, err
	}
	if _, err := result.RowsAffected(); err != nil {
		return false, 0, err
	}
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `select currval(pg_get_serial_sequence('relay_events', 'relay_sequence'))`).Scan(&sequence); err != nil {
		return false, 0, err
	}
	return true, sequence, tx.Commit()
}

func (s *Store) ListEventsAfter(ctx context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error) {
	rows, err := s.db.QueryContext(ctx, `
		select relay_sequence, envelope_json
		from relay_events
		where relay_sequence > $1
		order by relay_sequence asc
		limit $2
	`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []protocol.RelayCommitEnvelope
	for rows.Next() {
		var seq uint64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, err
		}
		var env protocol.RelayCommitEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		env.RelaySequence = seq
		events = append(events, env)
	}
	return events, rows.Err()
}

func (s *Store) GetActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	return s.LastActorCommit(ctx, actorDID)
}

func (s *Store) LastRelaySequence(ctx context.Context) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(relay_sequence), 0) from relay_events`).Scan(&sequence)
	return sequence, err
}

func (s *Store) GetConsumerCheckpoint(ctx context.Context, consumer string) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRowContext(ctx, `select last_sequence from relay_consumer_checkpoints where consumer_name = $1`, consumer).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return sequence, err
}

func (s *Store) SetConsumerCheckpoint(ctx context.Context, consumer string, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into relay_consumer_checkpoints (consumer_name, last_sequence, updated_at)
		values ($1, $2, $3)
		on conflict (consumer_name) do update set last_sequence = excluded.last_sequence, updated_at = excluded.updated_at
	`, consumer, sequence, time.Now().UTC())
	return err
}

func (s *Store) LastConsumerCheckpoint(ctx context.Context) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(last_sequence), 0) from relay_consumer_checkpoints`).Scan(&sequence)
	return sequence, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key")
}
