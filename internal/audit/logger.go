package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type Logger struct {
	mu       sync.Mutex
	log      zerolog.Logger
	dir      string
	maxSize  int64
	sequence uint64
}

type Record struct {
	Timestamp   string                 `json:"ts"`
	RequestID   string                 `json:"request_id"`
	AgentID     string                 `json:"agent_id"`
	Workspace   string                 `json:"workspace"`
	Operation   string                 `json:"operation"`
	Params      map[string]interface{} `json:"params"`
	Decision    string                 `json:"decision"`
	Rule        string                 `json:"rule,omitempty"`
	Detail      string                 `json:"detail,omitempty"`
	UpstreamStatus int                  `json:"upstream_status,omitempty"`
	LatencyMS   int64                  `json:"latency_ms"`
	Error       string                 `json:"error,omitempty"`
}

var globalLogger *Logger

func Init(dir string, maxSizeMB int) error {
	if globalLogger != nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating audit dir: %w", err)
	}
	globalLogger = &Logger{
		dir:     dir,
		maxSize: int64(maxSizeMB) * 1024 * 1024,
	}
	return nil
}

func GetLogger() *Logger {
	return globalLogger
}

func (l *Logger) Log(record *Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if record.RequestID == "" {
		record.RequestID = uuid.New().String()
	}
	if record.Timestamp == "" {
		record.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshaling audit record: %w", err)
	}

	filename := filepath.Join(l.dir, fmt.Sprintf("audit_%s.log", time.Now().UTC().Format("20060102")))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening audit file: %w", err)
	}
	defer f.Close()

	info, _ := f.Stat()
	if info.Size() >= l.maxSize {
		f.Close()
		filename = filepath.Join(l.dir, fmt.Sprintf("audit_%s_%d.log", time.Now().UTC().Format("20060102"), l.sequence))
		l.sequence++
		f, err = os.Create(filename)
		if err != nil {
			return fmt.Errorf("creating audit file: %w", err)
		}
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing audit record: %w", err)
	}

	return nil
}

func (l *Logger) LogAllow(requestID, agentID, workspace, operation string, params map[string]interface{}, latencyMS int64, upstreamStatus int) error {
	return l.Log(&Record{
		RequestID:       requestID,
		AgentID:         agentID,
		Workspace:       workspace,
		Operation:       operation,
		Params:          sanitizeParams(params),
		Decision:        "allow",
		UpstreamStatus:  upstreamStatus,
		LatencyMS:       latencyMS,
	})
}

func (l *Logger) LogDeny(requestID, agentID, workspace, operation, rule, detail string) error {
	return l.Log(&Record{
		RequestID: requestID,
		AgentID:   agentID,
		Workspace: workspace,
		Operation: operation,
		Decision:  "deny",
		Rule:       rule,
		Detail:     detail,
	})
}

func sanitizeParams(params map[string]interface{}) map[string]interface{} {
	if params == nil {
		return nil
	}
	sanitized := make(map[string]interface{})
	for k, v := range params {
		sanitized[k] = v
	}
	return sanitized
}
