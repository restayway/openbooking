package mst

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

// testSigner implements a minimal repo-like signing interface.
type testSigner struct {
	privateKey ed25519.PrivateKey
}

func (m testSigner) Sign(message []byte, _ string) ([]byte, error) {
	return ed25519.Sign(m.privateKey, message), nil
}

func buildCommit(t *testing.T, actorDID string, ops []protocol.Operation) (protocol.Commit, []protocol.Record) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := testSigner{privateKey: privateKey}

	records := make(map[string]protocol.Record)
	for _, op := range ops {
		if op.Action == protocol.OperationPut {
			records[op.Record.ID.URI()] = op.Record
		} else if op.Action == protocol.OperationDelete {
			delete(records, op.Record.ID.URI())
		}
	}
	snapshot := make([]protocol.Record, 0, len(records))
	for _, r := range records {
		snapshot = append(snapshot, r)
	}
	if len(snapshot) > 0 {
		// deterministic sort for snapshot
		for i := 0; i < len(snapshot); i++ {
			for j := i + 1; j < len(snapshot); j++ {
				if snapshot[i].ID.URI() > snapshot[j].ID.URI() {
					snapshot[i], snapshot[j] = snapshot[j], snapshot[i]
				}
			}
		}
	}

	tree, err := New(snapshot)
	if err != nil {
		t.Fatalf("new tree: %v", err)
	}
	commit := protocol.Commit{
		ActorDID:  actorDID,
		Revision:  1,
		RootCID:   tree.RootCID(),
		CreatedAt: time.Now().UTC(),
		Ops:       ops,
		KeyID:     actorDID + "#sig",
	}
	sigBytes, err := signer.Sign(func() []byte { b, _ := commit.SigningBytes(); return b }(), commit.KeyID)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	commit.Signature = base64.StdEncoding.EncodeToString(sigBytes)
	return commit, snapshot
}

func TestMSTNewProofVerify(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_ = privateKey

	record, err := protocol.NewRecord(protocol.RecordID{
		ActorDID: "did:web:provider.example",
		NSID:     lexicon.NSIDCatalogListing,
		RKey:     "listing-1",
	}, lexicon.Listing{
		Name:        "Harbor Hotel",
		Currency:    "EUR",
		LocationRef: "geo:41.0,29.0",
	}, time.Unix(1713571200, 0))
	if err != nil {
		t.Fatalf("new record: %v", err)
	}

	commit, snapshot := buildCommit(t, "did:web:provider.example", []protocol.Operation{{
		Action: protocol.OperationPut,
		Record: record,
	}})

	tree, err := New(snapshot)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}

	proof, err := tree.Proof(record.ID.URI())
	if err != nil {
		t.Fatalf("proof: %v", err)
	}
	if !VerifyProof(record.ID.URI(), record.Hash, proof, commit.RootCID) {
		t.Fatal("proof verification failed")
	}

	// For a single-leaf tree, the leaf hash directly equals the root, so proof is empty.
	if len(proof) != 0 {
		t.Fatalf("expected empty proof for single-leaf tree, got %d steps", len(proof))
	}

	// Ensure deterministic CID root across recomputation
	tree2, err := New(snapshot)
	if err != nil {
		t.Fatalf("tree2: %v", err)
	}
	if tree.RootCID() != tree2.RootCID() {
		t.Fatalf("MST root not deterministic: %s vs %s", tree.RootCID(), tree2.RootCID())
	}
}

func TestMSTEmptyTree(t *testing.T) {
	tree, err := New(nil)
	if err != nil {
		t.Fatalf("empty tree: %v", err)
	}
	root := tree.RootCID()
	if root == "" {
		t.Fatal("expected non-empty root for empty tree")
	}
	_, err = tree.Proof("ob://x/x/x")
	if err == nil {
		t.Fatal("expected error for proof in empty tree")
	}
}

func TestMSTMultipleRecords(t *testing.T) {
	var ops []protocol.Operation
	for i := 0; i < 5; i++ {
		record, err := protocol.NewRecord(
			protocol.RecordID{
				ActorDID: "did:web:provider.example",
				NSID:     lexicon.NSIDCatalogListing,
				RKey:     fmt.Sprintf("listing-%d", i),
			},
			lexicon.Listing{Name: fmt.Sprintf("Hotel %d", i), Currency: "USD"},
			time.Now(),
		)
		if err != nil {
			t.Fatalf("new record: %v", err)
		}
		ops = append(ops, protocol.Operation{Action: protocol.OperationPut, Record: record})
	}

	commit, snapshot := buildCommit(t, "did:web:provider.example", ops)

	tree, err := New(snapshot)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	for _, leaf := range tree.Leaves() {
		proof, err := tree.Proof(leaf.Key)
		if err != nil {
			t.Fatalf("proof for %s: %v", leaf.Key, err)
		}
		if !VerifyProof(leaf.Key, leaf.Value, proof, commit.RootCID) {
			t.Fatalf("proof verification failed for %s", leaf.Key)
		}
	}
}
