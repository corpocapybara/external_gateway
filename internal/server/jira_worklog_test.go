package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors/httprest"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/workcal"
	"github.com/rs/zerolog"
)

// fakeJira serves the Jira Cloud endpoints the worklog connector calls and
// records every write.
type fakeJira struct {
	mu       sync.Mutex
	jql      []string
	writes   []string // "METHOD path body"
	worklogs map[string][]map[string]interface{}
	paged    bool // one issue per search page, one worklog per worklog page
}

func wl(id, author, started string, secs int) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "author": map[string]interface{}{"accountId": author, "displayName": "name-" + author},
		"started": started, "timeSpentSeconds": secs,
		"comment": map[string]interface{}{"type": "doc", "version": 1, "content": []interface{}{
			map[string]interface{}{"type": "paragraph", "content": []interface{}{map[string]interface{}{"type": "text", "text": "c" + id}}},
		}},
	}
}

func (f *fakeJira) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	switch {
	case p == "/rest/api/3/myself":
		w.Write([]byte(`{"accountId":"me","displayName":"Me"}`))
	case p == "/rest/api/3/search/jql":
		var req struct {
			JQL           string `json:"jql"`
			NextPageToken string `json:"nextPageToken"`
		}
		json.Unmarshal(body, &req)
		f.jql = append(f.jql, req.JQL)
		issue1 := `{"id":"1","key":"THP-1","fields":{"summary":"Feature","issuetype":{"name":"Story"},"parent":{"key":"THP-100"}}}`
		issue2 := `{"id":"2","key":"THP-2","fields":{"summary":"Reviews","issuetype":{"name":"Epic"}}}`
		switch {
		case !f.paged:
			w.Write([]byte(`{"isLast":true,"issues":[` + issue1 + `,` + issue2 + `]}`))
		case req.NextPageToken == "":
			w.Write([]byte(`{"isLast":false,"nextPageToken":"p2","issues":[` + issue1 + `]}`))
		default:
			w.Write([]byte(`{"isLast":true,"issues":[` + issue2 + `]}`))
		}
	case strings.HasSuffix(p, "/worklog") && r.Method == "GET":
		key := strings.Split(p, "/")[5]
		all := f.worklogs[key]
		page := all
		if f.paged {
			start := 0
			fmt.Sscan(r.URL.Query().Get("startAt"), &start)
			page = all[min(start, len(all)):min(start+1, len(all))]
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"total": len(all), "worklogs": page})
	case strings.HasSuffix(p, "/worklog") && r.Method == "POST":
		f.writes = append(f.writes, "POST "+p+" "+string(body))
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		out := wl("900", "me", req["started"].(string), int(req["timeSpentSeconds"].(float64)))
		json.NewEncoder(w).Encode(out)
	case strings.Contains(p, "/worklog/"):
		parts := strings.Split(p, "/")
		key, id := parts[5], parts[7]
		var found map[string]interface{}
		for _, x := range f.worklogs[key] {
			if x["id"] == id {
				found = x
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			json.NewEncoder(w).Encode(found)
		case "PUT":
			f.writes = append(f.writes, "PUT "+p+" "+string(body))
			var req map[string]interface{}
			json.Unmarshal(body, &req)
			json.NewEncoder(w).Encode(wl(id, "me", req["started"].(string), int(req["timeSpentSeconds"].(float64))))
		case "DELETE":
			f.writes = append(f.writes, "DELETE "+p)
			w.WriteHeader(http.StatusNoContent)
		}
	case strings.HasPrefix(p, "/rest/api/3/issue/"):
		key := strings.Split(p, "/")[5]
		fmt.Fprintf(w, `{"id":"5","key":%q,"fields":{"summary":"Epic of %s","issuetype":{"name":"Epic"},"status":{"name":"Open"},
			"timetracking":{"originalEstimateSeconds":36000,"remainingEstimateSeconds":7200,"timeSpentSeconds":30600}}}`, key, key)
	default:
		http.NotFound(w, r)
	}
}

