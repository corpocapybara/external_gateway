package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
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

// fakeJira serves the Jira Cloud and Tempo endpoints the worklog connector calls
// and records every search, Tempo listing and write.
type fakeJira struct {
	mu       sync.Mutex
	jql      []string
	listings []string // Tempo listing "path?query"
	writes   []string // "METHOD path body"
	worklogs []map[string]interface{}
	paged    bool // one result per search page and per Tempo page
}

var (
	fakeIssueKeys  = map[string]string{"1": "THP-1", "2": "THP-2", "3": "OPS-3"}
	fakeIssueIDsRe = regexp.MustCompile(`id in \(([^)]*)\)`)
	fakeProjectsRe = regexp.MustCompile(`project in \(([^)]*)\)`)
)

func wl(id, issueID int, author, date, start string, secs int) map[string]interface{} {
	return map[string]interface{}{
		"tempoWorklogId": id, "issue": map[string]interface{}{"id": issueID},
		"author": map[string]interface{}{"accountId": author}, "startDate": date, "startTime": start,
		"timeSpentSeconds": secs, "billableSeconds": secs, "description": fmt.Sprint("c", id),
		"attributes": map[string]interface{}{"values": []interface{}{map[string]interface{}{"key": "_Role_", "value": "dev"}}},
	}
}

func fakeIssueID(key string) string {
	for id, k := range fakeIssueKeys {
		if k == key {
			return id
		}
	}
	return "5"
}

func (f *fakeJira) find(id string) map[string]interface{} {
	for _, x := range f.worklogs {
		if fmt.Sprint(x["tempoWorklogId"]) == id {
			return x
		}
	}
	return nil
}

// page serves Tempo results by offset and limit, one per page when paged.
func (f *fakeJira) page(w http.ResponseWriter, r *http.Request, all []map[string]interface{}) {
	f.listings = append(f.listings, r.URL.Path+"?"+r.URL.RawQuery)
	offset, limit := 0, 50
	fmt.Sscan(r.URL.Query().Get("offset"), &offset)
	fmt.Sscan(r.URL.Query().Get("limit"), &limit)
	if f.paged {
		limit = 1
	}
	end := min(offset+limit, len(all))
	results := all[min(offset, len(all)):end]
	meta := map[string]interface{}{"count": len(results), "offset": offset, "limit": limit}
	if end < len(all) {
		meta["next"] = "more"
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"self": "x", "metadata": meta, "results": results})
}

// only serves the Tempo paths (/4/...) or the Jira ones, so a call sent to the
// wrong API fails.
func (f *fakeJira) only(tempo bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/4/") != tempo {
			http.Error(w, "wrong api", http.StatusTeapot)
			return
		}
		f.handler(w, r)
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
	case p == "/rest/api/3/user":
		id := r.URL.Query().Get("accountId")
		fmt.Fprintf(w, `{"accountId":%q,"displayName":"name-%s"}`, id, id)
	case p == "/rest/api/3/search/jql":
		var req struct {
			JQL           string `json:"jql"`
			NextPageToken string `json:"nextPageToken"`
		}
		json.Unmarshal(body, &req)
		f.jql = append(f.jql, req.JQL)
		var projects []string
		if m := fakeProjectsRe.FindStringSubmatch(req.JQL); m != nil {
			projects = strings.Split(m[1], ",")
		}
		var issues []string
		for _, id := range strings.Split(fakeIssueIDsRe.FindStringSubmatch(req.JQL)[1], ",") {
			key, ok := fakeIssueKeys[id]
			if !ok || (projects != nil && !containsStr(projects, strings.Split(key, "-")[0])) {
				continue
			}
			parent := ""
			if key == "THP-1" {
				parent = `,"parent":{"key":"THP-100"}`
			}
			issues = append(issues, fmt.Sprintf(`{"id":%q,"key":%q,"fields":{"summary":"Summary of %s","issuetype":{"name":"Story"}%s}}`, id, key, key, parent))
		}
		if !f.paged {
			w.Write([]byte(`{"isLast":true,"issues":[` + strings.Join(issues, ",") + `]}`))
			return
		}
		i := 0
		fmt.Sscan(req.NextPageToken, &i)
		last := i+1 >= len(issues)
		page := ""
		if i < len(issues) {
			page = issues[i]
		}
		fmt.Fprintf(w, `{"isLast":%t,"nextPageToken":"%d","issues":[%s]}`, last, i+1, page)
	case strings.HasPrefix(p, "/rest/api/3/issue/"):
		key := strings.Split(p, "/")[5]
		fmt.Fprintf(w, `{"id":%q,"key":%q,"fields":{"summary":"Epic of %s","issuetype":{"name":"Epic"},"status":{"name":"Open"},
			"timetracking":{"originalEstimateSeconds":36000,"remainingEstimateSeconds":7200,"timeSpentSeconds":30600}}}`, fakeIssueID(key), key, key)
	case strings.HasPrefix(p, "/4/worklogs/user/"):
		user, q := strings.TrimPrefix(p, "/4/worklogs/user/"), r.URL.Query()
		var all []map[string]interface{}
		for _, x := range f.worklogs {
			d := x["startDate"].(string)
			if user == "me" && d >= q.Get("from") && d <= q.Get("to") {
				all = append(all, x)
			}
		}
		f.page(w, r, all)
	case strings.HasPrefix(p, "/4/worklogs/issue/"):
		var all []map[string]interface{}
		for _, x := range f.worklogs {
			if fmt.Sprint(x["issue"].(map[string]interface{})["id"]) == strings.TrimPrefix(p, "/4/worklogs/issue/") {
				all = append(all, x)
			}
		}
		f.page(w, r, all)
	case p == "/4/worklogs" && r.Method == "POST":
		f.writes = append(f.writes, "POST "+p+" "+string(body))
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(wl(900, int(req["issueId"].(float64)), req["authorAccountId"].(string),
			req["startDate"].(string), req["startTime"].(string), int(req["timeSpentSeconds"].(float64))))
	case strings.HasPrefix(p, "/4/worklogs/"):
		found := f.find(strings.TrimPrefix(p, "/4/worklogs/"))
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
			json.NewEncoder(w).Encode(wl(found["tempoWorklogId"].(int), found["issue"].(map[string]interface{})["id"].(int),
				req["authorAccountId"].(string), req["startDate"].(string), req["startTime"].(string), int(req["timeSpentSeconds"].(float64))))
		case "DELETE":
			f.writes = append(f.writes, "DELETE "+p)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.NotFound(w, r)
	}
}

