package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/external_gateway/internal/config"
	"gopkg.in/yaml.v3"
)

// Filing /feedback todos into an adopted repo R (T0048): through the ToDo tool when it
// runs, else by writing the draft file ourselves as the todo rule's "Without the tool"
// section says. Before R commits todo/_config.yaml the legacy list gets the item (appendTodo).

const (
	todoPriority  = "P2"
	todoSize      = "S"
	todoTitleCap  = 120
	todoConfig    = "todo/_config.yaml"
	todoPriorityF = "todo/PRIORITY.md"
	gitIdentity   = "egw-feedback"
	gitEmail      = "egw-feedback@localhost"
)

// errTodoNotFiled is the one client-facing failure of the todo path; the cause goes to the log.
var errTodoNotFiled = errors.New("feedback logged, but the todo was not filed")

// todoRun runs argv in dir with env and returns stdout. A command that started and
// exited non-zero returns *cmdExitError; a context timeout returns the context error;
// anything else is a start error. Tests replace it.
var todoRun = runTodoCmd

// todoToolTimeout bounds every todoRun call (adopted check, built-in writer git calls, tool).
var todoToolTimeout = 30 * time.Second

// beforeTodoCreate runs between choosing the new id and creating its file (tests only).
var beforeTodoCreate = func(path string) {}

type cmdExitError struct {
	code   int
	stderr string
}

func (e *cmdExitError) Error() string {
	return fmt.Sprintf("exit status %d: %s", e.code, strings.TrimSpace(e.stderr))
}

func runTodoCmd(ctx context.Context, argv, env []string, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = env, dir
	// After a timeout kills the child, stop waiting for grandchildren (hooks, git under
	// python) that still hold the output pipes, so todoToolTimeout really bounds the call.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s: %w", argv[0], ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, &cmdExitError{code: ee.ExitCode(), stderr: stderr.String()}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// os/exec returns ErrWaitDelay only when the child itself exited 0 and a grandchild
		// kept the pipes open: a success, never "the tool did not start".
		return out, nil
	}
	return out, err
}

// exitCode reports the exit code of a command that started and exited, ok=false otherwise.
func exitCode(err error) (int, bool) {
	var ce *cmdExitError
	if errors.As(err, &ce) {
		return ce.code, true
	}
	return 0, false
}

// isStartError reports a command that never ran (not found, not executable), as opposed to
// one that exited non-zero or timed out.
func isStartError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := exitCode(err); ok {
		return false
	}
	return !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
}

// todoEnv is the environment of every git and python child: fixed identity, safe.directory for R
// by command-scope config, and PATH prefixed with the directory of todo_python (LocalSystem has no
// user PATH, and R's hooks look for python on PATH).
func todoEnv(fb config.FeedbackConfig) []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME="+gitIdentity,
		"GIT_COMMITTER_NAME="+gitIdentity,
		"GIT_AUTHOR_EMAIL="+gitEmail,
		"GIT_COMMITTER_EMAIL="+gitEmail,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0="+filepath.ToSlash(fb.TodoRepo),
		"PATH="+filepath.Dir(fb.TodoPython)+string(filepath.ListSeparator)+os.Getenv("PATH"),
	)
}

func todoCall(ctx context.Context, fb config.FeedbackConfig, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, todoToolTimeout)
	defer cancel()
	return todoRun(ctx, argv, todoEnv(fb), fb.TodoRepo)
}

