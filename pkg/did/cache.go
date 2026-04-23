package did

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CacheEntry holds a resolved DID document with its expiration time.
type CacheEntry struct {
	Doc       Document
	ExpiresAt time.Time
	Err       error
}

// CachingResolver wraps a Resolver with TTL-based caching.
// Negative results (errors) are cached separately with a typically shorter TTL.
type CachingResolver struct {
	delegate    Resolver
	cache       map[string]CacheEntry
	errCache    map[string]CacheEntry
	mu          sync.RWMutex
	ttl         time.Duration
	errTTL      time.Duration
	clock       func() time.Time
}

// NewCachingResolver wraps the given resolver with a cache.
// ttl controls positive cache duration, errTTL controls negative cache duration.
func NewCachingResolver(delegate Resolver, ttl, errTTL time.Duration) *CachingResolver {
	return &CachingResolver{
		delegate: delegate,
		cache:    make(map[string]CacheEntry),
		errCache: make(map[string]CacheEntry),
		ttl:      ttl,
		errTTL:   errTTL,
		clock:    time.Now,
	}
}

func (c *CachingResolver) Resolve(ctx context.Context, did string) (Document, error) {
	now := c.clock()

	c.mu.RLock()
	ent, ok := c.cache[did]
	errEnt, errOk := c.errCache[did]
	c.mu.RUnlock()

	if ok && ent.ExpiresAt.After(now) {
		return ent.Doc, nil
	}
	if errOk && errEnt.ExpiresAt.After(now) {
		return Document{}, fmt.Errorf("cached error for %s: %w", did, errEnt.Err)
	}

	doc, err := c.delegate.Resolve(ctx, did)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.errCache[did] = CacheEntry{Doc: Document{}, ExpiresAt: now.Add(c.errTTL), Err: err}
		// Evict stale positive cache if any
		delete(c.cache, did)
		return Document{}, err
	}
	c.cache[did] = CacheEntry{Doc: doc, ExpiresAt: now.Add(c.ttl)}
	delete(c.errCache, did)
	return doc, nil
}

// Invalidate removes a DID from both positive and negative caches.
func (c *CachingResolver) Invalidate(did string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, did)
	delete(c.errCache, did)
}
