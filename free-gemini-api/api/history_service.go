package api

import (
	"goapi/db"
	"log"
	"sync"
	"sync/atomic"
)

// HistoryService decouples request handlers from SQLite. Every write is
// enqueued on a bounded channel and consumed by a small, fixed set of workers.
// The previous implementation started one goroutine per request, which let a
// burst of chat traffic spawn an unbounded number of goroutines and serialise
// them all on the database mutex (F-23).
type HistoryService struct {
	msgQueue   chan messageLogItem
	reqQueue   chan requestLogItem
	mediaQueue chan mediaLogItem

	stopChan  chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu     sync.RWMutex
	closed bool

	dropped atomic.Uint64
}

type messageLogItem struct {
	conversationID string
	userID         string
	role           string
	content        string
	model          string
	tokens         int
	tier           string
	accountID      string
}

type requestLogItem struct {
	endpoint  string
	method    string
	userIP    string
	status    int
	elapsedMS float64
}

type mediaLogItem struct {
	mediaType      string
	conversationID string
	prompt         string
	fileName       string
	filePath       string
	url            string
	aspectRatio    string
	responseID     string
}

const defaultHistoryQueueSize = 4096

var (
	historyOnce    sync.Once
	globalHistory  *HistoryService
	historyQueueSz = defaultHistoryQueueSize
)

// GetHistoryService returns the process-wide history writer.
func GetHistoryService() *HistoryService {
	historyOnce.Do(func() {
		globalHistory = NewHistoryService(historyQueueSz)
	})
	return globalHistory
}

// NewHistoryService starts a bounded writer with one worker per queue.
func NewHistoryService(queueSize int) *HistoryService {
	if queueSize <= 0 {
		queueSize = defaultHistoryQueueSize
	}
	s := &HistoryService{
		msgQueue:   make(chan messageLogItem, queueSize),
		reqQueue:   make(chan requestLogItem, queueSize),
		mediaQueue: make(chan mediaLogItem, queueSize),
		stopChan:   make(chan struct{}),
	}
	s.wg.Add(3)
	go s.runMessageWorker()
	go s.runRequestWorker()
	go s.runMediaWorker()
	return s
}

func (s *HistoryService) enqueueMessage(item messageLogItem) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.msgQueue <- item:
	default:
		s.dropped.Add(1)
	}
}

func (s *HistoryService) enqueueRequest(item requestLogItem) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.reqQueue <- item:
	default:
		s.dropped.Add(1)
	}
}

func (s *HistoryService) enqueueMedia(item mediaLogItem) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.mediaQueue <- item:
	default:
		s.dropped.Add(1)
	}
}

// LogMessage enqueues one chat message for asynchronous persistence.
func (s *HistoryService) LogMessage(conversationID, userID, role, content, model string, tokens int, tier, accountID string) {
	s.enqueueMessage(messageLogItem{
		conversationID: conversationID,
		userID:         userID,
		role:           role,
		content:        content,
		model:          model,
		tokens:         tokens,
		tier:           tier,
		accountID:      accountID,
	})
}

// LogTurn records the user prompt and the assistant response in one call.
func (s *HistoryService) LogTurn(conversationID, userID, prompt, response, model string, promptTokens, responseTokens int, tier, accountID string) {
	RecordTokenUsage(tier, promptTokens+responseTokens)
	s.LogMessage(conversationID, userID, "user", prompt, model, promptTokens, tier, accountID)
	if response != "" {
		s.LogMessage(conversationID, userID, "assistant", response, model, responseTokens, tier, accountID)
	}
}

// LogRequest enqueues an HTTP request log entry.
func (s *HistoryService) LogRequest(endpoint, method, userIP string, status int, elapsedMS float64) {
	s.enqueueRequest(requestLogItem{
		endpoint:  endpoint,
		method:    method,
		userIP:    userIP,
		status:    status,
		elapsedMS: elapsedMS,
	})
}

// LogMedia enqueues a generated media row.
func (s *HistoryService) LogMedia(mediaType, conversationID, prompt, fileName, filePath, url, aspectRatio, responseID string) {
	s.enqueueMedia(mediaLogItem{
		mediaType:      mediaType,
		conversationID: conversationID,
		prompt:         prompt,
		fileName:       fileName,
		filePath:       filePath,
		url:            url,
		aspectRatio:    aspectRatio,
		responseID:     responseID,
	})
}

func (s *HistoryService) runMessageWorker() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.msgQueue:
			s.processMessage(item)
		case <-s.stopChan:
			for {
				select {
				case item := <-s.msgQueue:
					s.processMessage(item)
				default:
					return
				}
			}
		}
	}
}

func (s *HistoryService) runRequestWorker() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.reqQueue:
			s.processRequest(item)
		case <-s.stopChan:
			for {
				select {
				case item := <-s.reqQueue:
					s.processRequest(item)
				default:
					return
				}
			}
		}
	}
}

func (s *HistoryService) runMediaWorker() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.mediaQueue:
			s.processMedia(item)
		case <-s.stopChan:
			for {
				select {
				case item := <-s.mediaQueue:
					s.processMedia(item)
				default:
					return
				}
			}
		}
	}
}

func (s *HistoryService) processMessage(item messageLogItem) {
	if err := db.LogMessageWithTier(item.conversationID, item.userID, item.role, item.content, item.model, item.tokens, item.tier, item.accountID); err != nil {
		log.Printf("⚠️ history: failed to persist message: %v", err)
	}
}

func (s *HistoryService) processRequest(item requestLogItem) {
	if err := db.LogRequest(item.endpoint, item.method, item.userIP, item.status, item.elapsedMS); err != nil {
		log.Printf("⚠️ history: failed to persist request log: %v", err)
	}
	ScheduleAnalyticsExport()
}

func (s *HistoryService) processMedia(item mediaLogItem) {
	if err := db.LogMediaGenerationForConversation(item.conversationID, item.mediaType, item.prompt, item.fileName, item.filePath, item.url, item.aspectRatio, item.responseID); err != nil {
		log.Printf("⚠️ history: failed to persist media row: %v", err)
	}
}

// Dropped returns the number of records discarded because a queue was full or
// the service was closing.
func (s *HistoryService) Dropped() uint64 {
	return s.dropped.Load()
}

// FlushAndClose stops accepting work, drains every queue and waits for the
// workers to finish. It is safe to call more than once.
func (s *HistoryService) FlushAndClose() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stopChan)
		s.wg.Wait()
	})
}
