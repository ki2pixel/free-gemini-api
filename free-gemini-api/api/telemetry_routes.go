package api

import (
	"goapi/db"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// HandleTelemetrySessions exposes the aggregated session telemetry plus the
// recent spans and anomalies of one session. Only pseudonymous identifiers and
// categorical labels are returned: no prompt, reasoning body or tool argument.
func HandleTelemetrySessions(c fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 50
	}

	sessionID := strings.TrimSpace(c.Query("session_id"))
	trialFilter := strings.TrimSpace(c.Query("trial_id"))
	if sessionID == "" && trialFilter != "" {
		// An explicit trial filter must never degrade into "every session":
		// a miss or an invalid id is an empty result, not a silent wildcard.
		correlation, err := db.ResolveSessionForTrial(sanitizeBenchmarkTrialID(trialFilter))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to resolve trial"})
		}
		if correlation.SessionID == "" {
			return c.JSON(fiber.Map{
				"session_id": "",
				"sessions":   []db.SessionTelemetrySummary{},
				"spans":      []db.ToolCallSpanRecord{},
				"anomalies":  []db.TurnAnomalyRecord{},
			})
		}
		sessionID = correlation.SessionID
	}

	if sessionID == "" {
		summaries, err := db.ListSessionTelemetrySummaries(limit)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read sessions"})
		}
		if summaries == nil {
			summaries = []db.SessionTelemetrySummary{}
		}
		return c.JSON(fiber.Map{"sessions": summaries})
	}

	summary, err := db.GetSessionTelemetrySummary(sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read session"})
	}
	spans, err := db.ListToolSpans(sessionID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read tool spans"})
	}
	anomalies, err := db.ListTurnAnomalies(sessionID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read anomalies"})
	}
	if spans == nil {
		spans = []db.ToolCallSpanRecord{}
	}
	if anomalies == nil {
		anomalies = []db.TurnAnomalyRecord{}
	}

	sessions := []db.SessionTelemetrySummary{}
	if summary.SessionID != "" {
		sessions = append(sessions, summary)
	}
	return c.JSON(fiber.Map{
		"session_id": sessionID,
		"sessions":   sessions,
		"spans":      spans,
		"anomalies":  anomalies,
	})
}
