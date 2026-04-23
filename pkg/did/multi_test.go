package did

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockResolver implements Resolver for testing.
type mockResolver struct {
	resolveFunc func(ctx context.Context, did string) (Document, error)
}

func (m *mockResolver) Resolve(ctx context.Context, did string) (Document, error) {
	return m.resolveFunc(ctx, did)
}

func TestMethodResolverWeb(t *testing.T) {
	target, _ := didWebURL("did:web:example.com")
	mock := &mockResolver{resolveFunc: func(ctx context.Context, did string) (Document, error) {
		return Document{ID: did, VerificationMethod: []VerificationMethod{{ID: "#1"}}}, nil
	}}
	mr := NewMethodResolver()
	mr.Register("web", mock)

	doc, err := mr.Resolve(context.Background(), "did:web:example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc.ID != "did:web:example.com" {
		t.Fatalf("unexpected doc id: %s", doc.ID)
	}
	_ = target
}

func TestMethodResolverUnsupported(t *testing.T) {
	mr := NewMethodResolver()
	_, err := mr.Resolve(context.Background(), "did:plc:abc123")
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

func TestMethodResolverFallback(t *testing.T) {
	mock := &mockResolver{resolveFunc: func(ctx context.Context, did string) (Document, error) {
		return Document{ID: "fallback"}, nil
	}}
	mr := NewMethodResolver()
	mr.RegisterFallback(mock)
	_, err := mr.Resolve(context.Background(), "did:unknown:x")
	if err != nil {
		t.Fatalf("fallback resolve error: %v", err)
	}
}

func TestCachingResolverHit(t *testing.T) {
	calls := 0
	mock := &mockResolver{resolveFunc: func(ctx context.Context, did string) (Document, error) {
		calls++
		return Document{ID: did}, nil
	}}
	cr := NewCachingResolver(mock, time.Minute, time.Second)
	// Override clock to a fixed time for determinism
	base := time.Now()
	cr.clock = func() time.Time { return base }

	_, _ = cr.Resolve(context.Background(), "did:web:example.com")
	_, _ = cr.Resolve(context.Background(), "did:web:example.com")
	if calls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", calls)
	}
}

func TestCachingResolverNegativeCache(t *testing.T) {
	calls := 0
	mock := &mockResolver{resolveFunc: func(ctx context.Context, did string) (Document, error) {
		calls++
		return Document{}, errors.New("network error")
	}}
	cr := NewCachingResolver(mock, time.Minute, time.Minute)
	base := time.Now()
	cr.clock = func() time.Time { return base }

	_, err1 := cr.Resolve(context.Background(), "did:web:example.com")
	if err1 == nil {
		t.Fatal("expected error on first resolve")
	}
	_, err2 := cr.Resolve(context.Background(), "did:web:example.com")
	if err2 == nil {
		t.Fatal("expected error on second resolve (negative cache)")
	}
	if calls != 1 {
		t.Fatalf("expected 1 delegate call, got %d", calls)
	}
}

func TestCachingResolverInvalidate(t *testing.T) {
	calls := 0
	mock := &mockResolver{resolveFunc: func(ctx context.Context, did string) (Document, error) {
		calls++
		return Document{ID: did}, nil
	}}
	cr := NewCachingResolver(mock, time.Minute, time.Minute)
	base := time.Now()
	cr.clock = func() time.Time { return base }

	_, _ = cr.Resolve(context.Background(), "did:web:example.com")
	cr.Invalidate("did:web:example.com")
	_, _ = cr.Resolve(context.Background(), "did:web:example.com")
	if calls != 2 {
		t.Fatalf("expected 2 delegate calls after invalidate, got %d", calls)
	}
}