// todoAdopted reports whether todo/_config.yaml is committed in R's HEAD. The working tree and
// index are not consulted. Any unexpected git result is an error, never "not adopted".
func todoAdopted(ctx context.Context, fb config.FeedbackConfig) (bool, error) {
	_, err := todoCall(ctx, fb, "git", "rev-parse", "--verify", "-q", "HEAD")
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil // unborn HEAD
	}
	if err != nil {
		return false, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	out, err := todoCall(ctx, fb, "git", "ls-tree", "HEAD", "--", todoConfig)
	if err != nil {
		return false, fmt.Errorf("git ls-tree HEAD %s: %w", todoConfig, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// recordTodo files a todo-type feedback item in fb.TodoRepo: as a draft todo once the repo is
// adopted, else appended to the legacy list. It returns the response status ("added" or
// "duplicate") and the new todo id (todo path only); a non-nil error is the client-facing 500
// text, the cause is logged.
func (s *Server) recordTodo(ctx context.Context, fb config.FeedbackConfig, text, agentID string) (status, id string, err error) {
	adopted, err := todoAdopted(ctx, fb)
	if err != nil {
		s.logger.Error().Err(err).Str("repo", fb.TodoRepo).Msg("feedback: todo adoption check failed")
		return "", "", errTodoNotFiled
	}
	if adopted {
		if id, err = fileTodo(ctx, fb, text, agentID); err != nil {
			s.logger.Error().Err(err).Str("repo", fb.TodoRepo).Msg("feedback: todo not filed")
			return "", "", errTodoNotFiled
		}
		if id == "" {
			return "duplicate", "", nil
		}
		return "added", id, nil
	}
	path := fb.LegacyTodoPath()
	added, err := appendTodo(path, text, agentID)
	if err != nil {
		s.logger.Error().Err(err).Str("path", path).Msg("feedback: todo write failed")
		if errors.Is(err, errTodoTooLarge) {
			return "", "", errors.New("feedback logged, but the TODO file is over 1 MB so nothing was appended")
		}
		return "", "", errors.New("feedback logged, but writing the TODO file failed")
	}
	if !added {
		return "duplicate", "", nil
	}
	return "added", "", nil
}

// todoTitle cuts text at the last space within todoTitleCap runes, hard at the cap without one.
func todoTitle(text string) string {
	r := []rune(text)
	if len(r) <= todoTitleCap {
		return text
	}
	lim := r[:todoTitleCap+1]
	for i := len(lim) - 1; i > 0; i-- {
		if lim[i] == ' ' {
			return strings.TrimRight(string(r[:i]), " ")
		}
	}
	return string(r[:todoTitleCap])
}

func todoBody(text, agentID string) string {
	return text + "\n\nFiled via /feedback by " + agentID + " on " + todayUTC() + "."
}

// todoDuplicate reports whether a todo/T*.md file in R has a body whose first paragraph is text.
func todoDuplicate(repo, text string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(repo, "todo", "T*.md"))
	if err != nil {
		return false, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return false, err
		}
		if firstParagraph(string(data)) == text {
			return true, nil
		}
	}
	return false, nil
}

// firstParagraph is the normalised text after the front matter up to the first blank line.
func firstParagraph(doc string) string {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	i := 1
	for i < len(lines) && strings.TrimSpace(lines[i]) != "---" {
		i++
	}
	var para []string
	for _, l := range lines[min(i+1, len(lines)):] {
		if strings.TrimSpace(l) == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, l)
	}
	return normalizeTodoText(strings.Join(para, " "))
}

type todoRepoConfig struct {
	IDPrefix   string   `yaml:"id_prefix"`
	Priorities []string `yaml:"priorities"`
	Buckets    []struct {
		Name string `yaml:"name"`
	} `yaml:"buckets"`
}

func readTodoRepoConfig(repo string) (todoRepoConfig, error) {
	var c todoRepoConfig
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(todoConfig)))
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", todoConfig, err)
	}
	if c.IDPrefix == "" {
		c.IDPrefix = "T"
	}
	return c, nil
}

// fileTodo files text as a draft todo in the adopted repo, under feedbackTodoMu. It returns
// the new id, or "" for a duplicate.
func fileTodo(ctx context.Context, fb config.FeedbackConfig, text, agentID string) (string, error) {
	feedbackTodoMu.Lock()
	defer feedbackTodoMu.Unlock()
	dup, err := todoDuplicate(fb.TodoRepo, text)
	if err != nil || dup {
		return "", err
	}
	title, body := todoTitle(text), todoBody(text, agentID)
	if fb.TodoTool != "" {
		if st, err := os.Stat(fb.TodoTool); err == nil && st.Mode().IsRegular() {
			id, started, err := fileViaTool(ctx, fb, title, body)
			if started {
				return id, err
			}
		}
	}
	return fileByHand(ctx, fb, title, body)
}

