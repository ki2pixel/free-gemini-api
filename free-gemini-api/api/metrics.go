package api

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"goapi/gemini"

	"github.com/gofiber/fiber/v3"
)

const (
	metricsContentType = "text/plain; version=0.0.4; charset=utf-8"
	// PublicMetricsEnv allows unauthenticated scrapes from loopback only.
	PublicMetricsEnv = "PUBLIC_METRICS"
)

var (
	httpRequestCounters   sync.Map // status string -> *atomic.Uint64
	tokenCounters         sync.Map // tier string -> *atomic.Uint64
	doomLoopInterventions sync.Map // signal string -> *atomic.Uint64
	arrangerRepairs       sync.Map // kind string -> *atomic.Uint64
	arrangerRejections    sync.Map // reason string -> *atomic.Uint64
	rateLimitDrops        atomic.Uint64
	refusalRetries        atomic.Uint64
)

// RecordHTTPRequest increments the HTTP status counter exposed on /metrics.
func RecordHTTPRequest(status int) {
	if status <= 0 {
		status = fiber.StatusInternalServerError
	}
	incrementCounter(&httpRequestCounters, strconv.Itoa(status), 1)
}

// RecordTokenUsage increments the token counter for an account tier.
func RecordTokenUsage(tier string, tokens int) {
	if tokens <= 0 {
		return
	}
	incrementCounter(&tokenCounters, normalizeMetricLabel(tier), uint64(tokens))
}

// RecordRateLimitDrop increments the limiter drop counter.
func RecordRateLimitDrop() {
	rateLimitDrops.Add(1)
}

// RecordDoomLoopIntervention increments the anti-loop counter for a signal
// ("repeat", "cycle" or "passive_read").
func RecordDoomLoopIntervention(signal string) {
	incrementCounter(&doomLoopInterventions, normalizeMetricLabel(signal), 1)
}

// RecordArrangerRepair increments the arranger repair counter for a kind
// ("control_chars", "brace_envelope" or "disclaimer").
func RecordArrangerRepair(kind string) {
	incrementCounter(&arrangerRepairs, normalizeMetricLabel(kind), 1)
}

// RecordArrangerRejection increments the arranger rejection counter for a
// reason ("mid_value_truncation", "schema_invalid" or "undeclared_tool").
func RecordArrangerRejection(reason string) {
	incrementCounter(&arrangerRejections, normalizeMetricLabel(reason), 1)
}

// RecordRefusalRetry counts a transparent conversation reset triggered by a
// false tool refusal.
func RecordRefusalRetry() {
	refusalRetries.Add(1)
}

func incrementCounter(store *sync.Map, key string, delta uint64) {
	counter, ok := store.Load(key)
	if !ok {
		counter, _ = store.LoadOrStore(key, new(atomic.Uint64))
	}
	counter.(*atomic.Uint64).Add(delta)
}

func counterValue(store *sync.Map, key string) uint64 {
	counter, ok := store.Load(key)
	if !ok {
		return 0
	}
	return counter.(*atomic.Uint64).Load()
}

func normalizeMetricLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	return value
}

func publicMetricsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(PublicMetricsEnv)), "true")
}

func publicMetricsAllowedForIP(ip string) bool {
	return publicMetricsEnabled() && isLoopbackHost(ip)
}

func publicMetricsRequestAllowed(c fiber.Ctx) bool {
	return publicMetricsAllowedForIP(c.IP())
}

// MetricsAuthMiddleware protects /metrics. When PUBLIC_METRICS=true, loopback
// scrapers are allowed without a token; everything else needs API_TOKEN (or an
// explicitly supplied extra token such as ADMIN_TOKEN).
func MetricsAuthMiddleware(apiToken string, extraTokens ...string) fiber.Handler {
	tokens := append([]string{apiToken}, extraTokens...)
	return func(c fiber.Ctx) error {
		if publicMetricsRequestAllowed(c) {
			return c.Next()
		}
		if !anyTokenMatches(extractAPIToken(c), tokens...) {
			log.Printf("🔒 metrics auth denied: ip=%s", c.IP())
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}

// MetricsMiddleware counts every HTTP response by status code. It is installed
// before authentication so rejected requests are visible too.
func MetricsMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()
		status := c.Response().StatusCode()
		if err != nil {
			var fiberErr *fiber.Error
			if errors.As(err, &fiberErr) {
				status = fiberErr.Code
			} else if status < 400 {
				status = fiber.StatusInternalServerError
			}
		}
		RecordHTTPRequest(status)
		return err
	}
}

