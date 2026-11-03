package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/external_gateway/internal/secrets"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type Logger struct {
	mu       sync.Mutex
	log      zerolog.Logger
	dir      string
	maxSize  int64
	sequence uint64
	current  string // path of the active log file
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
	// A missing/zero max_size_mb must NOT mean "rotate on every record" — default
	// to a sane cap so an unset config doesn't spin up a new file (and leak a
	// descriptor) per audit line.
	if maxSizeMB <= 0 {
		maxSizeMB = 100
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

	// Pick the active file, rolling over on date change and rotating when the
	// CURRENT file (not a stale base name) reaches the size cap. A single file is
	// opened and closed per record — no reassigned-after-defer descriptor leak.
	today := time.Now().UTC().Format("20060102")
	base := filepath.Join(l.dir, fmt.Sprintf("audit_%s.log", today))
	if l.current == "" || !strings.Contains(filepath.Base(l.current), "audit_"+today) {
		l.current = base
		l.sequence = 0
	}
	if l.maxSize > 0 {
		if info, statErr := os.Stat(l.current); statErr == nil && info.Size() >= l.maxSize {
			l.sequence++
			l.current = filepath.Join(l.dir, fmt.Sprintf("audit_%s_%d.log", today, l.sequence))
		}
	}

	f, err := os.OpenFile(l.current, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening audit file: %w", err)
	}
	defer f.Close()

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

// sensitiveParamKeys are parameter names whose values are credentials or other
// secrets supplied inline by a caller (e.g. basic-auth `password`). Their values
// are replaced with a placeholder before the params map is written to the audit
// log, which is stored in cleartext.
//
// Matching is case-insensitive, by substring, and applies at every nesting depth.
// No operation in the shipped operations yaml has a param that merely contains
// one of these words without being a credential (checked: only `password`).
var sensitiveKeyParts = []string{
	"password", "passwd", "secret", "token", "apikey", "api_key", "api-key",
	"private_key", "privatekey", "authorization", "cookie", "credential",
}

// sensitiveExactKeys are credential names too short or generic for substring matching.
var sensitiveExactKeys = map[string]bool{"value": true, "pwd": true, "pass": true, "auth": true}

// harmlessKeySuffixes mark counters/metadata (e.g. `token_count`) that merely
// mention a credential word.
var harmlessKeySuffixes = []string{"_count", "_type", "_ttl", "_expires_in", "_length", "_len"}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, suf := range harmlessKeySuffixes {
		if strings.HasSuffix(lk, suf) {
			return false
		}
	}
	if sensitiveExactKeys[lk] {
		return true
	}
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lk, part) {
			return true
		}
	}
	return false
}

// sanitizeParams returns a deep copy of params with credential-named values
// replaced by "***" at any depth, and every remaining string run through the
// taint registry so a known secret value never reaches the cleartext audit log.
func sanitizeParams(params map[string]interface{}) map[string]interface{} {
	if params == nil {
		return nil
	}
	return sanitizeMap(params)
}

func sanitizeMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			out[k] = "***"
			continue
		}
		out[k] = sanitizeValue(v)
	}
	return out
}

func sanitizeValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return sanitizeMap(t)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = sanitizeValue(e)
		}
		return out
	case string:
		return secrets.GetTaintRegistry().RedactString(t)
	default:
		return v
	}
}
