package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/restayway/openbooking/pkg/pds"
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

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`create table if not exists repo_records (
			actor_did text not null,
			nsid text not null,
			rkey text not null,
			record_json jsonb not null,
			record_hash text not null,
			created_at timestamptz not null,
			primary key (actor_did, nsid, rkey)
		)`,
		`create table if not exists repo_commits (
			actor_did text not null,
			revision bigint not null,
			previous_root text not null default '',
			root_cid text not null,
			created_at timestamptz not null,
			key_id text not null,
			signature text not null,
			commit_json jsonb not null,
			primary key (actor_did, revision)
		)`,
		`create table if not exists repo_commit_ops (
			actor_did text not null,
			revision bigint not null,
			op_index integer not null,
			action text not null,
			nsid text not null,
			rkey text not null,
			record_json jsonb not null,
			primary key (actor_did, revision, op_index)
		)`,
		`create table if not exists mesh_outbox (
			id bigserial primary key,
			actor_did text not null,
			subject text not null,
			payload jsonb not null,
			created_at timestamptz not null,
			published_at timestamptz
		)`,
		`create index if not exists mesh_outbox_pending_idx on mesh_outbox (published_at, id)`,
		`create table if not exists consumer_checkpoints (
			consumer_name text primary key,
			subject text not null,
			last_sequence bigint not null default 0,
			updated_at timestamptz not null
		)`,
		`create table if not exists private_records (
			owner_did text not null,
			nsid text not null,
			rkey text not null,
			ciphertext text not null,
			reservation_intent text not null default '',
			created_at timestamptz not null,
			updated_at timestamptz not null,
			primary key (owner_did, nsid, rkey)
		)`,
		`create index if not exists private_records_reservation_idx on private_records (owner_did, reservation_intent)`,
		`create table if not exists private_consent_grants (
			owner_did text not null,
			grant_id text not null,
			grantee_did text not null,
			resource text not null,
			scopes jsonb not null,
			expires_at timestamptz not null,
			revoked_at timestamptz,
			ciphertext text not null,
			signature text not null,
			created_at timestamptz not null,
			updated_at timestamptz not null,
			primary key (owner_did, grant_id)
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LoadRepository(ctx context.Context, actorDID string) (pds.RepositoryState, error) {
	rows, err := s.db.QueryContext(ctx, `
		select record_json
		from repo_records
		where actor_did = $1
		order by nsid, rkey
	`, actorDID)
	if err != nil {
		return pds.RepositoryState{}, err
	}
	defer rows.Close()

	records := make([]protocol.Record, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return pds.RepositoryState{}, err
		}
		var record protocol.Record
		if err := json.Unmarshal(raw, &record); err != nil {
			return pds.RepositoryState{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return pds.RepositoryState{}, err
	}

	var (
		commitRaw []byte
		revision  uint64
		rootCID   string
	)
	err = s.db.QueryRowContext(ctx, `
		select commit_json, revision, root_cid
		from repo_commits
		where actor_did = $1
		order by revision desc
		limit 1
	`, actorDID).Scan(&commitRaw, &revision, &rootCID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return pds.RepositoryState{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return pds.RepositoryState{Records: records}, nil
	}

	var head protocol.Commit
	if err := json.Unmarshal(commitRaw, &head); err != nil {
		return pds.RepositoryState{}, err
	}
	return pds.RepositoryState{
		Records:  records,
		Head:     &head,
		Revision: revision,
		LastRoot: rootCID,
	}, nil
}

func (s *Store) SaveCommit(ctx context.Context, commit protocol.Commit, snapshot []protocol.Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, op := range commit.Ops {
		switch op.Action {
		case protocol.OperationPut:
			recordPayload, marshalErr := json.Marshal(op.Record)
			if marshalErr != nil {
				err = marshalErr
				return err
			}
			_, err = tx.ExecContext(ctx, `
				insert into repo_records (actor_did, nsid, rkey, record_json, record_hash, created_at)
				values ($1, $2, $3, $4, $5, $6)
				on conflict (actor_did, nsid, rkey) do update
				set record_json = excluded.record_json,
					record_hash = excluded.record_hash,
					created_at = excluded.created_at
			`, commit.ActorDID, op.Record.ID.NSID, op.Record.ID.RKey, recordPayload, op.Record.Hash, op.Record.CreatedAt)
		case protocol.OperationDelete:
			_, err = tx.ExecContext(ctx, `
				delete from repo_records
				where actor_did = $1 and nsid = $2 and rkey = $3
			`, commit.ActorDID, op.Record.ID.NSID, op.Record.ID.RKey)
		}
		if err != nil {
			return err
		}
	}

	commitPayload, err := json.Marshal(commit)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into repo_commits (actor_did, revision, previous_root, root_cid, created_at, key_id, signature, commit_json)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
	`, commit.ActorDID, commit.Revision, commit.Previous, commit.RootCID, commit.CreatedAt, commit.KeyID, commit.Signature, commitPayload)
	if err != nil {
		return err
	}

	for idx, op := range commit.Ops {
		recordPayload, marshalErr := json.Marshal(op.Record)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `
			insert into repo_commit_ops (actor_did, revision, op_index, action, nsid, rkey, record_json)
			values ($1, $2, $3, $4, $5, $6, $7)
		`, commit.ActorDID, commit.Revision, idx, op.Action, op.Record.ID.NSID, op.Record.ID.RKey, recordPayload)
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
		insert into mesh_outbox (actor_did, subject, payload, created_at)
		values ($1, $2, $3, $4)
	`, commit.ActorDID, commit.Subject(), commitPayload, commit.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRecord(ctx context.Context, actorDID, nsid, rkey string) (protocol.Record, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
		select record_json
		from repo_records
		where actor_did = $1 and nsid = $2 and rkey = $3
	`, actorDID, nsid, rkey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Record{}, pds.ErrNotFound
	}
	if err != nil {
		return protocol.Record{}, err
	}
	var record protocol.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return protocol.Record{}, err
	}
	return record, nil
}