// HandleMetrics renders the Prometheus text exposition format.
func HandleMetrics(c fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, metricsContentType)
	return c.Send(renderMetrics())
}

func renderMetrics() []byte {
	return renderMetricsWithStats(GetWorkerPool().GetStats())
}

func renderMetricsWithStats(stats WorkerPoolStats) []byte {
	var b strings.Builder

	writeHTTPRequestMetrics(&b)
	writeTokenMetrics(&b)

	b.WriteString("# HELP gemini_rate_limit_drops_total Requests rejected by the API rate limiter.\n")
	b.WriteString("# TYPE gemini_rate_limit_drops_total counter\n")
	fmt.Fprintf(&b, "gemini_rate_limit_drops_total %d\n", rateLimitDrops.Load())

	writeAgenticMetrics(&b)
	writeWorkerMetrics(&b, stats)
	writeCookieHealthMetric(&b)

	return []byte(b.String())
}

func writeAgenticMetrics(b *strings.Builder) {
	writeLabeledCounter(
		b,
		"gemini_doom_loop_interventions_total",
		"Doom-loop warnings and circuit breaks by signal.",
		"signal",
		&doomLoopInterventions,
		[]string{"repeat", "cycle", "passive_read"},
	)
	writeLabeledCounter(
		b,
		"gemini_arranger_repairs_total",
		"Tool-call repairs applied by the output arranger.",
		"kind",
		&arrangerRepairs,
		[]string{"control_chars", "brace_envelope", "disclaimer"},
	)
	writeLabeledCounter(
		b,
		"gemini_arranger_rejections_total",
		"Tool calls rejected by the arranger or the schema validator.",
		"reason",
		&arrangerRejections,
		[]string{"mid_value_truncation", "schema_invalid", "undeclared_tool"},
	)

	b.WriteString("# HELP gemini_refusal_retries_total Transparent conversation resets triggered by a false tool refusal.\n")
	b.WriteString("# TYPE gemini_refusal_retries_total counter\n")
	fmt.Fprintf(b, "gemini_refusal_retries_total %d\n", refusalRetries.Load())
}

// writeLabeledCounter renders a counter family, keeping the known label values
// visible at zero and appending any additional observed labels in sorted order.
func writeLabeledCounter(b *strings.Builder, name, help, label string, store *sync.Map, known []string) {
	b.WriteString("# HELP " + name + " " + help + "\n")
	b.WriteString("# TYPE " + name + " counter\n")

	seen := make(map[string]struct{}, len(known))
	for _, value := range known {
		seen[value] = struct{}{}
		fmt.Fprintf(b, "%s{%s=\"%s\"} %d\n", name, label, escapeLabelValue(value), counterValue(store, value))
	}

	var extra []string
	store.Range(func(key, _ any) bool {
		value := key.(string)
		if _, ok := seen[value]; !ok {
			extra = append(extra, value)
		}
		return true
	})
	sort.Strings(extra)
	for _, value := range extra {
		fmt.Fprintf(b, "%s{%s=\"%s\"} %d\n", name, label, escapeLabelValue(value), counterValue(store, value))
	}
}

