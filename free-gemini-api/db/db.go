package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	DB   *sql.DB
	once sync.Once
	dbMu sync.Mutex
)

const DBFileName = "data/gemini.db"

// dbFilePath returns the active database path. GEMINI_DB_PATH exists so tests
// and container deployments can place the database outside the package
// directory without changing the compiled default.
func dbFilePath() string {
	if path := strings.TrimSpace(os.Getenv("GEMINI_DB_PATH")); path != "" {
		return path
	}
	return DBFileName
}

// InitDB initializes SQLite database connection and auto-migrates all tables
func InitDB() (*sql.DB, error) {
	var initErr error
	once.Do(func() {
		dbPath := dbFilePath()
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0700); err != nil {
			initErr = err
			return
		}
		_ = os.Chmod(dir, 0700)

		database, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)&_pragma=temp_store(memory)&_pragma=foreign_keys(1)")
		if err != nil {
			initErr = err
			return
		}

		// Set connection pool
		database.SetMaxOpenConns(25)
		database.SetMaxIdleConns(10)
		database.SetConnMaxLifetime(time.Hour)

		// Create tables
		schema := `
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT,
			user_id TEXT,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			model TEXT,
			tokens INTEGER DEFAULT 0,
			account_tier TEXT DEFAULT 'free',
			account_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
		CREATE INDEX IF NOT EXISTS idx_messages_user ON messages(user_id);
		CREATE INDEX IF NOT EXISTS idx_messages_tier ON messages(account_tier);
		CREATE INDEX IF NOT EXISTS idx_messages_model ON messages(model);

		CREATE TABLE IF NOT EXISTS media_generations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT DEFAULT '',
			type TEXT NOT NULL, -- 'image', 'video', 'music'
			prompt TEXT NOT NULL,
			file_name TEXT NOT NULL,
			file_path TEXT NOT NULL,
			url TEXT,
			aspect_ratio TEXT,
			response_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_media_type ON media_generations(type);

		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			endpoint TEXT NOT NULL,
			method TEXT NOT NULL,
			user_ip TEXT,
			status_code INTEGER,
			elapsed_ms REAL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT UNIQUE NOT NULL,
			cookie_file TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			tier TEXT DEFAULT 'free',
			total_requests INTEGER DEFAULT 0,
			last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		-- Agent telemetry. Content-free by construction: session keys are
		-- hashed, reasoning is reduced to a digest, and no prompt, reasoning
		-- body or tool argument is ever stored. FOREIGN KEY enforcement is on
		-- every pooled connection through the DSN pragma above.
		CREATE TABLE IF NOT EXISTS agent_sessions (
			session_id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			system_scope_hash TEXT NOT NULL DEFAULT '',
			requested_model TEXT NOT NULL,
			effective_model TEXT NOT NULL,
			product_mode INTEGER NOT NULL CHECK (product_mode IN (0, 40)),
			routed_task_to_chat INTEGER NOT NULL DEFAULT 0 CHECK (routed_task_to_chat IN (0, 1)),
			thinking_tier TEXT NOT NULL DEFAULT 'none' CHECK (thinking_tier IN ('none', 'low', 'medium', 'high')),
			worker_id TEXT NOT NULL DEFAULT '',
			account_tier TEXT NOT NULL DEFAULT 'free' CHECK (account_tier IN ('free', 'subscription', 'ultra')),
			tools_declared_count INTEGER NOT NULL DEFAULT 0 CHECK (tools_declared_count >= 0),
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_sessions_conv ON agent_sessions(conversation_id);
		CREATE INDEX IF NOT EXISTS idx_sessions_paradigm ON agent_sessions(product_mode, routed_task_to_chat);
		CREATE INDEX IF NOT EXISTS idx_sessions_updated ON agent_sessions(updated_at);

		CREATE TABLE IF NOT EXISTS agent_turns (
			turn_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES agent_sessions(session_id) ON DELETE CASCADE,
			turn_index INTEGER NOT NULL CHECK (turn_index >= 0),
			upstream_conv_id TEXT NOT NULL DEFAULT '',
			send_model TEXT NOT NULL DEFAULT '',
			is_delta INTEGER NOT NULL DEFAULT 0 CHECK (is_delta IN (0, 1)),
			replay_reason TEXT NOT NULL DEFAULT 'none' CHECK (replay_reason IN ('none', 'delta', 'new', 'forced', 'worker_mismatch', 'conv_mismatch', 'identical', 'prefix_mismatch')),
			flat_prompt_bytes INTEGER NOT NULL DEFAULT 0 CHECK (flat_prompt_bytes >= 0),
			sent_prompt_bytes INTEGER NOT NULL DEFAULT 0 CHECK (sent_prompt_bytes >= 0),
			delta_bytes INTEGER NOT NULL DEFAULT 0 CHECK (delta_bytes >= 0),
			masked_obs_bytes_saved INTEGER NOT NULL DEFAULT 0 CHECK (masked_obs_bytes_saved >= 0),
			request_started_ms INTEGER NOT NULL CHECK (request_started_ms >= 1),
			attempt_started_ms INTEGER,
			first_thought_ms INTEGER,
			thought_ended_ms INTEGER,
			first_answer_ms INTEGER,
			client_first_thought_ms INTEGER,
			completed_ms INTEGER NOT NULL CHECK (completed_ms >= request_started_ms),
			input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
			thought_tokens INTEGER NOT NULL DEFAULT 0 CHECK (thought_tokens >= 0),
			answer_tokens INTEGER NOT NULL DEFAULT 0 CHECK (answer_tokens >= 0),
			token_estimator TEXT NOT NULL DEFAULT 'heuristic' CHECK (token_estimator IN ('heuristic', 'bpe')),
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			empty_gen_retries INTEGER NOT NULL DEFAULT 0 CHECK (empty_gen_retries >= 0),
			refusal_retries INTEGER NOT NULL DEFAULT 0 CHECK (refusal_retries >= 0),
			transport_retries INTEGER NOT NULL DEFAULT 0 CHECK (transport_retries >= 0),
			stream_stalled INTEGER NOT NULL DEFAULT 0 CHECK (stream_stalled IN (0, 1)),
			upstream_suspected_stop INTEGER NOT NULL DEFAULT 0 CHECK (upstream_suspected_stop IN (0, 1)),
			ground_truth_injected INTEGER NOT NULL DEFAULT 0 CHECK (ground_truth_injected IN (0, 1)),
			turn_class TEXT NOT NULL DEFAULT 'none' CHECK (turn_class IN ('none', 'tool_call', 'ok', 'greeting', 'incomplete', 'refusal', 'pseudo_report', 'unbacked_completion', 'untagged_mutation', 'schema_invalid')),
			finish_reason TEXT NOT NULL DEFAULT 'error' CHECK (finish_reason IN ('stop', 'tool_calls', 'error')),
			doom_loop_action TEXT NOT NULL DEFAULT 'none' CHECK (doom_loop_action IN ('none', 'warn_repeat', 'warn_passive_read', 'halt')),
			doom_loop_signal TEXT NOT NULL DEFAULT '' CHECK (doom_loop_signal IN ('', 'repeat', 'cycle', 'passive_read')),
			thought_unclosed INTEGER NOT NULL DEFAULT 0 CHECK (thought_unclosed IN (0, 1)),
			thought_sha256 TEXT NOT NULL DEFAULT '',
			visible_len INTEGER NOT NULL DEFAULT 0 CHECK (visible_len >= 0),
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			ttft_thought_ms INTEGER GENERATED ALWAYS AS (first_thought_ms - request_started_ms) VIRTUAL,
			ttft_answer_ms INTEGER GENERATED ALWAYS AS (first_answer_ms - request_started_ms) VIRTUAL,
			thought_duration_ms INTEGER GENERATED ALWAYS AS (thought_ended_ms - first_thought_ms) VIRTUAL,
			answer_duration_ms INTEGER GENERATED ALWAYS AS (completed_ms - first_answer_ms) VIRTUAL,
			total_duration_ms INTEGER GENERATED ALWAYS AS (completed_ms - request_started_ms) VIRTUAL,
			total_output_tokens INTEGER GENERATED ALWAYS AS (thought_tokens + answer_tokens) STORED,
			thinking_share REAL GENERATED ALWAYS AS (
				CASE WHEN thought_tokens + answer_tokens = 0 THEN 0.0
					ELSE CAST(thought_tokens AS REAL) / (thought_tokens + answer_tokens) END) VIRTUAL,
			thought_to_answer_ratio REAL GENERATED ALWAYS AS (
				CASE WHEN answer_tokens = 0 THEN 0.0
					ELSE CAST(thought_tokens AS REAL) / answer_tokens END) VIRTUAL,
			CHECK (first_thought_ms IS NULL OR first_thought_ms >= request_started_ms),
			CHECK (thought_ended_ms IS NULL OR thought_ended_ms >= request_started_ms),
			CHECK (first_answer_ms IS NULL OR first_answer_ms >= request_started_ms),
			CHECK (client_first_thought_ms IS NULL OR client_first_thought_ms >= request_started_ms),
			CHECK (thought_ended_ms IS NULL OR first_thought_ms IS NULL OR thought_ended_ms >= first_thought_ms),
			CHECK (first_answer_ms IS NULL OR completed_ms >= first_answer_ms),
			UNIQUE (session_id, turn_index)
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_turns_session_idx ON agent_turns(session_id, turn_index);
		CREATE INDEX IF NOT EXISTS idx_turns_class_finish ON agent_turns(turn_class, finish_reason);
		CREATE INDEX IF NOT EXISTS idx_turns_replay ON agent_turns(replay_reason) WHERE is_delta = 0;
		CREATE INDEX IF NOT EXISTS idx_turns_doom ON agent_turns(doom_loop_action) WHERE doom_loop_action != 'none';
		CREATE INDEX IF NOT EXISTS idx_turns_conv ON agent_turns(upstream_conv_id);
		CREATE INDEX IF NOT EXISTS idx_turns_created ON agent_turns(created_at);

		-- One row per session with at least one turn. AVG ignores the NULL
		-- milestones of non-streaming turns, so a mixed session still reports
		-- the mean of the turns that actually observed each phase.
		CREATE VIEW IF NOT EXISTS v_session_telemetry_summary AS
		SELECT
			s.session_id,
			s.user_id,
			s.requested_model,
			s.effective_model,
			s.product_mode,
			s.routed_task_to_chat,
			s.thinking_tier,
			s.account_tier,
			s.worker_id,
			COUNT(t.turn_id) AS total_turns,
			SUM(CASE WHEN t.is_delta = 0 THEN 1 ELSE 0 END) AS full_replays,
			COALESCE(SUM(t.thought_tokens), 0) AS sum_thought_tokens,
			COALESCE(SUM(t.answer_tokens), 0) AS sum_answer_tokens,
			COALESCE(SUM(t.total_output_tokens), 0) AS sum_output_tokens,
			CASE WHEN COALESCE(SUM(t.total_output_tokens), 0) = 0 THEN 0.0
				ELSE CAST(COALESCE(SUM(t.thought_tokens), 0) AS REAL) / SUM(t.total_output_tokens) END AS session_thinking_share,
			AVG(t.ttft_thought_ms) AS avg_ttft_thought_ms,
			AVG(t.ttft_answer_ms) AS avg_ttft_answer_ms,
			COALESCE(SUM(t.total_duration_ms), 0) AS sum_total_duration_ms,
			SUM(CASE WHEN t.doom_loop_action != 'none' THEN 1 ELSE 0 END) AS doom_loop_events,
			SUM(CASE WHEN t.stream_stalled = 1 THEN 1 ELSE 0 END) AS stream_stalls,
			COALESCE(SUM(t.refusal_retries), 0) AS refusal_retries,
			MAX(t.created_at) AS last_turn_at
		FROM agent_sessions s
		JOIN agent_turns t ON t.session_id = s.session_id
		GROUP BY s.session_id;

		`

		if _, err := database.Exec(schema); err != nil {
			initErr = err
			return
		}

		// Auto-migrate existing database tables
		_, _ = database.Exec(`ALTER TABLE messages ADD COLUMN account_tier TEXT DEFAULT 'free'`)
		_, _ = database.Exec(`ALTER TABLE messages ADD COLUMN account_id TEXT DEFAULT ''`)
		_, _ = database.Exec(`ALTER TABLE accounts ADD COLUMN tier TEXT DEFAULT 'free'`)
		_, _ = database.Exec(`ALTER TABLE media_generations ADD COLUMN conversation_id TEXT DEFAULT ''`)
		_, _ = database.Exec(`CREATE INDEX IF NOT EXISTS idx_media_conv ON media_generations(conversation_id)`)

		// SQLite creates the main database and its WAL/SHM companions with the
		// process umask. Tighten the main file explicitly; the WAL/SHM files are
		// short-lived and are chmod'ed once they exist below.
		_ = os.Chmod(dbPath, 0600)
		_ = os.Chmod(dbPath+"-wal", 0600)
		_ = os.Chmod(dbPath+"-shm", 0600)

		// Publish the handle under the writer mutex. An asynchronous history or
		// telemetry worker may already be running (tests start the chat service
		// before the first InitDB call), and an unsynchronised pointer swap is a
		// data race.
		dbMu.Lock()
		DB = database
		dbMu.Unlock()
		log.Printf("🗄️  SQLite Database initialized successfully at %s (WAL Mode)", dbPath)
	})

	return DB, initErr
}

