package gemini

import (
	"strings"
	"testing"
)

func TestParseResponseBasic(t *testing.T) {
	raw := `[["wrb.fr",null,"[null,[\"c_abc\",\"r_def\"],null,null,[[\"choice_1\",[\"Hello world\"]]]]"]]`
	res := &GeminiResponse{}
	parseResponse(raw, res)

	if res.ConversationID != "c_abc" {
		t.Errorf("ConversationID = %q, want c_abc", res.ConversationID)
	}
	if res.ResponseID != "r_def" {
		t.Errorf("ResponseID = %q, want r_def", res.ResponseID)
	}
	if res.ChoiceID != "choice_1" {
		t.Errorf("ChoiceID = %q, want choice_1", res.ChoiceID)
	}
	if res.Text != "Hello world" {
		t.Errorf("Text = %q, want Hello world", res.Text)
	}
}

func TestParseResponseIgnoresMalformedInput(t *testing.T) {
	inputs := []string{
		"",
		"not json",
		`[["wrb.fr"]]`,
		`[["other",null,"payload"]]`,
		`[["wrb.fr",null,"not-json"]]`,
	}
	for _, raw := range inputs {
		res := &GeminiResponse{}
		parseResponse(raw, res)
		if res.Text != "" || res.ConversationID != "" {
			t.Errorf("malformed input %q populated response: %+v", raw, res)
		}
	}
}

func TestParseResponseProbesMusicURLs(t *testing.T) {
	raw := `[["wrb.fr", null, "[null,[\"c_1\",\"r_1\"],null,null,[[\"ch\",[\"text\"],\"https://contribution.usercontent.google.com/download?filename=track_one.mp3\"]]]"]]`
	res := &GeminiResponse{}
	parseResponse(raw, res)

	if len(res.Music) != 1 {
		t.Fatalf("expected 1 music track, got %d", len(res.Music))
	}
	if res.Music[0].Title != "track one" {
		t.Errorf("unexpected track title %q", res.Music[0].Title)
	}
}

func TestNextStreamDelta(t *testing.T) {
	cases := []struct {
		sent, snapshot, delta, newSent string
	}{
		{"", "hello", "hello", "hello"},
		{"hello", "hello world", " world", "hello world"},
		{"hello", "hello", "", "hello"},
		{"abc", "xyz", "", "abc"},
		{"", "", "", ""},
	}
	for _, tc := range cases {
		delta, newSent := nextStreamDelta(tc.sent, tc.sent, tc.snapshot)
		if delta != tc.delta || newSent != tc.newSent {
			t.Errorf("nextStreamDelta(%q,%q) = (%q,%q), want (%q,%q)",
				tc.sent, tc.snapshot, delta, newSent, tc.delta, tc.newSent)
		}
	}
}

func TestBuildRequestStruct(t *testing.T) {
	req := buildRequestStruct("msg", []string{"en-GB"}, []interface{}{nil}, true)
	if len(req) != temporaryChatFlagIndex+1 {
		t.Fatalf("length = %d, want %d", len(req), temporaryChatFlagIndex+1)
	}
	if req[0] != "msg" {
		t.Errorf("req[0] = %v", req[0])
	}
	if req[temporaryChatFlagIndex] != 1 {
		t.Errorf("temporary flag = %v, want 1", req[temporaryChatFlagIndex])
	}

	req = buildRequestStruct("msg", nil, nil, false)
	if req[temporaryChatFlagIndex] != nil {
		t.Errorf("non-temporary flag = %v, want nil", req[temporaryChatFlagIndex])
	}
}

func TestTemporaryChatsEnabled(t *testing.T) {
	t.Setenv("GEMINI_TEMPORARY_CHATS", "")
	if !temporaryChatsEnabled() {
		t.Error("temporary chats should be enabled by default")
	}
	for _, value := range []string{"0", "false", "off", "no", "NO"} {
		t.Setenv("GEMINI_TEMPORARY_CHATS", value)
		if temporaryChatsEnabled() {
			t.Errorf("temporary chats should be disabled for %q", value)
		}
	}
	t.Setenv("GEMINI_TEMPORARY_CHATS", "1")
	if !temporaryChatsEnabled() {
		t.Error("temporary chats should be enabled for 1")
	}
}

