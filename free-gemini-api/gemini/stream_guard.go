package gemini

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// stallGuardReader watches an upstream response body for total inactivity.
// Every successful read re-arms the timer. When the body produces no bytes for
// the configured timeout, the reader cancels the request context and closes the
// body so a blocked Read returns immediately with ErrUpstreamStreamStalled.
//
// This is deliberately stricter than the HTTP client timeout: a stalled
// Gemini web stream keeps the connection open without emitting a single byte,
// so a transport-only timeout would keep an agentic session frozen for its
// entire budget.
type stallGuardReader struct {
	body    io.ReadCloser
	cancel  func()
	timeout time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	stalled bool
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

func newStallGuardReader(body io.ReadCloser, cancel func(), timeout time.Duration) *stallGuardReader {
	if timeout <= 0 {
		timeout = upstreamStreamStallTimeout
	}
	reader := &stallGuardReader{
		body:    body,
		cancel:  cancel,
		timeout: timeout,
	}
	reader.timer = time.AfterFunc(timeout, reader.onStall)
	return reader
}

func (r *stallGuardReader) onStall() {
	r.mu.Lock()
	if r.closed || r.stalled {
		r.mu.Unlock()
		return
	}
	r.stalled = true
	r.mu.Unlock()

	if r.cancel != nil {
		r.cancel()
	}
	_ = r.closeBody()
}

func (r *stallGuardReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)

	if n > 0 {
		r.mu.Lock()
		if !r.stalled && !r.closed && r.timer != nil {
			r.timer.Reset(r.timeout)
		}
		r.mu.Unlock()
	}

	if err != nil {
		r.mu.Lock()
		stalled := r.stalled
		r.mu.Unlock()
		if stalled {
			return n, fmt.Errorf("%w (timeout %s)", ErrUpstreamStreamStalled, r.timeout)
		}
	}
	return n, err
}

func (r *stallGuardReader) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.timer != nil {
			r.timer.Stop()
		}
	}
	r.mu.Unlock()
	return r.closeBody()
}

func (r *stallGuardReader) closeBody() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.body.Close()
	})
	return r.closeErr
}
