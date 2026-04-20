package lexicon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func ValidateRecord(nsid string, value json.RawMessage) error {
	switch nsid {
	case NSIDActorProfile:
		var record ActorProfile
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.DisplayName) == "" {
			return fmt.Errorf("displayName is required")
		}
		if strings.TrimSpace(record.Role) == "" {
			return fmt.Errorf("role is required")
		}
	case NSIDCatalogListing:
		var record Listing
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.Name) == "" {
			return fmt.Errorf("name is required")
		}
		if strings.TrimSpace(record.Currency) == "" {
			return fmt.Errorf("currency is required")
		}
		if strings.TrimSpace(record.LocationRef) == "" {
			return fmt.Errorf("locationRef is required")
		}
	case NSIDCatalogRatePlan:
		var record RatePlan
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.ListingURI) == "" {
			return fmt.Errorf("listingUri is required")
		}
		if strings.TrimSpace(record.Name) == "" {
			return fmt.Errorf("name is required")
		}
		if strings.TrimSpace(record.PolicyHash) == "" {
			return fmt.Errorf("policyHash is required")
		}
		if strings.TrimSpace(record.TermsHash) == "" {
			return fmt.Errorf("termsHash is required")
		}
		if strings.TrimSpace(record.Currency) == "" {
			return fmt.Errorf("currency is required")
		}
	case NSIDInventoryAvail:
		var record Availability
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if err := requireTime(record.StartDate, "startDate"); err != nil {
			return err
		}
		if err := requireTime(record.EndDate, "endDate"); err != nil {
			return err
		}
		if record.EndDate.Before(record.StartDate) {
			return fmt.Errorf("endDate must be after startDate")
		}
		if strings.TrimSpace(record.ListingURI) == "" {
			return fmt.Errorf("listingUri is required")
		}
		if strings.TrimSpace(record.RatePlanURI) == "" {
			return fmt.Errorf("ratePlanUri is required")
		}
		if record.AvailableUnits < 0 {
			return fmt.Errorf("availableUnits must be >= 0")
		}
	case NSIDOfferQuote:
		var record OfferQuote
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.ListingURI) == "" {
			return fmt.Errorf("listingUri is required")
		}
		if strings.TrimSpace(record.RatePlanURI) == "" {
			return fmt.Errorf("ratePlanUri is required")
		}
		if strings.TrimSpace(record.CommercialHash) == "" {
			return fmt.Errorf("commercialHash is required")
		}
		if strings.TrimSpace(record.PolicyHash) == "" {
			return fmt.Errorf("policyHash is required")
		}
		if err := requireTime(record.ExpiresAt, "expiresAt"); err != nil {
			return err
		}
	case NSIDReserveIntent:
		var record ReservationIntent
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.IntentID) == "" {
			return fmt.Errorf("intentId is required")
		}
		if strings.TrimSpace(record.ProviderDID) == "" {
			return fmt.Errorf("providerDid is required")
		}
		if strings.TrimSpace(record.CustomerDID) == "" {
			return fmt.Errorf("customerDid is required")
		}
		if strings.TrimSpace(record.OfferURI) == "" {
			return fmt.Errorf("offerUri is required")
		}
		if strings.TrimSpace(record.EnvelopeHash) == "" {
			return fmt.Errorf("envelopeHash is required")
		}
		if err := requireTime(record.RequestedAt, "requestedAt"); err != nil {
			return err
		}
	case NSIDReserveCommit:
		var record ReservationCommit
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.IntentID) == "" {
			return fmt.Errorf("intentId is required")
		}
		if strings.TrimSpace(record.OfferURI) == "" {
			return fmt.Errorf("offerUri is required")
		}
		if strings.TrimSpace(record.BookingReference) == "" {
			return fmt.Errorf("bookingReference is required")
		}
		if strings.TrimSpace(record.Status) == "" {
			return fmt.Errorf("status is required")
		}
		if strings.TrimSpace(record.EnvelopeHash) == "" {
			return fmt.Errorf("envelopeHash is required")
		}
		if err := requireTime(record.CommittedAt, "committedAt"); err != nil {
			return err
		}
	case NSIDReserveReject:
		var record ReservationReject
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.IntentID) == "" {
			return fmt.Errorf("intentId is required")
		}
		if strings.TrimSpace(record.OfferURI) == "" {
			return fmt.Errorf("offerUri is required")
		}
		if strings.TrimSpace(record.Code) == "" {
			return fmt.Errorf("code is required")
		}
		if err := requireTime(record.RejectedAt, "rejectedAt"); err != nil {
			return err
		}
	case NSIDReserveCancel:
		var record ReservationCancel
		if err := decodeStrict(value, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.BookingReference) == "" {
			return fmt.Errorf("bookingReference is required")
		}
		if strings.TrimSpace(record.ReasonCode) == "" {
			return fmt.Errorf("reasonCode is required")
		}
		if err := requireTime(record.CancelledAt, "cancelledAt"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported nsid: %s", nsid)
	}
	return nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func requireTime(t time.Time, field string) error {
	if t.IsZero() {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}