func worklogFixture(t *testing.T) (*fakeJira, *Server, *config.Workspace, func(action string, params map[string]interface{}) (map[string]interface{}, error)) {
	t.Helper()
	f := &fakeJira{worklogs: []map[string]interface{}{
		wl(11, 1, "me", "2026-11-09", "09:00:00", 6*3600),    // Mon of the week
		wl(12, 1, "other", "2026-11-09", "10:00:00", 2*3600), // someone else's
		wl(13, 1, "me", "2026-11-12", "00:30:00", 1800),      // Thu, just after midnight
		wl(14, 1, "me", "2026-11-03", "08:00:00", 8*3600),    // earlier in the month
		wl(15, 1, "me", "2026-10-30", "08:00:00", 8*3600),    // previous month
		wl(21, 2, "me", "2026-11-10", "13:00:00", 5400),
		wl(31, 3, "me", "2026-11-10", "09:00:00", 3600), // OPS, outside jira_projects
	}}
	jira := httptest.NewServer(f.only(false))
	t.Cleanup(jira.Close)
	tempo := httptest.NewServer(f.only(true))
	t.Cleanup(tempo.Close)
	ws := &config.Workspace{
		Sites: map[string]config.Site{
			"jira": {DefaultEnv: "prod", Environments: map[string]config.Environment{
				"prod":  {BaseURL: jira.URL},
				"corpo": {BaseURL: "http://corpo.invalid"},
			}},
			"tempo": {Environments: map[string]config.Environment{"prod": {BaseURL: tempo.URL}}},
		},
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
	// Mine in that week: 6 h + 1.5 h + 0.5 h = 8 h; the other author's 2 h and the OPS hour are ignored.
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
	// Tempo's startDate is the day: the 00:30 worklog belongs to Thursday 12 Nov.
	thu := days[3].(map[string]interface{})
	if num(t, thu, "logged_hours") != 0.5 || num(t, wed, "logged_hours") != 0 {
		t.Errorf("thu = %v, wed = %v", thu["logged_hours"], wed["logged_hours"])
	}
	entry := thu["worklogs"].([]interface{})[0].(map[string]interface{})
	if entry["id"] != "13" || entry["issue_key"] != "THP-1" || entry["started"] != "2026-11-12 00:30" || entry["comment"] != "c13" {
		t.Errorf("thu worklog = %v", entry)
	}
	byIssue := out["period"].(map[string]interface{})["by_issue"].([]interface{})
	first := byIssue[0].(map[string]interface{})
	if len(byIssue) != 2 || first["key"] != "THP-1" || first["hours"] != 6.5 || first["parent_key"] != "THP-100" {
		t.Errorf("by_issue = %v", byIssue)
	}
	if len(f.listings) != 1 || !strings.HasPrefix(f.listings[0], "/4/worklogs/user/me?") ||
		!strings.Contains(f.listings[0], "from=2026-11-01") || !strings.Contains(f.listings[0], "to=2026-11-30") {
		t.Errorf("tempo listings = %v", f.listings)
	}
	if len(f.jql) != 1 || f.jql[0] != "id in (1,2,3) AND project in (THP,SUP)" {
		t.Errorf("jql = %v", f.jql)
	}
}

// Every search page and Tempo page is read: one issue and one worklog per page
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
	if len(f.listings) != 6 {
		t.Errorf("tempo pages read = %d, want 6", len(f.listings))
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

func TestWorklogReportNeedsTempoSite(t *testing.T) {
	_, _, ws, run := worklogFixture(t)
	delete(ws.Sites, "tempo")
	if _, err := run("report", nil); err == nil || !strings.Contains(err.Error(), "site tempo not found") {
		t.Fatalf("err = %v", err)
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
	want := `POST /4/worklogs {"authorAccountId":"me","description":"MR !12","issueId":2,"startDate":"2026-11-11","startTime":"09:00:00","timeSpentSeconds":5400}`
	if f.writes[0] != want {
		t.Errorf("write = %s", f.writes[0])
	}
	worklog := out["worklog"].(map[string]interface{})
	if worklog["id"] != "900" || worklog["issue_key"] != "THP-2" || worklog["started"] != "2026-11-11 09:00" {
		t.Errorf("worklog = %v", worklog)
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
	changes := map[string]map[string]interface{}{
		"update out of scope": {"key": "OPS-3", "worklog_id": "31", "hours": 1.0},
		"delete out of scope": {"key": "OPS-3", "worklog_id": "31"},
		"bad worklog id":      {"key": "THP-1", "worklog_id": "11/../x"},
		"missing worklog":     {"key": "THP-1", "worklog_id": "99"},
	}
	for name, params := range changes {
		t.Run(name, func(t *testing.T) {
			action := "delete"
			if _, ok := params["hours"]; ok {
				action = "update"
			}
			if _, err := run(action, params); err == nil {
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
	if _, err := run("delete", map[string]interface{}{"key": "THP-1", "worklog_id": "21"}); err == nil || !strings.Contains(err.Error(), "is not on THP-1") {
		t.Fatalf("deleting a worklog through another issue's key: err = %v", err)
	}
	if _, err := run("update", map[string]interface{}{"key": "THP-1", "worklog_id": "11"}); err == nil {
		t.Fatal("an update with nothing to change must be refused")
	}
	if len(f.writes) != 0 {
		t.Fatalf("refused requests wrote: %v", f.writes)
	}

	// Changing only the hours keeps the original date, start, comment and work attributes.
	out, err := run("update", map[string]interface{}{"key": "THP-1", "worklog_id": "11", "hours": 7.25})
	if err != nil {
		t.Fatal(err)
	}
	want := `PUT /4/worklogs/11 {"attributes":[{"key":"_Role_","value":"dev"}],"authorAccountId":"me","billableSeconds":26100,"description":"c11","startDate":"2026-11-09","startTime":"09:00:00","timeSpentSeconds":26100}`
	if f.writes[0] != want {
		t.Errorf("write = %s", f.writes[0])
	}
	if num(t, out, "before", "hours") != 6 || num(t, out, "worklog", "hours") != 7.25 {
		t.Errorf("update result = %v", out)
	}

	if _, err := run("delete", map[string]interface{}{"key": "THP-1", "worklog_id": "13"}); err != nil {
		t.Fatal(err)
	}
	if f.writes[1] != "DELETE /4/worklogs/13" {
		t.Errorf("delete = %s", f.writes[1])
	}
}

func TestWorklogIssueTotals(t *testing.T) {
	f, _, _, run := worklogFixture(t)
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
	authors := out["by_author"].([]interface{})
	if len(out["worklogs"].([]interface{})) != 5 || len(authors) != 2 ||
		authors[0].(map[string]interface{})["author"] != "Me" || authors[1].(map[string]interface{})["author"] != "name-other" {
		t.Errorf("worklogs = %d, authors = %v", len(out["worklogs"].([]interface{})), authors)
	}
	if len(f.listings) != 1 || !strings.HasPrefix(f.listings[0], "/4/worklogs/issue/1?") {
		t.Errorf("tempo listings = %v", f.listings)
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
