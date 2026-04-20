package privdata

import "time"

const (
	NSIDPrivateProfile         = "com.openbooking.private.profile"
	NSIDPrivateContact         = "com.openbooking.private.contact"
	NSIDPrivatePreference      = "com.openbooking.private.preference"
	NSIDPrivateConsentGrant    = "com.openbooking.private.consentGrant"
	NSIDPrivateReservationLink = "com.openbooking.private.reservationLink"
)

const (
	ScopeReservationRead      = "reservation.read"
	ResourceReservationPrefix = "reservation:"
)

type Profile struct {
	GivenName         string `json:"givenName"`
	FamilyName        string `json:"familyName"`
	PreferredLanguage string `json:"preferredLanguage,omitempty"`
}

type Contact struct {
	Email string `json:"email"`
	Phone string `json:"phone,omitempty"`
}

type Preference struct {
	Locale          string   `json:"locale,omitempty"`
	SpecialRequests []string `json:"specialRequests,omitempty"`
	Accessibility   []string `json:"accessibility,omitempty"`
}

type ConsentGrant struct {
	GrantID    string     `json:"grantId"`
	SubjectDID string     `json:"subjectDid"`
	GranteeDID string     `json:"granteeDid"`
	Resource   string     `json:"resource"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Signature  string     `json:"signature"`
}

type ReservationLink struct {
	IntentID    string    `json:"intentId"`
	ProviderDID string    `json:"providerDid"`
	OfferURI    string    `json:"offerUri"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ProfileBundle struct {
	Profile    Profile    `json:"profile"`
	Contact    Contact    `json:"contact"`
	Preference Preference `json:"preference"`
}

type ReservationBundle struct {
	IntentID        string          `json:"intentId"`
	ReservationLink ReservationLink `json:"reservationLink"`
	Profile         Profile         `json:"profile"`
	Contact         Contact         `json:"contact"`
	Preference      Preference      `json:"preference"`
}
