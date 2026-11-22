package api

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"goapi/db"
)

func auditPrepareDB(b *testing.B, rows int) {
	b.Helper()
	old, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	if err := os.Chdir(dir); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.Chdir(old) })

	if _, err := db.InitDB(); err != nil {
		b.Fatal(err)
	}
	tx, err := db.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := tx.Exec(`INSERT INTO messages (conversation_id,user_id,role,content,model,tokens,account_tier,account_id) VALUES (?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("conv-%d", i/10), "bench-user", "user", strings.Repeat("payload ", 40), "gemini-3.8-flash", 40, "free", "bench"); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkAuditExportJSON5000(b *testing.B) {
	auditPrepareDB(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ExportAnalyticsToJSON(""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuditExportExcel5000(b *testing.B) {
	auditPrepareDB(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ExportAnalyticsToExcel(""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuditCountTokens100k(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100000/45)
	_ = CountTokens(text) // warm the BPE singleton
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CountTokens(text)
	}
}

func BenchmarkAuditEstimateTokens100k(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100000/45)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokens(text)
	}
}
