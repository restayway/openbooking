package appview

import (
	"context"
	"sync"
	"time"

	"github.com/restayway/openbooking/pkg/lexicon"
	"github.com/restayway/openbooking/pkg/protocol"
)

// CachedStore wraps a Store with an in-memory LRU cache for hot reads.
type CachedStore struct {
	delegate Store
	mu       sync.RWMutex
	listings map[string]cachedListings
	offers   map[string]cachedOffer
	avail    map[string]cachedAvailability
}

type cachedListings struct {
	items []lexicon.Listing
	at    time.Time
}
type cachedOffer struct {
	item *lexicon.OfferQuote
	at   time.Time
}
type cachedAvailability struct {
	item *lexicon.Availability
	at   time.Time
}

// NewCachedStore wraps the given store with a cache.
func NewCachedStore(delegate Store) *CachedStore {
	return &CachedStore{
		delegate: delegate,
		listings: make(map[string]cachedListings),
		offers:   make(map[string]cachedOffer),
		avail:    make(map[string]cachedAvailability),
	}
}

func cacheKey(parts ...string) string {
	k := ""
	for i, p := range parts {
		if i > 0 { k += "|" }
		k += p
	}
	return k
}

func (c *CachedStore) EnsureSchema(ctx context.Context) error { return c.delegate.EnsureSchema(ctx) }
func (c *CachedStore) Ping(ctx context.Context) error        { return c.delegate.Ping(ctx) }
func (c *CachedStore) GetCheckpoint(ctx context.Context, name string) (uint64, error) {
	return c.delegate.GetCheckpoint(ctx, name)
}
func (c *CachedStore) SetCheckpoint(ctx context.Context, name string, seq uint64) error {
	return c.delegate.SetCheckpoint(ctx, name, seq)
}

func (c *CachedStore) ApplyEnvelope(ctx context.Context, envelope interface{}) error {
	// Invalidate on any envelope application (naive but safe)
	c.mu.Lock()
	c.listings = make(map[string]cachedListings)
	c.offers = make(map[string]cachedOffer)
	c.avail = make(map[string]cachedAvailability)
	c.mu.Unlock()
	return c.delegate.ApplyEnvelope(ctx, envelope.(protocol.RelayCommitEnvelope))
}

func (c *CachedStore) ListListings(ctx context.Context, providerDID string) ([]lexicon.Listing, error) {
	key := providerDID
	c.mu.RLock()
	ent, ok := c.listings[key]
	c.mu.RUnlock()
	if ok && len(ent.items) > 0 {
		return ent.items, nil
	}
	items, err := c.delegate.ListListings(ctx, providerDID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.listings[key] = cachedListings{items: items, at: time.Now()}
	c.mu.Unlock()
	return items, nil
}

func (c *CachedStore) GetOffer(ctx context.Context, providerDID, rkey string) (*lexicon.OfferQuote, error) {
	key := cacheKey(providerDID, rkey)
	c.mu.RLock()
	ent, ok := c.offers[key]
	c.mu.RUnlock()
	if ok && ent.item != nil {
		return ent.item, nil
	}
	item, err := c.delegate.GetOffer(ctx, providerDID, rkey)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.offers[key] = cachedOffer{item: item, at: time.Now()}
	c.mu.Unlock()
	return item, nil
}

func (c *CachedStore) GetAvailability(ctx context.Context, providerDID, listingURI, ratePlanURI string) (*lexicon.Availability, error) {
	key := cacheKey(providerDID, listingURI, ratePlanURI)
	c.mu.RLock()
	ent, ok := c.avail[key]
	c.mu.RUnlock()
	if ok && ent.item != nil {
		return ent.item, nil
	}
	item, err := c.delegate.GetAvailability(ctx, providerDID, listingURI, ratePlanURI)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.avail[key] = cachedAvailability{item: item, at: time.Now()}
	c.mu.Unlock()
	return item, nil
}

func (c *CachedStore) GetReservation(ctx context.Context, providerDID, intentID string) (*ReservationView, error) {
	// Reservations are never cached to ensure fresh state.
	return c.delegate.GetReservation(ctx, providerDID, intentID)
}

// WarmCache preloads listings for a provider into the cache.
func (c *CachedStore) WarmCache(ctx context.Context, providerDID string) error {
	_, _ = c.ListListings(ctx, providerDID)
	return nil
}
