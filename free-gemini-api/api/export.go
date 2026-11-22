package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"goapi/db"
	"goapi/gemini"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/xuri/excelize/v2"
)

var (
	analyticsExportSignal = make(chan struct{}, 1)
	analyticsExportOnce   sync.Once

	// exportBuildSem bounds concurrent workbook/report generation so a burst of
	// /export requests cannot exhaust memory or CPU.
	exportBuildSem = make(chan struct{}, 2)
)

// AnalyticsService groups analytics report generation behind one type so the
// HTTP layer no longer knows how reports are built or persisted.
type AnalyticsService struct{}

// DefaultAnalyticsService is the process-wide analytics service.
var DefaultAnalyticsService = &AnalyticsService{}

// BuildJSON renders the analytics report in memory.
func (s *AnalyticsService) BuildJSON() ([]byte, error) {
	return buildAnalyticsReport()
}

// BuildExcel renders the workbook in memory.
func (s *AnalyticsService) BuildExcel() (*excelize.File, error) {
	return buildAnalyticsWorkbook()
}

// ExportJSON writes the report atomically to outputPath.
func (s *AnalyticsService) ExportJSON(outputPath string) (string, []byte, error) {
	return ExportAnalyticsToJSON(outputPath)
}

// ExportExcel writes the workbook atomically to outputPath.
func (s *AnalyticsService) ExportExcel(outputPath string) (string, error) {
	return ExportAnalyticsToExcel(outputPath)
}

// Schedule requests a debounced background refresh.
func (s *AnalyticsService) Schedule() {
	ScheduleAnalyticsExport()
}

// fixedWindowLimiter is a tiny per-key fixed-window limiter used to protect
// the expensive export endpoints. It deliberately keeps no unbounded history:
// expired windows are pruned on every call.
type fixedWindowLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[string]*fixedWindowEntry
}

type fixedWindowEntry struct {
	count int
	reset time.Time
}

func newFixedWindowLimiter(max int, window time.Duration) *fixedWindowLimiter {
	if max <= 0 {
		max = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &fixedWindowLimiter{
		max:     max,
		window:  window,
		entries: make(map[string]*fixedWindowEntry),
	}
}

func (l *fixedWindowLimiter) allow(key string) (allowed bool, remaining int, retryAfter time.Duration) {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	for k, entry := range l.entries {
		if now.After(entry.reset) {
			delete(l.entries, k)
		}
	}

	entry, ok := l.entries[key]
	if !ok || now.After(entry.reset) {
		entry = &fixedWindowEntry{reset: now.Add(l.window)}
		l.entries[key] = entry
	}
	if entry.count >= l.max {
		return false, 0, time.Until(entry.reset)
	}
	entry.count++
	return true, l.max - entry.count, 0
}

var exportRateLimiter = newFixedWindowLimiter(5, time.Minute)

// ExportRateLimitMiddleware applies the dedicated /export budget: five
// requests per minute per IP and token fingerprint.
func ExportRateLimitMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		key := c.IP()
		if token := extractAPIToken(c); token != "" {
			sum := sha256.Sum256([]byte(token))
			key += ":" + hex.EncodeToString(sum[:8])
		}

		allowed, remaining, retryAfter := exportRateLimiter.allow(key)
		c.Set("X-RateLimit-Limit", "5")
		c.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		if !allowed {
			RecordRateLimitDrop()
			c.Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "export rate limit exceeded"})
		}
		return c.Next()
	}
}

// ScheduleAnalyticsExport requests a debounced background refresh of the
// analytics files. It never blocks the request path: the channel has capacity
// one and duplicate signals are coalesced.
func ScheduleAnalyticsExport() {
	analyticsExportOnce.Do(func() {
		go analyticsExportWorker()
	})
	select {
	case analyticsExportSignal <- struct{}{}:
	default:
	}
}

func analyticsExportWorker() {
	for range analyticsExportSignal {
		// Debounce: a burst of chat requests produces one report.
		time.Sleep(5 * time.Second)

		// Drain signals that arrived while debouncing.
	drain:
		for {
			select {
			case <-analyticsExportSignal:
			default:
				break drain
			}
		}

		if err := exportAnalyticsAtomic(); err != nil {
			log.Printf("⚠️ Background analytics export failed: %v", err)
		}
	}
}

