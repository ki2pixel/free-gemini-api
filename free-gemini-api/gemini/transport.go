package gemini

import (
	"fmt"
	"io"
	"log"
	stdhttp "net/http"
	"net/url"
	"runtime"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

var chromeHeaderOrder = []string{
	"host",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"sec-ch-ua-arch",
	"sec-ch-ua-bitness",
	"sec-ch-ua-full-version",
	"user-agent",
	"accept",
	"accept-encoding",
	"accept-language",
	"cookie",
	"content-type",
	"origin",
	"referer",
	"x-same-domain",
	"x-goog-ext-525001261-jspb",
	"x-goog-ext-525005358-jspb",
	"x-goog-ext-73010989-jspb",
	"priority",
}

// geminiWebBaseURL and geminiUploadURL are variables so tests can point the
// client at a local HTTP server. Production always uses the Google endpoints.
var (
	geminiWebBaseURL = "https://gemini.google.com"
	geminiUploadURL  = "https://push.clients6.google.com/upload/"
)

var chromePseudoHeaderOrder = []string{
	":method",
	":authority",
	":scheme",
	":path",
}

func getPlatformDetails() (ua string, platform string, secChUa string) {
	switch runtime.GOOS {
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36", `"Windows"`, `"Not(A:Brand";v="99", "Google Chrome";v="152", "Chromium";v="152"`
	case "linux":
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36", `"Linux"`, `"Not(A:Brand";v="99", "Google Chrome";v="152", "Chromium";v="152"`
	default: // macOS
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36", `"macOS"`, `"Not(A:Brand";v="99", "Google Chrome";v="152", "Chromium";v="152"`
	}
}

var (
	CurrentChromeUA, CurrentPlatform, CurrentSecChUa = getPlatformDetails()
)

func (c *GeminiClient) doStreamRequestQUIC(fullURL string, data url.Values, modelCfg *ModelConfig, onChunk func(text string)) (string, error) {
	req, err := stdhttp.NewRequest("POST", fullURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	if modelCfg == nil {
		modelCfg = GetModelRegistry().Resolve("")
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()))
	req.Header.Set("X-Same-Domain", "1")
	jspbVal := modelCfg.BuildHeaderValue()
	log.Printf("📡 [Request] model=%s jspb=%s", modelCfg.ID, jspbVal)
	req.Header.Set("x-goog-ext-525001261-jspb", jspbVal)
	req.Header.Set("x-goog-ext-525005358-jspb", `["DIRECT-API-SESSION",1]`)
	req.Header.Set("x-goog-ext-73010989-jspb", modelCfg.BuildModeHeaderValue())
	if rawCookies := c.rawCookies(); rawCookies != "" {
		req.Header.Set("Cookie", rawCookies)
	}

	resp, err := c.quicClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("QUIC status %d", resp.StatusCode)
	}

	return c.readAndProcessStream(resp.Body, onChunk)
}

func (c *GeminiClient) doStreamRequestHTTP2(fullURL string, data url.Values, modelCfg *ModelConfig, onChunk func(text string)) (string, error) {
	req, err := http.NewRequest("POST", fullURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	if modelCfg == nil {
		modelCfg = GetModelRegistry().Resolve("")
	}

	req.Header[http.HeaderOrderKey] = chromeHeaderOrder
	req.Header[http.PHeaderOrderKey] = chromePseudoHeaderOrder

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()))
	req.Header.Set("X-Same-Domain", "1")
	jspbVal2 := modelCfg.BuildHeaderValue()
	log.Printf("📡 [HTTP2-Request] model=%s jspb=%s", modelCfg.ID, jspbVal2)
	req.Header.Set("x-goog-ext-525001261-jspb", jspbVal2)
	req.Header.Set("x-goog-ext-525005358-jspb", `["DIRECT-API-SESSION",1]`)
	req.Header.Set("x-goog-ext-73010989-jspb", modelCfg.BuildModeHeaderValue())

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP2 status %d", resp.StatusCode)
	}

	return c.readAndProcessStream(resp.Body, onChunk)
}

func (c *GeminiClient) sendRequestQUIC(fullURL string, data url.Values, modelCfg *ModelConfig) (string, error) {
	req, err := stdhttp.NewRequest("POST", fullURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	if modelCfg == nil {
		modelCfg = GetModelRegistry().Resolve("")
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()))
	req.Header.Set("X-Same-Domain", "1")
	jspbVal := modelCfg.BuildHeaderValue()
	log.Printf("📡 [sendRequestQUIC] model=%s jspb=%s", modelCfg.ID, jspbVal)
	req.Header.Set("x-goog-ext-525001261-jspb", jspbVal)
	req.Header.Set("x-goog-ext-525005358-jspb", `["DIRECT-API-SESSION",1]`)
	req.Header.Set("x-goog-ext-73010989-jspb", modelCfg.BuildModeHeaderValue())
	if rawCookies := c.rawCookies(); rawCookies != "" {
		req.Header.Set("Cookie", rawCookies)
	}

	resp, err := c.quicClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("QUIC HTTP %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bodyBytes), nil
}

func (c *GeminiClient) sendRequestHTTP2(fullURL string, data url.Values, modelCfg *ModelConfig) (string, error) {
	req, err := http.NewRequest("POST", fullURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	if modelCfg == nil {
		modelCfg = GetModelRegistry().Resolve("")
	}

	req.Header[http.HeaderOrderKey] = chromeHeaderOrder
	req.Header[http.PHeaderOrderKey] = chromePseudoHeaderOrder

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s%s/app", geminiWebBaseURL, c.getAccountPrefix()))
	req.Header.Set("X-Same-Domain", "1")
	jspbVal := modelCfg.BuildHeaderValue()
	log.Printf("📡 [sendRequestHTTP2] model=%s jspb=%s", modelCfg.ID, jspbVal)
	req.Header.Set("x-goog-ext-525001261-jspb", jspbVal)
	req.Header.Set("x-goog-ext-525005358-jspb", `["DIRECT-API-SESSION",1]`)
	req.Header.Set("x-goog-ext-73010989-jspb", modelCfg.BuildModeHeaderValue())

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("❌ [sendRequestHTTP2] Failed with status %d: %s (Headers: %+v)", resp.StatusCode, string(bodyBytes[:min(len(bodyBytes), 300)]), resp.Header)
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bodyBytes), nil
}
