package gemini

import (
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
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

func (c *GeminiClient) UploadImage(imageBytes []byte, filename string, mimeType string) (string, error) {
	if err := c.ensureInit(); err != nil {
		return "", err
	}

	urlStr := geminiUploadURL
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(""))
	if err != nil {
		return "", err
	}

	req.Header.Set("x-goog-upload-protocol", "resumable")
	req.Header.Set("x-goog-upload-command", "start")
	req.Header.Set("x-tenant-id", "bard-storage")
	req.Header.Set("x-goog-upload-header-content-length", fmt.Sprintf("%d", len(imageBytes)))
	req.Header.Set("x-goog-upload-header-content-type", mimeType)
	req.Header.Set("push-id", "feeds/mcudyrk2a4khkz")
	req.Header.Set("sec-ch-ua", CurrentSecChUa)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", CurrentPlatform)
	req.Header.Set("User-Agent", CurrentChromeUA)
	req.Header.Set("Origin", geminiWebBaseURL)
	req.Header.Set("Referer", geminiWebBaseURL+"/")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("UploadImage start failed: Status %d", resp.StatusCode)
	}

	uploadURL := resp.Header.Get("X-Goog-Upload-Url")
	if uploadURL == "" {
		uploadURL = resp.Header.Get("x-goog-upload-url")
	}
	if uploadURL == "" {
		return "", fmt.Errorf("UploadImage start failed: missing x-goog-upload-url header")
	}

	req2, err := http.NewRequest("POST", uploadURL, strings.NewReader(string(imageBytes)))
	if err != nil {
		return "", err
	}

	req2.Header.Set("x-goog-upload-protocol", "resumable")
	req2.Header.Set("x-goog-upload-command", "upload, finalize")
	req2.Header.Set("x-goog-upload-offset", "0")
	req2.Header.Set("x-tenant-id", "bard-storage")
	req2.Header.Set("Content-Type", mimeType)
	req2.Header.Set("sec-ch-ua", CurrentSecChUa)
	req2.Header.Set("sec-ch-ua-mobile", "?0")
	req2.Header.Set("sec-ch-ua-platform", CurrentPlatform)
	req2.Header.Set("Origin", geminiWebBaseURL)
	req2.Header.Set("Referer", geminiWebBaseURL+"/")
	req2.Header.Set("User-Agent", CurrentChromeUA)

	resp2, err := c.client.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		return "", fmt.Errorf("UploadImage finalize failed: Status %d", resp2.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp2.Body)
	return string(bodyBytes), nil
}

// ImageInput holds data for a single image to upload
type ImageInput struct {
	Data     []byte
	Filename string
	MimeType string
}

func (c *GeminiClient) AskWithImage(prompt string, imageBytes []byte, filename string, mimeType string) (*GeminiResponse, error) {
	return c.AskWithImagesModel(prompt, []ImageInput{{Data: imageBytes, Filename: filename, MimeType: mimeType}}, "")
}

func (c *GeminiClient) AskWithImages(prompt string, images []ImageInput) (*GeminiResponse, error) {
	return c.AskWithImagesModel(prompt, images, "")
}

func (c *GeminiClient) AskWithImagesModel(prompt string, images []ImageInput, modelName string) (*GeminiResponse, error) {
	start := time.Now()

	if err := c.ensureInit(); err != nil {
		return nil, err
	}

	modelCfg := GetModelRegistry().Resolve(modelName)

	// Upload all images and build imageRef array
	var imageRef []interface{}
	uploadErr := c.executeWithRetry("UploadImages", func() error {
		imageRef = nil // Reset list for retry
		for i, img := range images {
			mediaRefPath, err := c.UploadImage(img.Data, img.Filename, img.MimeType)
			if err != nil {
				return err
			}
			mediaRefPath = strings.TrimSpace(mediaRefPath)
			log.Printf("📸 Image %d/%d uploaded: ref=[%s]", i+1, len(images), mediaRefPath)

			imageRef = append(imageRef, []interface{}{
				[]interface{}{mediaRefPath, 1, nil, img.MimeType},
				img.Filename,
				nil, nil, nil, nil, nil, nil,
				[]int{0},
			})
		}
		return nil
	})

	if uploadErr != nil {
		return nil, uploadErr
	}

	var response *GeminiResponse
	var err error
	execErr := c.executeWithRetry("AskWithImages", func() error {
		_, response, err = c.sendRequest(prompt, "", imageRef, modelCfg)
		if err != nil {
			return err
		}
		if response.ConversationID == "" && response.Text == "" {
			return fmt.Errorf("empty response received with images (possible session expiry)")
		}
		return nil
	})

	if execErr != nil {
		return nil, execErr
	}

	if response.ConversationID != "" {
		c.updateConvState(modelCfg.ID, response.ConversationID, response.ResponseID, response.ChoiceID)
	}

	// Auto-detect video generation and poll for video URL
	if response.ConversationID != "" &&
		(strings.Contains(response.Text, "video_gen_chip") || strings.Contains(response.Text, "generating your video") || strings.Contains(response.Text, "video is being generated")) {
		c.pollVideoURL(response)
	}

	response.Elapsed = time.Since(start).Seconds()
	return response, nil
}