func exportAnalyticsAtomic() error {
	jsonPath := filepath.Join("./output", "gemini_analytics.json")
	excelPath := filepath.Join("./output", "gemini_analytics.xlsx")

	jsonErr := error(nil)
	if _, _, err := ExportAnalyticsToJSON(jsonPath); err != nil {
		jsonErr = err
		log.Printf("⚠️ Background analytics JSON warning: %v", err)
	}

	excelErr := error(nil)
	if _, err := ExportAnalyticsToExcel(excelPath); err != nil {
		excelErr = err
		log.Printf("⚠️ Background analytics Excel warning: %v", err)
	}

	if jsonErr != nil && excelErr != nil {
		return fmt.Errorf("json: %v; excel: %v", jsonErr, excelErr)
	}
	if jsonErr == nil && excelErr == nil {
		log.Printf("📊 Auto-exported dual analytics: %s & %s", jsonPath, excelPath)
	}
	return nil
}

// HandleExcelExport handles GET /export/excel. The workbook is generated and
// streamed entirely in memory, so concurrent exports cannot collide on disk.
func HandleExcelExport(c fiber.Ctx) error {
	exportBuildSem <- struct{}{}
	defer func() { <-exportBuildSem }()

	f, err := DefaultAnalyticsService.BuildExcel()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": fmt.Sprintf("Failed to export excel: %v", err)})
	}
	defer f.Close()

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": fmt.Sprintf("Failed to serialize excel: %v", err)})
	}

	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="gemini_analytics.xlsx"`)
	c.Set("Cache-Control", "no-store")
	return c.Send(buf.Bytes())
}

// HandleJSONExport handles GET /v1/analytics and GET /export/json. It builds
// the report in memory and never overwrites the on-disk analytics cache.
func HandleJSONExport(c fiber.Ctx) error {
	exportBuildSem <- struct{}{}
	defer func() { <-exportBuildSem }()

	data, err := DefaultAnalyticsService.BuildJSON()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": fmt.Sprintf("Failed to export JSON analytics: %v", err)})
	}
	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	c.Set("Cache-Control", "no-store")
	return c.Send(data)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ExportAnalyticsToJSON compiles system stats, history, and financial ROI into
// structured JSON and writes it atomically.
func ExportAnalyticsToJSON(outputPath string) (string, []byte, error) {
	if outputPath == "" {
		outputPath = filepath.Join("./output", "gemini_analytics.json")
	}

	data, err := buildAnalyticsReport()
	if err != nil {
		return "", nil, err
	}
	if err := atomicWriteFile(outputPath, data, 0600); err != nil {
		return "", nil, err
	}

	return outputPath, data, nil
}

// buildAnalyticsReport compiles the JSON report in memory.
func buildAnalyticsReport() ([]byte, error) {
	messages, _ := db.GetAllMessagesForExport()
	media, _ := db.GetAllMediaForExport()
	accounts, _ := db.GetAccounts()
	stats, _ := db.GetSystemStats()

	totalTokens := 0
	for _, m := range messages {
		if tok, ok := m["tokens"].(int); ok {
			totalTokens += tok
		}
	}

	mediaCounts := map[string]int{"image": 0, "video": 0, "music": 0}
	for _, md := range media {
		if mType, ok := md["type"].(string); ok {
			mediaCounts[strings.ToLower(mType)]++
		}
	}

	report := fiber.Map{
		"generated_at": time.Now().Format(time.RFC3339),
		"engine": fiber.Map{
			"name":          "Free Gemini API",
			"version":       "3.8",
			"primary_model": "Google Gemini 3.8 Flash",
			"bpe_tokenizer": "cl100k_base (tiktoken-go)", // #nosec G101 -- descriptive metadata, not a credential
			"database":      "SQLite (WAL Mode)",
			"transport":     "HTTP/3 QUIC + HTTP/2 TLS Fallback",
		},
		"summary": fiber.Map{
			"total_messages":   len(messages),
			"total_tokens":     totalTokens,
			"total_media":      len(media),
			"total_requests":   stats["total_requests"],
			"media_breakdown":  mediaCounts,
			"tracked_accounts": len(accounts),
			"active_workers":   gemini.GetActiveWorkerCount(),
			"tier_breakdown":   stats["tier_breakdown"],
			"model_breakdown":  stats["model_breakdown"],
		},
		"financial_roi": fiber.Map{
			"cost_saved_usd":           stats["cost_saved_usd"],
			"cost_saved_usd_formatted": stats["cost_saved_usd_formatted"],
			"cost_saved_inr":           stats["cost_saved_inr"],
			"cost_saved_inr_formatted": stats["cost_saved_inr_formatted"],
			"official_pricing_benchmarks": fiber.Map{
				"gemini_3_8_flash_per_1m_tokens_usd": 0.50,
				"imagen_3_per_image_usd":             0.030,
				"veo_per_video_usd":                  1.20,
				"music_per_track_usd":                0.08,
				"usd_to_inr_exchange_rate":           db.GetUSDToINRRate(),
			},
		},
		"recent_messages":   messages,
		"media_generations": media,
		"accounts":          accounts,
	}

	return json.MarshalIndent(report, "", "  ")
}

// ExportAnalyticsToExcel creates a multi-sheet formatted Excel workbook and
// writes it atomically.
func ExportAnalyticsToExcel(outputPath string) (string, error) {
	if outputPath == "" {
		outputPath = filepath.Join("./output", "gemini_analytics.xlsx")
	}

	f, err := buildAnalyticsWorkbook()
	if err != nil {
		return "", err
	}
	defer f.Close()

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, ".analytics-*.xlsx")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if err := f.SaveAs(tmpName); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, outputPath); err != nil {
		return "", err
	}

	return outputPath, nil
}

// buildAnalyticsWorkbook builds the Excel workbook in memory.
func buildAnalyticsWorkbook() (*excelize.File, error) {
	messages, _ := db.GetAllMessagesForExport()
	media, _ := db.GetAllMediaForExport()
	accounts, _ := db.GetAccounts()
	stats, _ := db.GetSystemStats()

	f := excelize.NewFile()

	// ─── Sheet 1: 📋 Chat History ───────────────────────────────────────────
	sheetMessages := "📋 Chat History"
	f.SetSheetName("Sheet1", sheetMessages)
	msgHeaders := []string{"ID", "Conv ID", "User / IP", "Role", "Model", "Account Tier", "Account ID", "Est. Tokens", "Created At", "Message Content"}
	var msgRows [][]interface{}
	totalTokens := 0
	for _, m := range messages {
		tok, _ := m["tokens"].(int)
		totalTokens += tok
		msgRows = append(msgRows, []interface{}{
			m["id"],
			m["conversation_id"],
			m["user_id"],
			m["role"],
			m["model"],
			m["account_tier"],
			m["account_id"],
			tok,
			m["created_at"],
			m["content"],
		})
	}
	_ = renderStyledSheet(f, sheetMessages, msgRows, msgHeaders, "#1E293B", "#1877F2")

	// ─── Sheet 2: 🎨 Generated Media ────────────────────────────────────────
	sheetMedia := "🎨 Generated Media"
	f.NewSheet(sheetMedia)
	mediaHeaders := []string{"ID", "Media Type", "Prompt", "File Name", "Download URL", "Aspect Ratio", "Response ID", "Created At"}
	var mediaRows [][]interface{}
	mediaCounts := map[string]int{"image": 0, "video": 0, "music": 0}
	for _, md := range media {
		mType, _ := md["type"].(string)
		mediaCounts[strings.ToLower(mType)]++
		mediaRows = append(mediaRows, []interface{}{
			md["id"],
			md["type"],
			md["prompt"],
			md["file_name"],
			md["url"],
			md["aspect_ratio"],
			md["response_id"],
			md["created_at"],
		})
	}
	_ = renderStyledSheet(f, sheetMedia, mediaRows, mediaHeaders, "#581C87", "#9333EA")

	// ─── Sheet 3: 📊 Executive Analytics ────────────────────────────────────
	sheetAnalytics := "📊 Executive Analytics"
	f.NewSheet(sheetAnalytics)
	_ = renderAnalyticsSummarySheet(f, sheetAnalytics, len(messages), len(media), totalTokens, stats, mediaCounts, accounts)

	// Set Sheet 1 as active view
	activeIdx, _ := f.GetSheetIndex(sheetMessages)
	f.SetActiveSheet(activeIdx)

	return f, nil
}

func renderStyledSheet(f *excelize.File, sheetName string, rows [][]interface{}, headers []string, headerBgColor string, tabColorRGB string) error {
	_ = f.SetSheetProps(sheetName, &excelize.SheetPropsOptions{
		TabColorRGB: &tabColorRGB,
	})

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 11, Bold: true, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{headerBgColor}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#CBD5E1", Style: 1},
			{Type: "right", Color: "#CBD5E1", Style: 1},
			{Type: "top", Color: "#CBD5E1", Style: 1},
			{Type: "bottom", Color: "#CBD5E1", Style: 1},
		},
	})

	cellStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 10},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	centerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 10},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	wrapStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 10},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "top", WrapText: true},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	// Header row
	_ = f.SetRowHeight(sheetName, 1, 28)
	for colIdx, headerText := range headers {
		cell, _ := excelize.CoordinatesToCellName(colIdx+1, 1)
		_ = f.SetCellValue(sheetName, cell, headerText)
		_ = f.SetCellStyle(sheetName, cell, cell, headerStyle)
	}

	// Data rows
	for rowIdx, rowData := range rows {
		rowNum := rowIdx + 2
		_ = f.SetRowHeight(sheetName, rowNum, 22)
		for colIdx, val := range rowData {
			cell, _ := excelize.CoordinatesToCellName(colIdx+1, rowNum)
			_ = f.SetCellValue(sheetName, cell, val)

			header := headers[colIdx]
			if strings.Contains(header, "Content") || strings.Contains(header, "Prompt") {
				_ = f.SetCellStyle(sheetName, cell, cell, wrapStyle)
			} else if strings.Contains(header, "ID") || strings.Contains(header, "Tokens") || strings.Contains(header, "Created") || strings.Contains(header, "Role") || strings.Contains(header, "Type") {
				_ = f.SetCellStyle(sheetName, cell, cell, centerStyle)
			} else {
				_ = f.SetCellStyle(sheetName, cell, cell, cellStyle)
			}
		}
	}

	// Auto-fit column widths
	for colIdx, header := range headers {
		colLetter, _ := excelize.ColumnNumberToName(colIdx + 1)
		if strings.Contains(header, "Content") || strings.Contains(header, "Prompt") {
			_ = f.SetColWidth(sheetName, colLetter, colLetter, 50)
		} else if strings.Contains(header, "URL") {
			_ = f.SetColWidth(sheetName, colLetter, colLetter, 35)
		} else if strings.Contains(header, "Conv ID") || strings.Contains(header, "Created") {
			_ = f.SetColWidth(sheetName, colLetter, colLetter, 22)
		} else {
			_ = f.SetColWidth(sheetName, colLetter, colLetter, 16)
		}
	}

	// Freeze header row and enable AutoFilter
	lastCol, _ := excelize.ColumnNumberToName(len(headers))
	lastRow := len(rows) + 1
	if lastRow < 2 {
		lastRow = 2
	}
	_ = f.AutoFilter(sheetName, fmt.Sprintf("A1:%s%d", lastCol, lastRow), nil)
	_ = f.SetPanes(sheetName, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		XSplit:      0,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	})

	return nil
}

func renderAnalyticsSummarySheet(f *excelize.File, sheetName string, totalMsgs, totalMedia, totalTokens int, stats map[string]any, mediaCounts map[string]int, accounts []map[string]any) error {
	tabColor := "10B981"
	_ = f.SetSheetProps(sheetName, &excelize.SheetPropsOptions{
		TabColorRGB: &tabColor,
	})

	titleStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Family: "Arial", Size: 16, Bold: true, Color: "#1E293B"},
	})

	sectionStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Family: "Arial", Size: 11, Bold: true, Color: "#0F172A"},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#F1F5F9"}},
		Border: []excelize.Border{
			{Type: "left", Color: "#CBD5E1", Style: 1},
			{Type: "right", Color: "#CBD5E1", Style: 1},
			{Type: "top", Color: "#CBD5E1", Style: 1},
			{Type: "bottom", Color: "#CBD5E1", Style: 1},
		},
	})

	metricLabelStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Family: "Arial", Size: 10, Bold: true, Color: "#334155"},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	metricValStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 13, Bold: true, Color: "#1877F2"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	savingsCardStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 13, Bold: true, Color: "#059669"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	savingsHeaderStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 11, Bold: true, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#065F46"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#A7F3D0", Style: 1},
			{Type: "right", Color: "#A7F3D0", Style: 1},
			{Type: "top", Color: "#A7F3D0", Style: 1},
			{Type: "bottom", Color: "#A7F3D0", Style: 1},
		},
	})

	savingsHighlightStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Family: "Arial", Size: 10, Bold: true, Color: "#065F46"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#ECFDF5"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "left", Color: "#CBD5E1", Style: 1},
			{Type: "right", Color: "#CBD5E1", Style: 1},
			{Type: "top", Color: "#CBD5E1", Style: 1},
			{Type: "bottom", Color: "#CBD5E1", Style: 1},
		},
	})

	tableDataStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Family: "Arial", Size: 10},
		Border: []excelize.Border{
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
		},
	})

	_ = f.SetCellValue(sheetName, "A1", "Free Gemini API — Intelligence & System Analytics")
	_ = f.SetCellStyle(sheetName, "A1", "A1", titleStyle)
	_ = f.SetRowHeight(sheetName, 1, 32)
	_ = f.SetCellValue(sheetName, "A2", fmt.Sprintf("Report Generated: %s | High-Performance Go Engine", time.Now().Format("02 Jan 2006, 03:04 PM")))

	// Official Google Cloud / Vertex AI & Gemini API pricing:
	costTokensUSD := float64(totalTokens) * 0.0000005      // $0.50 per 1M tokens ($0.0000005 per token)
	costImagesUSD := float64(mediaCounts["image"]) * 0.030 // $0.030 per image (Vertex AI Imagen 3)
	costVideosUSD := float64(mediaCounts["video"]) * 1.200 // $1.200 per video (Veo / Gemini video)
	costMusicUSD := float64(mediaCounts["music"]) * 0.080  // $0.080 per track (Audio / Music)
	totalSavedUSD := costTokensUSD + costImagesUSD + costVideosUSD + costMusicUSD
	usdToINR := db.GetUSDToINRRate()
	totalSavedINR := totalSavedUSD * usdToINR

	// KPI Summary Box
	_ = f.SetCellValue(sheetName, "A4", "Core Performance & Financial Metric")
	_ = f.SetCellValue(sheetName, "B4", "Value")
	_ = f.SetCellStyle(sheetName, "A4", "A4", sectionStyle)
	_ = f.SetCellStyle(sheetName, "B4", "B4", sectionStyle)

	totalRequests, _ := stats["total_requests"].(int)
	freeMsgs := 0
	freeToks := 0
	subMsgs := 0
	subToks := 0
	if tb, ok := stats["tier_breakdown"].(map[string]any); ok {
		if fMap, ok := tb["free"].(map[string]any); ok {
			if m, ok := fMap["messages"].(int); ok {
				freeMsgs = m
			}
			if t, ok := fMap["tokens"].(int); ok {
				freeToks = t
			}
		}
		if sMap, ok := tb["subscription"].(map[string]any); ok {
			if m, ok := sMap["messages"].(int); ok {
				subMsgs = m
			}
			if t, ok := sMap["tokens"].(int); ok {
				subToks = t
			}
		}
	}

	kpis := []struct {
		Label     string
		Val       string
		IsSavings bool
	}{
		{"Active Primary Model", "Google Gemini 3.8 Flash", false},
		{"Total Chat Messages Logged", fmt.Sprintf("%d", totalMsgs), false},
		{"Free Tier Usage (Messages / Tokens)", fmt.Sprintf("%d msgs / %d tokens", freeMsgs, freeToks), false},
		{"Subscription Tier Usage (Pro / Adv)", fmt.Sprintf("%d msgs / %d tokens", subMsgs, subToks), false},
		{"Total Media Items Generated", fmt.Sprintf("%d", totalMedia), false},
		{"Total Tokens Processed (Est.)", fmt.Sprintf("%d", totalTokens), false},
		{"Total HTTP Requests Logged", fmt.Sprintf("%d", totalRequests), false},
		{"Official API Value Equivalent", fmt.Sprintf("$%.4f USD", totalSavedUSD), false},
		{"Your Total Spend", "$0.00 (Zero API Keys)", false},
		{"Net Cost Saved (USD)", fmt.Sprintf("$%.4f USD (100%% ROI)", totalSavedUSD), true},
		{"Net Cost Saved (INR)", fmt.Sprintf("₹%.2f INR (@ ₹%.1f/$)", totalSavedINR, usdToINR), true},
		{"Active Multi-Account Profiles", fmt.Sprintf("%d", gemini.GetActiveAccountCount()), false},
		{"Connected Extension Workers", fmt.Sprintf("%d", gemini.GetActiveWorkerCount()), false},
		{"Engine Architecture", "HTTP/3 QUIC + Chrome JA4+ + SQLite WAL", false},
	}

	for i, k := range kpis {
		row := 5 + i
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), k.Label)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), k.Val)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), metricLabelStyle)
		if k.IsSavings {
			_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), savingsCardStyle)
		} else {
			_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), metricValStyle)
		}
	}

	// ─── Financial Savings Breakdown Table ───────────────────────────────────
	savStartRow := 5 + len(kpis) + 2
	savHeaders := []string{"AI Capability / Service", "Usage Volume", "Official Google API Rate", "Official Cost (USD)", "Official Cost (INR)", "Your Spend", "Net Cost Saved"}
	_ = f.SetRowHeight(sheetName, savStartRow, 26)
	for colIdx, hText := range savHeaders {
		cell, _ := excelize.CoordinatesToCellName(colIdx+1, savStartRow)
		_ = f.SetCellValue(sheetName, cell, hText)
		_ = f.SetCellStyle(sheetName, cell, cell, savingsHeaderStyle)
	}

	savRows := [][]interface{}{
		{"Gemini 3.8 Flash Chat Tokens", fmt.Sprintf("%d Tokens", totalTokens), "$0.50 / 1M Tokens", fmt.Sprintf("$%.4f", costTokensUSD), fmt.Sprintf("₹%.2f", costTokensUSD*usdToINR), "$0.00", fmt.Sprintf("$%.4f (100%%)", costTokensUSD)},
		{"Imagen 3 Ultra-HD Images", fmt.Sprintf("%d Images", mediaCounts["image"]), "$0.030 / Image", fmt.Sprintf("$%.4f", costImagesUSD), fmt.Sprintf("₹%.2f", costImagesUSD*usdToINR), "$0.00", fmt.Sprintf("$%.4f (100%%)", costImagesUSD)},
		{"AI Cinematic Videos (.mp4)", fmt.Sprintf("%d Videos", mediaCounts["video"]), "$1.200 / Video", fmt.Sprintf("$%.4f", costVideosUSD), fmt.Sprintf("₹%.2f", costVideosUSD*usdToINR), "$0.00", fmt.Sprintf("$%.4f (100%%)", costVideosUSD)},
		{"AI Music Synthesis (.mp3)", fmt.Sprintf("%d Tracks", mediaCounts["music"]), "$0.080 / Track", fmt.Sprintf("$%.4f", costMusicUSD), fmt.Sprintf("₹%.2f", costMusicUSD*usdToINR), "$0.00", fmt.Sprintf("$%.4f (100%%)", costMusicUSD)},
		{"🏆 TOTAL FINANCIAL VALUE SAVED", fmt.Sprintf("%d Operations", totalMsgs+totalMedia), "100% Free Engine", fmt.Sprintf("$%.4f", totalSavedUSD), fmt.Sprintf("₹%.2f", totalSavedINR), "$0.00", fmt.Sprintf("$%.4f (100%% ROI)", totalSavedUSD)},
	}

	for rIdx, sRow := range savRows {
		rowNum := savStartRow + 1 + rIdx
		_ = f.SetRowHeight(sheetName, rowNum, 22)
		isTotal := rIdx == len(savRows)-1
		for cIdx, val := range sRow {
			cell, _ := excelize.CoordinatesToCellName(cIdx+1, rowNum)
			_ = f.SetCellValue(sheetName, cell, val)
			if isTotal || cIdx == 6 {
				_ = f.SetCellStyle(sheetName, cell, cell, savingsHighlightStyle)
			} else {
				_ = f.SetCellStyle(sheetName, cell, cell, tableDataStyle)
			}
		}
	}

	// ─── Media Breakdown Table ──────────────────────────────────────────────
	startRow := savStartRow + len(savRows) + 2
	_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", startRow), "Media Generation Breakdown")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", startRow), "Count")
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("A%d", startRow), fmt.Sprintf("A%d", startRow), sectionStyle)
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", startRow), fmt.Sprintf("B%d", startRow), sectionStyle)

	mediaRows := []struct {
		Name  string
		Count int
	}{
		{"Images (Imagen 3 / 8K)", mediaCounts["image"]},
		{"Cinematic Videos (.mp4)", mediaCounts["video"]},
		{"Music Tracks (.mp3)", mediaCounts["music"]},
	}

	for i, mr := range mediaRows {
		row := startRow + 1 + i
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), mr.Name)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), mr.Count)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), tableDataStyle)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), tableDataStyle)
	}

	// ─── Accounts Pool Table ────────────────────────────────────────────────
	accStartRow := startRow + len(mediaRows) + 2
	_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", accStartRow), "Account Profile ID")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", accStartRow), "Account Tier")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("C%d", accStartRow), "Status")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", accStartRow), "Total Requests Handled")
	_ = f.SetCellValue(sheetName, fmt.Sprintf("E%d", accStartRow), "Last Used At")
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("A%d", accStartRow), fmt.Sprintf("A%d", accStartRow), sectionStyle)
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", accStartRow), fmt.Sprintf("B%d", accStartRow), sectionStyle)
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("C%d", accStartRow), fmt.Sprintf("C%d", accStartRow), sectionStyle)
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("D%d", accStartRow), fmt.Sprintf("D%d", accStartRow), sectionStyle)
	_ = f.SetCellStyle(sheetName, fmt.Sprintf("E%d", accStartRow), fmt.Sprintf("E%d", accStartRow), sectionStyle)

	for i, acc := range accounts {
		row := accStartRow + 1 + i
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), acc["account_id"])
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), acc["tier"])
		_ = f.SetCellValue(sheetName, fmt.Sprintf("C%d", row), acc["status"])
		_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", row), acc["total_requests"])
		_ = f.SetCellValue(sheetName, fmt.Sprintf("E%d", row), acc["last_used_at"])
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), tableDataStyle)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), tableDataStyle)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("C%d", row), fmt.Sprintf("C%d", row), tableDataStyle)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("D%d", row), fmt.Sprintf("D%d", row), tableDataStyle)
		_ = f.SetCellStyle(sheetName, fmt.Sprintf("E%d", row), fmt.Sprintf("E%d", row), tableDataStyle)
	}

	_ = f.SetColWidth(sheetName, "A", "A", 36)
	_ = f.SetColWidth(sheetName, "B", "B", 18)
	_ = f.SetColWidth(sheetName, "C", "C", 18)
	_ = f.SetColWidth(sheetName, "D", "D", 24)
	_ = f.SetColWidth(sheetName, "E", "E", 22)
	_ = f.SetColWidth(sheetName, "F", "F", 18)
	_ = f.SetColWidth(sheetName, "G", "G", 22)

	return nil
}
