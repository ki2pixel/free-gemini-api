package api

import (
	"context"
	"errors"
	"goapi/gemini"
	"io"
	"mime/multipart"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// errPromptTooLarge is returned by the decoder when a caller sends more than
// the configured prompt budget.
var errPromptTooLarge = errors.New("prompt exceeds 1 MiB limit")

// UnifiedChatInput is the decoded form of a /chat request, regardless of
// whether the caller used JSON or multipart/form-data.
type UnifiedChatInput struct {
	Prompt          string
	UserID          string
	Model           string
	ReasoningEffort string
	NewChat         bool
	Stream          bool
	Images          []gemini.ImageInput
}

// ChatService owns the coordination shared by the unified chat endpoint and
// the OpenAI-compatible endpoint: request decoding, model/tier resolution and
// asynchronous history bookkeeping.
type ChatService struct {
	pool    *WorkerPool
	history *HistoryService
}

var (
	chatServiceOnce sync.Once
	globalChatSvc   *ChatService
)

// GetChatService returns the process-wide chat coordinator.
func GetChatService() *ChatService {
	chatServiceOnce.Do(func() {
		globalChatSvc = &ChatService{
			pool:    GetWorkerPool(),
			history: GetHistoryService(),
		}
	})
	return globalChatSvc
}

// Pool returns the worker pool used by the service.
func (s *ChatService) Pool() *WorkerPool {
	return s.pool
}

// History returns the bounded history writer used by the service.
func (s *ChatService) History() *HistoryService {
	return s.history
}

// DecodeUnifiedChat parses the /chat payload. Multipart requests may attach
// one or more images under "images" or "image"; JSON requests use
// gemini.ChatRequest.
func (s *ChatService) DecodeUnifiedChat(c fiber.Ctx) (*UnifiedChatInput, error) {
	input := &UnifiedChatInput{}
	contentType := string(c.Request().Header.ContentType())

	if strings.Contains(contentType, "multipart/form-data") {
		input.Prompt = c.FormValue("prompt")
		input.UserID = c.FormValue("user_id")
		input.NewChat = c.FormValue("new_chat") == "true"
		input.Stream = c.FormValue("stream") == "true"
		input.Model = c.FormValue("model")
		input.ReasoningEffort = c.FormValue("reasoning_effort")

		form, err := c.MultipartForm()
		if err != nil {
			return nil, errors.New("invalid multipart form")
		}

		var fileHeaders []*multipart.FileHeader
		if fhs, ok := form.File["images"]; ok {
			fileHeaders = append(fileHeaders, fhs...)
		}
		if fhs, ok := form.File["image"]; ok {
			fileHeaders = append(fileHeaders, fhs...)
		}

		for _, fh := range fileHeaders {
			file, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				continue
			}
			input.Images = append(input.Images, gemini.ImageInput{
				Data:     data,
				Filename: fh.Filename,
				MimeType: uploadMimeType(fh),
			})
		}
	} else {
		var req gemini.ChatRequest
		if err := c.Bind().JSON(&req); err != nil {
			return nil, errors.New("invalid request")
		}
		input.Prompt = req.Prompt
		if input.Prompt == "" {
			input.Prompt = req.Message
		}
		input.UserID = req.UserID
		input.NewChat = req.NewChat
		input.Stream = req.Stream
		input.Model = req.Model
		input.ReasoningEffort = req.ReasoningEffort
	}

	if len(input.Prompt) > maxPromptBytes {
		return nil, errPromptTooLarge
	}
	return input, nil
}

func uploadMimeType(fh *multipart.FileHeader) string {
	mime := fh.Header.Get("Content-Type")
	if mime != "" {
		return mime
	}
	name := strings.ToLower(fh.Filename)
	switch {
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	case strings.HasSuffix(name, ".gif"):
		return "image/gif"
	case strings.HasSuffix(name, ".mp4"):
		return "video/mp4"
	default:
		return "image/jpeg"
	}
}

// ResolveModel resolves a requested model name and applies the task-mode
// override from the X-Gemini-Mode header, the mode query parameter or a
// -task model suffix.
func (s *ChatService) ResolveModel(c fiber.Ctx, requested string) (*gemini.ModelConfig, string, string) {
	modelCfg := gemini.GetModelRegistry().Resolve(requested)
	if strings.EqualFold(c.Get("X-Gemini-Mode"), "task") ||
		strings.EqualFold(c.Query("mode"), "task") ||
		strings.HasSuffix(strings.ToLower(requested), "-task") {
		cfgCopy := *modelCfg
		cfgCopy.AppMode = 40
		modelCfg = &cfgCopy
	}
	return modelCfg, modelCfg.ID, modelCfg.MinTier
}

// SessionID sanitises a caller-supplied identifier and falls back to the
// client IP when the value is empty.
func (s *ChatService) SessionID(c fiber.Ctx, raw string) string {
	return sanitizeSessionID(raw, c.IP())
}

// RequestDone returns the connection-closed channel of the underlying
// fasthttp request. It is captured before SendStreamWriter because the Fiber
// context is not valid once the handler returns.
func RequestDone(c fiber.Ctx) <-chan struct{} {
	return c.RequestCtx().Done()
}

// CancelWhenRequestDone cancels a stream context when the client disconnects.
// The goroutine exits as soon as either side is done.
func CancelWhenRequestDone(done <-chan struct{}, ctx context.Context, cancel context.CancelFunc) {
	if done == nil {
		return
	}
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
}

// StreamTimeout returns the timeout applied to long-lived SSE responses.
func StreamTimeout() time.Duration {
	return 15 * time.Minute
}
