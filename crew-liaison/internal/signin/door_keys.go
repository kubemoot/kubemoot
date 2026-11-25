package signin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

const maxSigningKeys = 4

// ParseSigningKeys reads PEM encoded ECDSA P-256 private keys (PKCS#8 or SEC 1), one block
// each. The first key signs; every key verifies, so a new key is put first and the old one
// stays listed until the tokens it signed have expired.
func ParseSigningKeys(text string) ([]*ecdsa.PrivateKey, error) {
	var keys []*ecdsa.PrivateKey
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		key, err := parsePrivateKey(block)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("no private key found: give an ECDSA P-256 key as PEM")
	}
	if len(keys) > maxSigningKeys {
		return nil, fmt.Errorf("at most %d keys may be listed", maxSigningKeys)
	}
	return keys, nil
}

func parsePrivateKey(block *pem.Block) (*ecdsa.PrivateKey, error) {
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("a %q block is not a private key", block.Type)
	}
	if err != nil {
		return nil, errors.New("a private key could not be read")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("signing keys must be ECDSA P-256")
	}
	return ec, nil
}
