package did

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// MethodResolver routes DID resolution by method prefix.
type MethodResolver struct {
	mu        sync.RWMutex
	methods   map[string]Resolver
	fallback  Resolver
}

// NewMethodResolver creates a new resolver that dispatches by DID method.
func NewMethodResolver() *MethodResolver {
	return &MethodResolver{
		methods: make(map[string]Resolver),
	}
}

// Register registers a resolver for a given method (e.g., "web", "plc", "key").
func (m *MethodResolver) Register(method string, resolver Resolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.methods[strings.ToLower(method)] = resolver
}

// RegisterFallback sets a fallback resolver if the method is not explicitly registered.
func (m *MethodResolver) RegisterFallback(resolver Resolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallback = resolver
}

func (m *MethodResolver) Resolve(ctx context.Context, did string) (Document, error) {
	method, err := parseMethod(did)
	if err != nil {
		return Document{}, err
	}

	m.mu.RLock()
	r, ok := m.methods[method]
	fallback := m.fallback
	m.mu.RUnlock()

	if ok {
		return r.Resolve(ctx, did)
	}
	if fallback != nil {
		return fallback.Resolve(ctx, did)
	}
	return Document{}, fmt.Errorf("unsupported DID method: %s", method)
}

func parseMethod(did string) (string, error) {
	parts := strings.SplitN(did, ":", 3)
	if len(parts) < 3 || parts[0] != "did" {
		return "", fmt.Errorf("invalid DID: %s", did)
	}
	return strings.ToLower(parts[1]), nil
}

// DefaultMethodResolver returns a MethodResolver pre-registered with did:web support.
func DefaultMethodResolver(client HTTPClient) *MethodResolver {
	if client == nil {
		client = http.DefaultClient
	}
	m := NewMethodResolver()
	m.Register("web", WebResolver{Client: client})
	return m
}