// fileViaTool runs `todo new` and `todo commit`. started=false means `todo new` could not start
// (the tool is not available) and the caller falls back to the built-in writer; with
// started=true every failure is final.
func fileViaTool(ctx context.Context, fb config.FeedbackConfig, title, body string) (id string, started bool, err error) {
	base := []string{fb.TodoPython, "-I", "-X", "utf8", fb.TodoTool}
	out, err := todoCall(ctx, fb, append(base, "new", "--repo", fb.TodoRepo, "--by", "agent",
		"--title="+title, "--body="+body, "--bucket="+fb.TodoBucket,
		"--priority="+todoPriority, "--size="+todoSize)...)
	if isStartError(err) {
		return "", false, err
	}
	if err != nil {
		return "", true, fmt.Errorf("todo new: %w", err)
	}
	prefix := "[A-Z]+"
	if c, err := readTodoRepoConfig(fb.TodoRepo); err == nil {
		prefix = regexp.QuoteMeta(c.IDPrefix)
	}
	first, _, _ := strings.Cut(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	m := regexp.MustCompile(`^(` + prefix + `\d{4,}) created: `).FindStringSubmatch(first)
	if _, err := todoCall(ctx, fb, append(base, "commit", "--repo", fb.TodoRepo, "--by", "agent")...); err != nil {
		return "", true, fmt.Errorf("todo commit: %w", err)
	}
	if m == nil {
		return "", true, fmt.Errorf("todo new: no id in output %q", first)
	}
	return m[1], true, nil
}

// fileByHand writes todo/<ID>.md and its PRIORITY.md line and commits only those two paths.
// Any failure removes the new file and restores PRIORITY.md's bytes.
func fileByHand(ctx context.Context, fb config.FeedbackConfig, title, body string) (id string, err error) {
	repo := fb.TodoRepo
	c, err := readTodoRepoConfig(repo)
	if err != nil {
		return "", err
	}
	if !hasBucket(c, fb.TodoBucket) {
		return "", fmt.Errorf("bucket %q is not in %s", fb.TodoBucket, todoConfig)
	}
	n, err := maxTodoID(ctx, fb, c.IDPrefix)
	if err != nil {
		return "", err
	}
	id = fmt.Sprintf("%s%04d", c.IDPrefix, n+1)
	titleYAML, err := yamlScalar(title)
	if err != nil {
		return "", err
	}
	// Read PRIORITY.md after the (slow) id scan, so a todo committed meanwhile keeps its line.
	prioPath := filepath.Join(repo, filepath.FromSlash(todoPriorityF))
	prio, err := os.ReadFile(prioPath)
	if err != nil {
		return "", err
	}
	newPrio, err := priorityWithLine(string(prio), "- "+id+" · draft · "+fb.TodoBucket+" · "+todoSize+" — "+title)
	if err != nil {
		return "", err
	}
	now := time.Now().Format("2006-01-02T15:04")
	doc := "---\nid: " + id + "\ntitle: " + titleYAML + "\nstate: draft\npriority: " + todoPriority +
		"\nbucket: " + fb.TodoBucket + "\nsize: " + todoSize + "\nupdated: {by: agent, at: " + now +
		"}\nhistory:\n  - {at: " + now + ", by: agent, to: draft}\n---\n" + body + "\n"

	rel := "todo/" + id + ".md"
	path := filepath.Join(repo, filepath.FromSlash(rel))
	beforeTodoCreate(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	defer func() {
		if err == nil {
			return
		}
		// Undo: the new file goes, PRIORITY.md gets its old bytes back unless someone else
		// rewrote it since our write, and the two paths are unstaged. Undo failures join err.
		var undo []error
		if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
			undo = append(undo, e)
		}
		if cur, e := os.ReadFile(prioPath); e == nil && string(cur) == newPrio {
			if e := os.WriteFile(prioPath, prio, 0644); e != nil {
				undo = append(undo, e)
			}
		}
		if _, e := todoCall(context.WithoutCancel(ctx), fb, "git", "reset", "-q", "--", rel, todoPriorityF); e != nil {
			undo = append(undo, fmt.Errorf("git reset: %w", e))
		}
		if len(undo) > 0 {
			err = errors.Join(err, fmt.Errorf("undo incomplete: %w", errors.Join(undo...)))
		}
	}()
	if _, err = f.WriteString(doc); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.WriteFile(prioPath, []byte(newPrio), 0644); err != nil {
		return "", err
	}
	if _, err = todoCall(ctx, fb, "git", "add", "--", rel, todoPriorityF); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}
	subject := "todo: " + id + " created: " + title + " [hand]"
	if _, err = todoCall(ctx, fb, "git", "commit", "-q", "-m", subject, "-m", "Changed-By: agent",
		"--", rel, todoPriorityF); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	return id, nil
}

