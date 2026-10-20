package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewID returns a hex-encoded random identifier of byteLen bytes.
func NewID(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashAPIKey returns the at-rest representation of an ingest API key.
// API keys are high-entropy random values, so a plain SHA-256 is sufficient;
// there is no dictionary to attack.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
