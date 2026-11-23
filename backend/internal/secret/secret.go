// Package secret encrypts stored credentials, hashes passwords and API keys,
// and generates random tokens.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Box seals and opens secrets with AES-256-GCM and signs values with HMAC.
type Box struct {
	aead    cipher.AEAD
	signKey []byte
}

// NewBox builds a Box from a 32-byte master key. Encryption and signing use
// keys derived from it so one never doubles as the other.
func NewBox(master []byte) (*Box, error) {
	if len(master) != 32 {
		return nil, errors.New("secret key must be 32 bytes")
	}
	encKey := derive(master, "oxalate-bridge:aes-gcm:v1")
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, signKey: derive(master, "oxalate-bridge:hmac:v1")}, nil
}

func derive(master []byte, label string) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// Seal encrypts plaintext; the nonce is prepended.
func (b *Box) Seal(plaintext string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value too short")
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("open sealed value: %w", err)
	}
	return string(out), nil
}

// Sign returns a hex HMAC-SHA256 of value.
func (b *Box) Sign(value string) string {
	mac := hmac.New(sha256.New, b.signKey)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// RandomToken returns n random bytes encoded URL-safe without padding.
func RandomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

const keyAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewAPIKey returns "sk-" followed by 48 random alphanumerics.
func NewAPIKey() string {
	const n = 48
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	out := make([]byte, n)
	for i, c := range buf {
		// 256 % 62 bias is negligible for an opaque token with 285 bits.
		out[i] = keyAlphabet[int(c)%len(keyAlphabet)]
	}
	return "sk-" + string(out)
}

// AdminTokenPrefix starts every admin API token.
const AdminTokenPrefix = "oba-"

// NewAdminToken returns "oba-" followed by 43 random base62 characters
// (about 256 bits), drawn without modulo bias.
func NewAdminToken() string {
	const n = 43
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, c := range buf {
			// 248 = 4 × 62: rejecting larger bytes keeps the draw uniform.
			if c < 248 && len(out) < n {
				out = append(out, keyAlphabet[int(c)%len(keyAlphabet)])
			}
		}
	}
	return AdminTokenPrefix + string(out)
}

// KeyHash is the lookup hash of an API key.
func KeyHash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// KeyPrefix is the display form of a key: its start and end.
func KeyPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:7] + "…" + key[len(key)-4:]
}

// Hash returns hex SHA-256 of value with a domain label, for identifiers that
// must not be stored in clear (session identities).
func Hash(label, value string) string {
	sum := sha256.Sum256([]byte(label + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// argon2id parameters (RFC 9106 second recommended option, scaled down).
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// CheckPassword verifies a password against a PHC argon2id hash.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
