package pdssqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/restayway/openbooking/pkg/pds"
	"github.com/restayway/openbooking/pkg/protocol"
)

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
		`create table if not exists repo_records (actor_did text not null, nsid text not null, rkey text not null, record_json text not null, record_hash text not null, created_at timestamp not null, primary key (actor_did, nsid, rkey))`,
		`create table if not exists repo_commits (actor_did text not null, revision bigint not null, previous_root text not null default '', root_cid text not null, created_at timestamp not null, key_id text not null, signature text not null, commit_json text not null, primary key (actor_did, revision))`,
		`create table if not exists repo_commit_ops (actor_did text not null, revision bigint not null, op_index integer not null, action text not null, nsid text not null, rkey text not null, record_json text not null, primary key (actor_did, revision, op_index))`,
		`create table if not exists mesh_outbox (id integer primary key autoincrement, actor_did text not null, subject text not null, payload text not null, created_at timestamp not null, published_at timestamp)`,
		`create index if not exists mesh_outbox_pending_idx on mesh_outbox (published_at, id)`,
		`create table if not exists consumer_checkpoints (consumer_name text primary key, subject text not null, last_sequence bigint not null default 0, updated_at timestamp not null)`,
		`create table if not exists private_records (owner_did text not null, nsid text not null, rkey text not null, ciphertext text not null, reservation_intent text not null default '', created_at timestamp not null, updated_at timestamp not null, primary key (owner_did, nsid, rkey))`,
		`create index if not exists private_records_reservation_idx on private_records (owner_did, reservation_intent)`,
		`create table if not exists private_consent_grants (owner_did text not null, grant_id text not null, grantee_did text not null, resource text not null, scopes text not null, expires_at timestamp not null, revoked_at timestamp, ciphertext text not null, signature text not null, created_at timestamp not null, updated_at timestamp not null, primary key (owner_did, grant_id))`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil { return err }
	}
	return nil
}

func (s *Store) LoadRepository(ctx context.Context, actorDID string) (pds.RepositoryState, error) {
	rows, err := s.db.QueryContext(ctx, `select record_json from repo_records where actor_did = ? order by nsid, rkey`, actorDID)
	if err != nil { return pds.RepositoryState{}, err }
	defer rows.Close()
	var records []protocol.Record
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil { return pds.RepositoryState{}, err }
		var rec protocol.Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil { return pds.RepositoryState{}, err }
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil { return pds.RepositoryState{}, err }

	var raw string
	var rev uint64
	var root string
	qerr := s.db.QueryRowContext(ctx, `select commit_json, revision, root_cid from repo_commits where actor_did = ? order by revision desc limit 1`, actorDID).Scan(&raw, &rev, &root)
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) { return pds.RepositoryState{}, qerr }
	if errors.Is(qerr, sql.ErrNoRows) { return pds.RepositoryState{Records: records}, nil }

	var head protocol.Commit
	if err := json.Unmarshal([]byte(raw), &head); err != nil { return pds.RepositoryState{}, err }
	return pds.RepositoryState{Records: records, Head: &head, Revision: rev, LastRoot: root}, nil
}

