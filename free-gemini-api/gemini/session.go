package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type SavedSessionData struct {
	SNlM0e         string `json:"snlm0e"`
	FSID           string `json:"fsid"`
	BL             string `json:"bl"`
	SavedAt        int64  `json:"saved_at"`
	RawCookiesHash string `json:"raw_cookies_hash"`
	AccountTier    string `json:"account_tier,omitempty"`
}

type CookieObject struct {
	Domain         string  `json:"domain"`
	ExpirationDate float64 `json:"expirationDate,omitempty"`
	HostOnly       bool    `json:"hostOnly,omitempty"`
	HttpOnly       bool    `json:"httpOnly,omitempty"`
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	SameSite       string  `json:"sameSite,omitempty"`
	Secure         bool    `json:"secure,omitempty"`
	Session        bool    `json:"session,omitempty"`
	StoreId        string  `json:"storeId,omitempty"`
	Value          string  `json:"value"`
}

func (c *GeminiClient) loadCookies() error {
	if c.cookiesFile == "" || c.cookiesFile == "guest" {
		log.Println("🌐 [GeminiClient] Running in Guest/Anonymous mode (no cookies required).")
		c.setAccountTier("free")
		return nil
	}
	data, err := readCookieFile(c.cookiesFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("ℹ️ [GeminiClient] Cookie file %s not found, falling back to Guest mode.", c.cookiesFile)
			c.setAccountTier("free")
			return nil
		}
		return err
	}

	// JSON Array Format
	var cookieList []CookieObject
	if err := json.Unmarshal(data, &cookieList); err == nil && len(cookieList) > 0 {
		log.Printf("Loading cookies in JSON array format (%d cookies)", len(cookieList))
		jar := c.client.GetCookieJar()
		for _, ck := range cookieList {
			origin, cookie, ok := scopedCookie(ck)
			if !ok {
				log.Printf("⚠️ [GeminiClient] Skipping invalid cookie %q (domain %q)", ck.Name, ck.Domain)
				continue
			}
			jar.SetCookies(origin, []*http.Cookie{cookie})
		}
		c.setRawCookies(cookieHeaderForHost(jar, geminiCookieHost))
		return nil
	}

	// Fallback: Legacy Object Format
	var cookieData struct {
		Cookies   string  `json:"cookies"`
		UpdatedAt float64 `json:"updated_at"`
	}

	if err := json.Unmarshal(data, &cookieData); err == nil && cookieData.Cookies != "" {
		log.Printf("Loading cookies in legacy string format")
		u, err := url.Parse("https://" + geminiCookieHost)
		if err != nil {
			return fmt.Errorf("invalid Gemini cookie origin: %w", err)
		}
		jar := c.client.GetCookieJar()
		jar.SetCookies(u, parseCookieString(cookieData.Cookies, ".google.com"))
		c.setRawCookies(cookieHeaderForHost(jar, geminiCookieHost))
		return nil
	}

	return fmt.Errorf("unsupported cookie format in %s", c.cookiesFile)
}

// geminiCookieHost is the origin the web client authenticates against. Every
// Cookie header and jar query is scoped to it.
const geminiCookieHost = "gemini.google.com"

