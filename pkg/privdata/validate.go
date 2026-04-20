package privdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/restayway/openbooking/pkg/protocol"
)

func ValidateRecord(nsid string, raw json.RawMessage) error {
	switch nsid {
	case NSIDPrivateProfile:
		var record Profile
		if err := decodeStrict(raw, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.GivenName) == "" {
			return fmt.Errorf("givenName is required")
		}
		if strings.TrimSpace(record.FamilyName) == "" {
			return fmt.Errorf("familyName is required")
		}
	case NSIDPrivateContact:
		var record Contact
		if err := decodeStrict(raw, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.Email) == "" {
			return fmt.Errorf("email is required")
		}
	case NSIDPrivatePreference:
		var record Preference
		if err := decodeStrict(raw, &record); err != nil {
			return err
		}
	case NSIDPrivateReservationLink:
		var record ReservationLink
		if err := decodeStrict(raw, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.IntentID) == "" {
			return fmt.Errorf("intentId is required")
		}
		if strings.TrimSpace(record.ProviderDID) == "" {
			return fmt.Errorf("providerDid is required")
		}
		if strings.TrimSpace(record.OfferURI) == "" {
			return fmt.Errorf("offerUri is required")
		}
		if record.CreatedAt.IsZero() {
			return fmt.Errorf("createdAt is required")
		}
		if _, err := protocol.ParseRecordURI(record.OfferURI); err != nil {
			return err
		}
	case NSIDPrivateConsentGrant:
		var record ConsentGrant
		if err := decodeStrict(raw, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.GrantID) == "" {
			return fmt.Errorf("grantId is required")
		}
		if strings.TrimSpace(record.SubjectDID) == "" {
			return fmt.Errorf("subjectDid is required")
		}
		if strings.TrimSpace(record.GranteeDID) == "" {
			return fmt.Errorf("granteeDid is required")
		}
		if strings.TrimSpace(record.Resource) == "" {
			return fmt.Errorf("resource is required")
		}
		if len(record.Scopes) == 0 {
			return fmt.Errorf("scopes are required")
		}
		if record.ExpiresAt.IsZero() {
			return fmt.Errorf("expiresAt is required")
		}
	default:
		return fmt.Errorf("unsupported private nsid: %s", nsid)
	}
	return nil
}

func ValidateConsentResource(resource string) error {
	if !strings.HasPrefix(resource, ResourceReservationPrefix) {
		return fmt.Errorf("resource must start with %s", ResourceReservationPrefix)
	}
	intentID := strings.TrimPrefix(resource, ResourceReservationPrefix)
	if strings.TrimSpace(intentID) == "" {
		return fmt.Errorf("reservation resource requires intent id")
	}
	return nil
}

func HasScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func IsGrantActive(grant ConsentGrant, now time.Time) bool {
	if grant.RevokedAt != nil && !grant.RevokedAt.IsZero() {
		return false
	}
	return now.UTC().Before(grant.ExpiresAt.UTC()) || now.UTC().Equal(grant.ExpiresAt.UTC())
}
