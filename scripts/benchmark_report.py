#!/usr/bin/env python3
"""Read-only report for the benchmark matrix and its correlated telemetry.

Sources:
  --tsv PATH    results.tsv produced by scripts/benchmark_matrix.sh
  --db PATH     SQLite database written by the proxy (default:
                free-gemini-api/data/gemini.db)
  --suite ID    restrict both sources to one suite_id

The report prints:
  1. the raw success rate (all primary_failure_code values) and the
     conditional rate excluding layer A (stall/timeout, upstream refusal,
     incomplete prose);
  2. the paired AB/BA difference (Task - Chat) per shared pair_id, with its
     cluster standard error;
  3. SQLite aggregates per (suite, task, paradigm, thinking_tier): Cost-of-Pass,
     median/mean tool roundtrip, anomaly categories and the masked observation
     bytes saved.

The database is opened with mode=ro: this utility never mutates telemetry.
"""

from __future__ import annotations

import argparse
import csv
import math
import sqlite3
import statistics
import sys
from collections import defaultdict
from pathlib import Path

LAYER_A_FAILURES = {
    "E_STALL_OR_TIMEOUT",
    "E_UPSTREAM_REFUSAL",
    "E_INCOMPLETE_PROSE",
}

TSV_REQUIRED_COLUMNS = {
    "model",
    "result",
    "primary_failure_code",
    "suite_id",
    "pair_id",
    "paradigm",
    "task_id",
}


def fmt(value, digits: int = 3) -> str:
    if value is None:
        return "n/a"
    if isinstance(value, float):
        return f"{value:.{digits}f}"
    return str(value)


def print_table(headers: list[str], rows: list[list[object]]) -> None:
    if not rows:
        print("  (aucune ligne)")
        return
    rendered = [[fmt(value) for value in row] for row in rows]
    widths = [len(header) for header in headers]
    for row in rendered:
        for index, value in enumerate(row):
            widths[index] = max(widths[index], len(value))
    print("  " + "  ".join(header.ljust(widths[i]) for i, header in enumerate(headers)))
    print("  " + "  ".join("-" * width for width in widths))
    for row in rendered:
        print("  " + "  ".join(value.ljust(widths[i]) for i, value in enumerate(row)))


def read_tsv(path: Path, suite: str) -> list[dict[str, str]]:
    if not path.is_file():
        print(f"AVERTISSEMENT: TSV introuvable: {path}", file=sys.stderr)
        return []
    rows: list[dict[str, str]] = []
    malformed = 0
    with path.open(encoding="utf-8", errors="replace", newline="") as handle:
        reader = csv.DictReader(handle, delimiter="\t")
        missing = TSV_REQUIRED_COLUMNS - set(reader.fieldnames or [])
        if missing:
            print(
                "AVERTISSEMENT: colonnes TSV absentes: " + ", ".join(sorted(missing)),
                file=sys.stderr,
            )
            return []
        for row in reader:
            if suite and row.get("suite_id", "") != suite:
                continue
            if row.get("result") not in {"PASS", "FAIL"}:
                malformed += 1
                continue
            rows.append(row)
    if malformed:
        print(f"AVERTISSEMENT: {malformed} ligne(s) TSV mal formée(s) ignorée(s)", file=sys.stderr)
    return rows


def report_success_rates(rows: list[dict[str, str]]) -> None:
    print("\n=== Taux de succès (TSV) ===")
    if not rows:
        print("  (aucune ligne)")
        return

    total = len(rows)
    passes = sum(1 for row in rows if row["result"] == "PASS")
    kept = [row for row in rows if row.get("primary_failure_code", "none") not in LAYER_A_FAILURES]
    kept_passes = sum(1 for row in kept if row["result"] == "PASS")
    excluded = total - len(kept)

    print(f"  brut            : {passes}/{total} = {fmt(passes / total)}")
    print(f"  couche A exclue : {excluded} ligne(s) ({', '.join(sorted(LAYER_A_FAILURES))})")
    if kept:
        print(f"  hors couche A   : {kept_passes}/{len(kept)} = {fmt(kept_passes / len(kept))}")
    else:
        print("  hors couche A   : n/a (toutes les lignes sont en couche A)")

    by_model: dict[str, list[dict[str, str]]] = defaultdict(list)
    for row in rows:
        by_model[row["model"]].append(row)

    print("\n  Par modèle:")
    table = []
    for model in sorted(by_model):
        model_rows = by_model[model]
        model_passes = sum(1 for row in model_rows if row["result"] == "PASS")
        model_kept = [
            row
            for row in model_rows
            if row.get("primary_failure_code", "none") not in LAYER_A_FAILURES
        ]
        model_kept_passes = sum(1 for row in model_kept if row["result"] == "PASS")
        table.append(
            [
                model,
                len(model_rows),
                model_passes,
                model_passes / len(model_rows),
                len(model_kept),
                model_kept_passes,
                (model_kept_passes / len(model_kept)) if model_kept else None,
            ]
        )
    print_table(
        ["model", "n", "passes", "brut", "n_hors_A", "passes_hors_A", "hors_A"],
        table,
    )

    by_task: dict[str, list[dict[str, str]]] = defaultdict(list)
    for row in rows:
        by_task[row.get("task_id", "") or "(legacy)"].append(row)
    print("\n  Par tâche:")
    task_table = []
    for task in sorted(by_task):
        task_rows = by_task[task]
        task_passes = sum(1 for row in task_rows if row["result"] == "PASS")
        task_table.append([task, len(task_rows), task_passes, task_passes / len(task_rows)])
    print_table(["task", "n", "passes", "brut"], task_table)


