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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseV2AllOperators(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		operator   Operator
		right      string
	}{
		{name: "equal", expression: "cpu == 80", operator: OpEqual, right: "80"},
		{name: "not equal", expression: "status != 200", operator: OpNotEqual, right: "200"},
		{name: "greater equal", expression: "queue_depth >= 100", operator: OpGreaterEqual, right: "100"},
		{name: "regex", expression: "version =~ v1\\..*", operator: OpRegex, right: `v1\..*`},
		{name: "not regex", expression: "host !~ ^canary-", operator: OpNotRegex, right: "^canary-"},
		{name: "less equal with spaces", expression: " latency  <=  5 ", operator: OpLessEqual, right: "5"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			condition, err := ParseCondition(test.expression, ParserV2)
			require.NoError(t, err)
			assert.Equal(t, test.operator, condition.operator)
			assert.Equal(t, test.right, condition.right)
		})
	}
}

func TestParseV2RejectsInvalidExpressions(t *testing.T) {
	invalid := []string{
		"",
		"cpu 80",        // no operator at all
		"cpu ~= 80",     // unsupported, reversed regex operator
		"cpu == ",       // missing right operand
		"== 80",         // missing left operand
		"cpu > 10 > 20", // trailing operator
		"host =~ [",     // invalid regular expression
	}

	for _, expression := range invalid {
		t.Run(expression, func(t *testing.T) {
			_, err := ParseCondition(expression, ParserV2)
			require.Error(t, err, "expression %q must fail to parse instead of becoming a literal", expression)
		})
	}
}

func TestEvaluateOperators(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		value      string
		wantMatch  bool
	}{
		// Numeric comparisons.
		{"equal numeric hit", "cpu == 80", "80", true},
		{"equal numeric miss", "cpu == 80", "79", false},
		{"not equal numeric hit", "status != 200", "500", true},
		{"not equal numeric miss", "status != 200", "200", false},
		{"greater hit", "queue_depth > 100", "250", true},
		{"greater miss", "queue_depth > 100", "100", false},
		{"greater equal boundary", "queue_depth >= 100", "100", true},
		{"less hit", "latency < 5", "2", true},
		{"less equal boundary", "latency <= 5", "5", true},
		// Textual equality / inequality.
		{"equal string hit", "state == ready", "ready", true},
		{"not equal string hit", "state != ready", "degraded", true},
		// Regular-expression matching.
		{"regex hit", "version =~ ^v1\\.", "v1.4.2", true},
		{"regex miss", "version =~ ^v1\\.", "v2.0.0", false},
		{"not regex hit", "host !~ ^canary-", "prod-node-1", true},
		{"not regex miss", "host !~ ^canary-", "canary-node-2", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			condition, err := ParseCondition(test.expression, ParserV2)
			require.NoError(t, err)
			matched, err := condition.Evaluate(test.value)
			require.NoError(t, err)
			assert.Equal(t, test.wantMatch, matched)
		})
	}
}

func TestRegexOperandMayContainOperatorCharacters(t *testing.T) {
	// The right-hand side of a regex may legitimately contain characters
	// such as "="; only a second full operator token is invalid.
	condition, err := ParseCondition(`payload =~ key=value|other`, ParserV2)
	require.NoError(t, err)
	assert.Equal(t, OpRegex, condition.operator)
	assert.Equal(t, `key=value|other`, condition.right)

	matched, err := condition.Evaluate("key=value")
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestEvaluateNonNumericComparisonErrors(t *testing.T) {
	condition, err := ParseCondition("queue_depth > 100", ParserV2)
	require.NoError(t, err)

	_, err = condition.Evaluate("not-a-number")
	require.Error(t, err)
}

// TestParserV1PreservesLegacyBehaviour pins the historical semantics that
// existing rule files rely on: simple comparisons keep working while
// unsupported operators are treated as literal string targets.
func TestParserV1PreservesLegacyBehaviour(t *testing.T) {
	// Simple comparisons behave identically under both parsers.
	for _, expression := range []string{"cpu == 80", "queue_depth > 100", "latency <= 5"} {
		legacy, err := ParseCondition(expression, ParserV1)
		require.NoError(t, err)
		strict, err := ParseCondition(expression, ParserV2)
		require.NoError(t, err)

		for _, value := range []string{"80", "100", "5", "500"} {
			legacyMatch, legacyErr := legacy.Evaluate(value)
			require.NoError(t, legacyErr)
			strictMatch, strictErr := strict.Evaluate(value)
			require.NoError(t, strictErr)
			assert.Equal(t, strictMatch, legacyMatch,
				"expression %q with value %q changed behaviour", expression, value)
		}
	}

	// The previously broken operators remain literals under v1 ...
	legacy, err := ParseCondition("status != 200", ParserV1)
	require.NoError(t, err)
	assert.Equal(t, "status != 200", legacy.literalTarget)
	matched, err := legacy.Evaluate("500")
	require.NoError(t, err)
	assert.False(t, matched, "legacy parser must keep treating the expression as a literal")

	// ... and become real operators under v2.
	strict, err := ParseCondition("status != 200", ParserV2)
	require.NoError(t, err)
	matched, err = strict.Evaluate("500")
	require.NoError(t, err)
	assert.True(t, matched)
}
