package gemini

import (
	"net/url"
	"path/filepath"
	"testing"
)

func TestIsAllowedDownloadHost(t *testing.T) {
	allowed := []string{
		"google.com",
		"gemini.google.com",
		"lh3.googleusercontent.com",
		"www.gstatic.com",
		"storage.googleapis.com",
		"r1.googlevideo.com",
		"GOOGLE.COM.",
	}
	for _, host := range allowed {
		if !isAllowedDownloadHost(host) {
			t.Errorf("isAllowedDownloadHost(%q) = false, want true", host)
		}
	}

	rejected := []string{
		"",
		"evil.example",
		"evilgoogle.com",
		"google.com.evil.example",
		"googleusercontent.com.evil.example",
		"notgoogle.com",
		"google.com@evil.example",
	}
	for _, host := range rejected {
		if isAllowedDownloadHost(host) {
			t.Errorf("isAllowedDownloadHost(%q) = true, want false", host)
		}
	}
}

func TestSameCookieDomain(t *testing.T) {
	parse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return u
	}

	cases := []struct {
		from, to string
		want     bool
	}{
		{"https://gemini.google.com/a", "https://gemini.google.com/b", true},
		{"https://google.com/a", "https://gemini.google.com/b", true},
		{"https://google.com/a", "https://lh3.googleusercontent.com/b", false},
		{"https://lh3.googleusercontent.com/a", "https://google.com/b", false},
		{"https://gemini.google.com/a", "https://evil.example/b", false},
		{"http://gemini.google.com/a", "https://gemini.google.com/b", false},
	}
	for _, tc := range cases {
		if got := sameCookieDomain(parse(tc.from), parse(tc.to)); got != tc.want {
			t.Errorf("sameCookieDomain(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestDownloadFileRejectsUntrustedTargets(t *testing.T) {
	client := &GeminiClient{}
	targets := []string{
		"http://gemini.google.com/image.png",
		"https://evil.example/image.png",
		"file:///etc/passwd",
	}
	for _, target := range targets {
		err := client.DownloadFile(target, filepath.Join(t.TempDir(), "download.bin"))
		if err == nil {
			t.Errorf("DownloadFile(%q) accepted an untrusted target", target)
		}
	}
}
