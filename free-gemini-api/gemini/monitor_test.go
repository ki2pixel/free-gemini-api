package gemini

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCookieDomainAndValidation(t *testing.T) {
	allowed := []string{"google.com", ".google.com", "sub.google.com", "googleusercontent.com"}
	for _, domain := range allowed {
		if !isAllowedCookieDomain(domain) {
			t.Errorf("domain %q should be allowed", domain)
		}
	}
	for _, domain := range []string{"", "evil.com", "google.com.evil.com", "notgoogle.com"} {
		if isAllowedCookieDomain(domain) {
			t.Errorf("domain %q should be rejected", domain)
		}
	}

	if _, err := validateIncomingCookies(nil); err == nil {
		t.Fatal("empty payload should be rejected")
	}
	tooMany := make([]CookieObject, maxCookiesPerSync+1)
	if _, err := validateIncomingCookies(tooMany); err == nil {
		t.Fatal("oversized payload should be rejected")
	}
	if _, err := validateIncomingCookies([]CookieObject{{Name: "SID", Domain: "evil.com", Value: "x"}}); err == nil {
		t.Fatal("non-Google cookie should be rejected")
	}

	valid, err := validateIncomingCookies([]CookieObject{
		{Name: "SID", Domain: ".google.com", Value: "value", Path: ""},
		{Name: "evil", Domain: ".google.com", Value: "value"},
		{Name: "X", Domain: ".google.com", Value: "value"},
	})
	if err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if len(valid) != 1 || valid[0].Name != "SID" || valid[0].Path != "/" {
		t.Fatalf("unexpected validated cookies: %+v", valid)
	}
}

func TestGetAccountIDFromCookies(t *testing.T) {
	if got := getAccountIDFromCookies(nil); got != "primary" {
		t.Fatalf("empty cookies id = %q, want primary", got)
	}
	got := getAccountIDFromCookies([]CookieObject{{Name: "__Secure-1PSID", Value: "abc-DEF-1234567890"}})
	if got != "abcDEF1234" {
		t.Fatalf("account id = %q, want abcDEF1234", got)
	}
	short := getAccountIDFromCookies([]CookieObject{{Name: "SID", Value: "abc"}})
	if short != "abc" {
		t.Fatalf("short account id = %q", short)
	}
}

func TestProcessAndSaveCookies(t *testing.T) {
	t.Chdir(t.TempDir())

	cookies := []CookieObject{
		{Name: "SID", Domain: ".google.com", Value: "sid-value", Path: "/"},
		{Name: "HSID", Domain: ".google.com", Value: "hsid-value", Path: "/"},
	}

	called := 0
	OnCookiesUpdated = func() { called++ }
	t.Cleanup(func() { OnCookiesUpdated = nil })

	path, accountID, err := ProcessAndSaveCookies(cookies, nil)
	if err != nil {
		t.Fatalf("ProcessAndSaveCookies: %v", err)
	}
	if accountID == "" || path == "" {
		t.Fatalf("unexpected result %q %q", path, accountID)
	}
	if called != 1 {
		t.Fatalf("OnCookiesUpdated called %d times, want 1", called)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat account file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("cookie file mode = %o, want 0600", info.Mode().Perm())
	}
	if getCachedImageCookies() == "" {
		t.Fatal("cached image cookies were not updated")
	}

	// Identical payload must be a no-op.
	if _, _, err := ProcessAndSaveCookies(cookies, nil); err != nil {
		t.Fatalf("second ProcessAndSaveCookies: %v", err)
	}
	if called != 1 {
		t.Fatalf("identical payload should not fire the callback again, got %d", called)
	}

	profileID := 7
	profilePath, profileAccount, err := ProcessAndSaveCookies(cookies, &profileID)
	if err != nil {
		t.Fatalf("profile ProcessAndSaveCookies: %v", err)
	}
	if profileAccount != "profile_7" {
		t.Fatalf("profile account = %q", profileAccount)
	}
	if filepath.Base(profilePath) != "account_profile_7.json" {
		t.Fatalf("profile path = %q", profilePath)
	}

	if _, _, err := ProcessAndSaveCookies([]CookieObject{{Name: "SID", Domain: "evil.com", Value: "x"}}, nil); err == nil {
		t.Fatal("invalid payload should fail")
	}
}