func (s *Store) ListCommitsAfter(ctx context.Context, actorDID string, afterRevision uint64, limit int) ([]protocol.Commit, error) {
	rows, err := s.db.QueryContext(ctx, `
		select commit_json
		from repo_commits
		where actor_did = $1 and revision > $2
		order by revision asc
		limit $3
	`, actorDID, afterRevision, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	commits := make([]protocol.Commit, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var commit protocol.Commit
		if err := json.Unmarshal(raw, &commit); err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	return commits, rows.Err()
}

func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]pds.OutboxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		select id, subject, payload, created_at
		from mesh_outbox
		where published_at is null
		order by id asc
		limit $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]pds.OutboxMessage, 0)
	for rows.Next() {
		var message pds.OutboxMessage
		if err := rows.Scan(&message.ID, &message.Subject, &message.Payload, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id int64, publishedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		update mesh_outbox
		set published_at = $2
		where id = $1 and published_at is null
	`, id, publishedAt)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("outbox message %d not found or already published", id)
	}
	return nil
}

func (s *Store) PendingOutboxCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `select count(*) from mesh_outbox where published_at is null`).Scan(&count)
	return count, err
}

func (s *Store) SavePrivateRecord(ctx context.Context, record pds.PrivateStoredRecord) error {
	_, err := s.db.ExecContext(ctx, `
		insert into private_records (owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (owner_did, nsid, rkey) do update
		set ciphertext = excluded.ciphertext,
			reservation_intent = excluded.reservation_intent,
			updated_at = excluded.updated_at
	`, record.OwnerDID, record.NSID, record.RKey, record.Ciphertext, record.ReservationIntent, record.CreatedAt, record.UpdatedAt)
	return err
}

func (s *Store) GetPrivateRecord(ctx context.Context, ownerDID, nsid, rkey string) (pds.PrivateStoredRecord, error) {
	var record pds.PrivateStoredRecord
	err := s.db.QueryRowContext(ctx, `
		select owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at
		from private_records
		where owner_did = $1 and nsid = $2 and rkey = $3
	`, ownerDID, nsid, rkey).Scan(
		&record.OwnerDID,
		&record.NSID,
		&record.RKey,
		&record.Ciphertext,
		&record.ReservationIntent,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return pds.PrivateStoredRecord{}, pds.ErrNotFound
	}
	return record, err
}

func (s *Store) ListPrivateRecordsByReservation(ctx context.Context, ownerDID, intentID string) ([]pds.PrivateStoredRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		select owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at
		from private_records
		where owner_did = $1 and reservation_intent = $2
		order by nsid, rkey
	`, ownerDID, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []pds.PrivateStoredRecord
	for rows.Next() {
		var record pds.PrivateStoredRecord
		if err := rows.Scan(&record.OwnerDID, &record.NSID, &record.RKey, &record.Ciphertext, &record.ReservationIntent, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SaveConsentGrant(ctx context.Context, grant pds.StoredConsentGrant) error {
	scopes, err := json.Marshal(grant.Scopes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		insert into private_consent_grants (owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		on conflict (owner_did, grant_id) do update
		set grantee_did = excluded.grantee_did,
			resource = excluded.resource,
			scopes = excluded.scopes,
			expires_at = excluded.expires_at,
			revoked_at = excluded.revoked_at,
			ciphertext = excluded.ciphertext,
			signature = excluded.signature,
			updated_at = excluded.updated_at
	`, grant.OwnerDID, grant.GrantID, grant.GranteeDID, grant.Resource, scopes, grant.ExpiresAt, grant.RevokedAt, grant.Ciphertext, grant.Signature, grant.CreatedAt, grant.UpdatedAt)
	return err
}

func (s *Store) GetConsentGrant(ctx context.Context, ownerDID, grantID string) (pds.StoredConsentGrant, error) {
	var grant pds.StoredConsentGrant
	var scopes []byte
	err := s.db.QueryRowContext(ctx, `
		select owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at
		from private_consent_grants
		where owner_did = $1 and grant_id = $2
	`, ownerDID, grantID).Scan(
		&grant.OwnerDID,
		&grant.GrantID,
		&grant.GranteeDID,
		&grant.Resource,
		&scopes,
		&grant.ExpiresAt,
		&grant.RevokedAt,
		&grant.Ciphertext,
		&grant.Signature,
		&grant.CreatedAt,
		&grant.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return pds.StoredConsentGrant{}, pds.ErrNotFound
	}
	if err != nil {
		return pds.StoredConsentGrant{}, err
	}
	if err := json.Unmarshal(scopes, &grant.Scopes); err != nil {
		return pds.StoredConsentGrant{}, err
	}
	return grant, nil
}

func (s *Store) ListConsentGrants(ctx context.Context, ownerDID string) ([]pds.StoredConsentGrant, error) {
	rows, err := s.db.QueryContext(ctx, `
		select owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at
		from private_consent_grants
		where owner_did = $1
		order by created_at asc
	`, ownerDID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []pds.StoredConsentGrant
	for rows.Next() {
		var grant pds.StoredConsentGrant
		var scopes []byte
		if err := rows.Scan(&grant.OwnerDID, &grant.GrantID, &grant.GranteeDID, &grant.Resource, &scopes, &grant.ExpiresAt, &grant.RevokedAt, &grant.Ciphertext, &grant.Signature, &grant.CreatedAt, &grant.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(scopes, &grant.Scopes); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (s *Store) RevokeConsentGrant(ctx context.Context, ownerDID, grantID string, revokedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		update private_consent_grants
		set revoked_at = $3, updated_at = $3
		where owner_did = $1 and grant_id = $2
	`, ownerDID, grantID, revokedAt)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return pds.ErrNotFound
	}
	return nil
}
