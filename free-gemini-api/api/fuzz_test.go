package api

import (
	"encoding/json"
	"testing"
)

func FuzzFlattenContent(f *testing.F) {
	f.Add([]byte(`"hello world"`))
	f.Add([]byte(`[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`))
	f.Add([]byte(`{"unexpected":"shape"}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		_ = FlattenContent(json.RawMessage(data))
	})
}

func FuzzSafeOutputPath(f *testing.F) {
	f.Add("image.png")
	f.Add("../etc/passwd")
	f.Add("%2e%2e%2fetc%2fpasswd")
	f.Add("a/b/c.txt")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		path, ok := safeOutputPath(name)
		if ok && path == "" {
			t.Fatal("accepted path must not be empty")
		}
	})
}

func FuzzSafeMCPPath(f *testing.F) {
	root := f.TempDir()
	f.Add("image.png")
	f.Add("../etc/passwd")
	f.Add("/etc/passwd")
	f.Add("sub/../../escape")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		_, _ = safeMCPPath(root, name)
	})
}

func FuzzSanitizeSessionID(f *testing.F) {
	f.Add("valid_id-1")
	f.Add("with spaces")
	f.Add("")
	f.Add("../../etc/passwd")

	f.Fuzz(func(t *testing.T, raw string) {
		got := sanitizeSessionID(raw, "127.0.0.1")
		if got == "" {
			t.Fatal("sanitized id must not be empty")
		}
		if len(got) > 80 {
			t.Fatalf("sanitized id unexpectedly long: %d", len(got))
		}
	})
}
