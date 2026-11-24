package gemini

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chromeStyleCookies mirrors cookies/account_ga000FAlQD.json: the same OSID /
// __Secure-OSID names exist for several Google subdomains, and
// console.cloud.google.com appears last in the export.
func chromeStyleCookies() []CookieObject {
	return []CookieObject{
		{Domain: ".google.com", Name: "__Secure-1PSID", Value: "google-psid", Path: "/", Secure: true},
		{Domain: ".google.com", Name: "SAPISID", Value: "google-sapisid", Path: "/", Secure: true},
		{Domain: "myaccount.google.com", HostOnly: true, Name: "OSID", Value: "myaccount-osid", Path: "/", Secure: true},
		{Domain: "myaccount.google.com", HostOnly: true, Name: "__Secure-OSID", Value: "myaccount-secure-osid", Path: "/", Secure: true},
		{Domain: "gemini.google.com", HostOnly: true, Name: "OSID", Value: "gemini-osid", Path: "/", Secure: true},
		{Domain: "gemini.google.com", HostOnly: true, Name: "__Secure-OSID", Value: "gemini-secure-osid", Path: "/", Secure: true},
		{Domain: "gemini.google.com", HostOnly: true, Name: "COMPASS", Value: "gemini-compass", Path: "/", Secure: true},
		{Domain: "console.cloud.google.com", HostOnly: true, Name: "OSID", Value: "console-osid", Path: "/", Secure: true},
		{Domain: "console.cloud.google.com", HostOnly: true, Name: "__Secure-OSID", Value: "console-secure-osid", Path: "/", Secure: true},
		{Domain: "console.cloud.google.com", HostOnly: true, Name: "COMPASS", Value: "console-compass", Path: "/", Secure: true},
	}
}

func writeCookieFileForTest(t *testing.T, cookies []CookieObject) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account_test.json")
	data, err := json.Marshal(cookies)
	if err != nil {
		t.Fatalf("marshal cookies: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write cookies: %v", err)
	}
	return path
}

