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
	"os"

	"gopkg.in/yaml.v3"
)

// Rule is a single monitoring-query based scaling rule. The rule file format is
// intentionally left untouched: existing files keep loading without changes.
type Rule struct {
	// Name is the unique, human-readable identifier used in metrics and
	// trigger records.
	Name string `yaml:"name"`
	// Tenant scopes the rule to a tenant and is also used by the rollout
	// configuration to decide which parser version handles the rule.
	Tenant string `yaml:"tenant"`
	// Query is sent to the monitoring endpoint; its returned value is checked
	// against Expression.
	Query string `yaml:"query"`
	// Expression uses one of the supported comparison/matching operators.
	Expression string `yaml:"expression"`

	condition *Condition
}

// RuleFile is the on-disk rule file.
type RuleFile struct {
	Rules []Rule `yaml:"rules"`
}

// RolloutConfig controls the tenant-scoped rollout of the strict v2 parser.
// Tenants listed in V2Tenants are parsed with the new parser; every other
// tenant keeps the legacy v1 parser, so existing rule files do not need to be
// modified.
type RolloutConfig struct {
	// V2Tenants is the allow-list of tenants that use the strict parser.
	V2Tenants []string `yaml:"v2Tenants"`
	// V2All enables the strict parser for every tenant. It is intended for
	// the final cut-over after the rollout is complete.
	V2All bool `yaml:"v2All"`
}

// LoadRules reads and parses a rule file. Each rule is parsed with the parser
// version selected by the rollout configuration for its tenant. With the v2
// parser a malformed expression aborts loading with an error instead of being
// treated as a literal.
func LoadRules(path string, rollout RolloutConfig) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading rule file %q: %w", path, err)
	}

	var ruleFile RuleFile
	if err := yaml.Unmarshal(data, &ruleFile); err != nil {
		return nil, fmt.Errorf("parsing rule file %q: %w", path, err)
	}

	seen := make(map[string]struct{}, len(ruleFile.Rules))
	for i := range ruleFile.Rules {
		rule := &ruleFile.Rules[i]
		if rule.Name == "" {
			return nil, fmt.Errorf("rule at index %d has no name", i)
		}
		if _, ok := seen[rule.Name]; ok {
			return nil, fmt.Errorf("duplicate rule name %q", rule.Name)
		}
		seen[rule.Name] = struct{}{}
		if rule.Query == "" {
			return nil, fmt.Errorf("rule %q has no query", rule.Name)
		}
		if rule.Expression == "" {
			return nil, fmt.Errorf("rule %q has no expression", rule.Name)
		}

		version := rollout.ParserVersionFor(rule.Tenant)
		condition, parseErr := ParseCondition(rule.Expression, version)
		if parseErr != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.Name, parseErr)
		}
		rule.condition = condition
	}

	return ruleFile.Rules, nil
}

// LoadRolloutConfig reads the optional rollout configuration. A missing file
// is treated as an empty rollout, i.e. every tenant stays on the legacy
// parser.
func LoadRolloutConfig(path string) (RolloutConfig, error) {
	var rollout RolloutConfig
	if path == "" {
		return rollout, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rollout, nil
		}
		return rollout, fmt.Errorf("reading rollout config %q: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &rollout); err != nil {
		return rollout, fmt.Errorf("parsing rollout config %q: %w", path, err)
	}
	return rollout, nil
}

// ParserVersionFor returns the parser version that handles rules of the given
// tenant, allowing the strict parser to be enabled for a subset of tenants
// without touching the rule files.
func (r RolloutConfig) ParserVersionFor(tenant string) ParserVersion {
	if r.V2All {
		return ParserV2
	}
	for _, enabledTenant := range r.V2Tenants {
		if enabledTenant == tenant {
			return ParserV2
		}
	}
	return ParserV1
}

// Condition exposes the parsed condition to the engine.
func (r Rule) Condition() *Condition {
	return r.condition
}
