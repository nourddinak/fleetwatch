// Package secret encrypts small secrets (TOTP keys, notification tokens) before they hit SQLite.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Box seals and opens values with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead}, nil
}

// Seal returns nonce||ciphertext. ad binds the value to its purpose (e.g. "totp"),
// so a ciphertext copied into another column will not decrypt.
func (b *Box) Seal(plain []byte, ad string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(ad))
}

func (b *Box) Open(sealed []byte, ad string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], []byte(ad))
}

// LoadKey reads OPS_SECRET_KEY (base64, 32 bytes) or, if unset, dataDir/secret.key,
// generating that file with mode 0600 on first start.
func LoadKey(envValue, dataDir string) ([]byte, error) {
	if envValue != "" {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envValue))
		if err != nil || len(k) != 32 {
			return nil, errors.New("OPS_SECRET_KEY must be 32 bytes, base64 encoded (openssl rand -base64 32)")
		}
		return k, nil
	}
	path := filepath.Join(dataDir, "secret.key")
	raw, err := os.ReadFile(path)
	if err == nil {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("%s is corrupt", path)
		}
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	// O_EXCL: never overwrite a key that appeared meanwhile; losing it would orphan every secret.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		return nil, err
	}
	return k, f.Sync()
}