def report_paired(rows: list[dict[str, str]]) -> None:
    print("\n=== Différence appariée AB/BA (Task - Chat) ===")
    pairs: dict[str, dict[str, list[float]]] = defaultdict(lambda: {"chat": [], "task": []})
    for row in rows:
        pair_id = row.get("pair_id", "")
        paradigm = row.get("paradigm", "")
        if not pair_id or paradigm not in {"chat", "task"}:
            continue
        pairs[pair_id][paradigm].append(1.0 if row["result"] == "PASS" else 0.0)

    deltas: list[float] = []
    wins = losses = ties = 0
    for pair_id in sorted(pairs):
        arms = pairs[pair_id]
        if not arms["chat"] or not arms["task"]:
            continue
        chat = statistics.fmean(arms["chat"])
        task = statistics.fmean(arms["task"])
        delta = task - chat
        deltas.append(delta)
        if delta > 0:
            wins += 1
        elif delta < 0:
            losses += 1
        else:
            ties += 1

    if not deltas:
        print("  (aucune paire AB/BA complète)")
        return

    count = len(deltas)
    mean = statistics.fmean(deltas)
    if count > 1:
        variance = sum((delta - mean) ** 2 for delta in deltas) / (count - 1)
        standard_error = math.sqrt(variance / count)
        ci_low = mean - 1.96 * standard_error
        ci_high = mean + 1.96 * standard_error
    else:
        standard_error = None
        ci_low = ci_high = None

    print(f"  paires complètes : {count}")
    print(f"  Task > Chat      : {wins}   Task < Chat : {losses}   égalité : {ties}")
    print(f"  delta moyen      : {fmt(mean)}")
    print(f"  SE par grappe    : {fmt(standard_error)}")
    print(f"  IC 95% approx.   : [{fmt(ci_low)} ; {fmt(ci_high)}]")


def open_readonly(path: Path) -> sqlite3.Connection | None:
    if not path.is_file():
        print(f"AVERTISSEMENT: base SQLite introuvable: {path}", file=sys.stderr)
        return None
    try:
        connection = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
        connection.row_factory = sqlite3.Row
        return connection
    except sqlite3.Error as exc:
        print(f"AVERTISSEMENT: ouverture SQLite impossible ({path}): {exc}", file=sys.stderr)
        return None


def has_object(connection: sqlite3.Connection, name: str) -> bool:
    row = connection.execute(
        "SELECT 1 FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')",
        (name,),
    ).fetchone()
    return row is not None


def is_complexity_trap(suite: str) -> bool:
    lowered = suite.lower()
    return "ctrap" in lowered or "complexity" in lowered


