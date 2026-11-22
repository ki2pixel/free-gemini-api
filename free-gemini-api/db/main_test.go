package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "goapi-db-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("GEMINI_DB_PATH", filepath.Join(dir, "gemini.db"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