func (s *Store) SaveCommit(ctx context.Context, commit protocol.Commit, snapshot []protocol.Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer func() { if err != nil { _ = tx.Rollback() } }()

	for _, op := range commit.Ops {
		if op.Action == protocol.OperationPut {
			payload, _ := json.Marshal(op.Record)
			_, err = tx.ExecContext(ctx, `insert into repo_records (actor_did, nsid, rkey, record_json, record_hash, created_at) values (?, ?, ?, ?, ?, ?) on conflict (actor_did, nsid, rkey) do update set record_json = excluded.record_json, record_hash = excluded.record_hash, created_at = excluded.created_at`, commit.ActorDID, op.Record.ID.NSID, op.Record.ID.RKey, string(payload), op.Record.Hash, op.Record.CreatedAt)
		} else {
			_, err = tx.ExecContext(ctx, `delete from repo_records where actor_did = ? and nsid = ? and rkey = ?`, commit.ActorDID, op.Record.ID.NSID, op.Record.ID.RKey)
		}
		if err != nil { return err }
	}

	commitPayload, _ := json.Marshal(commit)
	_, err = tx.ExecContext(ctx, `insert into repo_commits (actor_did, revision, previous_root, root_cid, created_at, key_id, signature, commit_json) values (?, ?, ?, ?, ?, ?, ?, ?)`, commit.ActorDID, commit.Revision, commit.Previous, commit.RootCID, commit.CreatedAt, commit.KeyID, commit.Signature, string(commitPayload))
	if err != nil { return err }

	for idx, op := range commit.Ops {
		recordPayload, _ := json.Marshal(op.Record)
		_, err = tx.ExecContext(ctx, `insert into repo_commit_ops (actor_did, revision, op_index, action, nsid, rkey, record_json) values (?, ?, ?, ?, ?, ?, ?)`, commit.ActorDID, commit.Revision, idx, op.Action, op.Record.ID.NSID, op.Record.ID.RKey, string(recordPayload))
		if err != nil { return err }
	}

	_, err = tx.ExecContext(ctx, `insert into mesh_outbox (actor_did, subject, payload, created_at) values (?, ?, ?, ?)`, commit.ActorDID, commit.Subject(), string(commitPayload), commit.CreatedAt)
	if err != nil { return err }
	return tx.Commit()
}

func (s *Store) GetRecord(ctx context.Context, actorDID, nsid, rkey string) (protocol.Record, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `select record_json from repo_records where actor_did = ? and nsid = ? and rkey = ?`, actorDID, nsid, rkey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) { return protocol.Record{}, pds.ErrNotFound }
	if err != nil { return protocol.Record{}, err }
	var rec protocol.Record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil { return protocol.Record{}, err }
	return rec, nil
}