// pollVideoURL polls hNvQHb RPC to find the video download URL
func (c *GeminiClient) pollVideoURL(response *GeminiResponse) {
	log.Println("🎬 Polling hNvQHb for video download URL...")
	vidRe := regexp.MustCompile(`https?://contribution\.usercontent\.google\.com/download\??[^"\\` + "`" + `\s]+`)

	delays := []time.Duration{2500 * time.Millisecond, 3000 * time.Millisecond, 4000 * time.Millisecond}
	for i := 0; i < 60; i++ {
		sleepDur := 5 * time.Second
		if i < len(delays) {
			sleepDur = delays[i]
		}
		time.Sleep(sleepDur)
		log.Printf("🎬 Video Poll %d/60 (waited %v)...", i+1, sleepDur)

		hPayload := fmt.Sprintf(`["%s",10,null,1,[1],[4],null,1]`, response.ConversationID)
		hBody, err := c.CallRPC("hNvQHb", hPayload)
		if err != nil {
			log.Printf("⚠️ hNvQHb error: %v", err)
			continue
		}

		hBody = strings.ReplaceAll(hBody, `\\u0026`, "&")
		hBody = strings.ReplaceAll(hBody, `\\u003d`, "=")
		hBody = strings.ReplaceAll(hBody, `\u0026`, "&")
		hBody = strings.ReplaceAll(hBody, `\u003d`, "=")

		if strings.Contains(hBody, "too many requests") || strings.Contains(hBody, "a lot of requests") ||
			strings.Contains(hBody, "try again later") || strings.Contains(hBody, "couldn't do that") ||
			strings.Contains(hBody, "can't create more videos") || strings.Contains(hBody, "can\\'t create more videos") ||
			strings.Contains(hBody, "I can't create") || strings.Contains(hBody, "find videos from the web") ||
			strings.Contains(hBody, "और ज़्यादा वीडियो") || strings.Contains(hBody, "नहीं जनरेट कर सकता") ||
			strings.Contains(hBody, "ढूँढ सकता हूँ") || strings.Contains(hBody, "limit") {
			log.Println("❌ Video generation refused (rate limit/quota exhausted)")
			response.Text = "Video generation failed: daily limit/quota exhausted."
			response.Videos = nil
			break
		}

		// Find ALL matched URLs
		matches := vidRe.FindAllString(hBody, -1)
		var chosenURL string
		for _, m := range matches {
			// Skip reference input videos uploaded by our server (which always contain "vid_")
			if strings.Contains(m, "vid_") {
				log.Printf("⏭️ Skipping matched reference video URL: %s", m[:min(len(m), 100)])
				continue
			}
			// Skip non-video assets like protobuf context files (ensure it ends with .mp4 or contains filename=video.mp4)
			if !strings.Contains(m, ".mp4") && !strings.Contains(m, "filename=video") {
				log.Printf("⏭️ Skipping matched non-mp4 asset URL: %s", m[:min(len(m), 100)])
				continue
			}
			chosenURL = m
			break
		}

		if chosenURL != "" {
			response.Videos = []string{chosenURL}
			log.Println("✅ Video URL found!")
			break
		}

		if strings.Contains(hBody, "Your video is ready") {
			log.Println("📝 Video is ready but URL not found in expected format, continuing...")
		}

		// Debug: log tail of response
		snippet := hBody
		if len(snippet) > 200 {
			snippet = hBody[len(hBody)-200:]
		}
		log.Printf("🔍 Poll response (%d chars): ...%s", len(hBody), snippet)
	}
}

const maxMediaBytes = 20 << 20 // 20 MiB

var allowedDownloadDomains = []string{
	"google.com",
	"googleusercontent.com",
	"gstatic.com",
	"googleapis.com",
	"googlevideo.com",
}

func isAllowedDownloadHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, suffix := range allowedDownloadDomains {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// sameCookieDomain limits cookie forwarding to HTTPS hops that stay on the
// same host or a subdomain. A redirect to an unrelated (even Google-owned)
// host never receives the account cookie jar.
func sameCookieDomain(from, to *url.URL) bool {
	if from == nil || to == nil || from.Scheme != "https" || to.Scheme != "https" {
		return false
	}
	fromHost := strings.ToLower(from.Hostname())
	toHost := strings.ToLower(to.Hostname())
	return toHost == fromHost || strings.HasSuffix(toHost, "."+fromHost)
}

func (c *GeminiClient) DownloadFile(urlStr, savePath string) error {
	// Build a separate tls-client that does NOT follow redirects automatically.
	// We follow redirects manually so we can inject Cookie header only on
	// trusted hops.
	jar := tls_client.NewCookieJar()
	dlClient, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(profiles.Chrome_152),
		tls_client.WithCookieJar(jar),
		tls_client.WithNotFollowRedirects(),
	)
	if err != nil {
		return fmt.Errorf("download client init failed: %w", err)
	}

	// Use the cookies of the worker client that produced the URL. The previous
	// implementation preferred a process-global cookie snapshot from the
	// extension bridge, which could belong to a different Google account.
	cookieStr := c.rawCookies()

	currentURL := urlStr
	var previous *url.URL
	for hops := 0; hops < 10; hops++ {
		current, err := url.Parse(currentURL)
		if err != nil {
			return fmt.Errorf("invalid download URL: %w", err)
		}
		if current.Scheme != "https" || !isAllowedDownloadHost(current.Hostname()) {
			return fmt.Errorf("download host not allowed: %s", current.Hostname())
		}

		req, err := http.NewRequest("GET", current.String(), nil)
		if err != nil {
			return err
		}

		req.Header[http.HeaderOrderKey] = chromeHeaderOrder
		req.Header[http.PHeaderOrderKey] = chromePseudoHeaderOrder

		req.Header.Set("User-Agent", CurrentChromeUA)
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
		req.Header.Set("sec-ch-ua", CurrentSecChUa)
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", CurrentPlatform)
		req.Header.Set("Referer", geminiWebBaseURL+"/")

		// The first hop may receive the account cookie jar. Redirect hops only
		// receive it when they remain on the same cookie domain.
		if cookieStr != "" && (previous == nil || sameCookieDomain(previous, current)) {
			req.Header.Set("Cookie", cookieStr)
		}

		resp, err := dlClient.Do(req)
		if err != nil {
			return fmt.Errorf("download request failed: %w", err)
		}

		// Handle redirects manually
		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return fmt.Errorf("redirect with no Location header (status %d)", resp.StatusCode)
			}

			next, err := current.Parse(location)
			if err != nil {
				return fmt.Errorf("invalid redirect target: %w", err)
			}
			if next.Scheme != "https" || !isAllowedDownloadHost(next.Hostname()) {
				return fmt.Errorf("redirect target not allowed: %s", next.Hostname())
			}

			log.Printf("🔀 Download redirect %d: %s → %s", resp.StatusCode, currentURL[:min(len(currentURL), 60)], next.String()[:min(len(next.String()), 80)])
			previous = current
			currentURL = next.String()
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("download failed: Status %d at %s", resp.StatusCode, currentURL[:min(len(currentURL), 80)])
		}

		// Success — write to a private temporary file. The caller renames it
		// into ./output after this function returns.
		dir := filepath.Dir(savePath)
		if err := os.MkdirAll(dir, 0700); err != nil {
			resp.Body.Close()
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}

		out, err := os.OpenFile(savePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			resp.Body.Close()
			return err
		}

		written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxMediaBytes+1))
		resp.Body.Close()
		closeErr := out.Close()
		if copyErr != nil {
			_ = os.Remove(savePath)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(savePath)
			return closeErr
		}
		if written > maxMediaBytes {
			_ = os.Remove(savePath)
			return fmt.Errorf("download exceeds %d byte limit", maxMediaBytes)
		}

		info, err := os.Stat(savePath)
		if err != nil {
			_ = os.Remove(savePath)
			return err
		}
		if info.Size() < 1000 {
			_ = os.Remove(savePath)
			return fmt.Errorf("file too small (%d bytes), likely failed download", info.Size())
		}

		return nil
	}

	return fmt.Errorf("too many redirects downloading %s", urlStr[:min(len(urlStr), 80)])
}
