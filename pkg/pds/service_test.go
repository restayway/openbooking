package pds

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restayway/openbooking/pkg/did"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/privdata"
	"github.com/restayway/openbooking/pkg/protocol"
)

type memoryStore struct {
	records        map[string]protocol.Record
	commits        []protocol.Commit
	outbox         []OutboxMessage
	privateRecords map[string]PrivateStoredRecord
	consentGrants  map[string]StoredConsentGrant
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		records:        map[string]protocol.Record{},
		privateRecords: map[string]PrivateStoredRecord{},
		consentGrants:  map[string]StoredConsentGrant{},
	}
}

func (m *memoryStore) EnsureSchema(context.Context) error { return nil }
func (m *memoryStore) Ping(context.Context) error         { return nil }

func (m *memoryStore) LoadRepository(context.Context, string) (RepositoryState, error) {
	records := make([]protocol.Record, 0, len(m.records))
	for _, record := range m.records {
		records = append(records, record)
	}
	var head *protocol.Commit
	var revision uint64
	var lastRoot string
	if len(m.commits) > 0 {
		copyHead := m.commits[len(m.commits)-1]
		head = &copyHead
		revision = copyHead.Revision
		lastRoot = copyHead.RootCID
	}
	return RepositoryState{Records: records, Head: head, Revision: revision, LastRoot: lastRoot}, nil
}

func (m *memoryStore) SaveCommit(_ context.Context, commit protocol.Commit, snapshot []protocol.Record) error {
	m.commits = append(m.commits, commit)
	m.records = map[string]protocol.Record{}
	for _, record := range snapshot {
		m.records[record.ID.URI()] = record
	}
	payload, _ := json.Marshal(commit)
	m.outbox = append(m.outbox, OutboxMessage{
		ID:      int64(len(m.outbox) + 1),
		Subject: commit.Subject(),
		Payload: payload,
	})
	return nil
}

func (m *memoryStore) GetRecord(_ context.Context, actorDID, nsid, rkey string) (protocol.Record, error) {
	record, ok := m.records[protocol.RecordID{ActorDID: actorDID, NSID: nsid, RKey: rkey}.URI()]
	if !ok {
		return protocol.Record{}, ErrNotFound
	}
	return record, nil
}

