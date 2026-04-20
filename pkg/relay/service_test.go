package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"openbooking/pkg/did"
	"openbooking/pkg/protocol"
)

type memoryStore struct {
	events []protocol.RelayCommitEnvelope
	heads  map[string]protocol.Commit
}

func (m *memoryStore) EnsureSchema(context.Context) error { return nil }
func (m *memoryStore) Ping(context.Context) error         { return nil }
func (m *memoryStore) LastActorCommit(_ context.Context, actorDID string) (*protocol.Commit, error) {
	if m.heads == nil {
		return nil, nil
	}
	commit, ok := m.heads[actorDID]
	if !ok {
		return nil, nil
	}
	return &commit, nil
}
func (m *memoryStore) SaveEvent(_ context.Context, envelope protocol.RelayCommitEnvelope) (bool, uint64, error) {
	m.events = append(m.events, envelope)
	if m.heads == nil {
		m.heads = map[string]protocol.Commit{}
	}
	m.heads[envelope.Commit.ActorDID] = envelope.Commit
	return true, uint64(len(m.events)), nil
}
func (m *memoryStore) ListEventsAfter(_ context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error) {
	var out []protocol.RelayCommitEnvelope
	for _, event := range m.events {
		if event.RelaySequence > after {
			out = append(out, event)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *memoryStore) GetActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	return m.LastActorCommit(ctx, actorDID)
}
func (m *memoryStore) LastRelaySequence(context.Context) (uint64, error) {
	return uint64(len(m.events)), nil
}
func (m *memoryStore) GetConsumerCheckpoint(context.Context, string) (uint64, error) {
	return 0, nil
}
func (m *memoryStore) SetConsumerCheckpoint(context.Context, string, uint64) error {
	return nil
}
func (m *memoryStore) LastConsumerCheckpoint(context.Context) (uint64, error) {
	return 0, nil
}

type publisher struct{ subjects []string }

func (p *publisher) Publish(_ context.Context, subject string, _ []byte) error {
	p.subjects = append(p.subjects, subject)
	return nil
}

type resolver struct{ doc did.Document }

func (r resolver) Resolve(context.Context, string) (did.Document, error) { return r.doc, nil }

func TestRelayIngestCommit(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	publisher := &publisher{}
	service, err := NewService(context.Background(), "did:web:relay.example", store, publisher, resolver{doc: did.Document{
		ID: "did:web:provider.example",
		VerificationMethod: []did.VerificationMethod{{
			ID:         "did:web:provider.example#sig",
			Type:       "JsonWebKey2020",
			Controller: "did:web:provider.example",
			PublicKeyJWK: &did.JWK{
				Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub),
			},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	commit := protocol.Commit{
		ActorDID:  "did:web:provider.example",
		Revision:  1,
		RootCID:   "root1",
		CreatedAt: time.Unix(1, 0).UTC(),
		KeyID:     "did:web:provider.example#sig",
	}
	payload, _ := commit.SigningBytes()
	commit.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	envelope, err := service.IngestCommit(context.Background(), commit, "OBP.REPO.x.COMMIT", 7, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.RelaySequence != 1 {
		t.Fatalf("unexpected relay sequence: %d", envelope.RelaySequence)
	}
	if len(publisher.subjects) != 2 {
		t.Fatalf("expected relay publish and checkpoint, got %#v", publisher.subjects)
	}
}
