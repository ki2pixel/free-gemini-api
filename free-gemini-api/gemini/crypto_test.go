package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCookieKey  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testCookieKey2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func TestCryptoRoundTrip(t *testing.T) {
	plaintext := []byte(`[{"domain":".google.com","name":"SID","value":"top-secret"}]`)

	for name, key := range map[string]string{
		"hex key":          testCookieKey,
		"passphrase key":   "correct horse battery staple",
		"short passphrase": "local-dev",
	} {
		t.Run(name, func(t *testing.T) {
			encrypted, err := EncryptCookieData(plaintext, key)
			if err != nil {
				t.Fatalf("EncryptCookieData: %v", err)
			}
			if !IsEncryptedPayload(encrypted) {
				t.Fatal("encrypted payload was not detected as an envelope")
			}
			if bytes.Contains(encrypted, []byte("top-secret")) {
				t.Fatal("ciphertext contains plaintext")
			}

			decrypted, err := DecryptCookieData(encrypted, key)
			if err != nil {
				t.Fatalf("DecryptCookieData: %v", err)
			}
			if !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("round trip mismatch: got %q", decrypted)
			}
		})
	}
}

func TestCryptoEncryptRequiresKey(t *testing.T) {
	if _, err := EncryptCookieData([]byte("data"), ""); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if _, err := EncryptCookieData([]byte("data"), "   "); err == nil {
		t.Fatal("blank key must be rejected")
	}
}