// LogMessage saves a chat turn into SQLite (legacy fallback)
func LogMessage(conversationID, userID, role, content, model string, tokens int) error {
	return LogMessageWithTier(conversationID, userID, role, content, model, tokens, "free", "")
}

// LogMessageWithTier saves a chat turn into SQLite with account tier and worker ID
func LogMessageWithTier(conversationID, userID, role, content, model string, tokens int, accountTier, accountID string) error {
	if DB == nil {
		return nil
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if accountTier == "" {
		accountTier = "free"
	}
	query := `INSERT INTO messages (conversation_id, user_id, role, content, model, tokens, account_tier, account_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, conversationID, userID, role, content, model, tokens, accountTier, accountID)
	return err
}

// LogMediaGeneration records a generated image, video or music track.
func LogMediaGeneration(mediaType, prompt, fileName, filePath, url, aspectRatio, responseID string) error {
	return LogMediaGenerationForConversation("", mediaType, prompt, fileName, filePath, url, aspectRatio, responseID)
}

// LogMediaGenerationForConversation records a generated media row and ties it
// to the originating conversation so GDPR deletion can remove it.
func LogMediaGenerationForConversation(conversationID, mediaType, prompt, fileName, filePath, url, aspectRatio, responseID string) error {
	if DB == nil {
		return nil
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	query := `INSERT INTO media_generations (conversation_id, type, prompt, file_name, file_path, url, aspect_ratio, response_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, conversationID, mediaType, prompt, fileName, filePath, url, aspectRatio, responseID)
	return err
}

// LogRequest records an API request log
func LogRequest(endpoint, method, userIP string, statusCode int, elapsedMs float64) error {
	if DB == nil {
		return nil
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	query := `INSERT INTO request_logs (endpoint, method, user_ip, status_code, elapsed_ms) VALUES (?, ?, ?, ?, ?)`
	_, err := DB.Exec(query, endpoint, method, userIP, statusCode, elapsedMs)
	return err
}

// GetRecentMessages retrieves conversation history
func GetRecentMessages(conversationID string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	rows, err := DB.Query(`SELECT role, content, model, created_at FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var role, content, model, createdAt string
		if err := rows.Scan(&role, &content, &model, &createdAt); err == nil {
			result = append(result, map[string]any{
				"role":       role,
				"content":    content,
				"model":      model,
				"created_at": createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// SearchMessages searches past chat history across all messages
func SearchMessages(query string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	searchPattern := "%" + query + "%"
	rows, err := DB.Query(`SELECT id, COALESCE(role, ''), COALESCE(content, ''), COALESCE(model, ''), COALESCE(created_at, '') FROM messages WHERE content LIKE ? ORDER BY id DESC LIMIT ?`, searchPattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var id int
		var role, content, model, createdAt string
		if err := rows.Scan(&id, &role, &content, &model, &createdAt); err == nil {
			result = append(result, map[string]any{
				"id":         id,
				"role":       role,
				"content":    content,
				"model":      model,
				"created_at": createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetMediaByPrompt retrieves generated media items matching a filter/query
func GetMediaByPrompt(mediaType, query string, limit int) ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	sqlQuery := `SELECT id, type, prompt, file_name, url, created_at FROM media_generations WHERE 1=1`
	var args []any
	if mediaType != "" && mediaType != "all" {
		sqlQuery += ` AND type = ?`
		args = append(args, mediaType)
	}
	if query != "" {
		sqlQuery += ` AND prompt LIKE ?`
		args = append(args, "%"+query+"%")
	}
	sqlQuery += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := DB.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var id int
		var mType, prompt, fileName, urlStr, createdAt string
		if err := rows.Scan(&id, &mType, &prompt, &fileName, &urlStr, &createdAt); err == nil {
			result = append(result, map[string]any{
				"id":         id,
				"type":       mType,
				"prompt":     prompt,
				"file_name":  fileName,
				"url":        urlStr,
				"created_at": createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetSystemStats returns aggregate statistics, official API cost savings, and separate Free vs Subscription metrics
func GetSystemStats() (map[string]any, error) {
	if DB == nil {
		return map[string]any{"status": "database_not_connected"}, nil
	}
	var totalMessages, totalMedia, totalRequests, totalTokens int
	var totalImages, totalVideos, totalMusic int

	_ = DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&totalMessages)
	_ = DB.QueryRow(`SELECT COALESCE(SUM(tokens), 0) FROM messages`).Scan(&totalTokens)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM media_generations`).Scan(&totalMedia)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM media_generations WHERE type='image'`).Scan(&totalImages)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM media_generations WHERE type='video'`).Scan(&totalVideos)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM media_generations WHERE type='music'`).Scan(&totalMusic)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&totalRequests)

	// Free Tier vs Subscription Tier Breakdown
	var freeMessages, subMessages int
	var freeTokens, subTokens int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE account_tier = 'free' OR account_tier IS NULL OR account_tier = ''`).Scan(&freeMessages)
	_ = DB.QueryRow(`SELECT COALESCE(SUM(tokens), 0) FROM messages WHERE account_tier = 'free' OR account_tier IS NULL OR account_tier = ''`).Scan(&freeTokens)
	_ = DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE account_tier = 'subscription'`).Scan(&subMessages)
	_ = DB.QueryRow(`SELECT COALESCE(SUM(tokens), 0) FROM messages WHERE account_tier = 'subscription'`).Scan(&subTokens)

	// Models breakdown by model and tier
	modelStats := make(map[string]map[string]int)
	mRows, err := DB.Query(`SELECT COALESCE(model, 'unknown'), COALESCE(account_tier, 'free'), COUNT(*), COALESCE(SUM(tokens), 0) FROM messages GROUP BY model, account_tier`)
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var mName, mTier string
			var mCount, mTok int
			if err := mRows.Scan(&mName, &mTier, &mCount, &mTok); err == nil {
				key := fmt.Sprintf("%s (%s)", mName, mTier)
				modelStats[key] = map[string]int{
					"messages": mCount,
					"tokens":   mTok,
				}
			}
		}
		if err := mRows.Err(); err != nil {
			return nil, err
		}
	}

	// Cost savings:
	// Free Tier Flash tokens: $0.50 / 1M tokens ($0.0000005/token)
	// Subscription Tier Pro tokens: $2.50 / 1M tokens ($0.0000025/token)
	freeCostTokensUSD := float64(freeTokens) * 0.0000005
	subCostTokensUSD := float64(subTokens) * 0.0000025
	mediaCostUSD := (float64(totalImages) * 0.030) + (float64(totalVideos) * 1.200) + (float64(totalMusic) * 0.080)

	costSavedUSD := freeCostTokensUSD + subCostTokensUSD + mediaCostUSD
	usdToINR := GetUSDToINRRate()
	costSavedINR := costSavedUSD * usdToINR

	return map[string]any{
		"total_messages": totalMessages,
		"total_tokens":   totalTokens,
		"tier_breakdown": map[string]any{
			"free": map[string]any{
				"messages":       freeMessages,
				"tokens":         freeTokens,
				"cost_saved_usd": fmt.Sprintf("$%.4f", freeCostTokensUSD),
			},
			"subscription": map[string]any{
				"messages":       subMessages,
				"tokens":         subTokens,
				"cost_saved_usd": fmt.Sprintf("$%.4f", subCostTokensUSD),
			},
		},
		"model_breakdown":          modelStats,
		"total_media":              totalMedia,
		"total_images":             totalImages,
		"total_videos":             totalVideos,
		"total_music":              totalMusic,
		"total_requests":           totalRequests,
		"cost_saved_usd":           costSavedUSD,
		"cost_saved_inr":           costSavedINR,
		"cost_saved_usd_formatted": fmt.Sprintf("$%.4f", costSavedUSD),
		"cost_saved_inr_formatted": fmt.Sprintf("₹%.2f", costSavedINR),
		"engine":                   "SQLite WAL Mode",
		"database_file":            dbFilePath(),
	}, nil
}

// RecordAccountUsage inserts or updates account status, tier and usage metrics in SQLite
func RecordAccountUsage(accountID, cookieFile, status, tier string) error {
	if DB == nil {
		return nil
	}
	dbMu.Lock()
	defer dbMu.Unlock()

	if tier == "" {
		tier = "free"
	}

	query := `
	INSERT INTO accounts (account_id, cookie_file, status, tier, total_requests, last_used_at)
	VALUES (?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
	ON CONFLICT(account_id) DO UPDATE SET
		cookie_file = excluded.cookie_file,
		status = excluded.status,
		tier = excluded.tier,
		total_requests = accounts.total_requests + 1,
		last_used_at = CURRENT_TIMESTAMP;
	`
	_, err := DB.Exec(query, accountID, cookieFile, status, tier)
	return err
}

// GetAccounts retrieves all tracked accounts, tiers, and their usage statistics
func GetAccounts() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	rows, err := DB.Query(`SELECT account_id, cookie_file, status, COALESCE(tier, 'free'), total_requests, last_used_at FROM accounts ORDER BY last_used_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []map[string]any
	for rows.Next() {
		var accountID, cookieFile, status, tier, lastUsed string
		var totalRequests int
		if err := rows.Scan(&accountID, &cookieFile, &status, &tier, &totalRequests, &lastUsed); err == nil {
			accounts = append(accounts, map[string]any{
				"account_id":     accountID,
				"cookie_file":    cookieFile,
				"status":         status,
				"tier":           tier,
				"total_requests": totalRequests,
				"last_used_at":   lastUsed,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

// GetAllMessagesForExport fetches messages for Excel and JSON export in chronological order (User first, Assistant next)
func GetAllMessagesForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, conversation_id, user_id, role, content, model, tokens, COALESCE(account_tier, 'free'), COALESCE(account_id, ''), created_at FROM messages ORDER BY id ASC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id, tokens int
		var convID, userID, role, content, model, tier, accID, createdAt string
		if err := rows.Scan(&id, &convID, &userID, &role, &content, &model, &tokens, &tier, &accID, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id":              id,
				"conversation_id": convID,
				"user_id":         userID,
				"role":            role,
				"content":         content,
				"model":           model,
				"tokens":          tokens,
				"account_tier":    tier,
				"account_id":      accID,
				"created_at":      createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// GetAllMediaForExport fetches media generations for Excel report generation
func GetAllMediaForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, conversation_id, type, prompt, file_name, file_path, url, aspect_ratio, response_id, created_at FROM media_generations ORDER BY id DESC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id int
		var convID, mType, prompt, fileName, filePath, urlStr, aspect, respID, createdAt string
		if err := rows.Scan(&id, &convID, &mType, &prompt, &fileName, &filePath, &urlStr, &aspect, &respID, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id":              id,
				"conversation_id": convID,
				"type":            mType,
				"prompt":          prompt,
				"file_name":       fileName,
				"file_path":       filePath,
				"url":             urlStr,
				"aspect_ratio":    aspect,
				"response_id":     respID,
				"created_at":      createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// GetAllRequestLogsForExport fetches request logs for Excel report generation
func GetAllRequestLogsForExport() ([]map[string]any, error) {
	if DB == nil {
		return nil, nil
	}
	query := `SELECT id, endpoint, method, user_ip, status_code, elapsed_ms, created_at FROM request_logs ORDER BY id DESC LIMIT 5000`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []map[string]any
	for rows.Next() {
		var id, status int
		var endpoint, method, userIP, createdAt string
		var elapsed float64
		if err := rows.Scan(&id, &endpoint, &method, &userIP, &status, &elapsed, &createdAt); err == nil {
			list = append(list, map[string]any{
				"id":          id,
				"endpoint":    endpoint,
				"method":      method,
				"user_ip":     userIP,
				"status_code": status,
				"elapsed_ms":  elapsed,
				"created_at":  createdAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// PurgeExpiredData removes messages, media rows and request logs older than
// retentionDays. A non-positive retention disables the purge and returns 0.
// The return value is the total number of deleted rows.
func PurgeExpiredData(retentionDays int) (int64, error) {
	if DB == nil || retentionDays <= 0 {
		return 0, nil
	}

	cutoff := fmt.Sprintf("-%d days", retentionDays)
	tables := []string{"messages", "media_generations", "request_logs"}

	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := DB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	for _, table := range tables {
		res, err := tx.Exec("DELETE FROM "+table+" WHERE created_at < datetime('now', ?)", cutoff)
		if err != nil {
			return total, fmt.Errorf("purge %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}

	// Agent sessions are aged on their last activity, not their creation: a
	// long-running session must stay while it is still used. Deleting the
	// session cascades to its turns, which RowsAffected does not count.
	res, err := tx.Exec(`DELETE FROM agent_sessions WHERE updated_at < datetime('now', ?)`, cutoff)
	if err != nil {
		return total, fmt.Errorf("purge agent_sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	// Turns of a session that is still active are aged individually, so a
	// months-old session cannot retain its oldest rows indefinitely.
	res, err = tx.Exec(`DELETE FROM agent_turns WHERE created_at < datetime('now', ?)`, cutoff)
	if err != nil {
		return total, fmt.Errorf("purge agent_turns: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n

	if err := tx.Commit(); err != nil {
		return total, err
	}
	return total, nil
}

// DeleteConversation removes every message, media row and agent telemetry row
// attached to a conversation. It is idempotent: deleting an unknown id returns
// (0, nil).
func DeleteConversation(conversationID string) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, fmt.Errorf("conversation id is required")
	}
	if DB == nil {
		return 0, nil
	}

	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := DB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	for _, query := range []string{
		`DELETE FROM messages WHERE conversation_id = ?`,
		`DELETE FROM media_generations WHERE conversation_id = ?`,
	} {
		res, err := tx.Exec(query, conversationID)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	// A session may span several upstream resets (c_1 then c_2), so turns are
	// matched on the conversation they belong to while the session row is
	// matched on the last upstream id it recorded. Deleting the session
	// cascades to any remaining turns.
	res, err := tx.Exec(`DELETE FROM agent_turns WHERE upstream_conv_id = ?`, conversationID)
	if err != nil {
		return total, err
	}
	n, _ := res.RowsAffected()
	total += n

	res, err = tx.Exec(`DELETE FROM agent_sessions WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return total, err
	}
	n, _ = res.RowsAffected()
	total += n

	if err := tx.Commit(); err != nil {
		return total, err
	}
	return total, nil
}

var (
	retentionOnce     sync.Once
	retentionStop     chan struct{}
	retentionStopOnce sync.Once
	retentionWG       sync.WaitGroup
)

// RetentionDaysFromEnv reads RETENTION_DAYS (default 30). A value of 0 or a
// negative value disables automatic retention.
func RetentionDaysFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("RETENTION_DAYS"))
	if raw == "" {
		return 30
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		return 30
	}
	return days
}

// StartRetentionWorker runs the GDPR retention purge immediately and then once
// every 24 hours. It is a no-op when retentionDays <= 0.
func StartRetentionWorker(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	retentionOnce.Do(func() {
		retentionStop = make(chan struct{})
		retentionWG.Add(1)
		go func() {
			defer retentionWG.Done()

			purge := func() {
				n, err := PurgeExpiredData(retentionDays)
				if err != nil {
					log.Printf("⚠️ retention: purge failed: %v", err)
					return
				}
				if n > 0 {
					log.Printf("🧹 retention: purged %d rows older than %d days", n, retentionDays)
				}
			}
			purge()

			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					purge()
				case <-retentionStop:
					return
				}
			}
		}()
	})
}

// StopRetentionWorker stops the background retention loop, if running.
func StopRetentionWorker() {
	retentionStopOnce.Do(func() {
		if retentionStop != nil {
			close(retentionStop)
			retentionWG.Wait()
		}
	})
}

// GetUSDToINRRate returns the exchange rate (default: 95.89 INR/USD, configurable via USD_TO_INR env var)
func GetUSDToINRRate() float64 {
	rate := 95.89
	if envRate := os.Getenv("USD_TO_INR"); envRate != "" {
		if r, err := strconv.ParseFloat(envRate, 64); err == nil && r > 0 {
			rate = r
		}
	}
	return rate
}