func writeHTTPRequestMetrics(b *strings.Builder) {
	b.WriteString("# HELP gemini_http_requests_total Total HTTP requests by response status.\n")
	b.WriteString("# TYPE gemini_http_requests_total counter\n")

	var statuses []string
	httpRequestCounters.Range(func(key, _ any) bool {
		statuses = append(statuses, key.(string))
		return true
	})
	sort.Slice(statuses, func(i, j int) bool {
		left, leftErr := strconv.Atoi(statuses[i])
		right, rightErr := strconv.Atoi(statuses[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return statuses[i] < statuses[j]
	})
	for _, status := range statuses {
		fmt.Fprintf(b, "gemini_http_requests_total{status=\"%s\"} %d\n", escapeLabelValue(status), counterValue(&httpRequestCounters, status))
	}
}

func writeTokenMetrics(b *strings.Builder) {
	b.WriteString("# HELP gemini_tokens_total Tokens consumed per account tier.\n")
	b.WriteString("# TYPE gemini_tokens_total counter\n")

	var tiers []string
	tokenCounters.Range(func(key, _ any) bool {
		tiers = append(tiers, key.(string))
		return true
	})
	sort.Strings(tiers)
	for _, tier := range tiers {
		fmt.Fprintf(b, "gemini_tokens_total{tier=\"%s\"} %d\n", escapeLabelValue(tier), counterValue(&tokenCounters, tier))
	}
}

func writeWorkerMetrics(b *strings.Builder, stats WorkerPoolStats) {
	tierCounts := make(map[string]int)
	for _, worker := range stats.Workers {
		tierCounts[normalizeMetricLabel(worker.Tier)]++
	}
	// Keep the core tiers visible even before the first worker connects.
	for _, tier := range []string{"free", "subscription", "ultra"} {
		if _, ok := tierCounts[tier]; !ok {
			tierCounts[tier] = 0
		}
	}
	tiers := make([]string, 0, len(tierCounts))
	for tier := range tierCounts {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)

	b.WriteString("# HELP gemini_workers_total Number of workers per account tier.\n")
	b.WriteString("# TYPE gemini_workers_total gauge\n")
	for _, tier := range tiers {
		fmt.Fprintf(b, "gemini_workers_total{tier=\"%s\"} %d\n", escapeLabelValue(tier), tierCounts[tier])
	}

	workers := append([]WorkerStats(nil), stats.Workers...)
	sort.Slice(workers, func(i, j int) bool {
		if workers[i].ID == workers[j].ID {
			return workers[i].Tier < workers[j].Tier
		}
		return workers[i].ID < workers[j].ID
	})

	b.WriteString("# HELP gemini_worker_status Worker availability (1=active, 0=cooldown/inactive).\n")
	b.WriteString("# TYPE gemini_worker_status gauge\n")
	for _, worker := range workers {
		status := 0
		if worker.Status == "active" && worker.CooldownSecsRemaining == 0 {
			status = 1
		}
		fmt.Fprintf(b, "gemini_worker_status{id=\"%s\",tier=\"%s\"} %d\n", escapeLabelValue(worker.ID), escapeLabelValue(normalizeMetricLabel(worker.Tier)), status)
	}

	b.WriteString("# HELP gemini_worker_cooldown_seconds Remaining cooldown for each worker.\n")
	b.WriteString("# TYPE gemini_worker_cooldown_seconds gauge\n")
	for _, worker := range workers {
		fmt.Fprintf(b, "gemini_worker_cooldown_seconds{id=\"%s\"} %d\n", escapeLabelValue(worker.ID), worker.CooldownSecsRemaining)
	}

	b.WriteString("# HELP gemini_worker_inflight_requests In-flight requests per worker.\n")
	b.WriteString("# TYPE gemini_worker_inflight_requests gauge\n")
	for _, worker := range workers {
		fmt.Fprintf(b, "gemini_worker_inflight_requests{id=\"%s\"} %d\n", escapeLabelValue(worker.ID), worker.InFlight)
	}

	b.WriteString("# HELP gemini_worker_requests_total Successful requests served per worker.\n")
	b.WriteString("# TYPE gemini_worker_requests_total counter\n")
	for _, worker := range workers {
		fmt.Fprintf(b, "gemini_worker_requests_total{id=\"%s\"} %d\n", escapeLabelValue(worker.ID), worker.TotalServed)
	}

	b.WriteString("# HELP gemini_worker_errors_total Failed requests per worker.\n")
	b.WriteString("# TYPE gemini_worker_errors_total counter\n")
	for _, worker := range workers {
		fmt.Fprintf(b, "gemini_worker_errors_total{id=\"%s\"} %d\n", escapeLabelValue(worker.ID), worker.TotalErrors)
	}
}

func writeCookieHealthMetric(b *strings.Builder) {
	health := gemini.InspectAccountCookieHealth()
	value := 0
	if health.Status == "healthy" {
		value = 1
	}

	b.WriteString("# HELP gemini_cookie_health_status Cookie health (1=healthy, 0=warning/expired/unavailable).\n")
	b.WriteString("# TYPE gemini_cookie_health_status gauge\n")
	fmt.Fprintf(b, "gemini_cookie_health_status %d\n", value)
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}