func TestParseCookieString(t *testing.T) {
	cookies := parseCookieString("A=1; B=two; INVALID; C=three=four", ".google.com")
	if len(cookies) != 3 {
		t.Fatalf("expected 3 cookies, got %d", len(cookies))
	}
	if cookies[0].Name != "A" || cookies[0].Value != "1" {
		t.Errorf("unexpected first cookie: %+v", cookies[0])
	}
	if cookies[2].Value != "three=four" {
		t.Errorf("value with = should be preserved: %+v", cookies[2])
	}
	for _, cookie := range cookies {
		if cookie.Domain != ".google.com" || cookie.Path != "/" {
			t.Errorf("unexpected cookie scope: %+v", cookie)
		}
	}
}

func TestSecureReqID(t *testing.T) {
	seen := make(map[int]bool)
	for i := 0; i < 100; i++ {
		id := secureReqID()
		if id < 1000000 || id > 10000000 {
			t.Fatalf("id %d out of range", id)
		}
		seen[id] = true
	}
	if len(seen) < 90 {
		t.Fatalf("expected high entropy ids, got %d unique", len(seen))
	}
}

func TestConversationStateIsolation(t *testing.T) {
	client := &GeminiClient{}
	client.updateConvStateFor("session-a", "model-x", "c_a", "r_a", "ch_a")
	client.updateConvStateFor("session-b", "model-x", "c_b", "r_b", "ch_b")

	if got := client.CurrentConversationIDFor("session-a", "model-x"); got != "c_a" {
		t.Errorf("session-a conv = %q, want c_a", got)
	}
	if got := client.CurrentConversationIDFor("session-b", "model-x"); got != "c_b" {
		t.Errorf("session-b conv = %q, want c_b", got)
	}
	if got := client.CurrentConversationIDFor("session-c", "model-x"); got != "" {
		t.Errorf("unknown session conv = %q, want empty", got)
	}

	client.ResetConversationStateSession("session-a", "model-x")
	if got := client.CurrentConversationIDFor("session-a", "model-x"); got != "" {
		t.Errorf("reset session conv = %q, want empty", got)
	}

	client.RestoreConversationStateSession("session-a", "model-x", "c_restored", "r", "ch")
	if got := client.CurrentConversationIDFor("session-a", "model-x"); got != "c_restored" {
		t.Errorf("restored session conv = %q, want c_restored", got)
	}
	client.RestoreConversationStateSession("session-z", "model-x", "", "", "")
	if got := client.CurrentConversationIDFor("session-z", "model-x"); got != "" {
		t.Errorf("empty restore should be ignored, got %q", got)
	}
}

func TestDownloadFileRejectsDisallowedTargets(t *testing.T) {
	client, err := NewClient("guest")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	if err := client.DownloadFile("http://google.com/file", t.TempDir()+"/x"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
	if err := client.DownloadFile("https://evil.example/file", t.TempDir()+"/x"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected host rejection, got %v", err)
	}
}

func TestGuestClientInitAndClose(t *testing.T) {
	client, err := NewClient("guest")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.InitSession(); err != nil {
		t.Fatalf("guest InitSession: %v", err)
	}
	if !client.IsInitialized() {
		t.Error("guest client should be initialized")
	}
	if tier := client.GetAccountTier(); tier != "free" {
		t.Errorf("guest tier = %q, want free", tier)
	}
	client.Close()
	// Close must be safe to call twice.
	client.Close()
}

func TestAccountPrefixResolution(t *testing.T) {
	t.Setenv("GEMINI_ACCOUNT_PREFIX", "/u/7")
	if got := resolveAccountPrefix(); got != "/u/7" {
		t.Errorf("prefix = %q, want /u/7", got)
	}
	t.Setenv("GEMINI_ACCOUNT_PREFIX", "")
	t.Setenv("GEMINI_ACCOUNT_INDEX", "3")
	if got := resolveAccountPrefix(); got != "/u/3" {
		t.Errorf("prefix = %q, want /u/3", got)
	}
	t.Setenv("GEMINI_ACCOUNT_INDEX", "/u/9")
	if got := resolveAccountPrefix(); got != "/u/9" {
		t.Errorf("prefix = %q, want /u/9", got)
	}
	t.Setenv("GEMINI_ACCOUNT_INDEX", "")
	if got := resolveAccountPrefix(); got != "" {
		t.Errorf("prefix = %q, want empty", got)
	}
}