func TestLoadCookiesKeepsSubdomainCookiesScoped(t *testing.T) {
	client, err := NewClient(writeCookieFileForTest(t, chromeStyleCookies()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	header := client.rawCookies()
	if header == "" {
		t.Fatal("raw cookie header is empty")
	}

	// The Gemini host-only values must win: console.cloud.google.com is last in
	// the export, so the old loader let it overwrite gemini.google.com.
	for _, want := range []string{
		"OSID=gemini-osid",
		"__Secure-OSID=gemini-secure-osid",
		"COMPASS=gemini-compass",
		"__Secure-1PSID=google-psid",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("cookie header %q is missing %q", header, want)
		}
	}
	for _, forbidden := range []string{
		"myaccount-osid",
		"myaccount-secure-osid",
		"console-osid",
		"console-secure-osid",
		"console-compass",
	} {
		if strings.Contains(header, forbidden) {
			t.Errorf("cookie header leaked %q from another Google subdomain: %q", forbidden, header)
		}
	}

	// Exactly one OSID entry: the jar must not collapse two domain-scoped
	// cookies of the same name into one.
	if got := strings.Count(header, "OSID="); got != 2 {
		t.Errorf("OSID entries = %d in %q, want 2 (OSID and __Secure-OSID)", got, header)
	}
}

func TestCookieJarScopesByRequestHost(t *testing.T) {
	client, err := NewClient(writeCookieFileForTest(t, chromeStyleCookies()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	geminiURL, _ := url.Parse("https://gemini.google.com/app")
	values := map[string]string{}
	for _, c := range client.client.GetCookieJar().Cookies(geminiURL) {
		values[c.Name] = c.Value
	}
	if values["OSID"] != "gemini-osid" {
		t.Errorf("gemini OSID = %q, want gemini-osid", values["OSID"])
	}
	if values["__Secure-OSID"] != "gemini-secure-osid" {
		t.Errorf("gemini __Secure-OSID = %q, want gemini-secure-osid", values["__Secure-OSID"])
	}
	if values["__Secure-1PSID"] != "google-psid" {
		t.Errorf(".google.com __Secure-1PSID = %q, want google-psid", values["__Secure-1PSID"])
	}

	consoleURL, _ := url.Parse("https://console.cloud.google.com/")
	consoleValues := map[string]string{}
	for _, c := range client.client.GetCookieJar().Cookies(consoleURL) {
		consoleValues[c.Name] = c.Value
	}
	if consoleValues["OSID"] != "console-osid" {
		t.Errorf("console OSID = %q, want console-osid", consoleValues["OSID"])
	}
	// A .google.com domain cookie is legitimately sent to every Google
	// subdomain; what must never happen is a host-only cookie leaking.
	if consoleValues["__Secure-1PSID"] != "google-psid" {
		t.Errorf("domain cookie __Secure-1PSID = %q, want google-psid", consoleValues["__Secure-1PSID"])
	}

	myAccountURL, _ := url.Parse("https://myaccount.google.com/")
	for _, c := range client.client.GetCookieJar().Cookies(myAccountURL) {
		if c.Name == "COMPASS" {
			t.Errorf("myaccount received COMPASS %q, which is host-only to gemini/console", c.Value)
		}
	}
}

func TestCookieJar_HostOnlyVsDomainAttribute(t *testing.T) {
	client, err := NewClient(writeCookieFileForTest(t, []CookieObject{
		{Domain: "gemini.google.com", HostOnly: true, Name: "__Secure-OSID", Value: "gemini-host-only", Path: "/", Secure: true},
		{Domain: ".google.com", Name: "SID", Value: "google-domain", Path: "/", Secure: true},
	}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	jar := client.client.GetCookieJar()
	requestCookies := func(rawURL string) map[string]string {
		t.Helper()
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %s: %v", rawURL, err)
		}
		values := make(map[string]string)
		for _, c := range jar.Cookies(u) {
			values[c.Name] = c.Value
		}
		return values
	}

	gemini := requestCookies("https://gemini.google.com/app")
	if gemini["__Secure-OSID"] != "gemini-host-only" {
		t.Errorf("gemini __Secure-OSID = %q, want gemini-host-only", gemini["__Secure-OSID"])
	}
	if gemini["SID"] != "google-domain" {
		t.Errorf("gemini SID = %q, want google-domain", gemini["SID"])
	}

	for _, target := range []string{"https://mail.google.com/", "https://sub.gemini.google.com/"} {
		got := requestCookies(target)
		if value, ok := got["__Secure-OSID"]; ok {
			t.Errorf("%s received host-only __Secure-OSID = %q", target, value)
		}
		if got["SID"] != "google-domain" {
			t.Errorf("%s SID = %q, want google-domain", target, got["SID"])
		}
	}
}

func TestScopedCookieHonorsHostOnly(t *testing.T) {
	origin, cookie, ok := scopedCookie(CookieObject{
		Domain: "gemini.google.com", HostOnly: true, Name: "OSID", Value: "v", Secure: true,
	})
	if !ok {
		t.Fatal("host-only cookie was rejected")
	}
	if origin.Host != "gemini.google.com" {
		t.Errorf("origin host = %q", origin.Host)
	}
	if cookie.Domain != "" {
		t.Errorf("host-only cookie must not carry a Domain attribute, got %q", cookie.Domain)
	}

	_, domainCookie, ok := scopedCookie(CookieObject{Domain: ".google.com", Name: "SID", Value: "v"})
	if !ok {
		t.Fatal("domain cookie was rejected")
	}
	if domainCookie.Domain != "google.com" {
		t.Errorf("domain cookie Domain = %q, want google.com", domainCookie.Domain)
	}
}

func TestLegacyCookieStringStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(`{"cookies":"SID=legacy-sid; HSID=legacy-hsid","updated_at":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	header := client.rawCookies()
	if !strings.Contains(header, "SID=legacy-sid") {
		t.Errorf("legacy cookie header = %q", header)
	}
}
