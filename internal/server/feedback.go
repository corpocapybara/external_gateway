package server

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxTodoTextLen   = 400
	maxFeedbackBody  = 64 << 10
	maxTodoFileBytes = 1 << 20
)

var (
	feedbackTodoMu  sync.Mutex
	errTodoTooLarge = errors.New("TODO file is over 1 MB, refusing to append")
	todoSuffixRe    = regexp.MustCompile(`^ \(\d{4}-\d{2}-\d{2}, via /feedback\)\.$`)
)

func todayUTC() string { return time.Now().UTC().Format("2006-01-02") }

// normalizeTodoText drops control and format characters (NUL, ESC, bidi
// overrides, ...) and collapses whitespace runs, newlines included, to single spaces.
func normalizeTodoText(s string) string {
	s = strings.Map(func(r rune) rune {
		if !unicode.IsSpace(r) && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// hasTodoItem reports whether a TODO line is a checklist item that is exactly
// "<text>" or "<text> (YYYY-MM-DD, via /feedback)." after the checkbox prefix.
func hasTodoItem(line, text string) bool {
	line = normalizeTodoText(line)
	for _, p := range []string{"- [ ] ", "- [x] ", "- [X] "} {
		if rest, ok := strings.CutPrefix(line, p); ok {
			if rest == text {
				return true
			}
			suffix, ok := strings.CutPrefix(rest, text)
			return ok && todoSuffixRe.MatchString(suffix)
		}
	}
	return false
}

// appendTodo appends "- [ ] <text> (date, via /feedback)." to the file at path
// with O_APPEND, without rewriting it. It reports added=false when an item with
// the same text already exists. A missing file is created with a "# TODO"
// header; a missing parent directory is an error. CRLF is used only when the
// file's first line ending is CRLF.
func appendTodo(path, text string) (added bool, err error) {
	feedbackTodoMu.Lock()
	defer feedbackTodoMu.Unlock()

	data, err := os.ReadFile(path)
	prefix, eol := "", "\n"
	switch {
	case errors.Is(err, os.ErrNotExist):
		prefix = "# TODO\n\n"
	case err != nil:
		return false, err
	default:
		if len(data) > maxTodoFileBytes {
			return false, errTodoTooLarge
		}
		content := string(data)
		if i := strings.Index(content, "\n"); i > 0 && content[i-1] == '\r' {
			eol = "\r\n"
		}
		for _, line := range strings.Split(content, "\n") {
			if hasTodoItem(line, text) {
				return false, nil
			}
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			prefix = eol
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	line := prefix + "- [ ] " + text + " (" + todayUTC() + ", via /feedback)." + eol
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}
