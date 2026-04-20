package pds

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"openbooking/pkg/did"
	"openbooking/pkg/lexicon"
	"openbooking/pkg/privdata"
	"openbooking/pkg/protocol"
	"openbooking/pkg/repo"
)

var ErrNotFound = errors.New("not found")

type Role string

const (
	RoleGuest    Role = "guest"
	RoleProvider Role = "provider"
)

type Store interface {
	EnsureSchema(ctx context.Context) error
	LoadRepository(ctx context.Context, actorDID string) (RepositoryState, error)
	SaveCommit(ctx context.Context, commit protocol.Commit, snapshot []protocol.Record) error
	GetRecord(ctx context.Context, actorDID, nsid, rkey string) (protocol.Record, error)
	ListCommitsAfter(ctx context.Context, actorDID string, afterRevision uint64, limit int) ([]protocol.Commit, error)
	ListPendingOutbox(ctx context.Context, limit int) ([]OutboxMessage, error)
	MarkOutboxPublished(ctx context.Context, id int64, publishedAt time.Time) error
	Ping(ctx context.Context) error
	PendingOutboxCount(ctx context.Context) (int64, error)
	SavePrivateRecord(ctx context.Context, record PrivateStoredRecord) error
	GetPrivateRecord(ctx context.Context, ownerDID, nsid, rkey string) (PrivateStoredRecord, error)
	ListPrivateRecordsByReservation(ctx context.Context, ownerDID, intentID string) ([]PrivateStoredRecord, error)
	SaveConsentGrant(ctx context.Context, grant StoredConsentGrant) error
	GetConsentGrant(ctx context.Context, ownerDID, grantID string) (StoredConsentGrant, error)
	ListConsentGrants(ctx context.Context, ownerDID string) ([]StoredConsentGrant, error)
	RevokeConsentGrant(ctx context.Context, ownerDID, grantID string, revokedAt time.Time) error
}

type Publisher interface {
	Publish(ctx context.Context, subject string, payload []byte) error
}

type Signer interface {
	Sign(ctx context.Context, message []byte, keyID string) ([]byte, error)
}

type Cipher interface {
	Encrypt(ctx context.Context, plaintext []byte, keyID string) (string, error)
	Decrypt(ctx context.Context, ciphertext, keyID string) ([]byte, error)
}

type Resolver interface {
	Resolve(ctx context.Context, did string) (did.Document, error)
}

type RepositoryState struct {
	Records  []protocol.Record
	Head     *protocol.Commit
	Revision uint64
	LastRoot string
}

type OutboxMessage struct {
	ID        int64
	Subject   string
	Payload   []byte
	CreatedAt time.Time
}

type Config struct {
	ActorDID        string
	Role            Role
	KeyID           string
	EncryptionKeyID string
}

type Service struct {
	config    Config
	store     Store
	publisher Publisher
	signer    Signer
	cipher    Cipher
	resolver  Resolver

	mu   sync.Mutex
	repo *repo.Repository
	head *protocol.Commit
}

type IntentDecision string

const (
	IntentDecisionCommit IntentDecision = "commit"
	IntentDecisionReject IntentDecision = "reject"
)

type RecordWriteInput struct {
	ActorDID string          `json:"actorDid"`
	NSID     string          `json:"nsid"`
	RKey     string          `json:"rkey"`
	Value    json.RawMessage `json:"value"`
}

type IntentResult struct {
	Duplicate          bool             `json:"duplicate"`
	Decision           IntentDecision   `json:"decision"`
	RepoCommit         protocol.Commit  `json:"repoCommit"`
	IntentRecord       protocol.Record  `json:"intentRecord"`
	TerminalRecord     protocol.Record  `json:"terminalRecord"`
	AvailabilityRecord *protocol.Record `json:"availabilityRecord,omitempty"`
}

