package pdssqlite

import (
	"context"
	"testing"
	"time"

	"github.com/restayway/openbooking/pkg/pds"
	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

func TestPDSCompatibleInterface(t *testing.T) {
	var _ pds.Store = (*Store)(nil)
}

func TestPDSLoadSaveRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(":memory:")
	if err != nil { t.Fatalf("open: %v", err) }
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil { t.Fatalf("schema: %v", err) }

	rec, _ := protocol.NewRecord(protocol.RecordID{ActorDID: "did:web:x", NSID: lexicon.NSIDCatalogListing, RKey: "l1"}, lexicon.Listing{Name: "Sea", Currency: "USD"}, time.Now())
	commit := protocol.Commit{ActorDID: "did:web:x", Revision: 1, RootCID: "abc", CreatedAt: time.Now().UTC(), Ops: []protocol.Operation{{Action: protocol.OperationPut, Record: rec}}, KeyID: "k1", Signature: "sig"}
	if err := store.SaveCommit(ctx, commit, nil); err != nil { t.Fatalf("save: %v", err) }

	state, err := store.LoadRepository(ctx, "did:web:x")
	if err != nil { t.Fatalf("load: %v", err) }
	if state.Revision != 1 { t.Fatalf("revision mismatch") }
	if len(state.Records) != 1 { t.Fatalf("records mismatch") }
}
