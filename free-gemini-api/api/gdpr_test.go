package api

import (
	"encoding/json"
	"goapi/db"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestDeleteConversationDB(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	convID := "conv_gdpr_db_test"
	_ = db.LogMessageWithTier(convID, "user1", "user", "hello", "gemini-3.8-flash", 10, "free", "acc")
	_ = db.LogMessageWithTier(convID, "user1", "assistant", "world", "gemini-3.8-flash", 20, "free", "acc")
	_ = db.LogMediaGenerationForConversation(convID, "image", "a cat", "cat.png", "./output/cat.png", "http://localhost/output/cat.png", "16:9", "resp1")

	deleted, err := db.DeleteConversation(convID)
	if err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 deleted rows, got %d", deleted)
	}

	messages, err := db.GetRecentMessages(convID, 10)
	if err != nil {
		t.Fatalf("GetRecentMessages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected no messages after deletion, got %d", len(messages))
	}

	// Idempotent second deletion.
	deleted, err = db.DeleteConversation(convID)
	if err != nil {
		t.Fatalf("second DeleteConversation: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deleted rows on second call, got %d", deleted)
	}
}

func TestPurgeExpiredData(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	oldConv := "conv_purge_old"
	recentConv := "conv_purge_recent"
	_ = db.LogMessageWithTier(oldConv, "u", "user", "old", "m", 1, "free", "")
	_ = db.LogMessageWithTier(recentConv, "u", "user", "recent", "m", 1, "free", "")
	if _, err := db.DB.Exec(`UPDATE messages SET created_at = datetime('now', '-45 days') WHERE conversation_id = ?`, oldConv); err != nil {
		t.Fatalf("age message: %v", err)
	}

	deleted, err := db.PurgeExpiredData(30)
	if err != nil {
		t.Fatalf("PurgeExpiredData: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("expected at least one purged row, got %d", deleted)
	}

	oldMessages, _ := db.GetRecentMessages(oldConv, 10)
	if len(oldMessages) != 0 {
		t.Fatalf("old message should have been purged, got %d", len(oldMessages))
	}
	recentMessages, _ := db.GetRecentMessages(recentConv, 10)
	if len(recentMessages) != 1 {
		t.Fatalf("recent message should remain, got %d", len(recentMessages))
	}

	// Retention disabled must be a no-op.
	deleted, err = db.PurgeExpiredData(0)
	if err != nil || deleted != 0 {
		t.Fatalf("expected disabled retention no-op, got %d err %v", deleted, err)
	}
}

func TestRetentionDaysFromEnv(t *testing.T) {
	t.Setenv("RETENTION_DAYS", "")
	if got := db.RetentionDaysFromEnv(); got != 30 {
		t.Fatalf("expected default 30, got %d", got)
	}
	t.Setenv("RETENTION_DAYS", "7")
	if got := db.RetentionDaysFromEnv(); got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
	t.Setenv("RETENTION_DAYS", "0")
	if got := db.RetentionDaysFromEnv(); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
	t.Setenv("RETENTION_DAYS", "not-a-number")
	if got := db.RetentionDaysFromEnv(); got != 30 {
		t.Fatalf("expected fallback 30, got %d", got)
	}
}

func TestDeleteConversationRouteAuthAndIdempotence(t *testing.T) {
	if _, err := db.InitDB(); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	const apiToken = "gdpr-api-token"
	app := fiber.New()
	RegisterRoutesWithAuth(app, "gdpr-admin-token", apiToken)

	convID := "conv_gdpr_route"
	_ = db.LogMessageWithTier(convID, "u", "user", "hello", "m", 1, "free", "")

	// No token -> 401.
	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, "/history/"+convID, nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Valid token -> 200 and deletion.
	req := httptest.NewRequest(http.MethodDelete, "/history/"+convID, nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var payload map[string]any
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["status"] != "deleted" || payload["conversation_id"] != convID {
		t.Fatalf("unexpected payload: %v", payload)
	}
	if payload["messages_deleted"].(float64) < 1 {
		t.Fatalf("expected at least one deleted message, got %v", payload["messages_deleted"])
	}

	// Idempotent second call.
	req = httptest.NewRequest(http.MethodDelete, "/history/"+convID, nil)
	req.Header.Set("X-API-Token", apiToken)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected idempotent 200, got %d", resp.StatusCode)
	}

	// Invalid id -> 400.
	req = httptest.NewRequest(http.MethodDelete, "/history/bad!id", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("invalid id request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid id, got %d", resp.StatusCode)
	}
}