// scopedCookie maps a Chrome-exported cookie to the URL of the host that owns
// it. The cookie is registered against that exact origin and its Domain
// attribute is preserved only for domain cookies, so sibling Google
// subdomains (console.cloud.google.com, myaccount.google.com, ...) can never
// overwrite a gemini.google.com cookie of the same name.
func scopedCookie(ck CookieObject) (*url.URL, *http.Cookie, bool) {
	rawDomain := strings.TrimSpace(ck.Domain)
	host := strings.ToLower(strings.TrimPrefix(rawDomain, "."))
	if ck.Name == "" || strings.ContainsAny(host, "/?#@") {
		return nil, nil, false
	}
	// A file without any Domain attribute predates the extension export. Such
	// cookies used to be concatenated into the request header verbatim, so they
	// keep the historical .google.com scope instead of being dropped.
	domainCookie := rawDomain == "" || !ck.HostOnly || strings.HasPrefix(rawDomain, ".")
	if host == "" {
		host = "google.com"
	}
	origin, err := url.Parse("https://" + host)
	if err != nil || origin.Host == "" {
		return nil, nil, false
	}

	cookie := &http.Cookie{
		Name:   ck.Name,
		Value:  ck.Value,
		Path:   ck.Path,
		Secure: ck.Secure,
	}
	if cookie.Path == "" || !strings.HasPrefix(cookie.Path, "/") {
		cookie.Path = "/"
	}
	// A host-only cookie must be stored without a Domain attribute so the RFC
	// 6265 jar only sends it to that exact host.
	if domainCookie {
		cookie.Domain = host
	}
	if ck.ExpirationDate > 0 {
		cookie.Expires = time.Unix(int64(ck.ExpirationDate), 0)
	}
	return origin, cookie, true
}

// cookieHeaderFromJar renders the Cookie header a browser would send to u,
// using only the jar entries whose domain and path match it.
func cookieHeaderFromJar(jar http.CookieJar, u *url.URL) string {
	if jar == nil || u == nil {
		return ""
	}
	var parts []string
	for _, cookie := range jar.Cookies(u) {
		if cookie.Name == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

// cookieHeaderForHost is cookieHeaderFromJar for a bare host name.
func cookieHeaderForHost(jar http.CookieJar, host string) string {
	if host == "" {
		return ""
	}
	u, err := url.Parse("https://" + host + "/")
	if err != nil {
		return ""
	}
	return cookieHeaderFromJar(jar, u)
}

// cookieHeaderForURL renders the Cookie header applicable to an arbitrary
// request URL (used for media downloads that may leave the Gemini origin).
func (c *GeminiClient) cookieHeaderForURL(u *url.URL) string {
	if c == nil || c.client == nil {
		return ""
	}
	return cookieHeaderFromJar(c.client.GetCookieJar(), u)
}

// cookieHeader returns the Cookie header for the Gemini web origin, resolved
// live from the jar. Using the jar instead of the rawCookies snapshot means
// cookies rotated by an /app response (__Secure-1PSIDTS, __Secure-3PSIDTS) are
// sent on the very next request, and cookies belonging to other Google
// subdomains are never included.
func (c *GeminiClient) cookieHeader() string {
	if c == nil || c.client == nil {
		return ""
	}
	return cookieHeaderForHost(c.client.GetCookieJar(), geminiCookieHost)
}

func parseCookieString(raw, domain string) []*http.Cookie {
	var cookies []*http.Cookie
	parts := strings.Split(raw, "; ")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			cookies = append(cookies, &http.Cookie{
				Name:   strings.TrimSpace(kv[0]),
				Value:  strings.TrimSpace(kv[1]),
				Domain: domain,
				Path:   "/",
			})
		}
	}
	return cookies
}

func (c *GeminiClient) getSessionCachePath() string {
	if c.cookiesFile == "" || c.cookiesFile == "guest" {
		return ""
	}
	base := filepath.Base(c.cookiesFile)
	cleanName := strings.TrimSuffix(base, filepath.Ext(base))
	pfx := strings.ReplaceAll(c.getAccountPrefix(), "/", "_")
	return filepath.Join("cookies", fmt.Sprintf("session_%s%s.json", cleanName, pfx))
}

func (c *GeminiClient) hashCookies() string {
	h := sha256.Sum256([]byte(c.rawCookies()))
	return hex.EncodeToString(h[:])
}

