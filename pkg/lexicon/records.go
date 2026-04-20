package lexicon

import "time"

const (
	NSIDActorProfile    = "com.openbooking.actor.profile"
	NSIDCatalogListing  = "com.openbooking.catalog.listing"
	NSIDCatalogRatePlan = "com.openbooking.catalog.ratePlan"
	NSIDInventoryAvail  = "com.openbooking.inventory.availability"
	NSIDOfferQuote      = "com.openbooking.offer.quote"
	NSIDReserveIntent   = "com.openbooking.reserve.intent"
	NSIDReserveCommit   = "com.openbooking.reserve.commit"
	NSIDReserveReject   = "com.openbooking.reserve.reject"
	NSIDReserveCancel   = "com.openbooking.reserve.cancel"
)

const (
	RejectCodeOfferNotFound      = "offer_not_found"
	RejectCodeOfferExpired       = "offer_expired"
	RejectCodeInventoryNotFound  = "inventory_not_found"
	RejectCodeInventoryExhausted = "inventory_exhausted"
	RejectCodeProviderMismatch   = "provider_mismatch"
)

type ActorProfile struct {
	DisplayName string   `json:"displayName"`
	Handles     []string `json:"handles,omitempty"`
	Role        string   `json:"role"`
}

type Listing struct {
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	LocationRef string `json:"locationRef"`
}

type RatePlan struct {
	ListingURI string `json:"listingUri"`
	Name       string `json:"name"`
	PolicyHash string `json:"policyHash"`
	TermsHash  string `json:"termsHash"`
	Currency   string `json:"currency"`
}

type Availability struct {
	ListingURI       string    `json:"listingUri"`
	RatePlanURI      string    `json:"ratePlanUri"`
	InventoryVersion uint64    `json:"inventoryVersion"`
	StartDate        time.Time `json:"startDate"`
	EndDate          time.Time `json:"endDate"`
	AvailableUnits   int       `json:"availableUnits"`
}

type OfferQuote struct {
	ListingURI       string    `json:"listingUri"`
	RatePlanURI      string    `json:"ratePlanUri"`
	InventoryVersion uint64    `json:"inventoryVersion"`
	CommercialHash   string    `json:"commercialHash"`
	PolicyHash       string    `json:"policyHash"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type ReservationIntent struct {
	IntentID     string    `json:"intentId"`
	ProviderDID  string    `json:"providerDid"`
	CustomerDID  string    `json:"customerDid"`
	OfferURI     string    `json:"offerUri"`
	EnvelopeHash string    `json:"envelopeHash"`
	RequestedAt  time.Time `json:"requestedAt"`
}

type ReservationCommit struct {
	IntentID         string    `json:"intentId"`
	OfferURI         string    `json:"offerUri"`
	BookingReference string    `json:"bookingReference"`
	Status           string    `json:"status"`
	EnvelopeHash     string    `json:"envelopeHash"`
	CommittedAt      time.Time `json:"committedAt"`
}

type ReservationReject struct {
	IntentID   string    `json:"intentId"`
	OfferURI   string    `json:"offerUri"`
	Code       string    `json:"code"`
	RejectedAt time.Time `json:"rejectedAt"`
}

type ReservationCancel struct {
	BookingReference string    `json:"bookingReference"`
	ReasonCode       string    `json:"reasonCode"`
	CancelledAt      time.Time `json:"cancelledAt"`
}
