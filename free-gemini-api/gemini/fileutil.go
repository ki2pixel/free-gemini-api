package gemini

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// cookieFileMu serializes in-process cookie/session file reads and writes.
// Without it, a health-check migration of an old plaintext file could race a
// cookie sync and overwrite the freshly written encrypted file.
var cookieFileMu sync.Mutex

// writeFileAtomic writes data to a temporary file in the destination directory
// and renames it into place. Renaming within one directory is atomic on POSIX
// filesystems, so readers never observe a partially written cookie or session
// file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeCookieFileAtomic encrypts cookie or session-cache data with the active
// key (when configured) and writes the result atomically. If encryption fails
// the file is left untouched rather than falling back to plaintext.
func writeCookieFileAtomic(path string, data []byte, perm os.FileMode) error {
	cookieFileMu.Lock()
	defer cookieFileMu.Unlock()

	if key := GetCookieEncryptionKey(); key != "" {
		encrypted, err := EncryptCookieData(data, key)
		if err != nil {
			return fmt.Errorf("encrypt cookie file %s: %w", path, err)
		}
		data = encrypted
	}
	return writeFileAtomic(path, data, perm)
}

// readCookieFile reads a cookie or session-cache file. Encrypted envelopes are
// decrypted transparently; legacy plaintext files remain readable. When a key
// is configured, a legacy plaintext file is upgraded in place after a
// successful read so existing installations migrate without a sync.
func readCookieFile(path string) ([]byte, error) {
	cookieFileMu.Lock()
	defer cookieFileMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	key := GetCookieEncryptionKey()
	if !IsEncryptedPayload(data) {
		if key != "" {
			if encrypted, encErr := EncryptCookieData(data, key); encErr == nil {
				if writeErr := writeFileAtomic(path, encrypted, 0600); writeErr != nil {
					log.Printf("⚠️  Cookie encryption migration failed for %s: %v", path, writeErr)
				}
			}
		}
		return data, nil
	}

	if key == "" {
		return nil, fmt.Errorf("cookie file %s is encrypted but %s is not set", path, CookieEncryptionKeyEnv)
	}
	plaintext, err := DecryptCookieData(data, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookie file %s: %w", path, err)
	}
	return plaintext, nil
}
