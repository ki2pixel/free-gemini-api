package gemini

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Audit-only test: DownloadFile forwards the Google cookie header to every
// redirect hop, regardless of host. A media URL that redirects to an
// attacker-controlled origin therefore leaks the account session cookies.
func TestAuditDownloadFileForwardsCookiesAcrossRedirect(t *testing.T) {
	var receivedCookie string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(strings.Repeat("A", 2048)))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/asset.png", http.StatusFound)
	}))
	defer redirector.Close()

	CachedImageCookies = ""
	c, err := NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}
	c.RawCookies = "SECRETSESSION=super-secret-google-session"

	if err := c.DownloadFile(redirector.URL+"/start", filepath.Join(t.TempDir(), "asset.png")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receivedCookie, "SECRETSESSION=super-secret-google-session") {
		t.Fatalf("expected cookie to be forwarded across redirect, got %q", receivedCookie)
	}
	t.Logf("redirect target received Google cookie header: %q", receivedCookie)
}
