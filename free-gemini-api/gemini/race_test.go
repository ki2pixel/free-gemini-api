package gemini

import (
	"sync"
	"testing"
)

// TestAuditRace exercises the fields called out by the November 2026 audit:
// initialized state, account prefix, cached image cookies and conversation
// state. Run with: go test -race -run TestAuditRace ./gemini/
func TestAuditRace(t *testing.T) {
	client, err := NewClient("guest")
	if err != nil {
		t.Fatalf("NewClient(guest) failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(5)

		go func() {
			defer wg.Done()
			_ = client.ensureInit()
		}()

		go func() {
			defer wg.Done()
			_ = client.ReloadSession()
		}()

		go func() {
			defer wg.Done()
			_ = client.IsInitialized()
			_ = client.getAccountPrefix()
			_ = client.GetAccountTier()
		}()

		go func() {
			defer wg.Done()
			client.updateConvStateFor("session", "model", "conv", "resp", "choice")
			_, _, _ = client.getConvStateFor("session", "model")
			client.ResetConversationStateSession("session", "model")
		}()

		go func() {
			defer wg.Done()
			setCachedImageCookies("SID=value; HSID=value")
			_ = getCachedImageCookies()
		}()
	}

	wg.Wait()
}
