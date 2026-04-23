package protocol

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

const (
	SubjectPrefixIdentity = "OBP.IDENTITY"
	SubjectPrefixRepo     = "OBP.REPO"
	SubjectPrefixIntent   = "OBP.INTENT"
	SubjectPrefixRelay    = "OBP.RELAY"
)

var nsidPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)+$`)

type OperationAction string

const (
	OperationPut    OperationAction = "put"
	OperationDelete OperationAction = "delete"
)

type RecordID struct {
	ActorDID string `json:"actorDid"`
	NSID     string `json:"nsid"`
	RKey     string `json:"rkey"`
}

func (r RecordID) URI() string {
	return fmt.Sprintf("ob://%s/%s/%s", r.ActorDID, r.NSID, r.RKey)
}

func ParseRecordURI(raw string) (RecordID, error) {
	const prefix = "ob://"
	if !strings.HasPrefix(raw, prefix) {
		return RecordID{}, fmt.Errorf("invalid record uri scheme")
	}
	rest := strings.TrimPrefix(raw, prefix)
	lastSlash := strings.LastIndex(rest, "/")
	prevSlash := strings.LastIndex(rest[:lastSlash], "/")
	if lastSlash <= 0 || prevSlash <= 0 {
		return RecordID{}, fmt.Errorf("invalid record uri: %s", raw)
	}
	id := RecordID{
		ActorDID: rest[:prevSlash],
		NSID:     rest[prevSlash+1 : lastSlash],
		RKey:     rest[lastSlash+1:],
	}
	if err := id.Validate(); err != nil {
		return RecordID{}, err
	}
	return id, nil
}

func (r RecordID) Validate() error {
	if r.ActorDID == "" {
		return fmt.Errorf("actor DID is required")
	}
	if !nsidPattern.MatchString(r.NSID) {
		return fmt.Errorf("invalid nsid: %s", r.NSID)
	}
	if r.RKey == "" || strings.Contains(r.RKey, "/") {
		return fmt.Errorf("invalid rkey")
	}
	return nil
}

type Record struct {
	ID        RecordID        `json:"id"`
	Value     json.RawMessage `json:"value"`
	CreatedAt time.Time       `json:"createdAt"`
	Hash      string          `json:"hash"`
}

func NewRecord(id RecordID, value any, createdAt time.Time) (Record, error) {
	if err := id.Validate(); err != nil {
		return Record{}, err
	}
	raw, err := canonicalJSON(value)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(raw)
	cid, err := EncodeSHA256CID(sum)
	if err != nil {
		return Record{}, fmt.Errorf("record hash: %w", err)
	}
	return Record{
		ID:        id,
		Value:     raw,
		CreatedAt: createdAt.UTC(),
		Hash:      cid,
	}, nil
}

type Operation struct {
	Action OperationAction `json:"action"`
	Record Record          `json:"record"`
}

func (o Operation) Validate() error {
	if o.Action != OperationPut && o.Action != OperationDelete {
		return fmt.Errorf("invalid operation action: %s", o.Action)
	}
	return o.Record.ID.Validate()
}

type Commit struct {
	ActorDID  string      `json:"actorDid"`
	Revision  uint64      `json:"revision"`
	Previous  string      `json:"previous,omitempty"`
	RootCID   string      `json:"rootCid"`
	CreatedAt time.Time   `json:"createdAt"`
	Ops       []Operation `json:"ops"`
	KeyID     string      `json:"keyId"`
	Signature string      `json:"signature,omitempty"`
}

func (c Commit) Subject() string {
	return RepoCommitSubject(c.ActorDID)
}

func (c Commit) SigningBytes() ([]byte, error) {
	return canonicalJSON(struct {
		ActorDID  string      `json:"actorDid"`
		Revision  uint64      `json:"revision"`
		Previous  string      `json:"previous,omitempty"`
		RootCID   string      `json:"rootCid"`
		CreatedAt time.Time   `json:"createdAt"`
		Ops       []Operation `json:"ops"`
		KeyID     string      `json:"keyId"`
	}{
		ActorDID:  c.ActorDID,
		Revision:  c.Revision,
		Previous:  c.Previous,
		RootCID:   c.RootCID,
		CreatedAt: c.CreatedAt.UTC(),
		Ops:       c.Ops,
		KeyID:     c.KeyID,
	})
}

func IdentitySubject(actorDID string) string {
	return fmt.Sprintf("%s.%s", SubjectPrefixIdentity, sanitizeDID(actorDID))
}

func RepoCommitSubject(actorDID string) string {
	return fmt.Sprintf("%s.%s.COMMIT", SubjectPrefixRepo, sanitizeDID(actorDID))
}

func IntentSubject(providerDID string) string {
	return fmt.Sprintf("%s.%s", SubjectPrefixIntent, sanitizeDID(providerDID))
}

func RelayCheckpointSubject(relayDID string) string {
	return fmt.Sprintf("%s.%s.CHECKPOINT", SubjectPrefixRelay, sanitizeDID(relayDID))
}

func RelayCommitSubject(relayDID string) string {
	return fmt.Sprintf("%s.%s.COMMIT", SubjectPrefixRelay, sanitizeDID(relayDID))
}

type RelayCommitEnvelope struct {
	RelayDID       string    `json:"relayDid"`
	RelaySequence  uint64    `json:"relaySequence"`
	SourceSubject  string    `json:"sourceSubject"`
	SourceSequence uint64    `json:"sourceSequence"`
	ValidatedAt    time.Time `json:"validatedAt"`
	Commit         Commit    `json:"commit"`
}

func sanitizeDID(did string) string {
	replacer := strings.NewReplacer(":", "_", "/", "_", ".", "_", "%", "_")
	return replacer.Replace(did)
}

func canonicalJSON(v any) ([]byte, error) {
	// canonicalJSON produces deterministic JSON (RFC 8785 / JCS) for cross-platform hash stability.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(raw)
}
