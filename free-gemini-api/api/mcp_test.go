package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// A minimal PNG signature is enough for http.DetectContentType.
	data := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 64)...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write test png: %v", err)
	}
	return path
}

func TestSafeMCPPathAcceptsLocalRegularFile(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, root, "ok.png")

	full, err := safeMCPPath(root, "ok.png")
	if err != nil {
		t.Fatalf("expected ok.png to be accepted: %v", err)
	}
	if !strings.HasPrefix(full, root) {
		t.Fatalf("resolved path %q escapes root %q", full, root)
	}
}

func TestSafeMCPPathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.png")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	cases := []string{
		"../outside.png",
		"../../etc/passwd",
		"/etc/passwd",
		"..\\outside.png",
		"",
		".",
		"sub/../../outside.png",
	}
	for _, name := range cases {
		if _, err := safeMCPPath(root, name); err == nil {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestSafeMCPPathRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := writeTestPNG(t, root, "target.png")
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := safeMCPPath(root, "link.png"); err == nil {
		t.Fatal("symlink should be rejected")
	}
}

func TestSafeMCPPathRejectsLargeEmptyAndDirectories(t *testing.T) {
	root := t.TempDir()

	large := filepath.Join(root, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(mcpMaxMediaBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	empty := filepath.Join(root, "empty.png")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := safeMCPPath(root, "large.png"); err == nil {
		t.Error("oversized file should be rejected")
	}
	if _, err := safeMCPPath(root, "empty.png"); err == nil {
		t.Error("empty file should be rejected")
	}
	if _, err := safeMCPPath(root, "."); err == nil {
		t.Error("directory should be rejected")
	}
}

func TestResolveMCPMediaPathValidatesMIME(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MCP_MEDIA_ROOT", root)
	writeTestPNG(t, root, "ok.png")

	resolved, err := resolveMCPMediaPath("chat", "ok.png")
	if err != nil {
		t.Fatalf("valid PNG rejected: %v", err)
	}
	if resolved == "" {
		t.Fatal("expected resolved path")
	}

	fake := filepath.Join(root, "fake.png")
	if err := os.WriteFile(fake, []byte("this is plain text, not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMCPMediaPath("chat", "fake.png"); err == nil {
		t.Fatal("MIME mismatch should be rejected")
	}

	executable := filepath.Join(root, "evil.exe")
	if err := os.WriteFile(executable, []byte("MZ"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMCPMediaPath("chat", "evil.exe"); err == nil {
		t.Fatal("unsupported extension should be rejected")
	}
}

func TestMCPMediaRootDefaultAndOverride(t *testing.T) {
	t.Setenv("MCP_MEDIA_ROOT", "")
	if got := mcpMediaRoot(); got != "./output" {
		t.Fatalf("expected default ./output, got %q", got)
	}
	t.Setenv("MCP_MEDIA_ROOT", "/tmp/media")
	if got := mcpMediaRoot(); got != "/tmp/media" {
		t.Fatalf("expected override, got %q", got)
	}
}

func TestNegotiateMCPProtocolVersion(t *testing.T) {
	for _, version := range mcpSupportedProtocolVersions {
		got, err := negotiateMCPProtocolVersion(version)
		if err != nil || got != version {
			t.Fatalf("version %q: got %q err %v", version, got, err)
		}
	}

	got, err := negotiateMCPProtocolVersion("")
	if err != nil {
		t.Fatalf("empty version should select the newest: %v", err)
	}
	if got != mcpSupportedProtocolVersions[len(mcpSupportedProtocolVersions)-1] {
		t.Fatalf("unexpected default version %q", got)
	}

	if _, err := negotiateMCPProtocolVersion("1999-01-01"); err == nil {
		t.Fatal("unsupported version should return an error")
	}
}
