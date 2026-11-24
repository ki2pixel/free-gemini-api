package gemini

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"math/big"
	stdhttp "net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/publicsuffix"
)

// Upstream HTTP exchange budgets. The former 900-second timeout let a silent
// upstream stall freeze an agentic request for 15 minutes. The request is now
// bounded to two minutes, and stream_guard.go aborts as soon as the body stops
// producing bytes for 45 seconds so the proxy can reset the conversation and
// retry on a fresh session.
const (
	upstreamRequestTimeout     = 120 * time.Second
	upstreamStreamStallTimeout = 45 * time.Second
)

type GeminiClient struct {
	client         tls_client.HttpClient
	quicClient     *stdhttp.Client
	useQUIC        atomic.Bool
	cookiesFile    string
	SNlM0e         string
	FSID           string
	BL             string
	ReqID          int
	reqMu          sync.Mutex
	ConversationID string
	ResponseID     string
	ChoiceID       string
	initialized    atomic.Bool
	initMu         sync.Mutex
	sessionMu      sync.RWMutex
	RawCookies     string
	SessionSavedAt int64
	AccountTier    string // "free" or "subscription"
	AccountPrefix  string // e.g. "/u/4", resolved eagerly in NewClient
	convStates     map[string]*ModelConversationState
	convMu         sync.Mutex
	quicTransport  *http3.Transport
}

// resolveAccountPrefix reads the account routing configuration once, during
// client construction. Keeping AccountPrefix immutable afterwards removes the

func resolveAccountPrefix() string {
	if p := strings.TrimSpace(os.Getenv("GEMINI_ACCOUNT_PREFIX")); p != "" {
		return p
	}
	if idx := strings.TrimSpace(os.Getenv("GEMINI_ACCOUNT_INDEX")); idx != "" {
		if strings.HasPrefix(idx, "/u/") {
			return idx
		}
		return "/u/" + idx
	}
	return ""
}

func (c *GeminiClient) getAccountPrefix() string {
	return c.AccountPrefix
}

func (c *GeminiClient) nextReqID() string {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	c.ReqID += 100
	return strconv.Itoa(c.ReqID)
}

// IsInitialized reports whether the upstream session tokens are ready.
func (c *GeminiClient) IsInitialized() bool {
	return c.initialized.Load()
}

// GetAccountTier returns the detected account tier under the session lock.
func (c *GeminiClient) GetAccountTier() string {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.AccountTier
}

func (c *GeminiClient) setAccountTier(tier string) {
	c.sessionMu.Lock()
	c.AccountTier = tier
	c.sessionMu.Unlock()
}

func (c *GeminiClient) sessionTokens() (string, string, string) {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.SNlM0e, c.FSID, c.BL
}

func (c *GeminiClient) setSessionTokens(snlM0e, fsid, bl string) {
	c.sessionMu.Lock()
	c.SNlM0e = snlM0e
	c.FSID = fsid
	c.BL = bl
	c.sessionMu.Unlock()
}

func (c *GeminiClient) rawCookies() string {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.RawCookies
}

func (c *GeminiClient) setRawCookies(raw string) {
	c.sessionMu.Lock()
	c.RawCookies = raw
	c.sessionMu.Unlock()
}

// secureReqID returns a cryptographically random request id. The upstream
// protocol only requires an unpredictable numeric value; using crypto/rand
// removes the weak-RNG finding (G404) without changing the wire format.
func secureReqID() int {
	n, err := rand.Int(rand.Reader, big.NewInt(9000000))
	if err != nil {
		return int(time.Now().UnixNano()%9000000) + 1000000
	}
	return int(n.Int64()) + 1000000
}

func NewClient(cookiesFile string) (*GeminiClient, error) {
	// tls_client.NewCookieJar() is deliberately avoided: it buckets cookies by
	// eTLD+1 ("google.com" for every Google host) and de-duplicates by cookie
	// NAME alone. With a Chrome export that carries OSID/__Secure-OSID for
	// gemini.google.com, myaccount.google.com and console.cloud.google.com, the
	// last subdomain in the file silently replaced the Gemini value. The
	// RFC 6265 jar below keeps every (domain, path, name) entry distinct and
	// only sends a cookie to hosts that domain-match it.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(int(upstreamRequestTimeout / time.Second)),
		tls_client.WithClientProfile(profiles.Chrome_152),
		tls_client.WithCookieJar(jar),
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}

	// Initialize HTTP/3 QUIC transport with 0-RTT session resumption
	quicTransport := &http3.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS13,
			ClientSessionCache: tls.NewLRUClientSessionCache(32),
		},
		QUICConfig: &quic.Config{
			MaxIdleTimeout:  30 * time.Second,
			KeepAlivePeriod: 10 * time.Second,
		},
	}
	quicClient := &stdhttp.Client{
		Timeout:   upstreamRequestTimeout,
		Transport: quicTransport,
	}

	c := &GeminiClient{
		client:        client,
		quicClient:    quicClient,
		quicTransport: quicTransport,
		cookiesFile:   cookiesFile,
		ReqID:         secureReqID(),
		convStates:    make(map[string]*ModelConversationState),
		AccountTier:   "free",
		AccountPrefix: resolveAccountPrefix(),
	}

	if err := c.loadCookies(); err != nil {
		return nil, fmt.Errorf("failed to load cookies from %s: %w", cookiesFile, err)
	}

	return c, nil
}
