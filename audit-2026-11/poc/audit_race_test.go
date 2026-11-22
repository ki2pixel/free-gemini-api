package gemini

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Audit-only tests: these exercise state that production request paths touch
// concurrently. They are not part of the shipped test suite.

func TestAuditRaceAccountPrefix(t *testing.T) {
	t.Setenv("GEMINI_ACCOUNT_PREFIX", "/u/7")
	c, err := NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountPrefix = ""

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = c.getAccountPrefix()
		}()
	}
	close(start)
	wg.Wait()
}

func TestAuditRaceReloadSession(t *testing.T) {
	c, err := NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = c.ensureInit()
		}()
		go func() {
			defer wg.Done()
			_ = c.ReloadSession()
		}()
	}
	wg.Wait()
}

func TestAuditRaceCachedImageCookies(t *testing.T) {
	dir := t.TempDir()
	old, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	c, err := NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, _ = ProcessAndSaveCookies([]CookieObject{{
				Domain: ".google.com", Name: "SID", Value: "audit-race", Path: "/",
			}}, nil)
		}()
		go func() {
			defer wg.Done()
			_ = c.DownloadFile("http://127.0.0.1:1/audit", filepath.Join(dir, "audit.bin"))
		}()
	}
	wg.Wait()
}
