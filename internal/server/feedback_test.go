package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/rs/zerolog"
)

// feedbackServer returns a server whose feedback.log lands in a temp cwd.
func feedbackServer(t *testing.T, todoPath string) *Server {
	t.Helper()
	t.Chdir(t.TempDir())
	cfg := &config.Config{
		Feedback: config.FeedbackConfig{TodoPath: todoPath},
		Agents:   []config.Agent{{ID: "tester", TokenSHA: tokenSHA("tok")}},
	}
	return New(cfg, nil, "", zerolog.Nop())
}

func tokenSHA(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func postFeedback(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	s.handleFeedback(w, req)
	return w
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func wantResp(t *testing.T, w *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if w.Code != code || strings.TrimSpace(w.Body.String()) != body {
		t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), code, body)
	}
}

func item(text string) string {
	return "- [ ] " + text + " (" + todayUTC() + ", via /feedback by tester)."
}

func TestFeedbackRequiresValidToken(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	for name, auth := range map[string]string{"none": "", "wrong": "Bearer nope", "scheme": "Basic tok"} {
		req := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(`{"type":"todo","text":"injected"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		s.handleFeedback(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code=%d, want 401", name, w.Code)
		}
	}
	if _, err := os.Stat(todo); err == nil {
		t.Fatal("TODO written without auth")
	}
	if _, err := os.Stat("feedback.log"); err == nil {
		t.Fatal("feedback.log written without auth")
	}
}

func TestFeedbackTodoAppendedToTodoFile(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	os.WriteFile(todo, []byte("# TODO\r\n\r\n- [ ] old\r\n"), 0644)
	s := feedbackServer(t, todo)

	wantResp(t, postFeedback(s, `{"type":"todo","text":"  fix the\nthing "}`), 200, `{"status":"added"}`)
	if got, want := readFile(t, todo), "# TODO\r\n\r\n- [ ] old\r\n"+item("fix the thing")+"\r\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | ") {
		t.Fatal("feedback.log not written")
	}
}

func TestFeedbackMissingTrailingNewline(t *testing.T) {
	for _, tc := range []struct{ name, in, eol string }{
		{"lf", "# TODO\n- [ ] old", "\n"},
		{"crlf", "# TODO\r\n- [ ] old", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			todo := filepath.Join(t.TempDir(), "TODO.md")
			os.WriteFile(todo, []byte(tc.in), 0644)
			s := feedbackServer(t, todo)
			wantResp(t, postFeedback(s, `{"type":"todo","text":"x"}`), 200, `{"status":"added"}`)
			if got, want := readFile(t, todo), tc.in+tc.eol+item("x")+tc.eol; got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}

func TestFeedbackTodoDuplicates(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	initial := "# TODO\n- [ ] fix the thing (2026-01-01, via /feedback).\n- [x] done one\n- [X] done two (2026-02-02, via /feedback).\n- [ ] foo (bar)\n"
	os.WriteFile(todo, []byte(initial), 0644)
	s := feedbackServer(t, todo)

	for _, text := range []string{"fix the\nthing", "done one", "done two"} {
		wantResp(t, postFeedback(s, `{"type":"todo","text":"`+strings.ReplaceAll(text, "\n", `\n`)+`"}`), 200, `{"status":"duplicate"}`)
	}
	if readFile(t, todo) != initial {
		t.Fatal("duplicate modified TODO")
	}
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | done one") {
		t.Fatal("feedback.log not written")
	}
	// "foo" is not the same item as "foo (bar)".
	wantResp(t, postFeedback(s, `{"type":"todo","text":"foo"}`), 200, `{"status":"added"}`)
	if got, want := readFile(t, todo), initial+item("foo")+"\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFeedbackLFAndMissingFileCreated(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	postFeedback(s, `{"type":"todo","text":"a"}`)
	postFeedback(s, `{"type":"todo","text":"b"}`)
	if got, want := readFile(t, todo), "# TODO\n\n"+item("a")+"\n"+item("b")+"\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFeedbackNonTodoLeavesTodoUntouched(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	wantResp(t, postFeedback(s, `{"type":"bug","text":"x"}`), 200, `{"status":"recorded"}`)
	if _, err := os.Stat(todo); err == nil {
		t.Fatal("TODO created for non-todo type")
	}
}

func TestFeedbackKeyUnsetLeavesBehaviour(t *testing.T) {
	s := feedbackServer(t, "")
	wantResp(t, postFeedback(s, `{"type":"todo","text":"x"}`), 200, `{"status":"recorded"}`)
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | x") {
		t.Fatal("feedback.log not written")
	}
}

func TestFeedbackLogReplacesCR(t *testing.T) {
	s := feedbackServer(t, "")
	postFeedback(s, `{"type":"bug","text":"a\rb\nc"}`)
	if !strings.HasSuffix(readFile(t, "feedback.log"), "| bug | agent=tester | a b c\r\n") {
		t.Fatalf("log %q", readFile(t, "feedback.log"))
	}
}

func TestFeedbackTodoEmptyAndCap(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	if w := postFeedback(s, `{"type":"todo","text":" \n "}`); w.Code != 400 {
		t.Fatalf("empty: code=%d", w.Code)
	}
	wantResp(t, postFeedback(s, `{"type":"todo","text":"`+strings.Repeat("z", 2000)+`"}`), 200, `{"status":"added"}`)
	if got, want := readFile(t, todo), "# TODO\n\n"+item(strings.Repeat("z", 400))+"\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFeedbackTodoStripsControlAndBidi(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	wantResp(t, postFeedback(s, `{"type":"todo","text":"a\u0000b \u001b[31m‮c​d\te"}`), 200, `{"status":"added"}`)
	if got, want := readFile(t, todo), "# TODO\n\n"+item("ab [31mcd e")+"\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFeedbackOversizeBody413(t *testing.T) {
	s := feedbackServer(t, filepath.Join(t.TempDir(), "TODO.md"))
	w := postFeedback(s, `{"type":"todo","text":"`+strings.Repeat("a", 70<<10)+`"}`)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestFeedbackTodoFileOverOneMB500(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	big := strings.Repeat("x", 1<<20+1)
	os.WriteFile(todo, []byte(big), 0644)
	s := feedbackServer(t, todo)
	w := postFeedback(s, `{"type":"todo","text":"x"}`)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "over 1 MB") {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if readFile(t, todo) != big {
		t.Fatal("oversize file modified")
	}
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | x") {
		t.Fatal("feedback.log not written")
	}
}

func TestFeedbackTodoMissingParentIs500AndLogKept(t *testing.T) {
	s := feedbackServer(t, filepath.Join(t.TempDir(), "nodir", "TODO.md"))
	if w := postFeedback(s, `{"type":"todo","text":"x"}`); w.Code != 500 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(readFile(t, "feedback.log"), "| todo | agent=tester | x") {
		t.Fatal("feedback.log not written")
	}
}

// Same text posted concurrently must be added exactly once.
func TestFeedbackTodoConcurrentSameTextAddedOnce(t *testing.T) {
	todo := filepath.Join(t.TempDir(), "TODO.md")
	s := feedbackServer(t, todo)
	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := postFeedback(s, `{"type":"todo","text":"same"}`)
			if strings.Contains(w.Body.String(), "added") {
				mu.Lock()
				added++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if n := strings.Count(readFile(t, todo), "- [ ] same"); n != 1 || added != 1 {
		t.Fatalf("lines=%d added=%d", n, added)
	}
}
