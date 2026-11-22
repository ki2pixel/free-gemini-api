package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the API test suite from any production database in the
// working directory. Go runs package tests with the package directory as cwd,
// which previously made tests contend with a live server's data/gemini.db.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "goapi-api-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("GEMINI_DB_PATH", filepath.Join(dir, "gemini.db"))

	code := m.Run()

	os.RemoveAll(dir)
	os.Exit(code)
}
