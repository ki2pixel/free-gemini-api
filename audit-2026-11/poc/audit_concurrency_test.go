package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"goapi/gemini"
)

// Audit-only test: two concurrent DownloadAndClean calls share ./output/.temp.
// The first call to finish executes os.RemoveAll(tempDir), destroying the
// other call's in-flight temporary file.
func TestAuditDownloadAndCleanSharedTempCollision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		// Send one chunk so DownloadFile has created its temp file, then keep
		// the body open while the other download finishes and cleans the
		// shared temp directory.
		_, _ = w.Write(bytes.Repeat([]byte("A"), 2048))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if r.URL.Path == "/slow" {
			time.Sleep(600 * time.Millisecond)
		} else {
			time.Sleep(500 * time.Millisecond)
		}
		_, _ = w.Write(bytes.Repeat([]byte("B"), 2048))
	}))
	defer srv.Close()

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	if err := os.MkdirAll("output", 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := gemini.NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, job := range []struct{ url, name string }{
		{srv.URL + "/fast", "fast.png"},
		{srv.URL + "/slow", "slow.png"},
	} {
		wg.Add(1)
		go func(u, n string) {
			defer wg.Done()
			errs <- DownloadAndClean(c, u, n, "image")
		}(job.url, job.name)
	}
	wg.Wait()
	close(errs)

	failures := 0
	for err := range errs {
		if err != nil {
			failures++
			t.Logf("concurrent download failed: %v", err)
		}
	}
	if failures == 0 {
		t.Fatalf("expected the shared ./output/.temp directory to corrupt at least one concurrent download")
	}
	t.Logf("%d/2 concurrent downloads failed due to shared temp directory", failures)
}

// Audit-only test: DownloadAndClean joins the caller-supplied filename
// directly into ./output and ./output/.temp. A filename containing ../ writes
// outside the output directory.
func TestAuditDownloadAndCleanFilenameTraversal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte("A"), 2048))
	}))
	defer srv.Close()

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	if err := os.MkdirAll("output", 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := gemini.NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}
	if err := DownloadAndClean(c, srv.URL+"/asset.png", "../../escaped.txt", "image"); err != nil {
		t.Fatalf("download failed: %v", err)
	}
	if _, err := os.Stat("../escaped.txt"); err != nil {
		t.Fatalf("expected file to be written outside output/: %v", err)
	}
	t.Log("filename traversal wrote ../escaped.txt outside output/")
}
