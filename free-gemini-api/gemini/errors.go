package gemini

import (
	"errors"
	"strings"
)

var (
	// ErrConversationExpired reports Google's upstream error 1097:
	// "Can't continue conversation. Start a new one or refresh the page."
	// The conversation pointer is unusable; the API layer must replay the
	// (masked) transcript on a fresh upstream conversation instead of asking
	// executeWithRetry to resynchronize cookies.
	ErrConversationExpired = errors.New("upstream conversation expired (1097): start a new conversation")

	// ErrUpstreamStreamStalled reports a stream that accepted the HTTP
	// response headers but then produced no body bytes for the inactivity
	// timeout. Resetting the conversation immediately is far cheaper than
	// waiting for the transport timeout.
	ErrUpstreamStreamStalled = errors.New("upstream stream stalled: no bytes received within the inactivity timeout")
)

// isConversationExpiredBody reports whether a raw Gemini web response carries
// the 1097 conversation-expiry marker. Detection requires the distinctive
// "start a new one or refresh the page" sentence (or the 1097 code together
// with "continue conversation"), so an ordinary answer that merely mentions
// the number 1097 is not misclassified.
func isConversationExpiredBody(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "new one or refresh the page") {
		return true
	}
	return strings.Contains(raw, "(1097)") && strings.Contains(lower, "continue conversation")
}
