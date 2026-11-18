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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const promMetricsSubsystem = "query_rule"

// Metrics holds the Prometheus collectors that make rule evaluation
// observable: every evaluation, match and parse/evaluation error is counted
// per tenant, rule and operator.
type Metrics struct {
	Evaluations *prometheus.CounterVec
	Matched     *prometheus.CounterVec
	Errors      *prometheus.CounterVec
}

// NewMetrics builds the collectors and registers them on registerer. Passing
// nil registers them on the shared controller-runtime registry, matching the
// rest of the KEDA metrics.
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	m := &Metrics{
		Evaluations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "keda",
			Subsystem: promMetricsSubsystem,
			Name:      "evaluations_total",
			Help:      "Total number of monitoring-query rule evaluations, labeled by tenant, rule and operator.",
		}, []string{"tenant", "rule", "operator", "parser"}),
		Matched: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "keda",
			Subsystem: promMetricsSubsystem,
			Name:      "matched_total",
			Help:      "Total number of rule evaluations whose condition matched, labeled by tenant, rule and operator.",
		}, []string{"tenant", "rule", "operator", "parser"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "keda",
			Subsystem: promMetricsSubsystem,
			Name:      "errors_total",
			Help:      "Total number of rule parse or evaluation errors, labeled by tenant, rule and operator.",
		}, []string{"tenant", "rule", "operator", "parser", "stage"}),
	}

	if registerer == nil {
		registerer = metrics.Registry
	}
	registerer.MustRegister(m.Evaluations, m.Matched, m.Errors)
	return m
}

func (m *Metrics) recordEvaluation(tenant, rule string, parser ParserVersion, op Operator) {
	if m == nil {
		return
	}
	m.Evaluations.WithLabelValues(tenant, rule, string(op), string(parser)).Inc()
}

func (m *Metrics) recordMatch(tenant, rule string, parser ParserVersion, op Operator) {
	if m == nil {
		return
	}
	m.Matched.WithLabelValues(tenant, rule, string(op), string(parser)).Inc()
}

func (m *Metrics) recordError(tenant, rule string, parser ParserVersion, op Operator, stage string) {
	if m == nil {
		return
	}
	m.Errors.WithLabelValues(tenant, rule, string(op), string(parser), stage).Inc()
}
