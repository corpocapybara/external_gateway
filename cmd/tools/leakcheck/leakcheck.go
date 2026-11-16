package main

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// allowMarker on a content or message line skips that line for every term (design D5, acceptance "marker").
const allowMarker = "leakcheck:allow"

// termsRel is the gitignored terms file, relative to the main checkout (design D4).
const termsRel = ".ai/leakcheck-terms.txt"

const hint = "leakcheck: commit refused (EG0047): remove the names above, or add " + allowMarker +
	" to a line that is a deliberate false positive"

// publicHosts are public service hosts that configs name but that identify no workspace. Exact hosts, not
// domain suffixes: a tenant subdomain of a public SaaS domain names the tenant and stays a term (design D4).
var publicHosts = map[string]bool{
	"api.github.com": true, "github.com": true, "gitlab.com": true,
	"api.bitbucket.org": true, "bitbucket.org": true,
	"api.trello.com": true, "trello.com": true,
	"slack.com": true, "api.slack.com": true,
	"api.clockify.me": true, "api.tempo.io": true,
	"mcp.atlassian.com": true, "api.atlassian.com": true,
	"docs.googleapis.com": true, "oauth2.googleapis.com": true, "www.googleapis.com": true,
	"graph.microsoft.com": true, "login.microsoftonline.com": true,
	"imap.gmail.com": true, "smtp.gmail.com": true, "outlook.office365.com": true,
}

var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

var urlRE = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

var hunkRE = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// git runs a git command in dir with the inherited environment (inside a hook that is the committing
// worktree's GIT_DIR / GIT_INDEX_FILE, design D8) and returns its stdout.
func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func gitCommonDir(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// mainCheckout is the parent of the common git dir: the main checkout for the main checkout itself and for
// every linked worktree (design D8).
func mainCheckout(dir string) (string, error) {
	common, err := gitCommonDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

// loadTerms reads the terms file and the config hosts of the main checkout root (design D4). The result is
// lower-cased, de-duplicated and sorted. A missing or unreadable file is an error (fail closed, D9).
func loadTerms(root string) ([]string, error) {
	path := filepath.Join(root, filepath.FromSlash(termsRel))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("terms file %s: %v", path, err)
	}
	set := map[string]bool{}
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ToLower(line)] = true
	}
	configs, err := filepath.Glob(filepath.Join(root, "config.*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("config glob: %v", err)
	}
	for _, c := range configs {
		data, err := os.ReadFile(c)
		if err != nil {
			return nil, fmt.Errorf("config %s: %v", c, err)
		}
		for _, h := range configHosts(string(data)) {
			set[h] = true
		}
	}
	terms := make([]string, 0, len(set))
	for t := range set {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	return terms, nil
}

// configHosts returns the lower-cased host of every http(s) URL in text, minus public and loopback hosts.
// A text scan, not a YAML decode, so a broken config still yields its hosts.
func configHosts(text string) []string {
	var hosts []string
	for _, m := range urlRE.FindAllString(text, -1) {
		u, err := url.Parse(m)
		if err != nil {
			continue
		}
		h := strings.ToLower(u.Hostname())
		if h == "" || publicHosts[h] || loopbackHosts[h] {
			continue
		}
		hosts = append(hosts, h)
	}
	return hosts
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// contains reports whether the lower-cased line low holds term t (lower-cased), literally, with a word
// boundary at each term edge that is a word rune. Offsets and neighbours are both taken in low (design D5).
func contains(low, t string) bool {
	first, _ := utf8.DecodeRuneInString(t)
	last, _ := utf8.DecodeLastRuneInString(t)
	for from := 0; from <= len(low)-len(t); {
		i := strings.Index(low[from:], t)
		if i < 0 {
			return false
		}
		i += from
		ok := true
		if isWord(first) && i > 0 {
			if r, _ := utf8.DecodeLastRuneInString(low[:i]); isWord(r) {
				ok = false
			}
		}
		if ok && isWord(last) {
			if j := i + len(t); j < len(low) {
				if r, _ := utf8.DecodeRuneInString(low[j:]); isWord(r) {
					ok = false
				}
			}
		}
		if ok {
			return true
		}
		from = i + 1
	}
	return false
}

// matchTerms returns the terms found in s, in terms order, each once.
func matchTerms(s string, terms []string) []string {
	low := strings.ToLower(s)
	var found []string
	for _, t := range terms {
		if contains(low, t) {
			found = append(found, t)
		}
	}
	return found
}

// lineHits is matchTerms for a content or message line: a line carrying the allow marker has no hits.
func lineHits(line string, terms []string) []string {
	if strings.Contains(line, allowMarker) {
		return nil
	}
	return matchTerms(line, terms)
}

// report prints one line per hit plus the hint and returns the exit code.
func report(stderr io.Writer, hits []string) int {
	if len(hits) == 0 {
		return 0
	}
	for _, h := range hits {
		fmt.Fprintf(stderr, "leakcheck: %s\n", h)
	}
	fmt.Fprintln(stderr, hint)
	return 1
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "leakcheck: error: %v\n", err)
	return 1
}

// diffBase is HEAD, or the empty tree before the first commit (design D6).
func diffBase(dir string) (string, error) {
	if _, err := git(dir, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		return "HEAD", nil
	}
	out, err := git(dir, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runPreCommit(dir string, stderr io.Writer) int {
	root, err := mainCheckout(dir)
	if err != nil {
		return fail(stderr, err)
	}
	terms, err := loadTerms(root)
	if err != nil {
		return fail(stderr, err)
	}
	// Design D7: a conflicted merge's diff is the other side's change, checked when it was committed.
	mh, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(mh))); err == nil {
		return 0
	}
	base, err := diffBase(dir)
	if err != nil {
		return fail(stderr, err)
	}
	// No -C: a copy then shows as an added file, so every line it brings in is checked, also when it was
	// copied verbatim from a file that already leaks (review round 1).
	names, err := git(dir, "-c", "core.quotePath=false", "diff", "--cached", "--name-status", "-z", "-M", base)
	if err != nil {
		return fail(stderr, err)
	}
	hits := pathHits(names, terms)
	diff, err := git(dir, "-c", "core.quotePath=false", "diff", "--cached", "-U0", "--no-color", "--no-ext-diff",
		"-M", "--src-prefix=a/", "--dst-prefix=b/", base)
	if err != nil {
		return fail(stderr, err)
	}
	hits = append(hits, diffHits(diff, terms)...)
	return report(stderr, hits)
}

// pathHits checks the new path of each added, renamed or copied entry of `git diff --name-status -z`
// output: records A\0path\0, R100\0old\0new\0, and C075\0src\0new\0 should -C ever be passed (design D6).
func pathHits(out []byte, terms []string) []string {
	fields := strings.Split(string(out), "\x00")
	var hits []string
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		var path string
		switch status[0] {
		case 'R', 'C':
			if i+1 < len(fields) {
				path = fields[i+1]
			}
			i += 2
		case 'A':
			if i < len(fields) {
				path = fields[i]
			}
			i++
		default:
			i++
		}
		if path == "" {
			continue
		}
		for _, t := range matchTerms(path, terms) {
			hits = append(hits, fmt.Sprintf("%s: path contains %s", path, t))
		}
	}
	return hits
}

