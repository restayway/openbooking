package repo

import (
	"encoding/base64"
	"fmt"
	"sort"
	"time"

	"openbooking/pkg/mst"
	"openbooking/pkg/protocol"
)

type Signer interface {
	Sign(message []byte, keyID string) ([]byte, error)
}

type Repository struct {
	ActorDID string
	KeyID    string
	records  map[string]protocol.Record
	lastCID  string
	revision uint64
}

func New(actorDID, keyID string) *Repository {
	return &Repository{
		ActorDID: actorDID,
		KeyID:    keyID,
		records:  make(map[string]protocol.Record),
	}
}

func (r *Repository) Restore(records []protocol.Record, lastCID string, revision uint64) {
	r.records = make(map[string]protocol.Record, len(records))
	for _, record := range records {
		r.records[record.ID.URI()] = record
	}
	r.lastCID = lastCID
	r.revision = revision
}

func (r *Repository) State() ([]protocol.Record, string, uint64) {
	return r.Snapshot(), r.lastCID, r.revision
}

func (r *Repository) Snapshot() []protocol.Record {
	keys := make([]string, 0, len(r.records))
	for key := range r.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	records := make([]protocol.Record, 0, len(keys))
	for _, key := range keys {
		records = append(records, r.records[key])
	}
	return records
}

func (r *Repository) Apply(ops []protocol.Operation, now time.Time, signer Signer) (protocol.Commit, error) {
	if len(ops) == 0 {
		return protocol.Commit{}, fmt.Errorf("at least one operation is required")
	}
	for _, op := range ops {
		if err := op.Validate(); err != nil {
			return protocol.Commit{}, err
		}
		switch op.Action {
		case protocol.OperationPut:
			r.records[op.Record.ID.URI()] = op.Record
		case protocol.OperationDelete:
			delete(r.records, op.Record.ID.URI())
		}
	}

	tree, err := mst.New(r.Snapshot())
	if err != nil {
		return protocol.Commit{}, err
	}
	commit := protocol.Commit{
		ActorDID:  r.ActorDID,
		Revision:  r.revision + 1,
		Previous:  r.lastCID,
		RootCID:   tree.RootCID(),
		CreatedAt: now.UTC(),
		Ops:       ops,
		KeyID:     r.KeyID,
	}
	if signer != nil {
		payload, err := commit.SigningBytes()
		if err != nil {
			return protocol.Commit{}, err
		}
		sig, err := signer.Sign(payload, r.KeyID)
		if err != nil {
			return protocol.Commit{}, err
		}
		commit.Signature = base64.StdEncoding.EncodeToString(sig)
	}
	r.revision = commit.Revision
	r.lastCID = commit.RootCID
	return commit, nil
}

func ValidateCommitSequence(previous, next protocol.Commit) error {
	if previous.ActorDID != next.ActorDID {
		return fmt.Errorf("actor DID mismatch")
	}
	if next.Revision != previous.Revision+1 {
		return fmt.Errorf("invalid revision sequence")
	}
	if next.Previous != previous.RootCID {
		return fmt.Errorf("previous root mismatch")
	}
	return nil
}