func worklogFixture(t *testing.T) (*fakeJira, *Server, *config.Workspace, func(action string, params map[string]interface{}) (map[string]interface{}, error)) {
	t.Helper()
	f := &fakeJira{worklogs: map[string][]map[string]interface{}{
		"THP-1": {
			wl("11", "me", "2026-11-09T09:00:00.000+0100", 6*3600),    // Mon of the week
			wl("12", "other", "2026-11-09T10:00:00.000+0100", 2*3600), // someone else's
			wl("13", "me", "2026-11-12T00:30:00.000+0100", 1800),      // Thu, 00:30 Warsaw = 23:30 UTC the day before
			wl("14", "me", "2026-11-03T08:00:00.000+0100", 8*3600),    // earlier in the month
			wl("15", "me", "2026-10-30T08:00:00.000+0100", 8*3600),    // previous month
		},
		"THP-2": {
			wl("21", "me", "2026-11-10T13:00:00.000+0100", 5400),
		},
	}}
	upstream := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(upstream.Close)
	ws := &config.Workspace{
		Sites: map[string]config.Site{"jira": {DefaultEnv: "prod", Environments: map[string]config.Environment{
			"prod":  {BaseURL: upstream.URL},
			"corpo": {BaseURL: "http://corpo.invalid"},
		}}},
		Policy: config.WorkspacePolicy{JiraProjects: []string{"THP", "SUP"}},
	}
	s := &Server{logger: zerolog.Nop(), httpClient: httprest.NewConnector()}
	s.cfgPtr.Store(&config.Config{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")})
	run := func(action string, params map[string]interface{}) (map[string]interface{}, error) {
		op := &policy.Operation{ID: "jira.worklog_" + action, Connector: "jira-worklog", Site: "jira", Path: action}
		status, body, err := s.executeJiraWorklog(ws, op, params, "thp")
		if err != nil {
			return nil, err
		}
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out, nil
	}
	return f, s, ws, run
}

func num(t *testing.T, m map[string]interface{}, path ...string) float64 {
	t.Helper()
	var cur interface{} = m
	for _, p := range path {
		cur = cur.(map[string]interface{})[p]
	}
	v, ok := cur.(float64)
	if !ok {
		t.Fatalf("%v is %T, not a number", path, cur)
	}
	return v
}

func TestWorklogReportWeek(t *testing.T) {
	f, _, _, run := worklogFixture(t)
	out, err := run("report", map[string]interface{}{"period": "week", "date": "2026-11-12"})
	if err != nil {
		t.Fatal(err)
	}
	// Week 9-15 Nov 2026: 11 Nov is a holiday, so 4 working days = 32 h.
	// Mine in that week: 6 h + 1.5 h + 0.5 h = 8 h; the other author's 2 h is ignored.
	if got := num(t, out, "week", "norm_hours"); got != 32 {
		t.Errorf("week norm = %v", got)
	}
	if got := num(t, out, "week", "logged_hours"); got != 8 {
		t.Errorf("week logged = %v", got)
	}
	if got := num(t, out, "week", "left_hours"); got != 24 {
		t.Errorf("week left = %v", got)
	}
	// To date (Mon, Tue, Thu up to 12 Nov) = 24 h; balance 8 - 24.
	if got := num(t, out, "week", "norm_to_date_hours"); got != 24 {
		t.Errorf("week norm to date = %v", got)
	}
	if got := num(t, out, "week", "balance_to_date_hours"); got != -16 {
		t.Errorf("week balance = %v", got)
	}
	// November: 20 working days = 160 h; mine 8 h on 3 Nov + 8 h this week; 30 Oct is outside.
	if got := num(t, out, "month", "norm_hours"); got != 160 {
		t.Errorf("month norm = %v", got)
	}
	if got := num(t, out, "month", "logged_hours"); got != 16 {
		t.Errorf("month logged = %v", got)
	}
	if got := num(t, out, "month", "left_hours"); got != 144 {
		t.Errorf("month left = %v", got)
	}

	days := out["period"].(map[string]interface{})["days"].([]interface{})
	if len(days) != 7 {
		t.Fatalf("days = %d", len(days))
	}
	wed := days[2].(map[string]interface{})
	if wed["date"] != "2026-11-11" || wed["holiday"] != "Narodowe Święto Niepodległości" || wed["working_day"] != false {
		t.Errorf("11 Nov = %v", wed)
	}
	// The 00:30 Warsaw worklog belongs to Thursday 12 Nov, not Wednesday.
	thu := days[3].(map[string]interface{})
	if num(t, thu, "logged_hours") != 0.5 || num(t, wed, "logged_hours") != 0 {
		t.Errorf("thu = %v, wed = %v", thu["logged_hours"], wed["logged_hours"])
	}
	byIssue := out["period"].(map[string]interface{})["by_issue"].([]interface{})
	first := byIssue[0].(map[string]interface{})
	if first["key"] != "THP-1" || first["hours"] != 6.5 || first["parent_key"] != "THP-100" {
		t.Errorf("by_issue[0] = %v", first)
	}
	if len(f.jql) != 1 || !strings.HasSuffix(f.jql[0], "AND project in (THP,SUP)") || !strings.Contains(f.jql[0], "worklogAuthor = currentUser()") {
		t.Errorf("jql = %v", f.jql)
	}
}

// Every search page and worklog page is read: one issue and one worklog per page
// must give the same totals as the unpaged report.
func TestWorklogReportFollowsPages(t *testing.T) {
	f, _, _, run := worklogFixture(t)
	f.paged = true
	out, err := run("report", map[string]interface{}{"period": "week", "date": "2026-11-12"})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out, "week", "logged_hours"); got != 8 {
		t.Errorf("week logged = %v", got)
	}
	if got := num(t, out, "month", "logged_hours"); got != 16 {
		t.Errorf("month logged = %v", got)
	}
	if len(f.jql) != 2 {
		t.Errorf("search pages read = %d, want 2", len(f.jql))
	}
	out, err = run("issue", map[string]interface{}{"key": "THP-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out["worklogs"].([]interface{})); got != 5 {
		t.Errorf("issue worklogs = %d, want 5", got)
	}
}

func TestWorklogReportDayHasNoOtherDays(t *testing.T) {
	_, _, _, run := worklogFixture(t)
	out, err := run("report", map[string]interface{}{"period": "day", "date": "2026-11-09"})
	if err != nil {
		t.Fatal(err)
	}
	days := out["period"].(map[string]interface{})["days"].([]interface{})
	if len(days) != 1 || num(t, days[0].(map[string]interface{}), "logged_hours") != 6 {
		t.Fatalf("days = %v", days)
	}
	if num(t, out, "period", "norm", "norm_hours") != 8 {
		t.Errorf("day norm = %v", out["period"])
	}
}

func TestWorklogAddByActivity(t *testing.T) {
	f, _, _, run := worklogFixture(t)
	if _, err := run("add", map[string]interface{}{"activity": "code-review", "hours": 1.5}); err == nil {
		t.Fatal("unmapped activity must be refused")
	}
	if _, err := run("activity_set", map[string]interface{}{"name": "code-review", "key": "THP-2"}); err != nil {
		t.Fatal(err)
	}
	out, err := run("add", map[string]interface{}{"activity": "code-review", "hours": 1.5, "date": "2026-11-11", "comment": "MR !12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("writes = %v", f.writes)
	}
	w := f.writes[0]
	if !strings.HasPrefix(w, "POST /rest/api/3/issue/THP-2/worklog ") ||
		!strings.Contains(w, `"started":"2026-11-11T09:00:00.000+0100"`) ||
		!strings.Contains(w, `"timeSpentSeconds":5400`) || !strings.Contains(w, `"text":"MR !12"`) {
		t.Errorf("write = %s", w)
	}
	if out["note"] != "2026-11-11 is a public holiday (Narodowe Święto Niepodległości)" {
		t.Errorf("note = %v", out["note"])
	}
}

func TestWorklogAddRefusals(t *testing.T) {
	f, _, _, run := worklogFixture(t)
	cases := map[string]map[string]interface{}{
		"project out of scope": {"key": "OPS-1", "hours": 1.0},
		"bad key":              {"key": "THP-1/../x", "hours": 1.0},
		"key and activity":     {"key": "THP-1", "activity": "x", "hours": 1.0},
		"neither":              {"hours": 1.0},
		"no hours":             {"key": "THP-1"},
		"zero hours":           {"key": "THP-1", "hours": 0.0},
		"too many hours":       {"key": "THP-1", "hours": 25.0},
		"bad start":            {"key": "THP-1", "hours": 1.0, "start": "9:00"},
		"bad date":             {"key": "THP-1", "hours": 1.0, "date": "11/11/2026"},
		"other env":            {"key": "THP-1", "hours": 1.0, "env": "corpo"},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := run("add", params); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if len(f.writes) != 0 {
		t.Errorf("refused requests wrote: %v", f.writes)
	}
}

func TestWorklogUpdateAndDeleteOwnOnly(t *testing.T) {
	f, _, _, run := worklogFixture(t)
	if _, err := run("delete", map[string]interface{}{"key": "THP-1", "worklog_id": "12"}); err == nil || !strings.Contains(err.Error(), "only your own") {
		t.Fatalf("deleting another author's worklog: err = %v", err)
	}
	if _, err := run("update", map[string]interface{}{"key": "THP-1", "worklog_id": "12", "hours": 1.0}); err == nil {
		t.Fatal("updating another author's worklog must be refused")
	}
	if _, err := run("update", map[string]interface{}{"key": "THP-1", "worklog_id": "11"}); err == nil {
		t.Fatal("an update with nothing to change must be refused")
	}
	if len(f.writes) != 0 {
		t.Fatalf("refused requests wrote: %v", f.writes)
	}

	// Changing only the hours keeps the original start and comment.
	out, err := run("update", map[string]interface{}{"key": "THP-1", "worklog_id": "11", "hours": 7.25})
	if err != nil {
		t.Fatal(err)
	}
	w := f.writes[0]
	if !strings.HasPrefix(w, "PUT /rest/api/3/issue/THP-1/worklog/11 ") || !strings.Contains(w, `"started":"2026-11-09T09:00:00.000+0100"`) ||
		!strings.Contains(w, `"timeSpentSeconds":26100`) || !strings.Contains(w, `"text":"c11"`) {
		t.Errorf("write = %s", w)
	}
	if num(t, out, "before", "hours") != 6 || num(t, out, "worklog", "hours") != 7.25 {
		t.Errorf("update result = %v", out)
	}

	if _, err := run("delete", map[string]interface{}{"key": "THP-1", "worklog_id": "13"}); err != nil {
		t.Fatal(err)
	}
	if f.writes[1] != "DELETE /rest/api/3/issue/THP-1/worklog/13" {
		t.Errorf("delete = %s", f.writes[1])
	}
}

func TestWorklogIssueTotals(t *testing.T) {
	_, _, _, run := worklogFixture(t)
	out, err := run("issue", map[string]interface{}{"key": "THP-1"})
	if err != nil {
		t.Fatal(err)
	}
	// All authors: 6 + 2 + 0.5 + 8 + 8 = 24.5 h; mine 22.5 h.
	if num(t, out, "logged_hours") != 24.5 || num(t, out, "my_hours") != 22.5 {
		t.Errorf("logged = %v, mine = %v", out["logged_hours"], out["my_hours"])
	}
	if num(t, out, "time_tracking", "original_estimate_hours") != 10 || num(t, out, "time_tracking", "remaining_estimate_hours") != 2 {
		t.Errorf("time_tracking = %v", out["time_tracking"])
	}
	if len(out["worklogs"].([]interface{})) != 5 || len(out["by_author"].([]interface{})) != 2 {
		t.Errorf("worklogs = %d, authors = %v", len(out["worklogs"].([]interface{})), out["by_author"])
	}
	if _, err := run("issue", map[string]interface{}{"key": "OPS-7"}); err == nil {
		t.Error("issue outside jira_projects must be refused")
	}
}

func TestWorklogActivityMapPersists(t *testing.T) {
	_, s, _, run := worklogFixture(t)
	out, err := run("activities", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out["activities"].(map[string]interface{})) != 0 {
		t.Fatalf("map must start empty: %v", out)
	}
	if _, err := run("activity_set", map[string]interface{}{"name": "Code Review", "key": "THP-2"}); err == nil {
		t.Error("activity names must be lower-case slugs")
	}
	if _, err := run("activity_set", map[string]interface{}{"name": "support", "key": "OPS-2"}); err == nil {
		t.Error("an issue outside jira_projects must be refused")
	}
	if _, err := run("activity_set", map[string]interface{}{"name": "support", "key": "SUP-9"}); err != nil {
		t.Fatal(err)
	}
	acts, err := s.readWorklogActivities("thp")
	if err != nil {
		t.Fatal(err)
	}
	if a := acts["support"]; a.Key != "SUP-9" || a.IssueType != "Epic" || a.Summary != "Epic of SUP-9" {
		t.Errorf("stored = %+v", a)
	}
	if _, err := run("activity_remove", map[string]interface{}{"name": "support"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run("activity_remove", map[string]interface{}{"name": "support"}); err == nil {
		t.Error("removing a missing activity must fail")
	}
	out, _ = run("activities", nil)
	if len(out["activities"].(map[string]interface{})) != 0 {
		t.Errorf("after remove: %v", out)
	}
}

func TestPeriodRange(t *testing.T) {
	d := workcal.Date(2026, time.November, 1) // Sunday
	from, to := periodRange("week", d)
	if from.Format("2006-01-02") != "2026-10-26" || to.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("week = %s..%s", from, to)
	}
	from, to = periodRange("month", workcal.Date(2026, time.February, 15))
	if from.Format("2006-01-02") != "2026-02-01" || to.Format("2006-01-02") != "2026-02-28" {
		t.Errorf("month = %s..%s", from, to)
	}
}

func TestNormOvertimeAndFuturePeriod(t *testing.T) {
	from, to := periodRange("week", workcal.Date(2026, time.October, 26))
	n := norm(from, to, workcal.Date(2026, time.October, 30), 42*3600)
	if n.NormHours != 40 || n.LeftHours != 0 || n.OvertimeHours != 2 || n.BalanceToDate != 2 {
		t.Errorf("norm = %+v", n)
	}
	// As of a date before the period starts, nothing is due yet.
	n = norm(from, to, workcal.Date(2026, time.October, 20), 0)
	if n.NormToDateHours != 0 || n.LeftHours != 40 {
		t.Errorf("future norm = %+v", n)
	}
}
