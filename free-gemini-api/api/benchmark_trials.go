package api

import (
	"goapi/db"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// benchmarkTrialRequest is the verdict posted by scripts/benchmark_matrix.sh
// after the worktree oracle ran. Every string dimension is bounded so a shell
// or operator mistake surfaces as a 400 instead of a rejected CHECK deep in the
// writer.
type benchmarkTrialRequest struct {
	TrialID            string `json:"trial_id"`
	SuiteID            string `json:"suite_id"`
	TaskID             string `json:"task_id"`
	Stratum            string `json:"stratum"`
	RequestedModel     string `json:"requested_model"`
	EffectiveModel     string `json:"effective_model"`
	Paradigm           string `json:"paradigm"`
	ThinkingTier       string `json:"thinking_tier"`
	TrialIndex         int    `json:"trial_index"`
	PairID             string `json:"pair_id"`
	ArmOrder           string `json:"arm_order"`
	OraclePassed       bool   `json:"oracle_passed"`
	OracleMode         string `json:"oracle_mode"`
	OracleExitCode     int    `json:"oracle_exit_code"`
	WorktreeDiffFiles  int    `json:"worktree_diff_files"`
	TranscriptClean    bool   `json:"transcript_clean"`
	WallTimeMs         int64  `json:"wall_time_ms"`
	PrimaryFailureCode string `json:"primary_failure_code"`
	Notes              string `json:"notes"`
}

var (
	validBenchmarkParadigms   = map[string]struct{}{"chat": {}, "task": {}}
	validBenchmarkStrata      = map[string]struct{}{"": {}, "S1": {}, "S2": {}, "S3": {}}
	validBenchmarkArmOrders   = map[string]struct{}{"single": {}, "AB_first": {}, "AB_second": {}, "BA_first": {}, "BA_second": {}}
	validBenchmarkOracleModes = map[string]struct{}{"transcript": {}, "verify_sh": {}, "hybrid": {}}
	validBenchmarkFailures    = map[string]struct{}{
		"none": {}, "E_GREETING_LOOP": {}, "E_FALSE_QUOTA": {}, "E_UNBACKED_COMPLETION": {},
		"E_INCOMPLETE_PROSE": {}, "E_ERROR_FINISH": {}, "E_NO_TOOL_CALL": {},
		"E_ORACLE_ASSERTION_FAIL": {}, "E_STALL_OR_TIMEOUT": {}, "E_UPSTREAM_REFUSAL": {},
		"E_SCHEMA_INVALID": {}, "E_UNTAGGED_MUTATION": {}, "E_UNCLOSED_THOUGHT": {},
		"E_DOOM_LOOP_HALT": {},
	}
)

func validateBenchmarkTrialRequest(req *benchmarkTrialRequest) string {
	req.TrialID = sanitizeBenchmarkTrialID(req.TrialID)
	if req.TrialID == "" {
		return "trial_id is required and must match [A-Za-z0-9_.:-]{1,128}"
	}
	for name, value := range map[string]string{"suite_id": req.SuiteID, "task_id": req.TaskID} {
		if sanitizeBenchmarkTrialID(value) != strings.TrimSpace(value) || strings.TrimSpace(value) == "" {
			return name + " is required and must match [A-Za-z0-9_.:-]{1,128}"
		}
	}
	req.SuiteID = strings.TrimSpace(req.SuiteID)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.PairID = sanitizeBenchmarkTrialID(req.PairID)

	// Optional dimensions follow the same defaults as the DB layer, so a
	// compact payload (a minimal verdict) is accepted rather than bounced.
	if req.ArmOrder == "" {
		req.ArmOrder = "single"
	}
	if req.OracleMode == "" {
		req.OracleMode = "transcript"
	}
	if req.PrimaryFailureCode == "" {
		req.PrimaryFailureCode = "none"
	}

	if req.RequestedModel == "" || len(req.RequestedModel) > 128 {
		return "requested_model is required"
	}
	if _, ok := validBenchmarkParadigms[req.Paradigm]; !ok {
		return "paradigm must be chat or task"
	}
	if _, ok := validBenchmarkStrata[req.Stratum]; !ok {
		return "stratum must be empty, S1, S2 or S3"
	}
	if _, ok := validBenchmarkArmOrders[req.ArmOrder]; !ok {
		return "arm_order is invalid"
	}
	if _, ok := validBenchmarkOracleModes[req.OracleMode]; !ok {
		return "oracle_mode must be transcript, verify_sh or hybrid"
	}
	if _, ok := validBenchmarkFailures[req.PrimaryFailureCode]; !ok {
		return "primary_failure_code is not a known code"
	}
	switch req.ThinkingTier {
	case "", "none", "low", "medium", "high":
	default:
		return "thinking_tier must be none, low, medium or high"
	}
	if req.TrialIndex < 1 {
		return "trial_index must be >= 1"
	}
	if req.WorktreeDiffFiles < 0 || req.WallTimeMs < 0 {
		return "worktree_diff_files and wall_time_ms must be >= 0"
	}
	if len(req.Notes) > 4096 {
		return "notes is too long"
	}
	return ""
}

func (req benchmarkTrialRequest) toRecord() db.BenchmarkTrialRecord {
	return db.BenchmarkTrialRecord{
		TrialID:            req.TrialID,
		SuiteID:            req.SuiteID,
		TaskID:             req.TaskID,
		Stratum:            req.Stratum,
		RequestedModel:     req.RequestedModel,
		EffectiveModel:     req.EffectiveModel,
		Paradigm:           req.Paradigm,
		ThinkingTier:       req.ThinkingTier,
		TrialIndex:         req.TrialIndex,
		PairID:             req.PairID,
		ArmOrder:           req.ArmOrder,
		OraclePassed:       req.OraclePassed,
		OracleMode:         req.OracleMode,
		OracleExitCode:     req.OracleExitCode,
		WorktreeDiffFiles:  req.WorktreeDiffFiles,
		TranscriptClean:    req.TranscriptClean,
		WallTimeMs:         req.WallTimeMs,
		PrimaryFailureCode: req.PrimaryFailureCode,
		Notes:              req.Notes,
	}
}

// HandleRecordBenchmarkTrial persists one oracle verdict and returns the record
// enriched with the session, worker, tier and failure code resolved from the
// turn telemetry that carried the same trial id.
func HandleRecordBenchmarkTrial(c fiber.Ctx) error {
	var req benchmarkTrialRequest
	if err := c.Bind().JSON(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	if problem := validateBenchmarkTrialRequest(&req); problem != "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": problem})
	}

	// The verdict can arrive milliseconds after the stream ended, while the
	// telemetry worker is still committing the last turn. Wait for that tail,
	// bounded, so the correlation is not lost to a benign race.
	GetHistoryService().WaitTelemetryDrain(250 * time.Millisecond)

	saved, err := db.UpsertBenchmarkTrial(req.toRecord())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to persist benchmark trial"})
	}
	return c.JSON(saved)
}

// HandleListBenchmarkTrials returns the correlated trials and their
// Cost-of-Pass aggregates.
func HandleListBenchmarkTrials(c fiber.Ctx) error {
	suiteID := strings.TrimSpace(c.Query("suite_id"))
	taskID := strings.TrimSpace(c.Query("task_id"))
	limit, _ := strconv.Atoi(c.Query("limit"))

	trials, err := db.ListBenchmarkTrials(suiteID, taskID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read benchmark trials"})
	}
	cost, err := db.GetBenchmarkCostOfPass(suiteID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to read cost of pass"})
	}
	if trials == nil {
		trials = []db.BenchmarkTrialTelemetry{}
	}
	if cost == nil {
		cost = []db.BenchmarkCostOfPass{}
	}
	return c.JSON(fiber.Map{"trials": trials, "cost_of_pass": cost})
}