func TestCryptoTamperDetection(t *testing.T) {
	plaintext := []byte(`{"cookies":"SID=value"}`)
	encrypted, err := EncryptCookieData(plaintext, testCookieKey)
	if err != nil {
		t.Fatal(err)
	}

	var envelope EncryptedEnvelope
	if err := json.Unmarshal(encrypted, &envelope); err != nil {
		t.Fatal(err)
	}

	tamper := func(t *testing.T, mutate func(*EncryptedEnvelope)) {
		t.Helper()
		copy := envelope
		mutate(&copy)
		data, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecryptCookieData(data, testCookieKey); err == nil {
			t.Fatal("tampered envelope was accepted")
		}
	}

	t.Run("ciphertext bit flip", func(t *testing.T) {
		tamper(t, func(e *EncryptedEnvelope) {
			raw, err := base64.StdEncoding.DecodeString(e.Ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			raw[0] ^= 0x01
			e.Ciphertext = base64.StdEncoding.EncodeToString(raw)
		})
	})

	t.Run("nonce bit flip", func(t *testing.T) {
		tamper(t, func(e *EncryptedEnvelope) {
			raw, err := base64.StdEncoding.DecodeString(e.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			raw[0] ^= 0x01
			e.Nonce = base64.StdEncoding.EncodeToString(raw)
		})
	})

	t.Run("wrong key", func(t *testing.T) {
		if _, err := DecryptCookieData(encrypted, testCookieKey2); err == nil {
			t.Fatal("decryption with a different key succeeded")
		}
	})
}

func TestCryptoIsEncryptedPayload(t *testing.T) {
	plainCases := [][]byte{
		[]byte(`[]`),
		[]byte(`[{"name":"SID","value":"x"}]`),
		[]byte(`{"cookies":"SID=x","updated_at":1}`),
		[]byte(`not json`),
		[]byte(`{"__gemini_encrypted":false}`),
	}
	for _, data := range plainCases {
		if IsEncryptedPayload(data) {
			t.Errorf("plaintext payload detected as encrypted: %s", data)
		}
	}

	encrypted, err := EncryptCookieData([]byte(`[]`), testCookieKey)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedPayload(encrypted) {
		t.Fatal("valid envelope not detected")
	}
	if !IsEncryptedPayload([]byte(`{"__gemini_encrypted":true}`)) {
		t.Fatal("flag-only envelope should be detected; decryption validates the rest")
	}
}

func TestCryptoInvalidEnvelope(t *testing.T) {
	cases := map[string]string{
		"not encrypted":    `{"__gemini_encrypted":false,"version":1,"algorithm":"AES-256-GCM","nonce":"","ciphertext":""}`,
		"bad version":      `{"__gemini_encrypted":true,"version":2,"algorithm":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA","ciphertext":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		"bad algorithm":    `{"__gemini_encrypted":true,"version":1,"algorithm":"AES-128-GCM","nonce":"AAAAAAAAAAAAAAAA","ciphertext":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		"bad nonce b64":    `{"__gemini_encrypted":true,"version":1,"algorithm":"AES-256-GCM","nonce":"!!!","ciphertext":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		"short nonce":      `{"__gemini_encrypted":true,"version":1,"algorithm":"AES-256-GCM","nonce":"AAAA","ciphertext":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		"bad ciphertext":   `{"__gemini_encrypted":true,"version":1,"algorithm":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA","ciphertext":"!!!"}`,
		"short ciphertext": `{"__gemini_encrypted":true,"version":1,"algorithm":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA","ciphertext":"AAAA"}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecryptCookieData([]byte(payload), testCookieKey); err == nil {
				t.Fatal("invalid envelope was accepted")
			}
		})
	}

	if _, err := DecryptCookieData([]byte(`not json`), testCookieKey); err == nil {
		t.Fatal("non-JSON payload was accepted")
	}
}

func TestCryptoReadWriteCookieFile(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, testCookieKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies", "account_test.json")
	plaintext := []byte(`[{"name":"SID","value":"cookie-secret"}]`)

	if err := writeCookieFileAtomic(path, plaintext, 0600); err != nil {
		t.Fatalf("writeCookieFileAtomic: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedPayload(raw) {
		t.Fatal("file on disk is not an encrypted envelope")
	}
	if bytes.Contains(raw, []byte("cookie-secret")) {
		t.Fatal("plaintext cookie value leaked to disk")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}

	got, err := readCookieFile(path)
	if err != nil {
		t.Fatalf("readCookieFile: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted data mismatch: %q", got)
	}
}

func TestCryptoReadPlaintextFallback(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies", "account_plain.json")
	plaintext := []byte(`[{"name":"SID","value":"legacy"}]`)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, plaintext, 0600); err != nil {
		t.Fatal(err)
	}

	got, err := readCookieFile(path)
	if err != nil {
		t.Fatalf("readCookieFile: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("legacy data mismatch: %q", got)
	}

	// Without a key, writes must remain plaintext for backward compatibility.
	plainPath := filepath.Join(dir, "cookies", "account_plain_write.json")
	if err := writeCookieFileAtomic(plainPath, plaintext, 0600); err != nil {
		t.Fatalf("writeCookieFileAtomic without key: %v", err)
	}
	raw, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if IsEncryptedPayload(raw) || !bytes.Equal(raw, plaintext) {
		t.Fatalf("plaintext write was altered: %s", raw)
	}
}

func TestCryptoPlaintextMigration(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, testCookieKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies", "account_legacy.json")
	plaintext := []byte(`[{"name":"SID","value":"migrate-me"}]`)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, plaintext, 0600); err != nil {
		t.Fatal(err)
	}

	got, err := readCookieFile(path)
	if err != nil {
		t.Fatalf("readCookieFile: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("migrated read mismatch: %q", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedPayload(raw) {
		t.Fatal("legacy plaintext file was not upgraded to an encrypted envelope")
	}
	if bytes.Contains(raw, []byte("migrate-me")) {
		t.Fatal("legacy plaintext value remains on disk")
	}

	again, err := readCookieFile(path)
	if err != nil {
		t.Fatalf("second readCookieFile: %v", err)
	}
	if !bytes.Equal(again, plaintext) {
		t.Fatalf("second read mismatch: %q", again)
	}
}

func TestCryptoEncryptedFileRequiresKey(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, testCookieKey)
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies", "account_encrypted.json")
	if err := writeCookieFileAtomic(path, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(CookieEncryptionKeyEnv, "")
	if _, err := readCookieFile(path); err == nil {
		t.Fatal("encrypted file was read without a key")
	} else if !strings.Contains(err.Error(), CookieEncryptionKeyEnv) {
		t.Fatalf("error should mention %s: %v", CookieEncryptionKeyEnv, err)
	}
}

func TestCryptoProcessAndSaveCookiesEncrypted(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, testCookieKey)
	t.Chdir(t.TempDir())

	previousCallback := OnCookiesUpdated
	OnCookiesUpdated = nil
	t.Cleanup(func() { OnCookiesUpdated = previousCallback })

	cookies := []CookieObject{
		{Name: "SID", Domain: ".google.com", Value: "cookie-secret", Path: "/"},
		{Name: "HSID", Domain: ".google.com", Value: "hsid-secret", Path: "/"},
	}
	path, _, err := ProcessAndSaveCookies(cookies, nil)
	if err != nil {
		t.Fatalf("ProcessAndSaveCookies: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedPayload(raw) {
		t.Fatal("synced cookie file was not encrypted")
	}
	if bytes.Contains(raw, []byte("cookie-secret")) || bytes.Contains(raw, []byte("hsid-secret")) {
		t.Fatal("plaintext cookie value leaked to disk")
	}

	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient with encrypted cookies: %v", err)
	}
	defer client.Close()
	if got := client.rawCookies(); !strings.Contains(got, "SID=cookie-secret") {
		t.Fatalf("decrypted cookie jar is missing SID: %q", got)
	}

	health := InspectAccountCookieHealth()
	if health.MonitoredAccounts != 1 {
		t.Fatalf("monitored accounts = %d, want 1", health.MonitoredAccounts)
	}
}

func TestCryptoValidateConfig(t *testing.T) {
	t.Setenv(CookieEncryptionKeyEnv, "")
	t.Setenv(RequireCookieEncryptionEnv, "")
	if err := ValidateCookieEncryptionConfig(); err != nil {
		t.Fatalf("optional encryption should not fail: %v", err)
	}

	t.Setenv(RequireCookieEncryptionEnv, "true")
	if err := ValidateCookieEncryptionConfig(); err == nil {
		t.Fatal("required encryption without a key must fail")
	}

	t.Setenv(CookieEncryptionKeyEnv, testCookieKey)
	if err := ValidateCookieEncryptionConfig(); err != nil {
		t.Fatalf("configured encryption should pass: %v", err)
	}
}

func FuzzDecryptCookieData(f *testing.F) {
	encrypted, err := EncryptCookieData([]byte(`[{"name":"SID","value":"seed"}]`), testCookieKey)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encrypted)
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"__gemini_encrypted":true}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// The contract is simple: arbitrary input must never panic, and any
		// successful decryption must produce bytes.
		plaintext, err := DecryptCookieData(data, testCookieKey)
		if err == nil && plaintext == nil {
			t.Fatal("nil plaintext without error")
		}
	})
}