func hasBucket(c todoRepoConfig, name string) bool {
	for _, b := range c.Buckets {
		if b.Name == name {
			return true
		}
	}
	return false
}

var (
	numberLike = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	// YAML 1.1 words the tool's loader (PyYAML) reads as booleans or null.
	yaml11Words = regexp.MustCompile(`^(?:y|Y|yes|Yes|YES|n|N|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF|null|Null|NULL|~)$`)
)

// yamlScalar writes s the way the ToDo tool's canonical serializer does (todofmt/serialize.py
// scalar): plain when it reads back as the same string in block context, else JSON-quoted.
func yamlScalar(s string) (string, error) {
	plain := s != "" && s == strings.TrimSpace(s) && !numberLike.MatchString(s) && !yaml11Words.MatchString(s) &&
		!strings.ContainsFunc(s, func(r rune) bool {
			return r < 32 || (r >= 0x7F && r < 0xA0) || r == 0x2028 || r == 0x2029 || r == 0xFEFF
		})
	for _, doc := range []string{"k: " + s + "\n", "- " + s + "\n"} {
		if !plain {
			break
		}
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(doc), &n); err != nil || len(n.Content) != 1 || len(n.Content[0].Content) == 0 {
			plain = false
			break
		}
		v := n.Content[0].Content[len(n.Content[0].Content)-1]
		plain = v.Kind == yaml.ScalarNode && v.Style == 0 && v.ShortTag() == "!!str" && v.Value == s
	}
	if plain {
		return s, nil
	}
	return jsonString(s)
}

func jsonString(s string) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// maxTodoID is the highest id number in todo/, tasks/ and any todo:/tasks: commit subject.
func maxTodoID(ctx context.Context, fb config.FeedbackConfig, prefix string) (int, error) {
	idRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(prefix) + `(\d{4,})\b`)
	fileRe := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `(\d{4,})\.md$`)
	best := 0
	take := func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > best {
			best = n
		}
	}
	for _, dir := range []string{"todo", "tasks"} {
		entries, err := os.ReadDir(filepath.Join(fb.TodoRepo, dir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		for _, e := range entries {
			if m := fileRe.FindStringSubmatch(e.Name()); m != nil {
				take(m[1])
			}
		}
	}
	out, err := todoCall(ctx, fb, "git", "log", "--all", "--format=%s")
	if err != nil {
		return 0, fmt.Errorf("git log: %w", err)
	}
	for _, s := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(s, "todo:") || strings.HasPrefix(s, "tasks:") {
			for _, m := range idRe.FindAllStringSubmatch(s, -1) {
				take(m[1])
			}
		}
	}
	return best, nil
}

// priorityWithLine adds line as the last entry of the "## P2" section (a new draft has no
// `order` and the highest id, so it sorts last there), replacing a lone "none".
func priorityWithLine(doc, line string) (string, error) {
	lines := strings.Split(doc, "\n")
	head := -1
	for i, l := range lines {
		if strings.TrimRight(l, "\r") == "## "+todoPriority {
			head = i
			break
		}
	}
	if head < 0 {
		return "", fmt.Errorf("%s has no ## %s section", todoPriorityF, todoPriority)
	}
	end := len(lines)
	for i := head + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	last, none := -1, -1
	for i := head + 1; i < end; i++ {
		switch t := strings.TrimRight(lines[i], "\r"); {
		case strings.HasPrefix(t, "- "):
			last = i
		case t == "none":
			none = i
		}
	}
	switch {
	case last >= 0:
		lines = append(lines[:last+1], append([]string{line}, lines[last+1:]...)...)
	case none >= 0:
		lines[none] = line
	default:
		return "", fmt.Errorf("%s: ## %s section has neither entries nor none", todoPriorityF, todoPriority)
	}
	return strings.Join(lines, "\n"), nil
}
