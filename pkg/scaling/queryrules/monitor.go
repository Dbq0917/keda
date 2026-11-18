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
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MonitorClient queries a monitoring endpoint for the current value of a
// query. Values are returned as strings so that both numeric comparisons and
// textual regex matching are supported.
type MonitorClient interface {
	Query(ctx context.Context, query string) (string, error)
}

// HTTPMonitorClient is a minimal Prometheus-compatible monitoring client: it
// calls GET <address>/api/v1/query?query=<query> and reads data.result[0].
// value[1] (or data.result[0].metric.value for textual results).
type HTTPMonitorClient struct {
	Address    string
	HTTPClient *http.Client
}

type prometheusResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Value  []any `json:"value"`
			Metric struct {
				Value string `json:"value"`
			} `json:"metric"`
		} `json:"result"`
	} `json:"data"`
}

// Query executes the monitoring query and returns its scalar result.
func (c *HTTPMonitorClient) Query(ctx context.Context, query string) (string, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}

	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s",
		strings.TrimRight(c.Address, "/"), url.QueryEscape(query))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("monitoring query %q failed: %w", query, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("monitoring endpoint returned status %d: %s", response.StatusCode, string(body))
	}

	var parsed prometheusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding monitoring response: %w", err)
	}
	if parsed.Status != "success" {
		return "", fmt.Errorf("monitoring query %q failed: %s", query, parsed.Error)
	}
	if len(parsed.Data.Result) == 0 {
		return "", fmt.Errorf("monitoring query %q returned no result", query)
	}

	result := parsed.Data.Result[0]
	if len(result.Value) >= 2 {
		if text, ok := result.Value[1].(string); ok {
			return text, nil
		}
		return fmt.Sprint(result.Value[1]), nil
	}
	if result.Metric.Value != "" {
		return result.Metric.Value, nil
	}
	return "", fmt.Errorf("monitoring query %q returned an empty value", query)
}

// FakeMonitor is an in-process monitoring endpoint used for tests and local
// verification. Each query is configured with a fixed response sequence; the
// sequence advances on every call and, once exhausted, keeps returning its
// last value. It also implements MonitorClient directly, without a network
// hop, for unit tests.
type FakeMonitor struct {
	mu       sync.Mutex
	sequence map[string][]string
	calls    map[string]int
}

// NewFakeMonitor creates a fake monitor from a query -> response sequence map.
func NewFakeMonitor(sequence map[string][]string) *FakeMonitor {
	calls := make(map[string]int, len(sequence))
	for query := range sequence {
		calls[query] = 0
	}
	return &FakeMonitor{sequence: sequence, calls: calls}
}

// Query returns the next fixed value configured for the query.
func (f *FakeMonitor) Query(_ context.Context, query string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	values, ok := f.sequence[query]
	if !ok || len(values) == 0 {
		return "", fmt.Errorf("fake monitor has no sequence for query %q", query)
	}

	index := f.calls[query]
	if index >= len(values) {
		index = len(values) - 1
	}
	f.calls[query]++
	return values[index], nil
}

// Calls reports how many times a query has been executed.
func (f *FakeMonitor) Calls(query string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[query]
}

// Handler exposes the fake monitor as an HTTP endpoint speaking the same
// minimal Prometheus response format, so the real HTTPMonitorClient can be
// exercised end-to-end in tests.
func (f *FakeMonitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		value, err := f.Query(request.Context(), query)
		writer.Header().Set("Content-Type", "application/json")
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(writer).Encode(map[string]any{"status": "error", "error": err.Error()})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{
					{"value": []any{time.Now().Unix(), value}},
				},
			},
		})
	})
	return mux
}
