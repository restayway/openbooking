package protocol

import (
	"crypto/sha256"
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

// EncodeSHA256CID encodes a raw SHA-256 digest into a CIDv1 with DagCBOR codec
// and returns it as a lowercase base32 multibase string.
// The input is expected to be the 32-byte SHA-256 hash digest.
func EncodeSHA256CID(sum [32]byte) (string, error) {
	mh, err := multihash.EncodeName(sum[:], "sha2-256")
	if err != nil {
		return "", fmt.Errorf("multihash encode: %w", err)
	}
	c := cid.NewCidV1(cid.DagCBOR, mh)
	enc, err := multibase.Encode(multibase.Base32, c.Bytes())
	if err != nil {
		return "", fmt.Errorf("multibase encode: %w", err)
	}
	return enc, nil
}

// CanonicalHash serialises v to deterministic canonical JSON and returns a
// SHA-256 based CID.
func CanonicalHash(v any) (string, error) {
	raw, err := canonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return EncodeSHA256CID(sum)
}