func (m *memoryStore) ListCommitsAfter(_ context.Context, actorDID string, afterRevision uint64, limit int) ([]protocol.Commit, error) {
	out := make([]protocol.Commit, 0)
	for _, commit := range m.commits {
		if commit.ActorDID != actorDID || commit.Revision <= afterRevision {
			continue
		}
		out = append(out, commit)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memoryStore) ListPendingOutbox(_ context.Context, limit int) ([]OutboxMessage, error) {
	if limit > len(m.outbox) {
		limit = len(m.outbox)
	}
	out := make([]OutboxMessage, limit)
	copy(out, m.outbox[:limit])
	return out, nil
}

func (m *memoryStore) MarkOutboxPublished(_ context.Context, id int64, _ time.Time) error {
	filtered := m.outbox[:0]
	for _, item := range m.outbox {
		if item.ID != id {
			filtered = append(filtered, item)
		}
	}
	m.outbox = filtered
	return nil
}

func (m *memoryStore) PendingOutboxCount(_ context.Context) (int64, error) {
	return int64(len(m.outbox)), nil
}

func (m *memoryStore) SavePrivateRecord(_ context.Context, record PrivateStoredRecord) error {
	m.privateRecords[privateKey(record.OwnerDID, record.NSID, record.RKey)] = record
	return nil
}

func (m *memoryStore) GetPrivateRecord(_ context.Context, ownerDID, nsid, rkey string) (PrivateStoredRecord, error) {
	record, ok := m.privateRecords[privateKey(ownerDID, nsid, rkey)]
	if !ok {
		return PrivateStoredRecord{}, ErrNotFound
	}
	return record, nil
}

func (m *memoryStore) ListPrivateRecordsByReservation(_ context.Context, ownerDID, intentID string) ([]PrivateStoredRecord, error) {
	out := make([]PrivateStoredRecord, 0)
	for _, record := range m.privateRecords {
		if record.OwnerDID == ownerDID && record.ReservationIntent == intentID {
			out = append(out, record)
		}
	}
	return out, nil
}

func (m *memoryStore) SaveConsentGrant(_ context.Context, grant StoredConsentGrant) error {
	m.consentGrants[grantKey(grant.OwnerDID, grant.GrantID)] = grant
	return nil
}

func (m *memoryStore) GetConsentGrant(_ context.Context, ownerDID, grantID string) (StoredConsentGrant, error) {
	grant, ok := m.consentGrants[grantKey(ownerDID, grantID)]
	if !ok {
		return StoredConsentGrant{}, ErrNotFound
	}
	return grant, nil
}

func (m *memoryStore) ListConsentGrants(_ context.Context, ownerDID string) ([]StoredConsentGrant, error) {
	out := make([]StoredConsentGrant, 0)
	for _, grant := range m.consentGrants {
		if grant.OwnerDID == ownerDID {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (m *memoryStore) RevokeConsentGrant(_ context.Context, ownerDID, grantID string, revokedAt time.Time) error {
	key := grantKey(ownerDID, grantID)
	grant, ok := m.consentGrants[key]
	if !ok {
		return ErrNotFound
	}
	grant.RevokedAt = &revokedAt
	m.consentGrants[key] = grant
	return nil
}

type recordingPublisher struct {
	subjects []string
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, _ []byte) error {
	p.subjects = append(p.subjects, subject)
	return nil
}

type staticResolver struct {
	doc did.Document
}

func (s staticResolver) Resolve(context.Context, string) (did.Document, error) {
	return s.doc, nil
}

type edSigner struct {
	privateKey ed25519.PrivateKey
}

func (s edSigner) Sign(_ context.Context, message []byte, _ string) ([]byte, error) {
	return ed25519.Sign(s.privateKey, message), nil
}

type passthroughCipher struct{}

func (passthroughCipher) Encrypt(_ context.Context, plaintext []byte, keyID string) (string, error) {
	return keyID + ":" + base64.StdEncoding.EncodeToString(plaintext), nil
}

func (passthroughCipher) Decrypt(_ context.Context, ciphertext, keyID string) ([]byte, error) {
	prefix := keyID + ":"
	if !strings.HasPrefix(ciphertext, prefix) {
		return nil, fmt.Errorf("ciphertext key mismatch")
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, prefix))
}

func TestServicePutRecordAndPublishOutbox(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	store := newMemoryStore()
	publisher := &recordingPublisher{}

	service, err := NewService(context.Background(), Config{
		ActorDID: "did:web:provider.example",
		Role:     RoleProvider,
		KeyID:    "did:web:provider.example#sig",
	}, store, publisher, edSigner{privateKey: privateKey}, passthroughCipher{}, staticResolver{
		doc: did.Document{
			ID: "did:web:provider.example",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:provider.example#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:provider.example",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP",
					Crv: "Ed25519",
					X:   base64.RawURLEncoding.EncodeToString(publicKey),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	payload := json.RawMessage(`{"name":"Harbor Hotel","currency":"EUR","locationRef":"geo:41.0,29.0"}`)
	commit, record, err := service.PutRecord(context.Background(), RecordWriteInput{
		ActorDID: "did:web:provider.example",
		NSID:     lexicon.NSIDCatalogListing,
		RKey:     "listing-1",
		Value:    payload,
	}, time.Unix(1713571200, 0))
	if err != nil {
		t.Fatalf("put record: %v", err)
	}
	if commit.Revision != 1 {
		t.Fatalf("unexpected revision: %d", commit.Revision)
	}
	if record.ID.RKey != "listing-1" {
		t.Fatalf("unexpected record key: %s", record.ID.RKey)
	}
	if _, err := service.PublishPendingOutbox(context.Background(), 10); err != nil {
		t.Fatalf("publish outbox: %v", err)
	}
	if len(publisher.subjects) != 1 || publisher.subjects[0] != commit.Subject() {
		t.Fatalf("unexpected published subjects: %#v", publisher.subjects)
	}
}

func TestServiceProcessIntentCommitsAndDeduplicates(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	store := newMemoryStore()
	service, err := NewService(context.Background(), Config{
		ActorDID: "did:web:provider.example",
		Role:     RoleProvider,
		KeyID:    "did:web:provider.example#sig",
	}, store, &recordingPublisher{}, edSigner{privateKey: privateKey}, passthroughCipher{}, staticResolver{
		doc: did.Document{
			ID: "did:web:provider.example",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:provider.example#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:provider.example",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP",
					Crv: "Ed25519",
					X:   base64.RawURLEncoding.EncodeToString(publicKey),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	listingURI := "ob://did:web:provider.example/com.openbooking.catalog.listing/listing-1"
	ratePlanURI := "ob://did:web:provider.example/com.openbooking.catalog.ratePlan/rate-1"
	if _, _, err := service.PutRecord(context.Background(), RecordWriteInput{
		ActorDID: "did:web:provider.example",
		NSID:     lexicon.NSIDOfferQuote,
		RKey:     "offer-1",
		Value: json.RawMessage(`{
			"listingUri":"` + listingURI + `",
			"ratePlanUri":"` + ratePlanURI + `",
			"inventoryVersion":1,
			"commercialHash":"commercial",
			"policyHash":"policy",
			"expiresAt":"2024-04-20T01:00:00Z"
		}`),
	}, time.Unix(1713570000, 0)); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	if _, _, err := service.PutRecord(context.Background(), RecordWriteInput{
		ActorDID: "did:web:provider.example",
		NSID:     lexicon.NSIDInventoryAvail,
		RKey:     "avail-1",
		Value: json.RawMessage(`{
			"listingUri":"` + listingURI + `",
			"ratePlanUri":"` + ratePlanURI + `",
			"inventoryVersion":1,
			"startDate":"2024-04-20T00:00:00Z",
			"endDate":"2024-04-21T00:00:00Z",
			"availableUnits":1
		}`),
	}, time.Unix(1713570001, 0)); err != nil {
		t.Fatalf("seed availability: %v", err)
	}

	intent := lexicon.ReservationIntent{
		IntentID:     "intent-1",
		ProviderDID:  "did:web:provider.example",
		CustomerDID:  "did:web:guest.example",
		OfferURI:     "ob://did:web:provider.example/com.openbooking.offer.quote/offer-1",
		EnvelopeHash: "hash-1",
		RequestedAt:  time.Unix(1713571200, 0),
	}
	first, err := service.ProcessIntent(context.Background(), intent, time.Unix(1713571201, 0))
	if err != nil {
		t.Fatalf("first process: %v", err)
	}
	if first.Duplicate {
		t.Fatal("first process marked duplicate")
	}
	if first.Decision != IntentDecisionCommit {
		t.Fatalf("unexpected first decision: %s", first.Decision)
	}
	if first.RepoCommit.Revision != 3 {
		t.Fatalf("unexpected first revision: %d", first.RepoCommit.Revision)
	}
	if first.AvailabilityRecord == nil {
		t.Fatal("expected updated availability")
	}
	second, err := service.ProcessIntent(context.Background(), intent, time.Unix(1713571202, 0))
	if err != nil {
		t.Fatalf("second process: %v", err)
	}
	if !second.Duplicate {
		t.Fatal("expected duplicate intent")
	}
	if second.RepoCommit.Revision != 0 {
		t.Fatalf("duplicate intent should not create commit: %#v", second.RepoCommit)
	}
	if len(store.commits) != 3 {
		t.Fatalf("expected 3 commits including seed data, got %d", len(store.commits))
	}
}

func TestHTTPRejectsWrongActorDID(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	service, err := NewService(context.Background(), Config{
		ActorDID: "did:web:provider.example",
		Role:     RoleProvider,
		KeyID:    "did:web:provider.example#sig",
	}, newMemoryStore(), &recordingPublisher{}, edSigner{privateKey: privateKey}, passthroughCipher{}, staticResolver{
		doc: did.Document{
			ID: "did:web:provider.example",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:provider.example#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:provider.example",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP",
					Crv: "Ed25519",
					X:   base64.RawURLEncoding.EncodeToString(publicKey),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	body := `{"actorDid":"did:web:other.example","nsid":"com.openbooking.catalog.listing","rkey":"listing-1","value":{"name":"Hotel","currency":"EUR","locationRef":"geo:1,1"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/records", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPrivateProfileAndConsentAccess(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	service, err := NewService(context.Background(), Config{
		ActorDID:        "did:web:guest.example",
		Role:            RoleGuest,
		KeyID:           "did:web:guest.example#sig",
		EncryptionKeyID: "enc-key",
	}, newMemoryStore(), &recordingPublisher{}, edSigner{privateKey: privateKey}, passthroughCipher{}, staticResolver{
		doc: did.Document{
			ID: "did:web:guest.example",
			VerificationMethod: []did.VerificationMethod{{
				ID:         "did:web:guest.example#sig",
				Type:       "JsonWebKey2020",
				Controller: "did:web:guest.example",
				PublicKeyJWK: &did.JWK{
					Kty: "OKP",
					Crv: "Ed25519",
					X:   base64.RawURLEncoding.EncodeToString(publicKey),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	_, err = service.PutPrivateProfile(context.Background(), "did:web:guest.example", PrivateProfileInput{
		Profile:    privdata.Profile{GivenName: "Ada", FamilyName: "Lovelace", PreferredLanguage: "en"},
		Contact:    privdata.Contact{Email: "ada@example.com"},
		Preference: privdata.Preference{Locale: "en-GB", SpecialRequests: []string{"quiet-room"}},
	}, time.Unix(1713571200, 0))
	if err != nil {
		t.Fatalf("put private profile: %v", err)
	}
	_, err = service.PutReservationLink(context.Background(), "did:web:guest.example", "intent-1", ReservationLinkInput{
		ProviderDID: "did:web:provider.example",
		OfferURI:    "ob://did:web:provider.example/com.openbooking.offer.quote/offer-1",
	}, time.Unix(1713571201, 0))
	if err != nil {
		t.Fatalf("put reservation link: %v", err)
	}
	grant, err := service.CreateConsentGrant(context.Background(), "did:web:guest.example", ConsentGrantInput{
		GranteeDID: "did:web:provider.example",
		Resource:   "reservation:intent-1",
		Scopes:     []string{privdata.ScopeReservationRead},
		ExpiresAt:  time.Unix(1713574800, 0),
	}, time.Unix(1713571202, 0))
	if err != nil {
		t.Fatalf("create consent grant: %v", err)
	}

	bundle, err := service.GetPrivateReservation(context.Background(), "did:web:provider.example", "intent-1", time.Unix(1713571203, 0))
	if err != nil {
		t.Fatalf("get reservation via consent: %v", err)
	}
	if bundle.Profile.GivenName != "Ada" {
		t.Fatalf("unexpected profile bundle: %#v", bundle)
	}

	if err := service.RevokeConsentGrant(context.Background(), "did:web:guest.example", grant.GrantID, time.Unix(1713571204, 0)); err != nil {
		t.Fatalf("revoke consent: %v", err)
	}
	if _, err := service.GetPrivateReservation(context.Background(), "did:web:provider.example", "intent-1", time.Unix(1713571205, 0)); err == nil {
		t.Fatal("expected access denial after revocation")
	}
}

func privateKey(ownerDID, nsid, rkey string) string {
	return ownerDID + "|" + nsid + "|" + rkey
}

func grantKey(ownerDID, grantID string) string {
	return ownerDID + "|" + grantID
}
