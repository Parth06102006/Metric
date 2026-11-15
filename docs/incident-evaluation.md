# Evaluating Gemini advisory analysis

Gemini is advisory. Correct-looking output, self-reported confidence and the
product's evidence gate are **not validation of accuracy**. Use independently
reviewed incident outcomes and keep rule-based alerts and human review.

## Initial historical-data check

A small, read-only sample of historical alerts was submitted to
`gemini-decision-1`. The provider calls succeeded and the evidence gate would
withhold their results. The cases were **unlabeled**, their provenance is
unknown, and they may include test traffic. Historical snapshots lacked the
anonymous endpoint-error summary needed to substantiate endpoint-specific
suggestions.

This demonstrates API compatibility and abstention on that sample, not
diagnostic accuracy, calibration, production incident quality or safety. No
accuracy percentage can be calculated without independent labels. Detailed local
incident data and evaluation outputs are intentionally not included here.

## Repeat with reviewed cases

`apps/api/cmd/incident-eval` exports aggregate fields only: it does not export or
send project IDs, raw paths, IP addresses, messages or API keys. Timestamps and
aggregate service metrics are still potentially sensitive; get data-processing
approval before using an external provider. Cases contain local anomaly IDs for
mapping to human review; those IDs are not sent to the provider. The exporter
opens an existing SQLite database read-only and never migrates it. It selects at
most 10 cases per type from the latest 5,000 alert/snapshot matches. The nearest
snapshot must be within ten seconds. Snapshot RPS and window length reconstruct
a rounded request count; endpoint error summaries are unavailable in historical
snapshots. A baseline requires the same minimum sample count/span as the worker.
This is not necessarily an unbiased or representative sample.

```sh
cd apps/api
# The output must not already exist; it is created with mode 0600.
go run ./cmd/incident-eval export -db /path/to/traffic.db -out /secure/path/cases.json -per-type 3
```

Have a reviewer use incident evidence and outcomes **before looking at Gemini
answers**. Add an `expected` object to each reviewed case with one or more
acceptable values. Omit `expected` or either array for unreviewed dimensions;
the report excludes those from accuracy counts. Supported values are:

```json
"expected": {
  "patterns": ["error_burst"],
  "directions": ["failing_endpoints"]
}
```

Patterns: `traffic_surge`, `error_burst`, `latency_degradation`, `mixed`,
`unclear`. Directions: `traffic_sources`, `failing_endpoints`, `latency`,
`more_evidence`. Labels assess these limited descriptive categories, not a
proven root cause or mitigation. Preserve review rationale and outcomes
separately and securely. Do not commit case files or reports.

Set `GEMINI_API_KEY` securely in the environment, then explicitly set a call
budget at least as large as the number of cases:

```sh
go run ./cmd/incident-eval run -cases /secure/path/cases.json -max-calls 9
```

Each case causes one potentially billable API request, with no automatic retries;
results and labeled match counts are printed locally as JSON. The evaluation
runs independently and **does not use the running application's daily call
budget**. Assess coverage, abstention, false reassurance, per-category errors
and human utility on a sufficiently diverse, representative held-out set before
changing thresholds. Exact-match counts alone cannot establish calibration or
safety. The tool reports the product evidence gate separately; it does not
pretend an unlabeled case is correct.
