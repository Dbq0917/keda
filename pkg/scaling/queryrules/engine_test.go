/*
Copyright 2026 The KEDA Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package queryrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const rulesYAML = `
rules:
  - name: not-equal-error-rate
    tenant: payments
    query: http_5xx_rate
    expression: "http_5xx_rate != 0"
  - name: regex-canary-version
    tenant: payments
    query: deployed_version
    expression: "deployed_version =~ ^v2\\."
  - name: not-regex-stable-host
    tenant: payments
    query: node_pool
    expression: "node_pool !~ ^spot-"
  - name: greater-queue-depth
    tenant: payments
    query: queue_depth
    expression: "queue_depth > 100"
`

const legacyRulesYAML = `
rules:
  - name: equal-cpu
    tenant: legacy
    query: cpu
    expression: "cpu == 80"
  - name: greater-memory
    tenant: legacy
    query: memory
    expression: "memory > 60"
  - name: broken-regex
    tenant: legacy
    query: version
    expression: "version =~ ^v1\\."
`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestFourRulesWithDifferentOperatorsTriggerScaleOut is the acceptance test:
// four rules, each using a different operator, must all match on the fixed
// monitoring sequence and produce trigger records proving the operators were
// really applied.
func TestFourRulesWithDifferentOperatorsTriggerScaleOut(t *testing.T) {
	rulesPath := writeFile(t, "rules.yaml", rulesYAML)
	rolloutPath := writeFile(t, "rollout.yaml", "v2Tenants:\n  - payments\n")

	rollout, err := LoadRolloutConfig(rolloutPath)
	require.NoError(t, err)
	rules, err := LoadRules(rulesPath, rollout)
	require.NoError(t, err)
	require.Len(t, rules, 4)

	// Fixed sequence: a quiet value followed by the peak value that must
	// trigger every rule.
	fake := NewFakeMonitor(map[string][]string{
		"http_5xx_rate":    {"0", "7"},
		"deployed_version": {"v1.9.0", "v2.1.0"},
		"node_pool":        {"spot-az1-3", "on-demand-az1-1"},
		"queue_depth":      {"42", "250"},
	})

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	recorder, err := NewTriggerRecorder("")
	require.NoError(t, err)

	engine := NewEngine(rules, fake, metrics, recorder, rollout, zap.New(zap.UseDevMode(true)))

	// First loop: nothing matches, no scale-out.
	quiet := engine.Evaluate(context.Background())
	assert.False(t, quiet.ScaleOut)
	assert.Empty(t, quiet.MatchedRules)

	// Second loop: all four rules must match and trigger scale-out.
	peak := engine.Evaluate(context.Background())
	assert.True(t, peak.ScaleOut)
	assert.ElementsMatch(t, []string{
		"not-equal-error-rate", "regex-canary-version",
		"not-regex-stable-host", "greater-queue-depth",
	}, peak.MatchedRules)

	records := recorder.MatchedRecords()
	require.Len(t, records, 4, "every rule must leave a matched trigger record")

	operators := map[string]string{}
	for _, record := range records {
		assert.True(t, record.ScaledOut)
		assert.Equal(t, "v2", record.Parser)
		operators[record.Rule] = record.Operator
	}
	assert.Equal(t, map[string]string{
		"not-equal-error-rate":  "!=",
		"regex-canary-version":  "=~",
		"not-regex-stable-host": "!~",
		"greater-queue-depth":   ">",
	}, operators)

	// The four distinct operators must each show up as a matched metric.
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range metricFamilies {
		if family.GetName() != "keda_query_rule_matched_total" {
			continue
		}
		seenOperators := map[string]float64{}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			seenOperators[labels["operator"]] += metric.GetCounter().GetValue()
		}
		for _, operator := range []string{"!=", "=~", "!~", ">"} {
			assert.Greater(t, seenOperators[operator], 0.0,
				"matched_total missing evidence for operator %s", operator)
		}
	}
}

// TestLegacyRulesKeepIdenticalBehaviour verifies that existing rule files
// evaluated with the legacy parser produce exactly the same trigger records
// before and after the fix.
func TestLegacyRulesKeepIdenticalBehaviour(t *testing.T) {
	rulesPath := writeFile(t, "legacy-rules.yaml", legacyRulesYAML)
	rollout := RolloutConfig{} // no tenant on v2: everything stays on v1

	rules, err := LoadRules(rulesPath, rollout)
	require.NoError(t, err)

	fake := NewFakeMonitor(map[string][]string{
		"cpu":     {"80"},
		"memory":  {"90"},
		"version": {"v1.2.3"},
	})
	recorder, err := NewTriggerRecorder("")
	require.NoError(t, err)

	engine := NewEngine(rules, fake, nil, recorder, rollout, zap.New(zap.UseDevMode(true)))
	result := engine.Evaluate(context.Background())

	// == and > keep matching; the regex rule still behaves as a literal and
	// therefore does not match, exactly as it did before the fix.
	assert.ElementsMatch(t, []string{"equal-cpu", "greater-memory"}, result.MatchedRules)

	records := recorder.Records()
	require.Len(t, records, 3)
	for _, record := range records {
		assert.Equal(t, "v1", record.Parser)
	}

	regexRecord := records[2]
	assert.Equal(t, "broken-regex", regexRecord.Rule)
	assert.False(t, regexRecord.Matched)
	assert.Empty(t, regexRecord.Operator, "legacy literal conditions expose no operator")
}

// TestTenantRolloutEnablesV2ForSelectedTenants proves the rollout is
// tenant-scoped: the same rule file yields v2 for an enrolled tenant and v1
// for everyone else.
func TestTenantRolloutEnablesV2ForSelectedTenants(t *testing.T) {
	rollout := RolloutConfig{V2Tenants: []string{"payments"}}
	assert.Equal(t, ParserV2, rollout.ParserVersionFor("payments"))
	assert.Equal(t, ParserV1, rollout.ParserVersionFor("legacy"))

	allIn := RolloutConfig{V2All: true}
	assert.Equal(t, ParserV2, allIn.ParserVersionFor("legacy"))
}

// TestLoadRulesFailsOnUnparseableExpression ensures a malformed expression is
// reported at load time instead of silently becoming a literal.
func TestLoadRulesFailsOnUnparseableExpression(t *testing.T) {
	badRules := strings.ReplaceAll(rulesYAML,
		`expression: "http_5xx_rate != 0"`,
		`expression: "http_5xx_rate ~= 0"`)
	rulesPath := writeFile(t, "bad-rules.yaml", badRules)

	_, err := LoadRules(rulesPath, RolloutConfig{V2All: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-equal-error-rate")

	// The same file still loads for a tenant that has not been enrolled,
	// because the legacy parser is intentionally lenient.
	rules, err := LoadRules(rulesPath, RolloutConfig{})
	require.NoError(t, err)
	assert.Len(t, rules, 4)
}

// TestFakeMonitorOverHTTP exercises the real HTTP client against the fake
// monitoring endpoint, returning a fixed sequence.
func TestFakeMonitorOverHTTP(t *testing.T) {
	fake := NewFakeMonitor(map[string][]string{"queue_depth": {"1", "2", "3"}})
	server := httptest.NewServer(fake.Handler())
	defer server.Close()

	client := &HTTPMonitorClient{Address: server.URL}
	for _, expected := range []string{"1", "2", "3", "3"} {
		value, err := client.Query(context.Background(), "queue_depth")
		require.NoError(t, err)
		assert.Equal(t, expected, value)
	}
	assert.Equal(t, 4, fake.Calls("queue_depth"))
}

// TestTriggerRecorderPersistsJSONL checks that trigger records are written to
// a JSON Lines file so scale-out evidence can be inspected after the run.
func TestTriggerRecorderPersistsJSONL(t *testing.T) {
	sinkPath := filepath.Join(t.TempDir(), "triggers.jsonl")
	recorder, err := NewTriggerRecorder(sinkPath)
	require.NoError(t, err)

	require.NoError(t, recorder.Add(TriggerRecord{
		Tenant: "payments", Rule: "not-equal-error-rate", Operator: "!=",
		Expression: "http_5xx_rate != 0", Value: "7", Matched: true, ScaledOut: true,
	}))
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(sinkPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], `"operator":"!="`)
	assert.Contains(t, lines[0], `"scaledOut":true`)
}

// ExampleEngine_fourRuleTriggerRecords prints the trigger records produced
// when four rules with four different operators (!=, =~, !~, >) all match.
// Run it with:
//
//	go test ./pkg/scaling/queryrules -run ExampleEngine_fourRuleTriggerRecords -v
func ExampleEngine_fourRuleTriggerRecords() {
	rules, _ := LoadRules("testdata/rules.yaml", RolloutConfig{V2All: true})
	fake := NewFakeMonitor(map[string][]string{
		"http_5xx_rate":    {"7"},
		"deployed_version": {"v2.1.0"},
		"node_pool":        {"on-demand-az1-1"},
		"queue_depth":      {"250"},
	})
	engine := NewEngine(rules, fake, nil, nil, RolloutConfig{V2All: true}, logr.Discard())

	result := engine.Evaluate(context.Background())
	for _, record := range result.Records {
		var builder strings.Builder
		encoder := json.NewEncoder(&builder)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(record)
		fmt.Print(builder.String())
	}
	// Output:
	// {"timestamp":"0001-01-01T00:00:00Z","tenant":"payments","rule":"not-equal-error-rate","parser":"v2","operator":"!=","expression":"http_5xx_rate != 0","query":"http_5xx_rate","value":"7","matched":true,"scaledOut":true}
	// {"timestamp":"0001-01-01T00:00:00Z","tenant":"payments","rule":"regex-canary-version","parser":"v2","operator":"=~","expression":"deployed_version =~ ^v2\\.","query":"deployed_version","value":"v2.1.0","matched":true,"scaledOut":true}
	// {"timestamp":"0001-01-01T00:00:00Z","tenant":"payments","rule":"not-regex-stable-host","parser":"v2","operator":"!~","expression":"node_pool !~ ^spot-","query":"node_pool","value":"on-demand-az1-1","matched":true,"scaledOut":true}
	// {"timestamp":"0001-01-01T00:00:00Z","tenant":"payments","rule":"greater-queue-depth","parser":"v2","operator":">","expression":"queue_depth > 100","query":"queue_depth","value":"250","matched":true,"scaledOut":true}
}
