package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/restayway/openbooking/pkg/did"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
	"github.com/restayway/openbooking/pkg/repo"
)

var ErrInvalidCommit = errors.New("invalid commit")

type Store interface {
	EnsureSchema(ctx context.Context) error
	LastActorCommit(ctx context.Context, actorDID string) (*protocol.Commit, error)
	SaveEvent(ctx context.Context, envelope protocol.RelayCommitEnvelope) (bool, uint64, error)
	ListEventsAfter(ctx context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error)
	GetActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error)
	LastRelaySequence(ctx context.Context) (uint64, error)
	Ping(ctx context.Context) error
	GetConsumerCheckpoint(ctx context.Context, consumer string) (uint64, error)
	SetConsumerCheckpoint(ctx context.Context, consumer string, sequence uint64) error
	LastConsumerCheckpoint(ctx context.Context) (uint64, error)
}

type Publisher interface {
	Publish(ctx context.Context, subject string, payload []byte) error
}

type Resolver interface {
	Resolve(ctx context.Context, did string) (did.Document, error)
}

type Service struct {
	relayDID  string
	store     Store
	publisher Publisher
	resolver  Resolver
}

func NewService(ctx context.Context, relayDID string, store Store, publisher Publisher, resolver Resolver) (*Service, error) {
	if relayDID == "" {
		return nil, fmt.Errorf("relay DID is required")
	}
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	return &Service{relayDID: relayDID, store: store, publisher: publisher, resolver: resolver}, nil
}

func (s *Service) IngestCommit(ctx context.Context, commit protocol.Commit, sourceSubject string, sourceSequence uint64, now time.Time) (protocol.RelayCommitEnvelope, error) {
	if s.resolver == nil {
		return protocol.RelayCommitEnvelope{}, fmt.Errorf("resolver is required")
	}
	signingBytes, err := commit.SigningBytes()
	if err != nil {
		return protocol.RelayCommitEnvelope{}, err
	}
	signature, err := base64.StdEncoding.DecodeString(commit.Signature)
	if err != nil {
		return protocol.RelayCommitEnvelope{}, fmt.Errorf("%w: invalid signature encoding", ErrInvalidCommit)
	}
	doc, err := s.resolver.Resolve(ctx, commit.ActorDID)
	if err != nil {
		return protocol.RelayCommitEnvelope{}, err
	}
	if err := did.VerifyEd25519(doc, commit.KeyID, signingBytes, signature); err != nil {
		return protocol.RelayCommitEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidCommit, err)
	}
	if err := validateCommit(commit); err != nil {
		return protocol.RelayCommitEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidCommit, err)
	}
	prev, err := s.store.LastActorCommit(ctx, commit.ActorDID)
	if err != nil {
		return protocol.RelayCommitEnvelope{}, err
	}
	if prev != nil {
		if err := repo.ValidateCommitSequence(*prev, commit); err != nil {
			return protocol.RelayCommitEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidCommit, err)
		}
	} else if commit.Revision != 1 {
		return protocol.RelayCommitEnvelope{}, fmt.Errorf("%w: first commit must start at revision 1", ErrInvalidCommit)
	}

	envelope := protocol.RelayCommitEnvelope{
		RelayDID:       s.relayDID,
		SourceSubject:  sourceSubject,
		SourceSequence: sourceSequence,
		ValidatedAt:    now.UTC(),
		Commit:         commit,
	}
	applied, sequence, err := s.store.SaveEvent(ctx, envelope)
	if err != nil {
		return protocol.RelayCommitEnvelope{}, err
	}
	envelope.RelaySequence = sequence
	if !applied {
		return envelope, nil
	}
	if s.publisher != nil {
		payload, err := json.Marshal(envelope)
		if err != nil {
			return protocol.RelayCommitEnvelope{}, err
		}
		if err := s.publisher.Publish(ctx, protocol.RelayCommitSubject(s.relayDID), payload); err != nil {
			return protocol.RelayCommitEnvelope{}, err
		}
		checkpoint, err := json.Marshal(map[string]any{
			"relayDid":      s.relayDID,
			"relaySequence": sequence,
			"validatedAt":   now.UTC(),
		})
		if err != nil {
			return protocol.RelayCommitEnvelope{}, err
		}
		if err := s.publisher.Publish(ctx, protocol.RelayCheckpointSubject(s.relayDID), checkpoint); err != nil {
			return protocol.RelayCommitEnvelope{}, err
		}
	}
	return envelope, nil
}

func (s *Service) ListEvents(ctx context.Context, after uint64, limit int) ([]protocol.RelayCommitEnvelope, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.store.ListEventsAfter(ctx, after, limit)
}

func (s *Service) ActorHead(ctx context.Context, actorDID string) (*protocol.Commit, error) {
	return s.store.GetActorHead(ctx, actorDID)
}

func (s *Service) SetConsumerCheckpoint(ctx context.Context, consumer string, sequence uint64) error {
	return s.store.SetConsumerCheckpoint(ctx, consumer, sequence)
}

func (s *Service) Status(ctx context.Context) (map[string]any, error) {
	latestSequence, err := s.store.LastRelaySequence(ctx)
	if err != nil {
		return nil, err
	}
	lastSourceSequence, err := s.store.LastConsumerCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"relayDid":            s.relayDID,
		"latestRelaySequence": latestSequence,
		"lastSourceSequence":  lastSourceSequence,
	}, nil
}

func validateCommit(commit protocol.Commit) error {
	if commit.Signature == "" {
		return fmt.Errorf("empty signature")
	}
	if commit.KeyID == "" {
		return fmt.Errorf("empty key ID")
	}
	for _, op := range commit.Ops {
		if err := op.Validate(); err != nil {
			return err
		}
		if op.Action != protocol.OperationPut {
			continue
		}
		if err := lexicon.ValidateRecord(op.Record.ID.NSID, op.Record.Value); err != nil {
			return err
		}
		if err := validateRecordRefs(op.Record); err != nil {
			return err
		}
	}
	return nil
}

func validateRecordRefs(record protocol.Record) error {
	switch record.ID.NSID {
	case lexicon.NSIDCatalogRatePlan:
		var value lexicon.RatePlan
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.ListingURI)
		return err
	case lexicon.NSIDInventoryAvail:
		var value lexicon.Availability
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		if _, err := protocol.ParseRecordURI(value.ListingURI); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.RatePlanURI)
		return err
	case lexicon.NSIDOfferQuote:
		var value lexicon.OfferQuote
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		if _, err := protocol.ParseRecordURI(value.ListingURI); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.RatePlanURI)
		return err
	case lexicon.NSIDReserveIntent:
		var value lexicon.ReservationIntent
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.OfferURI)
		return err
	case lexicon.NSIDReserveCommit:
		var value lexicon.ReservationCommit
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.OfferURI)
		return err
	case lexicon.NSIDReserveReject:
		var value lexicon.ReservationReject
		if err := json.Unmarshal(record.Value, &value); err != nil {
			return err
		}
		_, err := protocol.ParseRecordURI(value.OfferURI)
		return err
	}
	return nil
}
