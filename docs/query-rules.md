# Monitoring-query scaling rules

This document describes the rule format used by the query-rule engine in
`pkg/scaling/queryrules`, the supported comparison and matching operators,
the tenant-scoped rollout of the strict parser, and how to verify rule
behaviour against a fake monitoring endpoint.

## Why this exists

Rules written with the not-equal (`!=`), regex (`=~`) and not-regex (`!~`)
operators were previously parsed by the legacy parser, which did not
recognize those operators and treated the whole expression as a literal
string. As a result the condition could never match a monitoring value and
the scale-out never triggered.

The strict **v2 parser** understands all operators below and returns a
load-time error for an expression it cannot parse, instead of silently
turning it into a literal. Simple comparisons (`==`, `>`, `>=`, `<`, `<=`)
keep their exact historical behaviour.

## Rule file format

The on-disk rule file is unchanged; existing files load without edits.

```yaml
rules:
  - name: not-equal-error-rate      # unique name, used in metrics and records
    tenant: payments                # tenant scope, also used for rollout
    query: http_5xx_rate            # query sent to the monitoring endpoint
    expression: "http_5xx_rate != 0"
  - name: regex-canary-version
    tenant: payments
    query: deployed_version
    expression: "deployed_version =~ ^v2\\."
```

Every expression has the form `<left> <operator> <right>`. The left-hand
side is descriptive (it mirrors the query name); the monitoring value is
compared against the right-hand operand.

### Supported operators

| Operator | Meaning | Operand type |
| --- | --- | --- |
| `==` | equal | numeric or string |
| `!=` | not equal | numeric or string |
| `>` | greater than | numeric |
| `>=` | greater than or equal | numeric |
| `<` | less than | numeric |
| `<=` | less than or equal | numeric |
| `=~` | value matches the Go regular expression | regex |
| `!~` | value does not match the Go regular expression | regex |

With the v2 parser the following are rejected with an error at load time
rather than treated as a literal: a missing operator, an unknown operator,
missing operands, a second trailing operator, or an invalid regular
expression.

## Tenant-scoped rollout

The parser version is selected per tenant through a separate rollout
configuration file, so the new parser can be enabled for a subset of rules
without touching any rule file:

```yaml
# rollout.yaml
v2Tenants:
  - payments
v2All: false
```

- A tenant listed in `v2Tenants` is parsed with the strict v2 parser.
- Every other tenant keeps the legacy v1 parser and its historical
  behaviour (including literal handling of unrecognized operators).
- Setting `v2All: true` switches every tenant to v2; use it only after the
  full rollout has been verified.

Load the rollout with `LoadRolloutConfig(path)` and pass it to
`LoadRules(rulesPath, rollout)`. A missing rollout file means "all tenants
stay on v1".

## Observability

### Prometheus metrics

`NewMetrics(registry)` registers the following collectors (labels:
`tenant`, `rule`, `operator`, `parser`, plus `stage` on errors):

| Metric | Meaning |
| --- | --- |
| `keda_query_rule_evaluations_total` | every rule evaluation |
| `keda_query_rule_matched_total` | evaluations whose condition matched |
| `keda_query_rule_errors_total` | query/evaluation/parse failures, labeled by `stage` |

A rising `matched_total` series for an `!=`/`=~`/`!~` rule is direct
evidence that the operator is parsed and applied correctly.

### Trigger records

`TriggerRecorder` keeps every evaluation in memory and, when constructed
with a path, appends one JSON object per line (JSON Lines) to that file:

```json
{"timestamp":"2026-11-18T02:00:00Z","tenant":"payments","rule":"not-equal-error-rate","parser":"v2","operator":"!=","expression":"http_5xx_rate != 0","query":"http_5xx_rate","value":"7","matched":true,"scaledOut":true}
```

`Records()` returns all records and `MatchedRecords()` returns only the
scale-out evidence. Each record includes the parser version, operator,
monitoring value and whether it triggered a scale-out.

## Verifying with a fake monitoring endpoint

`FakeMonitor` serves a fixed response sequence per query and doubles as an
HTTP endpoint speaking the minimal Prometheus response format, so the real
`HTTPMonitorClient` can be exercised end-to-end:

```go
fake := queryrules.NewFakeMonitor(map[string][]string{
    "http_5xx_rate":    {"0", "7"},
    "deployed_version": {"v1.9.0", "v2.1.0"},
    "node_pool":        {"spot-az1-3", "on-demand-az1-1"},
    "queue_depth":     {"42", "250"},
})
server := httptest.NewServer(fake.Handler())
defer server.Close()
client := &queryrules.HTTPMonitorClient{Address: server.URL}
```

Once a sequence is exhausted its last value keeps being returned. A query
with no configured sequence returns an error.

The acceptance test `TestFourRulesWithDifferentOperatorsTriggerScaleOut`
uses four rules (`!=`, `=~`, `!~`, `>`), asserts that all four match on the
peak sequence and leave matched trigger records, while the first (quiet)
sequence triggers no scale-out. `TestLegacyRulesKeepIdenticalBehaviour`
re-runs the legacy rule set on the v1 parser and pins the historical
behaviour. Run both with:

```bash
go test ./pkg/scaling/queryrules/... -v
```

The runnable example `ExampleEngine_fourRuleTriggerRecords` prints the four
trigger records that prove each operator is applied:

```bash
go test ./pkg/scaling/queryrules -run ExampleEngine_fourRuleTriggerRecords -v
```
