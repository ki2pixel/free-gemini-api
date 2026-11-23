package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"goapi/gemini"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UserSessions is a bounded, expiring cache. The eviction hook closes idle
// transport resources so a flood of distinct session identifiers cannot grow
// the process without limit (F-13).
var UserSessions = NewLRUCache(1024, 30*time.Minute, func(_ string, client *gemini.GeminiClient) {
	if client != nil {
		client.Close()
	}
})

const CookiesFile = "cookies/cookies.json"

// validIDRe defines the only characters accepted in a caller-supplied session
// or user identifier. Anything else is hashed before it is used as a cache key
// so control characters, separators and very long values cannot poison the
// session cache or upstream conversation bookkeeping.
var validIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// sanitizeSessionID validates raw and falls back to fallback (typically the
// client IP) when raw is empty. Invalid identifiers are replaced with a stable
// truncated SHA-256 digest, never with the raw value.
func sanitizeSessionID(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if validIDRe.MatchString(raw) {
		return raw
	}
	if raw == "" {
		raw = strings.TrimSpace(fallback)
		if validIDRe.MatchString(raw) {
			return raw
		}
		if raw == "" {
			return "anonymous"
		}
	}
	sum := sha256.Sum256([]byte(raw))
	return "s_" + hex.EncodeToString(sum[:16])
}

func DownloadAndClean(client *gemini.GeminiClient, urlStr, filename, fileType string) error {
	_ = fileType

	// Media filenames are generated internally, but validate them anyway so a
	// future caller cannot turn this helper into a path-writing primitive.
	base := filepath.Base(filename)
	if base == "" || base == "." || base == string(filepath.Separator) || base != filename || !filepath.IsLocal(base) {
		return fmt.Errorf("invalid media filename")
	}

	if err := os.MkdirAll("./output", 0700); err != nil {
		return err
	}

	// A dedicated directory per download removes the old shared .temp race
	// where two concurrent generations overwrote or deleted each other's files.
	tempDir, err := os.MkdirTemp("./output", ".dl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	tempPath := filepath.Join(tempDir, base)
	outputPath := filepath.Join("./output", base)

	if err := client.DownloadFile(urlStr, tempPath); err != nil {
		return err
	}

	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("failed to move downloaded media into output: %w", err)
	}
	_ = os.Chmod(outputPath, 0600)

	log.Printf("✨ Cleaned file moved to output: %s", outputPath)
	return nil
}

func GetOrCreateClient(sessionID string) (*gemini.GeminiClient, error) {
	if available := gemini.GetAvailableAccountCookieFiles(); len(available) > 0 {
		return GetOrCreateClientWithFile(sessionID, available[0])
	}
	return GetOrCreateClientWithFile(sessionID, CookiesFile)
}

func GetOrCreateClientWithFile(sessionID, cookieFilePath string) (*gemini.GeminiClient, error) {
	// Guest mode has no cookie file to stat or wait for.
	if cookieFilePath != "guest" {
		// If the requested path doesn't exist, check if an account file is available
		if _, err := os.Stat(cookieFilePath); os.IsNotExist(err) {
			if available := gemini.GetAvailableAccountCookieFiles(); len(available) > 0 {
				cookieFilePath = available[0]
			}
		}
	}

	cacheKey := fmt.Sprintf("%s_%s", sanitizeSessionID(sessionID, "session"), cookieFilePath)
	if client, ok := UserSessions.Get(cacheKey); ok {
		return client, nil
	}

	if cookieFilePath != "guest" {
		if _, err := os.Stat(cookieFilePath); os.IsNotExist(err) {
			log.Printf("⚠️ %s not found. Requesting Chrome Extension to proactively sync cookies...", cookieFilePath)
			gemini.BroadcastCookieRefresh()

			log.Println("⏳ Sleeping 5 seconds waiting for extension to sync cookies...")
			time.Sleep(5 * time.Second)

			if available := gemini.GetAvailableAccountCookieFiles(); len(available) > 0 {
				cookieFilePath = available[0]
			} else if _, err := os.Stat(cookieFilePath); os.IsNotExist(err) {
				return nil, fmt.Errorf("no account cookies found; ensure the Chrome extension is connected and syncs cookies first")
			}
		}
	}

	client, err := gemini.NewClient(cookieFilePath)
	if err != nil {
		return nil, err
	}

	UserSessions.Set(cacheKey, client)
	log.Printf("New session created for: %s (using %s)", sessionID, cookieFilePath)
	return client, nil
}

// ExecuteWithFailover executes an operation across all active worker accounts using the high-performance WorkerPool
func ExecuteWithFailover(sessionID string, op func(client *gemini.GeminiClient) (*gemini.GeminiResponse, error)) (*gemini.GeminiResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return GetWorkerPool().ExecuteQueued(ctx, sessionID, op)
}

func StartWebSocketBridge() {
	gemini.OnCookiesUpdated = func() {
		log.Println("♻️  Extension pushed new cookies. Reloading active sessions across all workers...")
		UserSessions.Range(func(key string, client *gemini.GeminiClient) bool {
			sessionID := key

			if err := client.ReloadSession(); err != nil {
				log.Printf("❌ Failed to reload session for %s: %v", sessionID, err)
			} else {
				log.Printf("✅ Session %s refreshed", sessionID)
			}
			return true
		})

		// Reload the multi-account WorkerPool
		GetWorkerPool().ReloadPool()
	}

	wsPortStr := os.Getenv("WS_PORT")
	if wsPortStr == "" {
		wsPortStr = "9226"
	}
	wsPort, err := strconv.Atoi(wsPortStr)
	if err != nil {
		wsPort = 9226
	}

	go gemini.StartCookieWebSocketServer(wsPort)
}
