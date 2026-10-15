package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// OWASP minimum argon2id profile: cheap enough for a small VPS, still memory-hard.
const (
	argonMemory  = 19 * 1024 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32

	MinPasswordLen = 12
	MaxPasswordLen = 1024 // bounds hashing cost of hostile input
)

var b64 = base64.RawStdEncoding

// HashPassword returns an argon2id PHC string: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
func HashPassword(pw string) (string, error) {
	if len(pw) > MaxPasswordLen {
		return "", errors.New("password too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a PHC string produced by HashPassword (or any argon2id PHC string).
func VerifyPassword(encoded, pw string) (bool, error) {
	if len(pw) > MaxPasswordLen {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errors.New("bad argon2 parameters")
	}
	if m > 1<<20 || t > 16 || p == 0 { // refuse absurd params from a tampered hash
		return false, errors.New("argon2 parameters out of range")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("bad salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("bad hash")
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ValidateNewPassword enforces the password policy for passwords set through the UI/CLI.
func ValidateNewPassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return InputError(fmt.Sprintf("password must be at least %d characters", MinPasswordLen))
	}
	if len(pw) > MaxPasswordLen {
		return InputError("password is too long")
	}
	return nil
}
