package gemini

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	// CookieEncryptionKeyEnv is the environment variable holding the cookie
	// encryption secret. A 64-character hexadecimal value is used directly as
	// a 32-byte AES key; any other non-empty value is stretched with PBKDF2.
	CookieEncryptionKeyEnv = "COOKIE_ENCRYPTION_KEY"

	// RequireCookieEncryptionEnv refuses to start the server when encryption
	// is required but no key is configured.
	RequireCookieEncryptionEnv = "REQUIRE_COOKIE_ENCRYPTION"

	cookieEncryptionVersion   = 1
	cookieEncryptionAlgorithm = "AES-256-GCM"
	cookieEncryptionSalt      = "free-gemini-api/cookie-encryption/v1"
	cookieEncryptionIters     = 100000
	cookieEncryptionKeySize   = 32
)

// EncryptedEnvelope is the on-disk representation of an encrypted cookie or
// session-cache file. The plaintext is never stored alongside the envelope.
type EncryptedEnvelope struct {
	GeminiEncrypted bool   `json:"__gemini_encrypted"`
	Version         int    `json:"version"`
	Algorithm       string `json:"algorithm"`
	Nonce           string `json:"nonce"`
	Ciphertext      string `json:"ciphertext"`
}

var (
	cookieKeyCacheMu      sync.Mutex
	cookieKeyCacheRaw     string
	cookieKeyCacheDerived []byte
)

// GetCookieEncryptionKey returns the configured cookie encryption secret, or
// an empty string when encryption is disabled.
func GetCookieEncryptionKey() string {
	return strings.TrimSpace(os.Getenv(CookieEncryptionKeyEnv))
}

// CookieEncryptionRequired reports whether the server must refuse to run
// without COOKIE_ENCRYPTION_KEY.
func CookieEncryptionRequired() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(RequireCookieEncryptionEnv)), "true")
}

// ValidateCookieEncryptionConfig is called at startup. It returns an error when
// encryption is explicitly required but no key was supplied.
func ValidateCookieEncryptionConfig() error {
	if GetCookieEncryptionKey() != "" {
		return nil
	}
	if CookieEncryptionRequired() {
		return fmt.Errorf("%s=true requires %s to be set", RequireCookieEncryptionEnv, CookieEncryptionKeyEnv)
	}
	return nil
}

// deriveCookieKey turns the configured secret into a 32-byte AES key. A
// hexadecimal secret is decoded directly; any other secret goes through
// PBKDF2-HMAC-SHA256 with a fixed application salt.
func deriveCookieKey(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, fmt.Errorf("%s is empty", CookieEncryptionKeyEnv)
	}

	cookieKeyCacheMu.Lock()
	if cookieKeyCacheRaw == secret && len(cookieKeyCacheDerived) == cookieEncryptionKeySize {
		cached := append([]byte(nil), cookieKeyCacheDerived...)
		cookieKeyCacheMu.Unlock()
		return cached, nil
	}
	cookieKeyCacheMu.Unlock()

	var derived []byte
	if len(secret) == hex.EncodedLen(cookieEncryptionKeySize) {
		if decoded, err := hex.DecodeString(secret); err == nil && len(decoded) == cookieEncryptionKeySize {
			derived = decoded
		}
	}
	if derived == nil {
		var err error
		derived, err = pbkdf2.Key(sha256.New, secret, []byte(cookieEncryptionSalt), cookieEncryptionIters, cookieEncryptionKeySize)
		if err != nil {
			return nil, fmt.Errorf("derive cookie encryption key: %w", err)
		}
	}

	cookieKeyCacheMu.Lock()
	cookieKeyCacheRaw = secret
	cookieKeyCacheDerived = append([]byte(nil), derived...)
	cookieKeyCacheMu.Unlock()

	return derived, nil
}

// EncryptCookieData seals plaintext with AES-256-GCM and returns a JSON
// envelope suitable for atomic replacement of a cookie file.
func EncryptCookieData(plaintext []byte, key string) ([]byte, error) {
	derived, err := deriveCookieKey(key)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("create cookie cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create cookie GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate cookie nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	envelope := EncryptedEnvelope{
		GeminiEncrypted: true,
		Version:         cookieEncryptionVersion,
		Algorithm:       cookieEncryptionAlgorithm,
		Nonce:           base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:      base64.StdEncoding.EncodeToString(ciphertext),
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal cookie envelope: %w", err)
	}
	return data, nil
}

// DecryptCookieData opens an encrypted envelope. Authentication failures,
// malformed envelopes and wrong keys all return an error without exposing
// plaintext.
func DecryptCookieData(data []byte, key string) ([]byte, error) {
	var envelope EncryptedEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid encrypted cookie envelope: %w", err)
	}
	if !envelope.GeminiEncrypted {
		return nil, errors.New("cookie payload is not encrypted")
	}
	if envelope.Version != cookieEncryptionVersion {
		return nil, fmt.Errorf("unsupported cookie envelope version %d", envelope.Version)
	}
	if !strings.EqualFold(envelope.Algorithm, cookieEncryptionAlgorithm) {
		return nil, fmt.Errorf("unsupported cookie encryption algorithm %q", envelope.Algorithm)
	}

	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode cookie nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode cookie ciphertext: %w", err)
	}

	derived, err := deriveCookieKey(key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, fmt.Errorf("create cookie cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create cookie GCM: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid cookie nonce length %d", len(nonce))
	}
	if len(ciphertext) < gcm.Overhead() {
		return nil, errors.New("invalid cookie ciphertext length")
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("cookie decryption failed: authentication tag mismatch")
	}
	return plaintext, nil
}

// IsEncryptedPayload reports whether data looks like a cookie envelope. It is
// intentionally cheap and tolerant: decryption performs the full validation.
func IsEncryptedPayload(data []byte) bool {
	var probe struct {
		GeminiEncrypted bool `json:"__gemini_encrypted"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.GeminiEncrypted
}
