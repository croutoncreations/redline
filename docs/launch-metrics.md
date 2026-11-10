# Launch metrics

Redline exposes an auditable metrics report through the service API and CLI:

```bash
redline metrics launch --days 21
redline metrics launch --days 7 --provider claude-main
```

The equivalent authenticated service endpoint is `GET /v1/metrics/launch?days=21`.

The report deliberately separates exact operational counters from empirical allowance estimates.

## Exact counters

- **RUN, WAIT, and UNKNOWN** count automatic scheduler decisions. Manual evaluations and executions are excluded.
- **Wait rate** is `WAIT / (RUN + WAIT + UNKNOWN)`. Dispatch errors with no decision are reported separately and excluded from the denominator.
- **Admitted, completed, failed, and active jobs** follow automatic admissions by their persisted run ID. A terminal run only counts when it completed inside the requested report window.
- **Completion rate** is `completed / (completed + failed)`.

`RUN` can be higher than admitted jobs. A RUN decision may find no eligible queued task, which is reported separately as `no_eligible_task`.

## Allowance conversion estimate

For each provider, Redline prices the tokens attributed to completed `redline-run` observations using its versioned provider accounting card. It divides that usage by the empirically calibrated weekly bucket range, then divides by the number of seven-day entitlement periods in the report window.

The API reports a low/high range, evidence coverage, calibration confidence, and one of:

- `available`: every completed job has usable evidence and calibration is medium or high confidence;
- `partial`: some jobs lack usable evidence or calibration confidence is low;
- `unavailable`: no defensible estimate can be calculated.

Redline does not fill evidence gaps with an average.
Fractional `conversion_rate_*` fields and display-ready `conversion_percent_*` fields are both included.

## Profile usage totals

`profile_usage` reports raw Redline-managed token totals for each execution profile and provider account. It counts
every completed or failed run whose completion time falls in `[since, until)` (manual runs too), and splits them into
`automatic`, `manual` (including `manual-task`), `unknown_trigger` (no retained dispatch attempt, or an unrecognized
trigger), and `all`. The `provider` filter limits the
list to runs on that provider account.

- **Identity.** The profile comes from the `run.started` audit snapshot, which records the profile the run actually used.
  If that snapshot is missing, Redline falls back to the task's current profile and counts the run in
  `runs_with_inferred_profile`. `provider_account_id` is the configured account the run was admitted on.
  `usage_providers` lists the usage families seen in evidence. A Hermes profile can bill a different family than its account.
  `harness_type` is taken from the first run of that profile in the window.
- **Evidence.** Only `redline-run` observations parsed from Redline-owned harness output are counted. Interactive,
  Gatepost, and Pi session usage are never included. Harnesses without a usage parser (such as `pi` and `command`) are
  shown with `tokens: null` and `evidence_coverage: 0`, meaning unknown rather than zero.
- **Token semantics.** `input_tokens` excludes cache reads and cache writes: Codex cached input is subtracted at parse time,
  and Claude reports cache tokens separately. Hermes `input` is stored as reported, on the assumption that it already
  excludes cache reads, as Hermes's separate `cache_read` and `cache_write` fields imply. `total_tokens` is therefore
  `input + output + cache_read + cache_creation` with no double-counting. Claude results with several models are stored as
  one row per model and summed. If a run has both a run-wide row and per-model rows for one provider, the run-wide row is
  dropped and counted in `duplicate_observations_ignored`.
- **Interpretation.** Cache reads usually dominate the total and cost far less than fresh input. A token total is a volume
  measure, not an allowance, cost, or savings figure. Use the allowance estimates above for allowance-equivalent claims.

## Capacity reclaimed estimate

This is the weekly-allowance equivalent used by completed jobs admitted in `window_slots` mode - the discrete-window rule that identified weekly allowance exceeding the capacity of the remaining five-hour windows. Pace-threshold admissions are excluded.

The value is best read as **measured managed usage while capacity was at risk**. It is not a counterfactual claim that every unit would certainly have expired without Redline.
