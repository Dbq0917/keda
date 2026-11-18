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
	"strings"
)

// ParserVersion selects the expression parser. ParserV2 is the strict parser
// that understands all comparison and matching operators and fails loudly on
// malformed expressions. ParserV1 reproduces the historical behaviour where
// unrecognized operators were treated as literal string targets; it exists so
// existing rule files keep their exact behaviour during the tenant rollout.
type ParserVersion string

const (
	// ParserV1 is the legacy, lenient parser.
	ParserV1 ParserVersion = "v1"
	// ParserV2 is the strict parser with full operator support.
	ParserV2 ParserVersion = "v2"
)

// legacyOperators are the only operators understood before the fix. Order
// matters: the two-character forms must be tried first.
var legacyOperators = []Operator{OpEqual, OpGreaterEqual, OpLessEqual, OpGreater, OpLess}

// Condition is a parsed rule expression ready to be evaluated.
type Condition struct {
	expression string
	operator   Operator
	right      string
	regex      *regexp.Regexp

	// literalTarget is set by the legacy parser for expressions whose
	// operator was not recognized and which therefore match the full
	// expression as a literal string.
	literalTarget string
}

// ParseCondition parses an expression with the requested parser. The v2
// parser returns an error instead of falling back to a literal match, while
// the v1 parser never errors and keeps the historical literal behaviour.
func ParseCondition(expression string, version ParserVersion) (*Condition, error) {
	switch version {
	case ParserV2:
		return parseV2(expression)
	case ParserV1:
		return parseV1(expression), nil
	default:
		return nil, fmt.Errorf("unknown parser version %q", version)
	}
}

func parseV2(expression string) (*Condition, error) {
	_, op, right, err := splitExpression(expression)
	if err != nil {
		return nil, err
	}

	condition := &Condition{
		expression: strings.TrimSpace(expression),
		operator:   op,
		right:      right,
	}

	if isRegexOperator(op) {
		pattern, compileErr := regexp.Compile(right)
		if compileErr != nil {
			return nil, fmt.Errorf("invalid regular expression in %q: %w", expression, compileErr)
		}
		condition.regex = pattern
	}

	return condition, nil
}

// parseV1 reproduces the historical behaviour: the simple comparison
// operators keep working exactly as before, while any other expression is
// matched against the monitoring value as a literal string.
func parseV1(expression string) *Condition {
	trimmed := strings.TrimSpace(expression)
	condition := &Condition{expression: trimmed}

	for _, op := range legacyOperators {
		idx := strings.Index(trimmed, string(op))
		if idx < 0 {
			continue
		}
		left := strings.TrimSpace(trimmed[:idx])
		right := strings.TrimSpace(trimmed[idx+len(op):])
		if left == "" || right == "" {
			break
		}
		condition.operator = op
		condition.right = right
		return condition
	}

	// No recognized operator: the whole expression is treated as a literal.
	condition.literalTarget = trimmed
	return condition
}

// Evaluate checks the monitoring value against the parsed condition.
func (c *Condition) Evaluate(value string) (bool, error) {
	if c.literalTarget != "" {
		return value == c.literalTarget, nil
	}
	if c.regex != nil {
		matched := c.regex.MatchString(value)
		if c.operator == OpNotRegex {
			return !matched, nil
		}
		return matched, nil
	}
	return evaluate(c.operator, c.right, value)
}

// Operator returns the parsed operator, or the empty string for a legacy
// literal condition.
func (c *Condition) Operator() Operator {
	return c.operator
}
