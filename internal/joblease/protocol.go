package joblease

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	Protocol       = 2
	QueryParameter = "job_lease_protocol"
	TokenBytes     = 32
	TokenLength    = TokenBytes * 2
)

func NewToken() (string, error) {
	var token [TokenBytes]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func ValidateToken(token string) error {
	if len(token) != TokenLength {
		return fmt.Errorf("claim_token must be %d hexadecimal characters", TokenLength)
	}
	if _, err := hex.DecodeString(token); err != nil {
		return fmt.Errorf("claim_token must be hexadecimal")
	}
	return nil
}

func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