def report_sqlite(path: Path, suite: str) -> None:
    print("\n=== SQLite : télémétrie corrélée ===")
    connection = open_readonly(path)
    if connection is None:
        return

    try:
        required = ["benchmark_trials"]
        absent = [name for name in required if not has_object(connection, name)]
        if absent:
            print(
                "  AVERTISSEMENT: schéma benchmark absent ("
                + ", ".join(absent)
                + "); aucune agrégation SQLite.",
                file=sys.stderr,
            )
            return

        groups: dict[tuple[str, str, str, str], dict[str, object]] = {}

        def group_key(row: sqlite3.Row) -> tuple[str, str, str, str]:
            return (
                row["suite_id"],
                row["task_id"],
                row["paradigm"],
                row["thinking_tier"] or "none",
            )

        if has_object(connection, "v_benchmark_cost_of_pass"):
            query = """
                SELECT suite_id, task_id, paradigm, thinking_tier, trials, passes,
                       pass_rate, avg_wall_time_ms, total_tokens_sum,
                       thought_tokens_sum, cost_of_pass_total_tokens,
                       cost_of_pass_output_tokens
                FROM v_benchmark_cost_of_pass
                WHERE (? = '' OR suite_id = ?)
                ORDER BY suite_id, task_id, paradigm, thinking_tier
            """
            for row in connection.execute(query, (suite, suite)):
                key = group_key(row)
                groups[key] = {
                    "trials": row["trials"],
                    "passes": row["passes"],
                    "pass_rate": row["pass_rate"],
                    "avg_wall_time_ms": row["avg_wall_time_ms"],
                    "cost_of_pass_total_tokens": row["cost_of_pass_total_tokens"],
                    "cost_of_pass_output_tokens": row["cost_of_pass_output_tokens"],
                    "total_tokens_sum": row["total_tokens_sum"],
                    "thought_tokens_sum": row["thought_tokens_sum"],
                    "masked_bytes": 0,
                    "masked_turns": 0,
                    "observed_turns": 0,
                    "roundtrips": [],
                    "anomalies": {},
                }
        else:
            query = """
                SELECT b.suite_id, b.task_id, b.paradigm,
                       COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier) AS thinking_tier,
                       COUNT(DISTINCT b.trial_id) AS trials,
                       SUM(b.oracle_passed) AS passes,
                       CAST(SUM(b.oracle_passed) AS REAL) / COUNT(*) AS pass_rate,
                       AVG(b.wall_time_ms) AS avg_wall_time_ms,
                       NULL AS cost_of_pass_total_tokens,
                       NULL AS cost_of_pass_output_tokens,
                       0 AS total_tokens_sum, 0 AS thought_tokens_sum
                FROM benchmark_trials b
                LEFT JOIN agent_sessions s ON s.session_id = b.session_id
                WHERE (? = '' OR b.suite_id = ?)
                GROUP BY b.suite_id, b.task_id, b.paradigm,
                         COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier)
                ORDER BY b.suite_id, b.task_id, b.paradigm, 4
            """
            for row in connection.execute(query, (suite, suite)):
                key = group_key(row)
                groups[key] = {
                    "trials": row["trials"],
                    "passes": row["passes"],
                    "pass_rate": row["pass_rate"],
                    "avg_wall_time_ms": row["avg_wall_time_ms"],
                    "cost_of_pass_total_tokens": row["cost_of_pass_total_tokens"],
                    "cost_of_pass_output_tokens": row["cost_of_pass_output_tokens"],
                    "total_tokens_sum": row["total_tokens_sum"],
                    "thought_tokens_sum": row["thought_tokens_sum"],
                    "masked_bytes": 0,
                    "masked_turns": 0,
                    "observed_turns": 0,
                    "roundtrips": [],
                    "anomalies": {},
                }

        if has_object(connection, "agent_turns") and has_object(connection, "agent_sessions"):
            masking_query = """
                SELECT b.suite_id, b.task_id, b.paradigm,
                       COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier) AS thinking_tier,
                       COUNT(DISTINCT b.trial_id) AS trials,
                       COUNT(t.turn_id) AS observed_turns,
                       COALESCE(SUM(t.masked_obs_bytes_saved), 0) AS masked_bytes,
                       SUM(CASE WHEN t.masked_obs_bytes_saved > 0 THEN 1 ELSE 0 END) AS masked_turns
                FROM benchmark_trials b
                LEFT JOIN agent_turns t ON t.trial_id = b.trial_id
                LEFT JOIN agent_sessions s ON s.session_id = b.session_id
                WHERE (? = '' OR b.suite_id = ?)
                GROUP BY b.suite_id, b.task_id, b.paradigm,
                         COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier)
            """
            for row in connection.execute(masking_query, (suite, suite)):
                key = group_key(row)
                entry = groups.setdefault(
                    key,
                    {
                        "trials": row["trials"],
                        "passes": 0,
                        "pass_rate": 0.0,
                        "avg_wall_time_ms": None,
                        "cost_of_pass_total_tokens": None,
                        "cost_of_pass_output_tokens": None,
                        "total_tokens_sum": 0,
                        "thought_tokens_sum": 0,
                        "masked_bytes": 0,
                        "masked_turns": 0,
                        "observed_turns": 0,
                        "roundtrips": [],
                        "anomalies": {},
                    },
                )
                entry["observed_turns"] = row["observed_turns"]
                entry["masked_bytes"] = row["masked_bytes"]
                entry["masked_turns"] = row["masked_turns"]

        if has_object(connection, "tool_call_spans") and has_object(connection, "agent_sessions"):
            roundtrip_query = """
                SELECT b.suite_id, b.task_id, b.paradigm,
                       COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier) AS thinking_tier,
                       sp.roundtrip_ms
                FROM benchmark_trials b
                JOIN tool_call_spans sp ON sp.session_id = b.session_id
                LEFT JOIN agent_sessions s ON s.session_id = b.session_id
                WHERE sp.roundtrip_ms IS NOT NULL AND (? = '' OR b.suite_id = ?)
            """
            for row in connection.execute(roundtrip_query, (suite, suite)):
                key = group_key(row)
                entry = groups.get(key)
                if entry is not None:
                    entry["roundtrips"].append(row["roundtrip_ms"])

        if has_object(connection, "turn_anomalies") and has_object(connection, "agent_turns"):
            anomaly_query = """
                SELECT b.suite_id, b.task_id, b.paradigm,
                       COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier) AS thinking_tier,
                       a.category, COUNT(*) AS occurrences
                FROM benchmark_trials b
                JOIN agent_turns t ON t.trial_id = b.trial_id
                JOIN turn_anomalies a ON a.turn_id = t.turn_id
                LEFT JOIN agent_sessions s ON s.session_id = b.session_id
                WHERE (? = '' OR b.suite_id = ?)
                GROUP BY b.suite_id, b.task_id, b.paradigm,
                         COALESCE(NULLIF(s.thinking_tier, ''), b.thinking_tier),
                         a.category
            """
            for row in connection.execute(anomaly_query, (suite, suite)):
                key = group_key(row)
                entry = groups.get(key)
                if entry is not None:
                    entry["anomalies"][row["category"]] = row["occurrences"]

        if not groups:
            print("  (aucun essai corrélé)")
            return

        table = []
        warnings: list[str] = []
        for key in sorted(groups):
            group_suite, task, paradigm, tier = key
            entry = groups[key]
            roundtrips = entry["roundtrips"]
            median_roundtrip = statistics.median(roundtrips) if roundtrips else None
            mean_roundtrip = statistics.fmean(roundtrips) if roundtrips else None
            anomalies = entry["anomalies"]
            anomaly_text = (
                ",".join(f"{name}={anomalies[name]}" for name in sorted(anomalies))
                if anomalies
                else "-"
            )
            table.append(
                [
                    group_suite,
                    task,
                    paradigm,
                    tier,
                    entry["trials"],
                    entry["passes"],
                    entry["pass_rate"],
                    entry["cost_of_pass_total_tokens"],
                    entry["cost_of_pass_output_tokens"],
                    entry["masked_bytes"],
                    entry["masked_turns"],
                    entry["observed_turns"],
                    median_roundtrip,
                    mean_roundtrip,
                    anomaly_text,
                ]
            )
            if is_complexity_trap(group_suite):
                if entry["masked_bytes"] == 0:
                    warnings.append(
                        f"{group_suite}/{task}/{paradigm}/{tier}: "
                        "SUM(masked_obs_bytes_saved)=0 — le masquage ne s'est pas activé, "
                        "l'écart Complexity Trap n'est pas interprétable."
                    )
                if tier not in {"medium", "high"}:
                    warnings.append(
                        f"{group_suite}/{task}/{paradigm}/{tier}: thinking_tier hors "
                        "{medium,high} — la fenêtre étendue n'était pas active."
                    )

        print_table(
            [
                "suite",
                "task",
                "paradigm",
                "tier",
                "trials",
                "passes",
                "pass_rate",
                "CoP_tokens",
                "CoP_output",
                "masked_bytes",
                "masked_turns",
                "obs_turns",
                "rt_median_ms",
                "rt_mean_ms",
                "anomalies",
            ],
            table,
        )
        print(
            "\n  CoP = tokens (entrée + sortie) par essai réussi; "
            "n/a signifie aucun succès dans le groupe."
        )
        for warning in warnings:
            print(f"  AVERTISSEMENT: {warning}", file=sys.stderr)
            print(f"  AVERTISSEMENT: {warning}")
    finally:
        connection.close()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Rapport lecture seule du benchmark matrix et de la télémétrie SQLite."
    )
    parser.add_argument("--tsv", type=Path, default=None, help="results.tsv du harness")
    parser.add_argument(
        "--db",
        type=Path,
        default=Path(__file__).resolve().parent.parent / "free-gemini-api/data/gemini.db",
        help="base SQLite du proxy (ouverte en mode=ro)",
    )
    parser.add_argument("--suite", default="", help="filtrer sur un suite_id")
    args = parser.parse_args()

    if args.tsv is None and not args.db.is_file():
        parser.error("fournir --tsv, --db, ou les deux (la base par défaut est absente)")

    print("Rapport de benchmark (lecture seule)")
    if args.suite:
        print(f"Suite: {args.suite}")

    rows: list[dict[str, str]] = []
    if args.tsv is not None:
        rows = read_tsv(args.tsv, args.suite)
        if args.suite and not rows:
            print(
                f"AVERTISSEMENT: aucune ligne TSV pour la suite {args.suite}",
                file=sys.stderr,
            )
        report_success_rates(rows)
        report_paired(rows)

    if args.db.is_file():
        try:
            report_sqlite(args.db, args.suite)
        except sqlite3.Error as exc:
            print(f"AVERTISSEMENT: agrégation SQLite impossible: {exc}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    sys.exit(main())