// diffHits scans the added lines of a -U0 diff. Headers are recognised only outside a hunk; a hunk ends when
// its old and new line counts are used up, so an added "+++x" line is content (design D6).
func diffHits(out []byte, terms []string) []string {
	var hits []string
	path := ""
	inHunk := false
	oldLeft, newLeft, lineNo := 0, 0, 0
	for _, raw := range bytes.Split(out, []byte("\n")) {
		line := strings.TrimSuffix(string(raw), "\r")
		if inHunk {
			switch {
			case strings.HasPrefix(line, "+"):
				for _, t := range lineHits(line[1:], terms) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", path, lineNo, t))
				}
				lineNo++
				newLeft--
			case strings.HasPrefix(line, "-"):
				oldLeft--
			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file": not a line of either side.
			case strings.HasPrefix(line, " "):
				oldLeft--
				newLeft--
				lineNo++
			default:
				inHunk = false
			}
			if inHunk {
				if oldLeft <= 0 && newLeft <= 0 {
					inHunk = false
				}
				continue
			}
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path = ""
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimSuffix(line[len("+++ "):], "\t")
			if strings.HasPrefix(p, `"`) {
				if u, err := strconv.Unquote(p); err == nil {
					p = u
				}
			}
			if p == "/dev/null" {
				path = ""
			} else {
				path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@ "):
			m := hunkRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			oldLeft, newLeft = count(m[1]), count(m[3])
			lineNo, _ = strconv.Atoi(m[2])
			inHunk = oldLeft > 0 || newLeft > 0
		}
		// Every other line outside a hunk (index, mode, rename/copy, similarity, binary, "\ No newline")
		// is skipped.
	}
	return hits
}

// count is a hunk header count; a missing count means 1.
func count(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}

func runCommitMsg(dir, file string, stderr io.Writer) int {
	root, err := mainCheckout(dir)
	if err != nil {
		return fail(stderr, err)
	}
	terms, err := loadTerms(root)
	if err != nil {
		return fail(stderr, err)
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fail(stderr, err)
	}
	var hits []string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "#") {
			if strings.Contains(line, "------------------------ >8 ------------------------") {
				break
			}
			continue
		}
		for _, t := range lineHits(line, terms) {
			hits = append(hits, fmt.Sprintf("commit message:%d: %s", i+1, t))
		}
	}
	return report(stderr, hits)
}