func (c *GeminiClient) loadCachedSession() bool {
	cachePath := c.getSessionCachePath()
	if cachePath == "" {
		return false
	}
	data, err := readCookieFile(cachePath)
	if err != nil {
		return false
	}
	var cached SavedSessionData
	if err := json.Unmarshal(data, &cached); err != nil {
		return false
	}

	// Valid for 18 hours and requires matching cookies hash
	if cached.SNlM0e != "" && cached.RawCookiesHash == c.hashCookies() &&
		time.Since(time.Unix(cached.SavedAt, 0)) < 18*time.Hour {
		c.setSessionTokens(cached.SNlM0e, cached.FSID, cached.BL)
		c.SessionSavedAt = cached.SavedAt
		if cached.AccountTier != "" {
			c.setAccountTier(cached.AccountTier)
		} else {
			c.setAccountTier(DetectAccountTier("", c.cookiesFile))
		}
		c.initialized.Store(true)
		log.Printf("⚡ [Zero-Wait Instant Start] Loaded cached session for %s (Tier: %s, 0s bootstrap!)", cachePath, c.GetAccountTier())
		return true
	}
	return false
}

func (c *GeminiClient) persistSession() {
	snlM0e, fsid, bl := c.sessionTokens()
	if snlM0e == "" || c.cookiesFile == "" || c.cookiesFile == "guest" {
		return
	}
	cachePath := c.getSessionCachePath()
	tier := c.GetAccountTier()
	saved := SavedSessionData{
		SNlM0e:         snlM0e,
		FSID:           fsid,
		BL:             bl,
		SavedAt:        time.Now().Unix(),
		RawCookiesHash: c.hashCookies(),
		AccountTier:    tier,
	}
	if data, err := json.MarshalIndent(saved, "", "  "); err == nil {
		_ = writeCookieFileAtomic(cachePath, data, 0600)
		log.Printf("💾 Persisted fresh session tokens to %s (valid for 18h, Tier: %s)", cachePath, tier)
	}
}

