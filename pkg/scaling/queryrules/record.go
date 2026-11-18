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
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// TriggerRecord is the durable, observable record of a single rule
// evaluation. Records are kept in memory, and optionally appended to a JSON
// Lines file, so that an operator can prove that a specific rule (and its
// operator) actually triggered a scale-out.
type TriggerRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Tenant     string    `json:"tenant"`
	Rule       string    `json:"rule"`
	Parser     string    `json:"parser"`
	Operator   string    `json:"operator"`
	Expression string    `json:"expression"`
	Query      string    `json:"query"`
	Value      string    `json:"value"`
	Matched    bool      `json:"matched"`
	ScaledOut  bool      `json:"scaledOut"`
	Error      string    `json:"error,omitempty"`
}

// TriggerRecorder stores trigger records in memory and, when configured,
// mirrors them to a JSON Lines file.
type TriggerRecorder struct {
	mu       sync.Mutex
	records  []TriggerRecord
	sinkPath string
	sinkFile *os.File
}

// NewTriggerRecorder creates a recorder. When sinkPath is non-empty every
// record is additionally appended to the file as one JSON object per line.
func NewTriggerRecorder(sinkPath string) (*TriggerRecorder, error) {
	recorder := &TriggerRecorder{sinkPath: sinkPath}
	if sinkPath == "" {
		return recorder, nil
	}

	file, err := os.OpenFile(sinkPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	recorder.sinkFile = file
	return recorder, nil
}

// Add appends a record. The timestamp is populated when unset.
func (r *TriggerRecorder) Add(record TriggerRecord) error {
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, record)

	if r.sinkFile != nil {
		var builder strings.Builder
		encoder := json.NewEncoder(&builder)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(record); err != nil {
			return err
		}
		line := strings.TrimRight(builder.String(), "\n")
		if _, err := r.sinkFile.Write(append([]byte(line), '\n')); err != nil {
			return err
		}
		if err := r.sinkFile.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// Records returns a copy of all stored trigger records.
func (r *TriggerRecorder) Records() []TriggerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	records := make([]TriggerRecord, len(r.records))
	copy(records, r.records)
	return records
}

// MatchedRecords returns only the records whose condition matched, i.e. the
// evidence that a rule triggered a scale-out.
func (r *TriggerRecorder) MatchedRecords() []TriggerRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matched []TriggerRecord
	for _, record := range r.records {
		if record.Matched {
			matched = append(matched, record)
		}
	}
	return matched
}

// Close releases the optional sink file.
func (r *TriggerRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sinkFile != nil {
		err := r.sinkFile.Close()
		r.sinkFile = nil
		return err
	}
	return nil
}
