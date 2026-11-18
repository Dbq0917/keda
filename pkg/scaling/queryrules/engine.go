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

	"github.com/go-logr/logr"
)

// Engine evaluates a fixed set of loaded rules against a monitoring endpoint
// on every evaluation loop. It is parser-agnostic: each rule already carries
// the condition produced by its tenant's parser, so tenant rollout is handled
// entirely at load time.
type Engine struct {
	rules    []Rule
	monitor  MonitorClient
	metrics  *Metrics
	recorder *TriggerRecorder
	rollout  RolloutConfig
	logger   logr.Logger
}

// NewEngine assembles an engine. Metrics and recorder are optional and may be
// nil; the logger falls back to a no-op logger when unset.
func NewEngine(rules []Rule, monitor MonitorClient, metrics *Metrics,
	recorder *TriggerRecorder, rollout RolloutConfig, logger logr.Logger) *Engine {
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}
	return &Engine{
		rules:    rules,
		monitor:  monitor,
		metrics:  metrics,
		recorder: recorder,
		rollout:  rollout,
		logger:   logger,
	}
}

// EvaluationResult summarizes one evaluation loop.
type EvaluationResult struct {
	// MatchedRules are the rule names whose condition matched this loop.
	MatchedRules []string
	// ScaleOut is true when at least one rule matched, which is the
	// scale-out signal consumed by the scaler.
	ScaleOut bool
	// Records holds one trigger record per evaluated rule.
	Records []TriggerRecord
}

// Evaluate runs every rule once against the monitoring endpoint.
func (e *Engine) Evaluate(ctx context.Context) EvaluationResult {
	result := EvaluationResult{Records: make([]TriggerRecord, 0, len(e.rules))}

	for _, rule := range e.rules {
		record := e.evaluateRule(ctx, rule)
		result.Records = append(result.Records, record)
		if record.Matched {
			result.MatchedRules = append(result.MatchedRules, rule.Name)
		}
	}

	result.ScaleOut = len(result.MatchedRules) > 0
	e.logger.Info("query rule evaluation finished",
		"matchedRules", result.MatchedRules, "scaleOut", result.ScaleOut)
	return result
}

func (e *Engine) evaluateRule(ctx context.Context, rule Rule) TriggerRecord {
	parserVersion := e.rollout.ParserVersionFor(rule.Tenant)
	condition := rule.Condition()

	record := TriggerRecord{
		Tenant:     rule.Tenant,
		Rule:       rule.Name,
		Parser:     string(parserVersion),
		Operator:   string(condition.operator),
		Expression: rule.Expression,
		Query:      rule.Query,
	}

	value, err := e.monitor.Query(ctx, rule.Query)
	if err != nil {
		record.Error = err.Error()
		e.metrics.recordError(rule.Tenant, rule.Name, parserVersion, condition.operator, "query")
		e.logger.Error(err, "monitoring query failed", "rule", rule.Name, "tenant", rule.Tenant)
	} else {
		record.Value = value
		e.metrics.recordEvaluation(rule.Tenant, rule.Name, parserVersion, condition.operator)

		matched, evalErr := condition.Evaluate(value)
		if evalErr != nil {
			record.Error = evalErr.Error()
			e.metrics.recordError(rule.Tenant, rule.Name, parserVersion, condition.operator, "evaluate")
			e.logger.Error(evalErr, "rule evaluation failed", "rule", rule.Name, "value", value)
		} else {
			record.Matched = matched
			record.ScaledOut = matched
			if matched {
				e.metrics.recordMatch(rule.Tenant, rule.Name, parserVersion, condition.operator)
				e.logger.Info("query rule matched, scale-out triggered",
					"rule", rule.Name, "tenant", rule.Tenant, "operator", condition.operator,
					"expression", rule.Expression, "value", value)
			}
		}
	}

	if e.recorder != nil {
		if err := e.recorder.Add(record); err != nil {
			e.logger.Error(err, "failed to persist trigger record", "rule", rule.Name)
		}
	}
	return record
}