func (s *Store) ListCommitsAfter(ctx context.Context, actorDID string, after uint64, limit int) ([]protocol.Commit, error) {
	rows, err := s.db.QueryContext(ctx, `select commit_json from repo_commits where actor_did = ? and revision > ? order by revision asc limit ?`, actorDID, after, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var commits []protocol.Commit
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil { return nil, err }
		var c protocol.Commit
		if err := json.Unmarshal([]byte(raw), &c); err != nil { return nil, err }
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]pds.OutboxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `select id, subject, payload, created_at from mesh_outbox where published_at is null order by id asc limit ?`, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var msgs []pds.OutboxMessage
	for rows.Next() {
		var m pds.OutboxMessage
		if err := rows.Scan(&m.ID, &m.Subject, &m.Payload, &m.CreatedAt); err != nil { return nil, err }
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id int64, publishedAt time.Time) error {
	r, err := s.db.ExecContext(ctx, `update mesh_outbox set published_at = ? where id = ? and published_at is null`, publishedAt, id)
	if err != nil { return err }
	n, _ := r.RowsAffected()
	if n == 0 { return fmt.Errorf("outbox message %d not found or already published", id) }
	return nil
}

func (s *Store) PendingOutboxCount(ctx context.Context) (int64, error) {
	var c int64
	err := s.db.QueryRowContext(ctx, `select count(*) from mesh_outbox where published_at is null`).Scan(&c)
	return c, err
}

func (s *Store) SavePrivateRecord(ctx context.Context, record pds.PrivateStoredRecord) error {
	_, err := s.db.ExecContext(ctx, `insert into private_records (owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at) values (?, ?, ?, ?, ?, ?, ?) on conflict (owner_did, nsid, rkey) do update set ciphertext = excluded.ciphertext, reservation_intent = excluded.reservation_intent, updated_at = excluded.updated_at`, record.OwnerDID, record.NSID, record.RKey, record.Ciphertext, record.ReservationIntent, record.CreatedAt, record.UpdatedAt)
	return err
}

func (s *Store) GetPrivateRecord(ctx context.Context, ownerDID, nsid, rkey string) (pds.PrivateStoredRecord, error) {
	var r pds.PrivateStoredRecord
	err := s.db.QueryRowContext(ctx, `select owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at from private_records where owner_did = ? and nsid = ? and rkey = ?`, ownerDID, nsid, rkey).Scan(&r.OwnerDID, &r.NSID, &r.RKey, &r.Ciphertext, &r.ReservationIntent, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) { return pds.PrivateStoredRecord{}, pds.ErrNotFound }
	return r, err
}

func (s *Store) ListPrivateRecordsByReservation(ctx context.Context, ownerDID, intentID string) ([]pds.PrivateStoredRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select owner_did, nsid, rkey, ciphertext, reservation_intent, created_at, updated_at from private_records where owner_did = ? and reservation_intent = ? order by nsid, rkey`, ownerDID, intentID)
	if err != nil { return nil, err }
	defer rows.Close()
	var recs []pds.PrivateStoredRecord
	for rows.Next() {
		var r pds.PrivateStoredRecord
		if err := rows.Scan(&r.OwnerDID, &r.NSID, &r.RKey, &r.Ciphertext, &r.ReservationIntent, &r.CreatedAt, &r.UpdatedAt); err != nil { return nil, err }
		recs = append(recs, r)
	}
	return recs, rows.Err()
}

func (s *Store) SaveConsentGrant(ctx context.Context, grant pds.StoredConsentGrant) error {
	scopes, _ := json.Marshal(grant.Scopes)
	_, err := s.db.ExecContext(ctx, `insert into private_consent_grants (owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) on conflict (owner_did, grant_id) do update set grantee_did = excluded.grantee_did, resource = excluded.resource, scopes = excluded.scopes, expires_at = excluded.expires_at, revoked_at = excluded.revoked_at, ciphertext = excluded.ciphertext, signature = excluded.signature, updated_at = excluded.updated_at`, grant.OwnerDID, grant.GrantID, grant.GranteeDID, grant.Resource, string(scopes), grant.ExpiresAt, grant.RevokedAt, grant.Ciphertext, grant.Signature, grant.CreatedAt, grant.UpdatedAt)
	return err
}

func (s *Store) GetConsentGrant(ctx context.Context, ownerDID, grantID string) (pds.StoredConsentGrant, error) {
	var g pds.StoredConsentGrant
	var scopes string
	err := s.db.QueryRowContext(ctx, `select owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at from private_consent_grants where owner_did = ? and grant_id = ?`, ownerDID, grantID).Scan(&g.OwnerDID, &g.GrantID, &g.GranteeDID, &g.Resource, &scopes, &g.ExpiresAt, &g.RevokedAt, &g.Ciphertext, &g.Signature, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) { return pds.StoredConsentGrant{}, pds.ErrNotFound }
	if err != nil { return pds.StoredConsentGrant{}, err }
	_ = json.Unmarshal([]byte(scopes), &g.Scopes)
	return g, nil
}

func (s *Store) ListConsentGrants(ctx context.Context, ownerDID string) ([]pds.StoredConsentGrant, error) {
	rows, err := s.db.QueryContext(ctx, `select owner_did, grant_id, grantee_did, resource, scopes, expires_at, revoked_at, ciphertext, signature, created_at, updated_at from private_consent_grants where owner_did = ? order by created_at asc`, ownerDID)
	if err != nil { return nil, err }
	defer rows.Close()
	var grants []pds.StoredConsentGrant
	for rows.Next() {
		var g pds.StoredConsentGrant
		var scopes string
		if err := rows.Scan(&g.OwnerDID, &g.GrantID, &g.GranteeDID, &g.Resource, &scopes, &g.ExpiresAt, &g.RevokedAt, &g.Ciphertext, &g.Signature, &g.CreatedAt, &g.UpdatedAt); err != nil { return nil, err }
		_ = json.Unmarshal([]byte(scopes), &g.Scopes)
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

func (s *Store) RevokeConsentGrant(ctx context.Context, ownerDID, grantID string, revokedAt time.Time) error {
	r, err := s.db.ExecContext(ctx, `update private_consent_grants set revoked_at = ?, updated_at = ? where owner_did = ? and grant_id = ?`, revokedAt, revokedAt, ownerDID, grantID)
	if err != nil { return err }
	n, _ := r.RowsAffected()
	if n == 0 { return pds.ErrNotFound }
	return nil
}