func TestAvailableCookieFilesAndCounts(t *testing.T) {
	t.Chdir(t.TempDir())

	if files := GetAvailableAccountCookieFiles(); len(files) != 0 {
		t.Fatalf("expected no cookie files, got %v", files)
	}
	if count := GetActiveAccountCount(); count != 0 {
		t.Fatalf("expected 0 accounts, got %d", count)
	}
	if count := GetActiveWorkerCount(); count != 0 {
		t.Fatalf("expected 0 workers, got %d", count)
	}

	if err := os.MkdirAll("cookies", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("cookies", "cookies.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	files := GetAvailableAccountCookieFiles()
	if len(files) != 1 || filepath.Base(files[0]) != "cookies.json" {
		t.Fatalf("unexpected default cookie files: %v", files)
	}

	if err := os.WriteFile(filepath.Join("cookies", "account_a.json"), []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	files = GetAvailableAccountCookieFiles()
	if len(files) != 1 || filepath.Base(files[0]) != "account_a.json" {
		t.Fatalf("unexpected account cookie files: %v", files)
	}
	if count := GetActiveAccountCount(); count != 1 {
		t.Fatalf("expected 1 account, got %d", count)
	}
}

func TestCookieHealthAndBroadcast(t *testing.T) {
	t.Chdir(t.TempDir())

	info := InspectAccountCookieHealth()
	if info.Status != "no_cookies" {
		t.Fatalf("status = %q, want no_cookies", info.Status)
	}

	// No connected clients: broadcast must be a harmless no-op.
	BroadcastCookieRefresh()
	BroadcastCookieRefresh()

	if err := os.MkdirAll("cookies", 0700); err != nil {
		t.Fatal(err)
	}
	cookies := []CookieObject{{Name: "SID", Domain: ".google.com", Value: "value", Path: "/"}}
	if _, _, err := ProcessAndSaveCookies(cookies, nil); err != nil {
		t.Fatal(err)
	}
	info = InspectAccountCookieHealth()
	if info.MonitoredAccounts != 1 {
		t.Fatalf("monitored accounts = %d, want 1", info.MonitoredAccounts)
	}
	if info.LastSyncedAt == "" {
		t.Fatal("expected last synced timestamp")
	}
}

func TestWebSocketSecurityHelpers(t *testing.T) {
	loopbacks := []string{"localhost", "127.0.0.1", "::1", "[::1]"}
	for _, host := range loopbacks {
		if !isLoopbackWSHost(host) {
			t.Errorf("host %q should be loopback", host)
		}
	}
	for _, host := range []string{"", "example.com", "10.0.0.1"} {
		if isLoopbackWSHost(host) {
			t.Errorf("host %q should not be loopback", host)
		}
	}

	origins := []string{"", "chrome-extension://abcdef", "http://127.0.0.1:8100", "https://localhost:3000"}
	for _, origin := range origins {
		if !isAllowedWSOrigin(origin) {
			t.Errorf("origin %q should be allowed", origin)
		}
	}
	for _, origin := range []string{"https://evil.example", "file:///tmp/x", "not a url"} {
		if isAllowedWSOrigin(origin) {
			t.Errorf("origin %q should be rejected", origin)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, "/ws?token=query-token", nil)
	if got := requestWSToken(req); got != "query-token" {
		t.Fatalf("query token = %q", got)
	}
	req, _ = http.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("X-WS-Token", "header-token")
	if got := requestWSToken(req); got != "header-token" {
		t.Fatalf("header token = %q", got)
	}
	req, _ = http.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer bearer-token")
	if got := requestWSToken(req); got != "bearer-token" {
		t.Fatalf("bearer token = %q", got)
	}
	req, _ = http.NewRequest(http.MethodGet, "/ws", nil)
	if got := requestWSToken(req); got != "" {
		t.Fatalf("missing token = %q", got)
	}
}