type PrivateStoredRecord struct {
	OwnerDID          string
	NSID              string
	RKey              string
	Ciphertext        string
	ReservationIntent string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type StoredConsentGrant struct {
	OwnerDID   string
	GrantID    string
	GranteeDID string
	Resource   string
	Scopes     []string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	Ciphertext string
	Signature  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type PrivateProfileInput struct {
	Profile    privdata.Profile    `json:"profile"`
	Contact    privdata.Contact    `json:"contact"`
	Preference privdata.Preference `json:"preference"`
}

type ReservationLinkInput struct {
	ProviderDID string `json:"providerDid"`
	OfferURI    string `json:"offerUri"`
}

type ConsentGrantInput struct {
	GrantID    string    `json:"grantId,omitempty"`
	GranteeDID string    `json:"granteeDid"`
	Resource   string    `json:"resource"`
	Scopes     []string  `json:"scopes"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func NewService(ctx context.Context, cfg Config, store Store, publisher Publisher, signer Signer, cipher Cipher, resolver Resolver) (*Service, error) {
	if cfg.ActorDID == "" {
		return nil, fmt.Errorf("actor DID is required")
	}
	if cfg.KeyID == "" {
		return nil, fmt.Errorf("key ID is required")
	}
	if cfg.Role != RoleGuest && cfg.Role != RoleProvider {
		return nil, fmt.Errorf("unsupported role: %s", cfg.Role)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	if resolver != nil {
		doc, err := resolver.Resolve(ctx, cfg.ActorDID)
		if err != nil {
			return nil, fmt.Errorf("resolve actor DID: %w", err)
		}
		if err := did.HasVerificationMethod(doc, cfg.KeyID); err != nil {
			return nil, fmt.Errorf("local signing key not present in DID document: %w", err)
		}
	}
	state, err := store.LoadRepository(ctx, cfg.ActorDID)
	if err != nil {
		return nil, err
	}

	r := repo.New(cfg.ActorDID, cfg.KeyID)
	r.Restore(state.Records, state.LastRoot, state.Revision)

	return &Service{
		config:    cfg,
		store:     store,
		publisher: publisher,
		signer:    signer,
		cipher:    cipher,
		resolver:  resolver,
		repo:      r,
		head:      state.Head,
	}, nil
}

func (s *Service) Config() Config {
	return s.config
}

func (s *Service) Head() *protocol.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.head == nil {
		return nil
	}
	head := *s.head
	return &head
}

func (s *Service) GetRecord(ctx context.Context, nsid, rkey string) (protocol.Record, error) {
	return s.store.GetRecord(ctx, s.config.ActorDID, nsid, rkey)
}

func (s *Service) ListCommits(ctx context.Context, afterRevision uint64, limit int) ([]protocol.Commit, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.store.ListCommitsAfter(ctx, s.config.ActorDID, afterRevision, limit)
}

func (s *Service) PutRecord(ctx context.Context, input RecordWriteInput, now time.Time) (protocol.Commit, protocol.Record, error) {
	if input.ActorDID != s.config.ActorDID {
		return protocol.Commit{}, protocol.Record{}, fmt.Errorf("actor DID mismatch")
	}
	if err := lexicon.ValidateRecord(input.NSID, input.Value); err != nil {
		return protocol.Commit{}, protocol.Record{}, err
	}

	record, err := protocol.NewRecord(protocol.RecordID{
		ActorDID: input.ActorDID,
		NSID:     input.NSID,
		RKey:     input.RKey,
	}, json.RawMessage(input.Value), now)
	if err != nil {
		return protocol.Commit{}, protocol.Record{}, err
	}

	commit, err := s.applyCommit(ctx, []protocol.Operation{{
		Action: protocol.OperationPut,
		Record: record,
	}}, now)
	return commit, record, err
}

func (s *Service) DeleteRecord(ctx context.Context, nsid, rkey string, now time.Time) (protocol.Commit, error) {
	current, err := s.GetRecord(ctx, nsid, rkey)
	if err != nil {
		return protocol.Commit{}, err
	}
	return s.applyCommit(ctx, []protocol.Operation{{
		Action: protocol.OperationDelete,
		Record: current,
	}}, now)
}

func (s *Service) ProcessIntent(ctx context.Context, intent lexicon.ReservationIntent, now time.Time) (IntentResult, error) {
	if s.config.Role != RoleProvider {
		return IntentResult{}, fmt.Errorf("intent intake requires provider role")
	}
	if intent.ProviderDID != s.config.ActorDID {
		return IntentResult{}, fmt.Errorf("provider DID mismatch")
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return IntentResult{}, err
	}
	if err := lexicon.ValidateRecord(lexicon.NSIDReserveIntent, raw); err != nil {
		return IntentResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	intentRecord, err := protocol.NewRecord(protocol.RecordID{
		ActorDID: s.config.ActorDID,
		NSID:     lexicon.NSIDReserveIntent,
		RKey:     intent.IntentID,
	}, json.RawMessage(raw), now)
	if err != nil {
		return IntentResult{}, err
	}

	if existing, ok := s.lookupRecordLocked(lexicon.NSIDReserveCommit, intent.IntentID); ok {
		return IntentResult{
			Duplicate:      true,
			Decision:       IntentDecisionCommit,
			IntentRecord:   s.mustRecordLocked(lexicon.NSIDReserveIntent, intent.IntentID),
			TerminalRecord: existing,
		}, nil
	}
	if existing, ok := s.lookupRecordLocked(lexicon.NSIDReserveReject, intent.IntentID); ok {
		return IntentResult{
			Duplicate:      true,
			Decision:       IntentDecisionReject,
			IntentRecord:   s.mustRecordLocked(lexicon.NSIDReserveIntent, intent.IntentID),
			TerminalRecord: existing,
		}, nil
	}

	offerID, err := protocol.ParseRecordURI(intent.OfferURI)
	if err != nil {
		return IntentResult{}, err
	}
	offerRecord, ok := s.lookupRecordLocked(offerID.NSID, offerID.RKey)
	if !ok {
		return s.writeRejectLocked(ctx, now, intentRecord, intent, lexicon.RejectCodeOfferNotFound)
	}
	var offer lexicon.OfferQuote
	if err := json.Unmarshal(offerRecord.Value, &offer); err != nil {
		return IntentResult{}, err
	}
	if now.UTC().After(offer.ExpiresAt.UTC()) {
		return s.writeRejectLocked(ctx, now, intentRecord, intent, lexicon.RejectCodeOfferExpired)
	}

	availabilityRecord, availability, ok, err := s.findAvailabilityLocked(offer.ListingURI, offer.RatePlanURI, offer.InventoryVersion)
	if err != nil {
		return IntentResult{}, err
	}
	if !ok {
		return s.writeRejectLocked(ctx, now, intentRecord, intent, lexicon.RejectCodeInventoryNotFound)
	}
	if availability.AvailableUnits <= 0 {
		return s.writeRejectLocked(ctx, now, intentRecord, intent, lexicon.RejectCodeInventoryExhausted)
	}

	availability.AvailableUnits--
	updatedAvailabilityRecord, err := protocol.NewRecord(availabilityRecord.ID, availability, now)
	if err != nil {
		return IntentResult{}, err
	}
	terminal, err := protocol.NewRecord(protocol.RecordID{
		ActorDID: s.config.ActorDID,
		NSID:     lexicon.NSIDReserveCommit,
		RKey:     intent.IntentID,
	}, lexicon.ReservationCommit{
		IntentID:         intent.IntentID,
		OfferURI:         intent.OfferURI,
		BookingReference: bookingReference(s.config.ActorDID, intent.IntentID),
		Status:           "confirmed",
		EnvelopeHash:     intent.EnvelopeHash,
		CommittedAt:      now.UTC(),
	}, now)
	if err != nil {
		return IntentResult{}, err
	}
	commit, err := s.applyCommitLocked(ctx, []protocol.Operation{
		{Action: protocol.OperationPut, Record: intentRecord},
		{Action: protocol.OperationPut, Record: terminal},
		{Action: protocol.OperationPut, Record: updatedAvailabilityRecord},
	}, now)
	if err != nil {
		return IntentResult{}, err
	}
	return IntentResult{
		Decision:           IntentDecisionCommit,
		RepoCommit:         commit,
		IntentRecord:       intentRecord,
		TerminalRecord:     terminal,
		AvailabilityRecord: &updatedAvailabilityRecord,
	}, nil
}

func (s *Service) PublishPendingOutbox(ctx context.Context, limit int) (int, error) {
	if s.publisher == nil {
		return 0, nil
	}
	messages, err := s.store.ListPendingOutbox(ctx, limit)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, message := range messages {
		if err := s.publisher.Publish(ctx, message.Subject, message.Payload); err != nil {
			return published, err
		}
		if err := s.store.MarkOutboxPublished(ctx, message.ID, time.Now().UTC()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (s *Service) PutPrivateProfile(ctx context.Context, requesterDID string, input PrivateProfileInput, now time.Time) (privdata.ProfileBundle, error) {
	if err := s.requireOwner(requesterDID); err != nil {
		return privdata.ProfileBundle{}, err
	}
	if err := s.savePrivateValue(ctx, privdata.NSIDPrivateProfile, "self", input.Profile, "", now); err != nil {
		return privdata.ProfileBundle{}, err
	}
	if err := s.savePrivateValue(ctx, privdata.NSIDPrivateContact, "self", input.Contact, "", now); err != nil {
		return privdata.ProfileBundle{}, err
	}
	if err := s.savePrivateValue(ctx, privdata.NSIDPrivatePreference, "self", input.Preference, "", now); err != nil {
		return privdata.ProfileBundle{}, err
	}
	return privdata.ProfileBundle{
		Profile:    input.Profile,
		Contact:    input.Contact,
		Preference: input.Preference,
	}, nil
}

func (s *Service) GetPrivateProfile(ctx context.Context, requesterDID string) (privdata.ProfileBundle, error) {
	if err := s.requireOwner(requesterDID); err != nil {
		return privdata.ProfileBundle{}, err
	}
	return s.loadProfileBundle(ctx)
}

func (s *Service) PutReservationLink(ctx context.Context, requesterDID, intentID string, input ReservationLinkInput, now time.Time) (privdata.ReservationLink, error) {
	if err := s.requireOwner(requesterDID); err != nil {
		return privdata.ReservationLink{}, err
	}
	link := privdata.ReservationLink{
		IntentID:    intentID,
		ProviderDID: input.ProviderDID,
		OfferURI:    input.OfferURI,
		CreatedAt:   now.UTC(),
	}
	if err := s.savePrivateValue(ctx, privdata.NSIDPrivateReservationLink, intentID, link, intentID, now); err != nil {
		return privdata.ReservationLink{}, err
	}
	return link, nil
}

func (s *Service) GetPrivateReservation(ctx context.Context, requesterDID, intentID string, now time.Time) (privdata.ReservationBundle, error) {
	if requesterDID != s.config.ActorDID {
		if err := s.requireConsent(ctx, requesterDID, intentID, now); err != nil {
			return privdata.ReservationBundle{}, err
		}
	}
	linkRecord, err := s.store.GetPrivateRecord(ctx, s.config.ActorDID, privdata.NSIDPrivateReservationLink, intentID)
	if err != nil {
		return privdata.ReservationBundle{}, err
	}
	var link privdata.ReservationLink
	if err := s.decryptInto(ctx, linkRecord.Ciphertext, &link); err != nil {
		return privdata.ReservationBundle{}, err
	}
	profileBundle, err := s.loadProfileBundle(ctx)
	if err != nil {
		return privdata.ReservationBundle{}, err
	}
	return privdata.ReservationBundle{
		IntentID:        intentID,
		ReservationLink: link,
		Profile:         profileBundle.Profile,
		Contact:         profileBundle.Contact,
		Preference:      profileBundle.Preference,
	}, nil
}

func (s *Service) CreateConsentGrant(ctx context.Context, requesterDID string, input ConsentGrantInput, now time.Time) (privdata.ConsentGrant, error) {
	if err := s.requireOwner(requesterDID); err != nil {
		return privdata.ConsentGrant{}, err
	}
	if err := privdata.ValidateConsentResource(input.Resource); err != nil {
		return privdata.ConsentGrant{}, err
	}
	if len(input.Scopes) == 0 {
		return privdata.ConsentGrant{}, fmt.Errorf("scopes are required")
	}
	if input.ExpiresAt.IsZero() {
		return privdata.ConsentGrant{}, fmt.Errorf("expiresAt is required")
	}
	if !input.ExpiresAt.After(now.UTC()) {
		return privdata.ConsentGrant{}, fmt.Errorf("expiresAt must be in the future")
	}
	grantID := input.GrantID
	if strings.TrimSpace(grantID) == "" {
		grantID = newGrantID(requesterDID, input.GranteeDID, input.Resource, now)
	}
	unsigned := privdata.ConsentGrant{
		GrantID:    grantID,
		SubjectDID: s.config.ActorDID,
		GranteeDID: input.GranteeDID,
		Resource:   input.Resource,
		Scopes:     input.Scopes,
		ExpiresAt:  input.ExpiresAt.UTC(),
	}
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return privdata.ConsentGrant{}, err
	}
	if err := privdata.ValidateRecord(privdata.NSIDPrivateConsentGrant, raw); err != nil {
		return privdata.ConsentGrant{}, err
	}
	signature, err := s.signer.Sign(ctx, raw, s.config.KeyID)
	if err != nil {
		return privdata.ConsentGrant{}, err
	}
	unsigned.Signature = base64.StdEncoding.EncodeToString(signature)
	ciphertext, err := s.encryptValue(ctx, unsigned)
	if err != nil {
		return privdata.ConsentGrant{}, err
	}
	if err := s.store.SaveConsentGrant(ctx, StoredConsentGrant{
		OwnerDID:   s.config.ActorDID,
		GrantID:    grantID,
		GranteeDID: input.GranteeDID,
		Resource:   input.Resource,
		Scopes:     append([]string(nil), input.Scopes...),
		ExpiresAt:  input.ExpiresAt.UTC(),
		Ciphertext: ciphertext,
		Signature:  unsigned.Signature,
		CreatedAt:  now.UTC(),
		UpdatedAt:  now.UTC(),
	}); err != nil {
		return privdata.ConsentGrant{}, err
	}
	return unsigned, nil
}

func (s *Service) RevokeConsentGrant(ctx context.Context, requesterDID, grantID string, now time.Time) error {
	if err := s.requireOwner(requesterDID); err != nil {
		return err
	}
	return s.store.RevokeConsentGrant(ctx, s.config.ActorDID, grantID, now.UTC())
}

func (s *Service) applyCommit(ctx context.Context, ops []protocol.Operation, now time.Time) (protocol.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.applyCommitLocked(ctx, ops, now)
}

func (s *Service) applyCommitLocked(ctx context.Context, ops []protocol.Operation, now time.Time) (protocol.Commit, error) {
	restoreRecords, restoreRoot, restoreRevision := s.repo.State()

	commit, err := s.repo.Apply(ops, now, signerAdapter{ctx: ctx, signer: s.signer})
	if err != nil {
		return protocol.Commit{}, err
	}

	snapshot := s.repo.Snapshot()
	if err := s.store.SaveCommit(ctx, commit, snapshot); err != nil {
		s.repo.Restore(restoreRecords, restoreRoot, restoreRevision)
		return protocol.Commit{}, err
	}
	s.head = &commit
	return commit, nil
}

func (s *Service) lookupRecordLocked(nsid, rkey string) (protocol.Record, bool) {
	records, _, _ := s.repo.State()
	for _, record := range records {
		if record.ID.NSID == nsid && record.ID.RKey == rkey {
			return record, true
		}
	}
	return protocol.Record{}, false
}

func (s *Service) mustRecordLocked(nsid, rkey string) protocol.Record {
	record, _ := s.lookupRecordLocked(nsid, rkey)
	return record
}

func (s *Service) findAvailabilityLocked(listingURI, ratePlanURI string, inventoryVersion uint64) (protocol.Record, lexicon.Availability, bool, error) {
	records, _, _ := s.repo.State()
	for _, record := range records {
		if record.ID.NSID != lexicon.NSIDInventoryAvail {
			continue
		}
		var availability lexicon.Availability
		if err := json.Unmarshal(record.Value, &availability); err != nil {
			return protocol.Record{}, lexicon.Availability{}, false, err
		}
		if availability.ListingURI == listingURI && availability.RatePlanURI == ratePlanURI && availability.InventoryVersion == inventoryVersion {
			return record, availability, true, nil
		}
	}
	return protocol.Record{}, lexicon.Availability{}, false, nil
}

func (s *Service) writeRejectLocked(ctx context.Context, now time.Time, intentRecord protocol.Record, intent lexicon.ReservationIntent, code string) (IntentResult, error) {
	terminal, err := protocol.NewRecord(protocol.RecordID{
		ActorDID: s.config.ActorDID,
		NSID:     lexicon.NSIDReserveReject,
		RKey:     intent.IntentID,
	}, lexicon.ReservationReject{
		IntentID:   intent.IntentID,
		OfferURI:   intent.OfferURI,
		Code:       code,
		RejectedAt: now.UTC(),
	}, now)
	if err != nil {
		return IntentResult{}, err
	}
	commit, err := s.applyCommitLocked(ctx, []protocol.Operation{
		{Action: protocol.OperationPut, Record: intentRecord},
		{Action: protocol.OperationPut, Record: terminal},
	}, now)
	if err != nil {
		return IntentResult{}, err
	}
	return IntentResult{
		Decision:       IntentDecisionReject,
		RepoCommit:     commit,
		IntentRecord:   intentRecord,
		TerminalRecord: terminal,
	}, nil
}

func (s *Service) requireOwner(requesterDID string) error {
	if strings.TrimSpace(requesterDID) == "" {
		return fmt.Errorf("requester DID is required")
	}
	if requesterDID != s.config.ActorDID {
		return fmt.Errorf("owner authorization required")
	}
	return nil
}

func (s *Service) requireConsent(ctx context.Context, requesterDID, intentID string, now time.Time) error {
	grants, err := s.store.ListConsentGrants(ctx, s.config.ActorDID)
	if err != nil {
		return err
	}
	resource := privdata.ResourceReservationPrefix + intentID
	for _, grant := range grants {
		if grant.GranteeDID != requesterDID || grant.Resource != resource {
			continue
		}
		if grant.RevokedAt != nil && !grant.RevokedAt.IsZero() {
			continue
		}
		decrypted, err := s.decryptConsent(ctx, grant)
		if err != nil {
			return err
		}
		if privdata.IsGrantActive(decrypted, now) && privdata.HasScope(decrypted.Scopes, privdata.ScopeReservationRead) {
			return nil
		}
	}
	return fmt.Errorf("active consent grant required")
}

func (s *Service) loadProfileBundle(ctx context.Context) (privdata.ProfileBundle, error) {
	profileRecord, err := s.store.GetPrivateRecord(ctx, s.config.ActorDID, privdata.NSIDPrivateProfile, "self")
	if err != nil {
		return privdata.ProfileBundle{}, err
	}
	contactRecord, err := s.store.GetPrivateRecord(ctx, s.config.ActorDID, privdata.NSIDPrivateContact, "self")
	if err != nil {
		return privdata.ProfileBundle{}, err
	}
	preferenceRecord, err := s.store.GetPrivateRecord(ctx, s.config.ActorDID, privdata.NSIDPrivatePreference, "self")
	if err != nil {
		return privdata.ProfileBundle{}, err
	}
	var bundle privdata.ProfileBundle
	if err := s.decryptInto(ctx, profileRecord.Ciphertext, &bundle.Profile); err != nil {
		return privdata.ProfileBundle{}, err
	}
	if err := s.decryptInto(ctx, contactRecord.Ciphertext, &bundle.Contact); err != nil {
		return privdata.ProfileBundle{}, err
	}
	if err := s.decryptInto(ctx, preferenceRecord.Ciphertext, &bundle.Preference); err != nil {
		return privdata.ProfileBundle{}, err
	}
	return bundle, nil
}

func (s *Service) savePrivateValue(ctx context.Context, nsid, rkey string, value any, reservationIntent string, now time.Time) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := privdata.ValidateRecord(nsid, raw); err != nil {
		return err
	}
	ciphertext, err := s.encryptValue(ctx, value)
	if err != nil {
		return err
	}
	return s.store.SavePrivateRecord(ctx, PrivateStoredRecord{
		OwnerDID:          s.config.ActorDID,
		NSID:              nsid,
		RKey:              rkey,
		Ciphertext:        ciphertext,
		ReservationIntent: reservationIntent,
		CreatedAt:         now.UTC(),
		UpdatedAt:         now.UTC(),
	})
}

func (s *Service) encryptValue(ctx context.Context, value any) (string, error) {
	if s.cipher == nil {
		return "", fmt.Errorf("cipher is required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	keyID := s.config.EncryptionKeyID
	if keyID == "" {
		keyID = s.config.KeyID
	}
	return s.cipher.Encrypt(ctx, raw, keyID)
}

func (s *Service) decryptInto(ctx context.Context, ciphertext string, target any) error {
	if s.cipher == nil {
		return fmt.Errorf("cipher is required")
	}
	keyID := s.config.EncryptionKeyID
	if keyID == "" {
		keyID = s.config.KeyID
	}
	raw, err := s.cipher.Decrypt(ctx, ciphertext, keyID)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func (s *Service) decryptConsent(ctx context.Context, stored StoredConsentGrant) (privdata.ConsentGrant, error) {
	var grant privdata.ConsentGrant
	if err := s.decryptInto(ctx, stored.Ciphertext, &grant); err != nil {
		return privdata.ConsentGrant{}, err
	}
	return grant, nil
}

func bookingReference(providerDID, intentID string) string {
	sum := sha256.Sum256([]byte(providerDID + ":" + intentID))
	return "OBK-" + strings.ToUpper(hex.EncodeToString(sum[:6]))
}

func newGrantID(ownerDID, granteeDID, resource string, now time.Time) string {
	sum := sha256.Sum256([]byte(ownerDID + ":" + granteeDID + ":" + resource + ":" + now.UTC().Format(time.RFC3339Nano)))
	return "grant-" + hex.EncodeToString(sum[:8])
}

type signerAdapter struct {
	ctx    context.Context
	signer Signer
}

func (s signerAdapter) Sign(message []byte, keyID string) ([]byte, error) {
	if s.signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	return s.signer.Sign(s.ctx, message, keyID)
}
