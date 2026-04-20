package repo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"openbooking/pkg/did"
	"openbooking/pkg/lexicon"
	"openbooking/pkg/mst"
	"openbooking/pkg/protocol"
)

type memorySigner struct {
	privateKey ed25519.PrivateKey
}

func (m memorySigner) Sign(message []byte, _ string) ([]byte, error) {
	return ed25519.Sign(m.privateKey, message), nil
}

func TestRepositoryCommitAndProof(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	repository := New("did:web:provider.example", "did:web:provider.example#sig")
	record, err := protocol.NewRecord(
		protocol.RecordID{
			ActorDID: "did:web:provider.example",
			NSID:     lexicon.NSIDCatalogListing,
			RKey:     "listing-1",
		},
		lexicon.Listing{
			Name:        "Harbor Hotel",
			Currency:    "EUR",
			LocationRef: "geo:41.0,29.0",
		},
		time.Unix(1713571200, 0),
	)
	if err != nil {
		t.Fatalf("new record: %v", err)
	}

	commit, err := repository.Apply([]protocol.Operation{{
		Action: protocol.OperationPut,
		Record: record,
	}}, time.Unix(1713571201, 0), memorySigner{privateKey: privateKey})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	tree, err := mst.New(repository.Snapshot())
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	proof, err := tree.Proof(record.ID.URI())
	if err != nil {
		t.Fatalf("proof: %v", err)
	}
	if !mst.VerifyProof(record.ID.URI(), record.Hash, proof, commit.RootCID) {
		t.Fatal("proof verification failed")
	}

	signingBytes, err := commit.SigningBytes()
	if err != nil {
		t.Fatalf("signing bytes: %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(commit.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	doc := did.Document{
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
	}
	if err := did.VerifyEd25519(doc, commit.KeyID, signingBytes, signature); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

func TestValidateCommitSequence(t *testing.T) {
	prev := protocol.Commit{
		ActorDID: "did:web:provider.example",
		Revision: 1,
		RootCID:  "root-1",
	}
	next := protocol.Commit{
		ActorDID: "did:web:provider.example",
		Revision: 2,
		Previous: "root-1",
		RootCID:  "root-2",
	}
	if err := ValidateCommitSequence(prev, next); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
