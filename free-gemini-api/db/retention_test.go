package db

import (
	"strings"
	"testing"
)

func TestLogMessageLegacyAndSearch(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	conv := "conv_search_" + t.Name()
	if err := LogMessage(conv, "user-search", "user", "the quick brown fox", "model-a", 12); err != nil {
		t.Fatalf("LogMessage: %v", err)
	}
	if err := LogMessageWithTier(conv, "user-search", "assistant", "jumps over the lazy dog", "model-a", 20, "subscription", "acc-search"); err != nil {
		t.Fatalf("LogMessageWithTier: %v", err)
	}

	recent, err := GetRecentMessages(conv, 10)
	if err != nil {
		t.Fatalf("GetRecentMessages: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent messages, got %d", len(recent))
	}

	results, err := SearchMessages("quick brown", 5)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	found := false
	for _, row := range results {
		if row["content"] == "the quick brown fox" {
			found = true
		}
	}
	if !found {
		t.Fatalf("search did not find the message: %v", results)
	}

	results, err = SearchMessages("", 0)
	if err != nil {
		t.Fatalf("SearchMessages empty: %v", err)
	}
	if len(results) > 10 {
		t.Fatalf("default limit not applied: %d", len(results))
	}
}

func TestMediaQueries(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	conv := "conv_media_" + t.Name()
	if err := LogMediaGeneration("image", "a red fox", "fox.png", "./output/fox.png", "http://x/fox.png", "16:9", "resp-1"); err != nil {
		t.Fatalf("LogMediaGeneration: %v", err)
	}
	if err := LogMediaGenerationForConversation(conv, "video", "a blue whale", "whale.mp4", "./output/whale.mp4", "http://x/whale.mp4", "9:16", "resp-2"); err != nil {
		t.Fatalf("LogMediaGenerationForConversation: %v", err)
	}

	images, err := GetMediaByPrompt("image", "red", 5)
	if err != nil {
		t.Fatalf("GetMediaByPrompt: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("expected at least one image")
	}
	for _, item := range images {
		if item["type"] != "image" {
			t.Errorf("unexpected media type %v", item["type"])
		}
	}

	all, err := GetMediaByPrompt("all", "", 50)
	if err != nil {
		t.Fatalf("GetMediaByPrompt all: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected at least 2 media rows, got %d", len(all))
	}

	exported, err := GetAllMediaForExport()
	if err != nil {
		t.Fatalf("GetAllMediaForExport: %v", err)
	}
	foundConv := false
	for _, item := range exported {
		if item["conversation_id"] == conv {
			foundConv = true
		}
	}
	if !foundConv {
		t.Fatal("media export did not include conversation_id")
	}
}

func TestRequestLogsExport(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if err := LogRequest("/test", "GET", "127.0.0.1", 200, 12.5); err != nil {
		t.Fatalf("LogRequest: %v", err)
	}
	logs, err := GetAllRequestLogsForExport()
	if err != nil {
		t.Fatalf("GetAllRequestLogsForExport: %v", err)
	}
	found := false
	for _, row := range logs {
		if row["endpoint"] == "/test" && row["status_code"] == 200 {
			found = true
		}
	}
	if !found {
		t.Fatalf("request log missing from export: %v", logs)
	}
}

func TestAccountUsageUpsert(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	accID := "acc_upsert_" + t.Name()
	if err := RecordAccountUsage(accID, "cookies/a.json", "active", ""); err != nil {
		t.Fatalf("first RecordAccountUsage: %v", err)
	}
	if err := RecordAccountUsage(accID, "cookies/a.json", "active", "ultra"); err != nil {
		t.Fatalf("second RecordAccountUsage: %v", err)
	}
	accounts, err := GetAccounts()
	if err != nil {
		t.Fatalf("GetAccounts: %v", err)
	}
	found := false
	for _, acc := range accounts {
		if acc["account_id"] == accID {
			found = true
			if acc["tier"] != "ultra" {
				t.Errorf("tier = %v, want ultra", acc["tier"])
			}
			if acc["total_requests"].(int) < 2 {
				t.Errorf("total_requests = %v, want >= 2", acc["total_requests"])
			}
		}
	}
	if !found {
		t.Fatal("upserted account not found")
	}
}

func TestGetSystemStatsShape(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	stats, err := GetSystemStats()
	if err != nil {
		t.Fatalf("GetSystemStats: %v", err)
	}
	for _, key := range []string{
		"total_messages", "total_tokens", "tier_breakdown", "model_breakdown",
		"total_media", "total_requests", "cost_saved_usd", "database_file",
	} {
		if _, ok := stats[key]; !ok {
			t.Errorf("missing stats key %q", key)
		}
	}
	if !strings.HasSuffix(stats["database_file"].(string), ".db") {
		t.Errorf("unexpected database_file %v", stats["database_file"])
	}
}

func TestGetUSDToINRRate(t *testing.T) {
	t.Setenv("USD_TO_INR", "")
	if got := GetUSDToINRRate(); got != 95.89 {
		t.Errorf("default rate = %v, want 95.89", got)
	}
	t.Setenv("USD_TO_INR", "100.5")
	if got := GetUSDToINRRate(); got != 100.5 {
		t.Errorf("configured rate = %v, want 100.5", got)
	}
	t.Setenv("USD_TO_INR", "invalid")
	if got := GetUSDToINRRate(); got != 95.89 {
		t.Errorf("invalid rate fallback = %v, want 95.89", got)
	}
}

func TestDeleteConversationValidation(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if _, err := DeleteConversation("   "); err == nil {
		t.Fatal("empty conversation id should be rejected")
	}
}

func TestRetentionWorkerLifecycle(t *testing.T) {
	if _, err := InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	// Zero/negative retention must not start a worker.
	StartRetentionWorker(0)
	StartRetentionWorker(-1)

	StartRetentionWorker(1)
	StopRetentionWorker()
}
