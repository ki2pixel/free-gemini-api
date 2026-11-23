package gemini

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

type pacedReadCloser struct {
	chunks [][]byte
	delay  time.Duration
	index  int
}

func (p *pacedReadCloser) Read(dst []byte) (int, error) {
	if p.index >= len(p.chunks) {
		time.Sleep(p.delay)
		return 0, io.EOF
	}
	time.Sleep(p.delay)
	n := copy(dst, p.chunks[p.index])
	p.index++
	return n, nil
}

func (p *pacedReadCloser) Close() error { return nil }

func TestStallGuardReaderRejectsSilentStream(t *testing.T) {
	body := newBlockingReadCloser()
	guard := newStallGuardReader(body, nil, 40*time.Millisecond)
	defer guard.Close()

	start := time.Now()
	n, err := guard.Read(make([]byte, 16))
	elapsed := time.Since(start)

	if n != 0 {
		t.Fatalf("read returned %d bytes from a silent stream", n)
	}
	if !errors.Is(err, ErrUpstreamStreamStalled) {
		t.Fatalf("read error = %v, want ErrUpstreamStreamStalled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("stall detection took %s, want a responsive timeout", elapsed)
	}
}

func TestStallGuardReaderRearmsOnBytes(t *testing.T) {
	// Total exchange time exceeds the inactivity timeout, but every individual
	// gap is shorter, so the guard must not fire.
	body := &pacedReadCloser{
		chunks: [][]byte{[]byte("alpha\n"), []byte("beta\n"), []byte("gamma\n")},
		delay:  20 * time.Millisecond,
	}
	guard := newStallGuardReader(body, nil, 80*time.Millisecond)
	defer guard.Close()

	data, err := io.ReadAll(guard)
	if err != nil {
		t.Fatalf("paced stream was aborted: %v", err)
	}
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("data = %q", data)
	}
}

func TestStallGuardReaderCloseIsIdempotent(t *testing.T) {
	body := newBlockingReadCloser()
	guard := newStallGuardReader(body, nil, time.Minute)
	if err := guard.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := guard.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestIsConversationExpiredBody(t *testing.T) {
	expired := []string{
		`Can't continue conversation. Start a new one or refresh the page. (1097)`,
		`{"error":"Can’t continue conversation. Start a new one or refresh the page. (1097)"}`,
	}
	for _, raw := range expired {
		if !isConversationExpiredBody(raw) {
			t.Errorf("expected 1097 detection for %q", raw)
		}
	}

	healthy := []string{
		`[["wrb.fr",null,"[null,[\"c_1\",\"r_1\"],null,null,[[\"ch\",[\"Hello\"]]]]"]]`,
		"The year 1097 was part of the First Crusade.",
		"Continue conversation with the next tool call.",
		"Tool exited with code 1097 (1097).",
	}
	for _, raw := range healthy {
		if isConversationExpiredBody(raw) {
			t.Errorf("false 1097 detection for %q", raw)
		}
	}
}

func TestExecuteWithRetrySkipsCookieResyncForFreshConversationErrors(t *testing.T) {
	client := &GeminiClient{}
	for _, sentinel := range []error{ErrConversationExpired, ErrUpstreamStreamStalled} {
		calls := 0
		err := client.executeWithRetry("test", func() error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want %v", err, sentinel)
		}
		if calls != 1 {
			t.Fatalf("%v triggered %d runFunc calls, want 1 (no cookie resync retry)", sentinel, calls)
		}
	}
}
