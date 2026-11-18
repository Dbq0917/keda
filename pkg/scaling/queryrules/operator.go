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
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Operator is the comparison or matching operator used in a rule expression.
type Operator string

const (
	// OpEqual is the numeric/string equality operator ("==").
	OpEqual Operator = "=="
	// OpNotEqual is the not-equal operator ("!=").
	OpNotEqual Operator = "!="
	// OpGreater is the greater-than operator (">").
	OpGreater Operator = ">"
	// OpGreaterEqual is the greater-than-or-equal operator (">=").
	OpGreaterEqual Operator = ">="
	// OpLess is the less-than operator ("<").
	OpLess Operator = "<"
	// OpLessEqual is the less-than-or-equal operator ("<=").
	OpLessEqual Operator = "<="
	// OpRegex matches when the value matches the operand as a regular expression ("=~").
	OpRegex Operator = "=~"
	// OpNotRegex matches when the value does not match the operand ("!~").
	OpNotRegex Operator = "!~"
)

// knownOperators is ordered so that the two-character variants are matched
// before their single-character prefixes during tokenization.
var knownOperators = []Operator{
	OpEqual, OpNotEqual, OpGreaterEqual, OpLessEqual, OpRegex, OpNotRegex,
	OpGreater, OpLess,
}

// IsKnownOperator reports whether op is a supported comparison/matching operator.
func IsKnownOperator(op string) bool {
	for _, known := range knownOperators {
		if string(known) == op {
			return true
		}
	}
	return false
}

// isRegexOperator reports whether the operator performs regular-expression matching.
func isRegexOperator(op Operator) bool {
	return op == OpRegex || op == OpNotRegex
}

// isComparisonOperator reports whether the operator is a numeric comparison.
func isComparisonOperator(op Operator) bool {
	switch op {
	case OpEqual, OpNotEqual, OpGreater, OpGreaterEqual, OpLess, OpLessEqual:
		return true
	default:
		return false
	}
}

// splitExpression splits an expression of the form "<left> <operator> <right>"
// into its operator and operands. It returns an error instead of silently
// treating the expression as a literal when no known operator is present.
func splitExpression(expression string) (left string, op Operator, right string, err error) {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return "", "", "", fmt.Errorf("empty expression")
	}

	for _, candidate := range knownOperators {
		idx := strings.Index(trimmed, string(candidate))
		if idx < 0 {
			continue
		}
		left = strings.TrimSpace(trimmed[:idx])
		right = strings.TrimSpace(trimmed[idx+len(candidate):])
		if left == "" {
			return "", "", "", fmt.Errorf("expression %q has no left-hand operand", expression)
		}
		if right == "" {
			return "", "", "", fmt.Errorf("expression %q has no right-hand operand", expression)
		}
		// Reject expressions containing a second operator, e.g. "a > b > c".
		// Operands themselves may legitimately contain "=" or "!" (for example
		// a regular expression), so only known operator tokens are rejected.
		for _, trailing := range knownOperators {
			if strings.Contains(right, string(trailing)) {
				return "", "", "", fmt.Errorf("expression %q contains an unexpected trailing operator %s",
					expression, trailing)
			}
		}
		return left, candidate, right, nil
	}

	return "", "", "", fmt.Errorf("expression %q contains no supported operator (%s)",
		expression, joinOperators())
}

func joinOperators() string {
	names := make([]string, 0, len(knownOperators))
	for _, op := range knownOperators {
		names = append(names, string(op))
	}
	return strings.Join(names, ", ")
}

// evaluate applies the parsed operator to a monitoring value. Monitoring values
// may be numeric (e.g. a queue depth) or textual (e.g. an error label), so the
// comparison operators accept both forms while the regex operators only accept
// regular expressions as their right-hand operand.
func evaluate(op Operator, right string, value string) (bool, error) {
	if isRegexOperator(op) {
		pattern, err := regexp.Compile(right)
		if err != nil {
			return false, fmt.Errorf("invalid regular expression %q: %w", right, err)
		}
		matched := pattern.MatchString(value)
		if op == OpNotRegex {
			return !matched, nil
		}
		return matched, nil
	}

	if !isComparisonOperator(op) {
		return false, fmt.Errorf("unsupported operator %q", op)
	}

	// Numeric comparison is used when both the monitoring value and the
	// operand parse as numbers; everything else falls back to string equality.
	numericValue, valueErr := strconv.ParseFloat(value, 64)
	numericRight, rightErr := strconv.ParseFloat(right, 64)
	if valueErr == nil && rightErr == nil {
		switch op {
		case OpEqual:
			return numericValue == numericRight, nil
		case OpNotEqual:
			return numericValue != numericRight, nil
		case OpGreater:
			return numericValue > numericRight, nil
		case OpGreaterEqual:
			return numericValue >= numericRight, nil
		case OpLess:
			return numericValue < numericRight, nil
		case OpLessEqual:
			return numericValue <= numericRight, nil
		}
	}

	switch op {
	case OpEqual:
		return value == right, nil
	case OpNotEqual:
		return value != right, nil
	default:
		return false, fmt.Errorf("operator %s requires numeric operands, got value %q and operand %q",
			op, value, right)
	}
}
