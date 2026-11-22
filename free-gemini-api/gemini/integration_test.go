package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func jspbLine(text string) string {
	payload := fmt.Sprintf(`[null,["c_test","r_test"],null,null,[["ch_test",[%s]]]]`, strconv.Quote(text))
	return fmt.Sprintf(`[["wrb.fr",null,%s]]`, strconv.Quote(payload))
}

func newGeminiTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	var (
		server    *httptest.Server
		mu        sync.Mutex
		appHits   int
		streamHit int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		appHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><script>window.APP={"SNlM0e":"token-123","FdrFJe":"fsid-456","cfb2h":"bl-789"};</script></html>`))
	})
	mux.HandleFunc("/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		streamHit++
		mu.Unlock()
		_ = r.ParseForm()
		text := "Hello"
		if strings.Contains(r.FormValue("f.req"), "Generate a video") {
			text = "generating your video"
		}
		_, _ = fmt.Fprintln(w, jspbLine(text))
		_, _ = fmt.Fprintln(w, jspbLine(text+" world"))
	})
	mux.HandleFunc("/_/BardChatUi/data/batchexecute", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`["https://contribution.usercontent.google.com/download?filename=video.mp4"]`))
	})
	mux.HandleFunc("/upload/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ref-upload-123"))
	})
	mux.HandleFunc("/upload/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Goog-Upload-Url", server.URL+"/upload/final")
		_, _ = w.Write([]byte("started"))
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		_ = appHits
		_ = streamHit
	})
	return server
}

func useTestGeminiEndpoints(t *testing.T, server *httptest.Server) {
	t.Helper()
	oldWeb, oldUpload := geminiWebBaseURL, geminiUploadURL
	geminiWebBaseURL = server.URL
	geminiUploadURL = server.URL + "/upload/"
	t.Cleanup(func() {
		geminiWebBaseURL = oldWeb
		geminiUploadURL = oldUpload
	})
}

func TestGeminiClientHTTPPaths(t *testing.T) {
	server := newGeminiTestServer(t)
	useTestGeminiEndpoints(t, server)

	client, err := NewClient("guest")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	if err := client.InitSession(); err != nil {
		t.Fatalf("InitSession: %v", err)
	}

	resp, err := client.Ask("hello")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Text == "" || resp.ConversationID != "c_test" {
		t.Fatalf("unexpected Ask response: %+v", resp)
	}

	var chunks []string
	resp, err = client.AskStream("hello", func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	if len(chunks) == 0 || !strings.Contains(resp.Text, "Hello") {
		t.Fatalf("stream did not emit chunks: %v %+v", chunks, resp)
	}

	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 32)...)
	resp, err = client.AskWithImage("describe", png, "a.png", "image/png")
	if err != nil {
		t.Fatalf("AskWithImage: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("expected a response for the image request")
	}

	_, err = client.AskWithImages("describe", []ImageInput{{Data: png, Filename: "a.png", MimeType: "image/png"}})
	if err != nil {
		t.Fatalf("AskWithImages: %v", err)
	}

	_, err = client.AskModelSession("hello", "", "session-a")
	if err != nil {
		t.Fatalf("AskModelSession: %v", err)
	}
	if got := client.CurrentConversationIDFor("session-a", ""); got != "" && got != "c_test" {
		t.Fatalf("unexpected conversation id %q", got)
	}

	_, err = client.AskWithToolModel("hello", "", "")
	if err != nil {
		t.Fatalf("AskWithToolModel: %v", err)
	}
	_, err = client.AskWithTool("hello", "")
	if err != nil {
		t.Fatalf("AskWithTool: %v", err)
	}
	_, err = client.AskWithToolModelSession("hello", "", "", "session-b")
	if err != nil {
		t.Fatalf("AskWithToolModelSession: %v", err)
	}

	body, err := client.CallRPC("hNvQHb", `[]`)
	if err != nil {
		t.Fatalf("CallRPC: %v", err)
	}
	if !strings.Contains(body, "contribution") {
		t.Fatalf("unexpected RPC body %q", body)
	}
	if _, err := client.pollConversation("c_test"); err != nil {
		t.Fatalf("pollConversation: %v", err)
	}

	// Video polling takes the first 2.5s delay and then finds the URL.
	start := time.Now()
	resp, err = client.AskVideo("a cat")
	if err != nil {
		t.Fatalf("AskVideo: %v", err)
	}
	if len(resp.Videos) == 0 {
		t.Fatalf("expected a video URL, got %+v", resp)
	}
	if time.Since(start) < 2*time.Second {
		t.Fatal("expected video polling delay")
	}

	if _, err := DiscoverAccountModels(client); err != nil {
		t.Fatalf("DiscoverAccountModels: %v", err)
	}

	// Force the QUIC path to fail and verify the HTTP/2 fallback still works.
	client.useQUIC.Store(true)
	if _, err := client.Ask("fallback"); err != nil {
		t.Fatalf("QUIC fallback Ask: %v", err)
	}
	client.useQUIC.Store(true)
	if _, err := client.AskStream("fallback", nil); err != nil {
		t.Fatalf("QUIC fallback AskStream: %v", err)
	}
	client.useQUIC.Store(false)
}

func TestSessionCachePersistAndReload(t *testing.T) {
	t.Chdir(t.TempDir())
	server := newGeminiTestServer(t)
	useTestGeminiEndpoints(t, server)

	cookieFile := filepath.Join(t.TempDir(), "cookies.json")
	cookieJSON, _ := json.Marshal([]CookieObject{
		{Domain: ".google.com", Name: "SID", Value: "sid-value", Path: "/", Secure: true},
		{Domain: ".google.com", Name: "HSID", Value: "hsid-value", Path: "/", Secure: true},
	})
	if err := os.WriteFile(cookieFile, cookieJSON, 0600); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(cookieFile)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.InitSession(); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if !client.IsInitialized() {
		t.Fatal("client should be initialized")
	}
	cachePath := client.getSessionCachePath()
	if cachePath == "" {
		t.Fatal("expected a session cache path")
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("session cache not persisted: %v", err)
	}

	// A second client with the same cookies must load the cached session
	// without another /app round-trip.
	client2, err := NewClient(cookieFile)
	if err != nil {
		t.Fatalf("second NewClient: %v", err)
	}
	if !client2.loadCachedSession() {
		t.Fatal("expected cached session to load")
	}
	if !client2.IsInitialized() {
		t.Fatal("cached client should be initialized")
	}

	// executeWithRetry must reinitialize after a failure and then succeed.
	attempts := 0
	err = client2.executeWithRetry("test-action", func() error {
		attempts++
		if attempts == 1 {
			return errors.New("transient failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("executeWithRetry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}

	if err := client2.ReloadSession(); err != nil {
		t.Fatalf("ReloadSession: %v", err)
	}
	if client2.IsInitialized() {
		t.Fatal("ReloadSession must mark the client uninitialized")
	}

	// Unsupported cookie formats are rejected.
	badFile := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(badFile, []byte(`{"unexpected":true}`), 0600)
	if _, err := NewClient(badFile); err == nil {
		t.Fatal("unsupported cookie format should fail")
	}

	// Missing cookie files fall back to guest mode.
	missing, err := NewClient(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing cookie file should fall back to guest: %v", err)
	}
	missing.Close()

	// Legacy string format.
	legacyFile := filepath.Join(t.TempDir(), "legacy.json")
	_ = os.WriteFile(legacyFile, []byte(`{"cookies":"SID=abc; HSID=def","updated_at":1}`), 0600)
	legacy, err := NewClient(legacyFile)
	if err != nil {
		t.Fatalf("legacy cookie format: %v", err)
	}
	if legacy.rawCookies() == "" {
		t.Fatal("legacy cookies were not loaded")
	}
	legacy.Close()
}

func TestLoadCookiesGuestAndCachePath(t *testing.T) {
	guest, err := NewClient("guest")
	if err != nil {
		t.Fatal(err)
	}
	if guest.getSessionCachePath() != "" {
		t.Fatal("guest clients have no session cache path")
	}
	firstHash := guest.hashCookies()
	if firstHash != guest.hashCookies() {
		t.Fatal("hashCookies must be deterministic")
	}
	guest.persistSession() // no-op for guest
	guest.Close()
}
