# Launch metrics

Redline exposes an auditable metrics report through the service API and CLI:

```bash
go run ./cmd/redline metrics launch --days 21
go run ./cmd/redline metrics launch --days 7 --provider claude-main
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

## Capacity reclaimed estimate

This is the weekly-allowance equivalent used by completed jobs admitted in `window_slots` mode—the discrete-window rule that identified weekly allowance exceeding the capacity of the remaining five-hour windows. Pace-threshold admissions are excluded.

The value is best read as **measured managed usage while capacity was at risk**. It is not a counterfactual claim that every unit would certainly have expired without Redline.