func (c *GeminiClient) InitSession() error {
	if c.cookiesFile == "" || c.cookiesFile == "guest" {
		c.setAccountTier("free")
		c.initialized.Store(true)
		log.Println("🌐 [GeminiClient] Initialized in Guest / Anonymous Mode (no login required for Free tier models).")
		return nil
	}

	// Step 1: Check instant session cache (0 roundtrips)
	if c.loadCachedSession() {
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		req, err := http.NewRequest("GET", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()), nil)
		if err != nil {
			return err
		}

		req.Header.Set("User-Agent", CurrentChromeUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
		req.Header.Set("sec-ch-ua", CurrentSecChUa)
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", CurrentPlatform)
		req.Header.Set("Referer", fmt.Sprintf("%s%s/", geminiWebBaseURL, c.getAccountPrefix()))

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(1 * time.Second)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gemini app failed: status %d (check cookies)", resp.StatusCode)
			time.Sleep(1 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		bodyStr := string(body)
		log.Printf("InitSession attempt %d: status %d, body length %d, snippet: %s", attempt, resp.StatusCode, len(bodyStr), func() string {
			if len(bodyStr) > 300 {
				return bodyStr[:300]
			}
			return bodyStr
		}())

		snlm0eRe := regexp.MustCompile(`"(?:SNlM0e|thykhd)":"(.*?)"`)
		fsidRe := regexp.MustCompile(`"FdrFJe":"(.*?)"`)
		cfb2hRe := regexp.MustCompile(`"cfb2h":"(.*?)"`)

		var snlM0e string
		if m := snlm0eRe.FindStringSubmatch(bodyStr); len(m) > 1 {
			snlM0e = m[1]
		} else if m := regexp.MustCompile(`"(AFWLb[A-Za-z0-9_-]{80,})"`).FindStringSubmatch(bodyStr); len(m) > 1 {
			snlM0e = m[1]
		}

		if snlM0e != "" {
			fsid := ""
			if fm := fsidRe.FindStringSubmatch(bodyStr); len(fm) > 1 {
				fsid = fm[1]
			}
			bl := ""
			if cm := cfb2hRe.FindStringSubmatch(bodyStr); len(cm) > 1 {
				bl = cm[1]
			}
			c.setSessionTokens(snlM0e, fsid, bl)
			log.Printf("Session Initialized (attempt %d). Token size: %d", attempt, len(snlM0e))
			c.initialized.Store(true)
			c.setAccountTier(DetectAccountTier(bodyStr, c.cookiesFile))
			if discovered := DiscoverModelsFromHTML(bodyStr); len(discovered) > 0 {
				reg := GetModelRegistry()
				for _, dm := range discovered {
					reg.Register(dm)
				}
			}
			c.persistSession()
			return nil
		}

		if strings.Contains(bodyStr, "ServiceLogin") || strings.Contains(bodyStr, "login.google.com") {
			if attempt == 1 {
				log.Println("⚠️ Session expired detected. Requesting Chrome Extension to proactively rotate cookies...")
				BroadcastCookieRefresh()
				time.Sleep(5 * time.Second)
				_ = c.loadCookies()
				continue
			}
			return fmt.Errorf("session expired: Google redirected to login. Please refresh cookies")
		}
		lastErr = fmt.Errorf("SNlM0e not found - Google might have changed UI or blocked the request")
	}

	return lastErr
}

func (c *GeminiClient) ensureInit() error {
	if c.initialized.Load() {
		return nil
	}

	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.initialized.Load() {
		return nil
	}
	if err := c.InitSession(); err != nil {
		return err
	}
	c.initialized.Store(true)
	return nil
}

// reinitialize forces a single serialized session bootstrap after a request
// failure. Concurrent callers wait for the same refresh instead of racing to
// overwrite SNlM0e/FSID/BL.
func (c *GeminiClient) reinitialize() error {
	c.initMu.Lock()
	defer c.initMu.Unlock()
	c.initialized.Store(false)
	_ = os.Remove(c.getSessionCachePath())
	if err := c.InitSession(); err != nil {
		return err
	}
	c.initialized.Store(true)
	return nil
}

// executeWithRetry executes a request function. If it fails, it refreshes session and retries.
func (c *GeminiClient) executeWithRetry(actionName string, runFunc func() error) error {
	err := runFunc()
	if err == nil {
		return nil
	}

	// An expired conversation (1097) or a silent stream stall is not a cookie
	// problem: refreshing the session would keep the same poisoned conversation
	// pointer and waste another full timeout. Return immediately so the API
	// layer resets the conversation and replays the masked transcript on a
	// genuinely fresh upstream session.
	if errors.Is(err, ErrConversationExpired) || errors.Is(err, ErrUpstreamStreamStalled) {
		log.Printf("⚠️ [%s] Upstream requires a fresh conversation (%v); skipping cookie resync", actionName, err)
		return err
	}

	log.Printf("⚠️ [%s] Request failed: %v. Triggering automatic cookie sync and session refresh...", actionName, err)

	// reinitialize serializes the recovery so concurrent failing requests do
	// not race to bootstrap the same client.
	if initErr := c.reinitialize(); initErr != nil {
		log.Printf("❌ [%s] Session recovery failed: %v", actionName, initErr)
		return fmt.Errorf("%s failed and auto-heal session recovery failed: %w", actionName, initErr)
	}

	log.Printf("🔄 [%s] Session successfully auto-healed! Retrying request...", actionName)
	return runFunc()
}

// ReloadSession re-reads the cookies file and marks the session to be
// re-initialized lazily on the next request.
func (c *GeminiClient) ReloadSession() error {
	c.initMu.Lock()
	defer c.initMu.Unlock()

	if err := c.loadCookies(); err != nil {
		return fmt.Errorf("failed to reload cookies: %v", err)
	}

	// Mark as uninitialized and invalidate session cache so the next request gets fresh tokens
	c.initialized.Store(false)
	_ = os.Remove(c.getSessionCachePath())
	log.Println("♻️  Cookies reloaded in memory. Session marked for lazy re-initialization.")
	return nil
}

// Close releases idle transport resources held by the client. It is invoked
// when a session is evicted from the bounded LRU cache.
func (c *GeminiClient) Close() {
	if c == nil {
		return
	}
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
	if c.quicTransport != nil {
		_ = c.quicTransport.Close()
	}
	if c.quicClient != nil {
		c.quicClient.CloseIdleConnections()
	}
}
