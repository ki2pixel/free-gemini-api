package gemini

import "testing"

func TestNextStreamDelta_ExtendsSnapshot(t *testing.T) {
	sent, visible := "", ""
	for _, snapshot := range []string{"The", "The history", "The history of tea"} {
		delta, nextSent := nextStreamDelta(sent, visible, snapshot)
		if delta == "" {
			t.Fatalf("expected a delta for snapshot %q", snapshot)
		}
		sent, visible = nextSent, visible+delta
	}
	if visible != "The history of tea" {
		t.Fatalf("unexpected projection: %q", visible)
	}
}

func TestNextStreamDelta_DuplicateSnapshotIgnored(t *testing.T) {
	sent := "A complete answer."
	if delta, newSent := nextStreamDelta(sent, sent, sent); delta != "" || newSent != sent {
		t.Fatalf("duplicate snapshot must not emit, got delta=%q newSent=%q", delta, newSent)
	}
}

func TestNextStreamDelta_DivergentRerenderIgnored(t *testing.T) {
	// Old behaviour appended the whole snapshot here, duplicating the answer.
	sent := "The history of tea is long."
	delta, newSent := nextStreamDelta(sent, sent, "Tea history is long and rich.")
	if delta != "" {
		t.Fatalf("divergent re-render should not be appended, got %q", delta)
	}
	if newSent != sent {
		t.Fatalf("upstream mark should be unchanged, got %q", newSent)
	}
}

func TestNextStreamDelta_EmptySnapshotIgnored(t *testing.T) {
	if delta, newSent := nextStreamDelta("abc", "abc", ""); delta != "" || newSent != "abc" {
		t.Fatalf("empty snapshot must be a no-op")
	}
}

func TestNextStreamDelta_Deduplication(t *testing.T) {
	t.Run("suppresses a whole-answer re-render", func(t *testing.T) {
		sent := "A"
		delta, newSent := nextStreamDelta(sent, sent, "A\nA")
		if delta != "" {
			t.Fatalf("re-render must be suppressed, got %q", delta)
		}
		if newSent != "A\nA" {
			t.Fatalf("upstream watermark must follow the snapshot, got %q", newSent)
		}
	})

	t.Run("keeps the continuation after a re-rendered line", func(t *testing.T) {
		visible := "First paragraph is here.\nSecond paragraph"
		sent := visible
		delta, _ := nextStreamDelta(sent, visible, visible+"\nSecond paragraph continued.")
		if delta != "\nSecond paragraph continued." {
			t.Fatalf("delta = %q", delta)
		}
	})

	t.Run("trims a repeated complete line before new text", func(t *testing.T) {
		visible := "Line one is long enough.\nLine two is also long.\n"
		sent := visible
		snapshot := sent + "Line two is also long.\nLine three arrives.\n"
		delta, _ := nextStreamDelta(sent, visible, snapshot)
		if delta != "Line three arrives.\n" {
			t.Fatalf("delta = %q", delta)
		}
	})

	t.Run("does not swallow a legitimate short repeated line", func(t *testing.T) {
		visible := "func main() {\n}\n"
		sent := visible
		delta, _ := nextStreamDelta(sent, visible, sent+"}\n")
		if delta != "}\n" {
			t.Fatalf("a short closing line is not a re-render, got delta %q", delta)
		}
	})

	t.Run("does not suppress a repeated sentence start", func(t *testing.T) {
		visible := "Bonjour, comment allez-vous ?"
		delta, _ := nextStreamDelta(visible, visible, visible+"\nBonjour, comment allez-vous ?")
		if delta != "\nBonjour, comment allez-vous ?" {
			t.Fatalf("a legitimate repeated sentence must be emitted, got %q", delta)
		}
	})

	t.Run("recovers a continuation after a retraction onto visible text", func(t *testing.T) {
		// The watermark is ahead of the client because a replay was trimmed.
		visible := "Answer"
		sent := "Answer\nAnswer"
		delta, newSent := nextStreamDelta(sent, visible, "Answer and more")
		if delta != " and more" {
			t.Fatalf("continuation after retraction was lost, delta = %q", delta)
		}
		if newSent != "Answer and more" {
			t.Fatalf("upstream mark = %q", newSent)
		}
	})

	t.Run("suppresses a replayed paragraph that spans several lines", func(t *testing.T) {
		visible := "First line of the answer.\nSecond line of the answer.\n"
		sent := visible
		snapshot := sent + "First line of the answer.\nSecond line of the answer.\nThird line is new.\n"
		delta, _ := nextStreamDelta(sent, visible, snapshot)
		if delta != "Third line is new.\n" {
			t.Fatalf("delta = %q", delta)
		}
	})
}

func TestIsArtifactStreamText(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://googleusercontent.com/lmdx_content/abc123", true},
		{"https://googleusercontent.com/card_content/42", true},
		{"Read more at https://googleusercontent.com/card_content/42 for details", false},
		{"plain answer text", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isArtifactStreamText(c.in); got != c.want {
			t.Errorf("isArtifactStreamText(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
