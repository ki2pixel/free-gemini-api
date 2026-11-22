package api

import (
	"goapi/db"
	"goapi/gemini"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// conversationIDRe is the accepted shape for a conversation identifier on the
// GDPR deletion endpoint.
var conversationIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// RegisterRoutes keeps the historical signature for callers that rely on the
// environment being the source of truth for tokens.
func RegisterRoutes(app *fiber.App) {
	RegisterRoutesWithAuth(app, os.Getenv("ADMIN_TOKEN"), os.Getenv("API_TOKEN"))
}

// safeOutputPath confines a caller-supplied name to ./output. It rejects
// absolute paths, parent traversal, Windows separators and symlinks that escape
// the output directory.
func safeOutputPath(name string) (string, bool) {
	lowerName := strings.ToLower(name)
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") ||
		strings.Contains(lowerName, "%2f") || strings.Contains(lowerName, "%5c") || strings.Contains(lowerName, "%00") {
		return "", false
	}

	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || !filepath.IsLocal(clean) {
		return "", false
	}

	root, err := filepath.Abs("./output")
	if err != nil {
		return "", false
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	// Resolve symlinks only when the target exists. A missing file is handled
	// by the handler as a 404, which avoids leaking whether a path exists.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		full = resolved
	}

	return full, true
}

// RegisterRoutesWithAuth registers all HTTP API routes and applies the admin
// token gate to state-changing administrative endpoints.
func RegisterRoutesWithAuth(app *fiber.App, adminToken, apiToken string) {
	adminAuth := AdminAuthMiddleware(adminToken, apiToken)

	// CLI Help & Developer Manual
	app.Get("/", HandleHelp)
	app.Get("/help", HandleHelp)

	// Health check endpoint
	app.Get("/health", func(c fiber.Ctx) error {
		cookieHealth := gemini.InspectAccountCookieHealth()
		return c.JSON(fiber.Map{
			"status":                   "online",
			"engine":                   "arranger + free-gemini-api (pure go)",
			"cookie_health":            cookieHealth,
			"active_extension_workers": gemini.GetActiveWorkerCount(),
			"cookie_accounts_in_pool":  gemini.GetActiveAccountCount(),
		})
	})

	// Prometheus metrics. PUBLIC_METRICS=true only relaxes authentication for
	// loopback scrapers; remote callers still need API_TOKEN/ADMIN_TOKEN.
	app.Get("/metrics", MetricsAuthMiddleware(apiToken, adminToken), HandleMetrics)

	// Live Worker Pool Metrics & Multi-Account concurrency stats
	app.Get("/v1/workers", func(c fiber.Ctx) error {
		return c.JSON(GetWorkerPool().GetStats())
	})
	app.Get("/api/pool", func(c fiber.Ctx) error {
		return c.JSON(GetWorkerPool().GetStats())
	})

	// Direct HTTP Cookie Sync endpoint (dual resilience fallback for Chrome Extension)
	app.Post("/api/sync-cookies", adminAuth, func(c fiber.Ctx) error {
		type SyncPayload struct {
			Cookies []gemini.CookieObject `json:"cookies"`
			// ProfileID is the farm profile this jar belongs to. chrome-manage
			// sends it; binding only `cookies` dropped it on the floor, which
			// made ProcessAndSaveCookies fall back to naming the file from the
			// cookie VALUE. Because __Secure-1PSID rotates, that minted a NEW
			// account_<hash>.json on nearly every refresh instead of updating
			// account_profile_<N>.json - so the pool grew a duplicate worker for
			// the same Google account each time (measured 2026-09-27: 54 -> 55
			// after a single refresh).
			//
			// Nil for the Chrome extension, which has no profile id and must
			// keep the old hash naming.
			ProfileID *int `json:"profile_id"`
		}
		var payload SyncPayload
		if err := c.Bind().JSON(&payload); err != nil || len(payload.Cookies) == 0 {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid cookies payload"})
		}

		filePath, accID, err := gemini.ProcessAndSaveCookies(payload.Cookies, payload.ProfileID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}

		resp := fiber.Map{
			"status":     "success",
			"account_id": accID,
			"file_path":  filePath,
			"count":      len(payload.Cookies),
		}
		if payload.ProfileID != nil {
			resp["profile_id"] = *payload.ProfileID
		}
		return c.JSON(resp)
	})

	// OpenAI-compatible models list (dynamically loaded from Model Registry)
	app.Get("/v1/models", func(c fiber.Ctx) error {
		reg := gemini.GetModelRegistry()
		models := reg.GetAll()
		var list []fiber.Map
		for _, m := range models {
			list = append(list, fiber.Map{
				"id":           m.ID,
				"object":       "model",
				"created":      1710000000,
				"owned_by":     "google",
				"display_name": m.DisplayName,
				"description":  m.Description,
				"min_tier":     m.MinTier,
				"mode_id":      m.ModeID,
				"is_default":   m.IsDefault,
				"capabilities": m.Capabilities,
				"aliases":      m.Aliases,
				"source":       m.Source,
			})
		}
		return c.JSON(fiber.Map{
			"object": "list",
			"data":   list,
		})
	})

	// Dynamic registration/addition of custom models and mode hashes at runtime
	app.Post("/v1/models", adminAuth, func(c fiber.Ctx) error {
		var cfg gemini.ModelConfig
		if err := c.Bind().JSON(&cfg); err != nil || cfg.ID == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid model config payload. 'id' is required."})
		}
		if cfg.DisplayName == "" {
			cfg.DisplayName = cfg.ID
		}
		if cfg.MinTier == "" {
			cfg.MinTier = "free"
		}
		if cfg.Source == "" {
			cfg.Source = "custom"
		}
		if err := gemini.ValidateModelConfig(&cfg); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Invalid model config: " + err.Error()})
		}
		if err := gemini.GetModelRegistry().Register(cfg); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": "Failed to register model: " + err.Error()})
		}
		return c.JSON(fiber.Map{
			"status":  "success",
			"message": "Model registered successfully",
			"model":   cfg,
		})
	})

	// Autonomous Model Discovery across all connected accounts
	app.Post("/v1/models/discover", adminAuth, func(c fiber.Ctx) error {
		res, err := GetWorkerPool().DiscoverModelsFromWorkers()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(res)
	})

	// OpenAI-compatible chat completions (with Native Tool Calling & Output Arranger)
	app.Post("/v1/chat/completions", HandleOpenAIChatCompletions)

	// Unified chat endpoint (text, images, screen recordings, ref videos)
	app.Post("/chat", HandleUnifiedChat)

	// Gemini Music generation
	app.Post("/music", HandleMusic)

	// Reset active session
	app.Post("/reset", func(c fiber.Ctx) error {
		type ResetRequest struct {
			UserID string `json:"user_id"`
		}
		var req ResetRequest
		c.Bind().JSON(&req)

		sessionID := sanitizeSessionID(req.UserID, c.IP())

		if _, ok := UserSessions.Get(sessionID); ok {
			UserSessions.Delete(sessionID)
			return c.JSON(fiber.Map{"status": "success", "message": "Session reset"})
		}

		return c.JSON(fiber.Map{"status": "error", "message": "No active session"})
	})

	// Session status & health info
	app.Get("/status", func(c fiber.Ctx) error {
		sessionID := sanitizeSessionID(c.Query("user_id"), c.IP())

		if client, ok := UserSessions.Get(sessionID); ok {
			return c.JSON(fiber.Map{
				"session_id":      sessionID,
				"initialized":     client.IsInitialized(),
				"conversation_id": client.CurrentConversationIDFor("", ""),
				"expired":         false,
			})
		}
		return c.JSON(fiber.Map{"session_id": sessionID, "active": false})
	})

	// SQLite History & Database endpoints
	app.Get("/history", func(c fiber.Ctx) error {
		convID := c.Query("conversation_id")
		limitStr := c.Query("limit")
		limit := 50
		if limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}
		history, err := db.GetRecentMessages(convID, limit)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"conversation_id": convID, "messages": history})
	})

	// GDPR article 17: authenticated conversation deletion. The global auth
	// middleware also protects this route, but applying the token check here
	// keeps the handler safe when routes are registered directly in tests.
	app.Delete("/history/:conversation_id", AuthMiddleware(apiToken), func(c fiber.Ctx) error {
		convID := strings.TrimSpace(c.Params("conversation_id"))
		if !conversationIDRe.MatchString(convID) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid conversation_id"})
		}

		deleted, err := db.DeleteConversation(convID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		log.Printf("audit action=gdpr_delete conversation_id=%q rows_deleted=%d ip=%s", convID, deleted, c.IP())
		return c.JSON(fiber.Map{
			"status":           "deleted",
			"conversation_id":  convID,
			"messages_deleted": deleted,
		})
	})

	app.Get("/media", func(c fiber.Ctx) error {
		if db.DB == nil {
			return c.JSON(fiber.Map{"media": []any{}})
		}
		rows, err := db.DB.Query(`SELECT id, type, prompt, file_name, file_path, url, created_at FROM media_generations ORDER BY id DESC LIMIT 50`)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer rows.Close()

		var mediaList []fiber.Map
		for rows.Next() {
			var id int
			var mType, prompt, fileName, filePath, urlStr, createdAt string
			if err := rows.Scan(&id, &mType, &prompt, &fileName, &filePath, &urlStr, &createdAt); err == nil {
				mediaList = append(mediaList, fiber.Map{
					"id":         id,
					"type":       mType,
					"prompt":     prompt,
					"file_name":  fileName,
					"file_path":  filePath,
					"url":        urlStr,
					"created_at": createdAt,
				})
			}
		}
		return c.JSON(fiber.Map{"media": mediaList})
	})

	// Search chat history endpoint
	app.Get("/history/search", func(c fiber.Ctx) error {
		q := c.Query("q")
		limitStr := c.Query("limit")
		limit := 10
		if limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}
		res, err := db.SearchMessages(q, limit)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"query": q, "results": res})
	})

	// Live SQLite stats endpoint
	app.Get("/stats", func(c fiber.Ctx) error {
		stats, err := db.GetSystemStats()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		stats["active_accounts"] = gemini.GetActiveAccountCount()
		stats["active_workers"] = gemini.GetActiveWorkerCount()
		stats["cookie_health"] = gemini.InspectAccountCookieHealth()
		return c.JSON(stats)
	})

	// Machine-readable JSON Analytics endpoint (Direct REST API)
	exportLimiter := ExportRateLimitMiddleware()
	app.Get("/v1/analytics", exportLimiter, HandleJSONExport)
	app.Get("/export/json", exportLimiter, HandleJSONExport)

	// Excel Analytics & Intelligence Exporter
	app.Get("/export/excel", exportLimiter, HandleExcelExport)

	// Static generated output files (images, videos, music)
	app.Get("/output/*", func(c fiber.Ctx) error {
		fullPath, ok := safeOutputPath(c.Params("*"))
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid path"})
		}
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		}
		return c.SendFile(fullPath)
	})
}
