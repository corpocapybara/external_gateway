package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Europe/Warsaw on hosts without a zoneinfo database

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
	"github.com/external_gateway/internal/workcal"
)

// The jira-worklog connector serves time tracking on Tempo worklogs: period reports
// against the Polish working-hours norm, per-issue totals, writes on the caller's own
// worklogs, and a map of non-ticket activities to issues. Tempo Cloud writes every
// worklog to Jira under its app user, so worklogs are read and written through the
// Tempo API (workspace site `tempo`); Jira (the op's site) supplies the caller's
// account, issue keys and summaries. The op's `path` names the action. Issue keys
// are scoped to the workspace's jira_projects.

const (
	worklogTimezone     = "Europe/Warsaw"
	worklogMaxIssues    = 500
	worklogDefaultStart = "09:00"
	worklogTempoSite    = "tempo"
	worklogTempoPage    = 1000
	worklogSearchChunk  = 100
)

var (
	jiraIssueKeyRe       = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)-[1-9][0-9]*$`)
	worklogIDRe          = regexp.MustCompile(`^[1-9][0-9]*$`)
	worklogActivityRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	worklogStartRe       = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	worklogActivitiesMu  sync.Mutex
	worklogLocationOnce  sync.Once
	worklogLocationValue *time.Location
)

func worklogLocation() *time.Location {
	worklogLocationOnce.Do(func() {
		loc, err := time.LoadLocation(worklogTimezone)
		if err != nil {
			loc = time.Local
		}
		worklogLocationValue = loc
	})
	return worklogLocationValue
}

type restClient struct {
	s        *Server
	api      string
	baseURL  string
	headers  map[string]string
	insecure bool
}

type jiraClient struct {
	*restClient
	tempo *restClient
}

func (s *Server) newJiraClient(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (*jiraClient, error) {
	site, ok := ws.Sites[op.Site]
	if !ok {
		return nil, fmt.Errorf("site %s not found in workspace", op.Site)
	}
	// Worklog ops speak the Jira Cloud v3 API, so they run on the site's default
	// environment only (the THP jira site's corpo env is Jira Server).
	if e, _ := params["env"].(string); e != "" && site.DefaultEnv != "" && e != site.DefaultEnv {
		return nil, fmt.Errorf("worklog operations run on environment %q only", site.DefaultEnv)
	}
	jira, err := s.newRestClient("jira", op.Site, site, params, workspace)
	if err != nil {
		return nil, err
	}
	tempoSite, ok := ws.Sites[worklogTempoSite]
	if !ok {
		return nil, fmt.Errorf("site %s not found in workspace; worklogs are read and written through the Tempo API", worklogTempoSite)
	}
	tempo, err := s.newRestClient("tempo", worklogTempoSite, tempoSite, nil, workspace)
	if err != nil {
		return nil, err
	}
	return &jiraClient{restClient: jira, tempo: tempo}, nil
}

func (s *Server) newRestClient(api, siteName string, site config.Site, params map[string]interface{}, workspace string) (*restClient, error) {
	envName, env, err := resolveEnv(site, siteName, params)
	if err != nil {
		return nil, err
	}
	if env.BaseURL == "" {
		return nil, fmt.Errorf("no base_url configured for environment %q", envName)
	}
	headers := map[string]string{"Accept": "application/json", "Content-Type": "application/json"}
	if env.SecretRef != "" && (env.Auth.Type == "basic" || env.Auth.Type == "bearer") {
		wsName, secretName := parseSecretRef(env.SecretRef)
		secret, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
		if err != nil {
			s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve secret")
		} else if env.Auth.Type == "basic" {
			headers["Authorization"] = "Basic " + base64Auth(env.User, strings.TrimSpace(string(secret)))
		} else {
			headers["Authorization"] = "Bearer " + strings.TrimSpace(string(secret))
		}
	}
	return &restClient{s: s, api: api, baseURL: strings.TrimSuffix(env.BaseURL, "/"), headers: headers, insecure: env.InsecureSkipVerify}, nil
}

// do sends one request and decodes a JSON reply into out. An upstream status of
// 400 or more is an error carrying the start of the reply.
func (c *restClient) do(method, path string, in, out interface{}) (int, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return 0, err
		}
	}
	c.s.logger.Info().Str("method", method).Str("url", c.baseURL+path).Str("api", c.api).Msg("worklog request")
	resp, err := c.s.httpClient.Execute(context.Background(), &connectors.Request{
		Method: method, Path: c.baseURL + path, Headers: c.headers, Body: body, Insecure: c.insecure,
	})
	if err != nil {
		return 0, fmt.Errorf("upstream request failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("%s %s %s returned %d: %s", c.api, method, strings.SplitN(path, "?", 2)[0], resp.StatusCode, string(resp.Body[:min(300, len(resp.Body))]))
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s returned an unreadable reply: %w", c.api, strings.SplitN(path, "?", 2)[0], err)
		}
	}
	return resp.StatusCode, nil
}

type jiraUser struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

type tempoWorklog struct {
	TempoWorklogID int64 `json:"tempoWorklogId"`
	Issue          struct {
		ID int64 `json:"id"`
	} `json:"issue"`
	Author struct {
		AccountID string `json:"accountId"`
	} `json:"author"`
	StartDate        string `json:"startDate"`
	StartTime        string `json:"startTime"`
	TimeSpentSeconds int    `json:"timeSpentSeconds"`
	BillableSeconds  int    `json:"billableSeconds"`
	Description      string `json:"description"`
	Attributes       struct {
		Values []tempoAttribute `json:"values"`
	} `json:"attributes"`
}

type tempoAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (w tempoWorklog) id() string {
	return strconv.FormatInt(w.TempoWorklogID, 10)
}

func (w tempoWorklog) issueID() string {
	return strconv.FormatInt(w.Issue.ID, 10)
}

type jiraIssueRef struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary   string `json:"summary"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
		TimeTracking struct {
			OriginalEstimateSeconds  int `json:"originalEstimateSeconds"`
			RemainingEstimateSeconds int `json:"remainingEstimateSeconds"`
			TimeSpentSeconds         int `json:"timeSpentSeconds"`
		} `json:"timetracking"`
	} `json:"fields"`
}

func (r jiraIssueRef) parentKey() string {
	if r.Fields.Parent == nil {
		return ""
	}
	return r.Fields.Parent.Key
}

func (c *jiraClient) myself() (jiraUser, error) {
	var u jiraUser
	if _, err := c.do("GET", "/rest/api/3/myself", nil, &u); err != nil {
		return u, err
	}
	if u.AccountID == "" {
		return u, fmt.Errorf("jira /myself returned no accountId")
	}
	return u, nil
}

func (c *jiraClient) issue(key, fields string) (jiraIssueRef, error) {
	var r jiraIssueRef
	_, err := c.do("GET", "/rest/api/3/issue/"+url.PathEscape(key)+"?fields="+url.QueryEscape(fields), nil, &r)
	return r, err
}

func (c *jiraClient) userName(accountID string) string {
	var u jiraUser
	if _, err := c.do("GET", "/rest/api/3/user?accountId="+url.QueryEscape(accountID), nil, &u); err != nil || u.DisplayName == "" {
		return accountID
	}
	return u.DisplayName
}

// tempoWorklogs reads every page of a Tempo worklog listing.
func (c *jiraClient) tempoWorklogs(path string, q url.Values) ([]tempoWorklog, error) {
	if q == nil {
		q = url.Values{}
	}
	var all []tempoWorklog
	for offset := 0; ; {
		q.Set("offset", fmt.Sprint(offset))
		q.Set("limit", fmt.Sprint(worklogTempoPage))
		var page struct {
			Metadata struct {
				Next string `json:"next"`
			} `json:"metadata"`
			Results []tempoWorklog `json:"results"`
		}
		if _, err := c.tempo.do("GET", path+"?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		offset += len(page.Results)
		if len(page.Results) == 0 || page.Metadata.Next == "" {
			return all, nil
		}
	}
}

// scopedIssues looks up the issues behind Tempo worklogs by id, keeping those in
// the workspace's jira_projects.
func (c *jiraClient) scopedIssues(ws *config.Workspace, wls []tempoWorklog) (map[string]jiraIssueRef, error) {
	var ids []string
	seen := map[string]bool{}
	for _, w := range wls {
		if id := w.issueID(); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > worklogMaxIssues {
		return nil, fmt.Errorf("more than %d issues have worklogs in this range; ask for a shorter period", worklogMaxIssues)
	}
	issues := map[string]jiraIssueRef{}
	for i := 0; i < len(ids); i += worklogSearchChunk {
		jql := fmt.Sprintf("id in (%s)", strings.Join(ids[i:min(i+worklogSearchChunk, len(ids))], ","))
		if projects := ws.Policy.JiraProjects; len(projects) > 0 {
			jql = fmt.Sprintf("%s AND project in (%s)", jql, strings.Join(projects, ","))
		}
		found, err := c.searchIssues(jql)
		if err != nil {
			return nil, err
		}
		for _, iss := range found {
			issues[iss.ID] = iss
		}
	}
	return issues, nil
}

func (c *jiraClient) searchIssues(jql string) ([]jiraIssueRef, error) {
	var issues []jiraIssueRef
	token := ""
	for {
		req := map[string]interface{}{"jql": jql, "maxResults": 100, "fields": []string{"summary", "issuetype", "parent"}}
		if token != "" {
			req["nextPageToken"] = token
		}
		var page struct {
			Issues        []jiraIssueRef `json:"issues"`
			NextPageToken string         `json:"nextPageToken"`
			IsLast        bool           `json:"isLast"`
		}
		if _, err := c.do("POST", "/rest/api/3/search/jql", req, &page); err != nil {
			return nil, err
		}
		issues = append(issues, page.Issues...)
		if page.IsLast || page.NextPageToken == "" || len(page.Issues) == 0 {
			return issues, nil
		}
		token = page.NextPageToken
	}
}

// --- shaping ---------------------------------------------------------------

func hoursOf(seconds int) float64 {
	return math.Round(float64(seconds)/36) / 100
}

func roundHours(h float64) float64 {
	return math.Round(h*100) / 100
}

// startHHMM is a Tempo start time (HH:MM:SS) cut to HH:MM.
func (w tempoWorklog) startHHMM() string {
	if len(w.StartTime) < 5 {
		return ""
	}
	return w.StartTime[:5]
}

type worklogEntry struct {
	ID        string  `json:"id"`
	IssueKey  string  `json:"issue_key"`
	Summary   string  `json:"summary,omitempty"`
	ParentKey string  `json:"parent_key,omitempty"`
	Author    string  `json:"author,omitempty"`
	Started   string  `json:"started"`
	Hours     float64 `json:"hours"`
	Comment   string  `json:"comment,omitempty"`
	date      time.Time
	seconds   int
}

// toEntry shapes a Tempo worklog. Tempo's startDate is already the worker's
// calendar date, so it is taken as is.
func toEntry(w tempoWorklog, issue jiraIssueRef) (worklogEntry, error) {
	d, err := time.Parse("2006-01-02", w.StartDate)
	if err != nil {
		return worklogEntry{}, fmt.Errorf("unreadable worklog date %q", w.StartDate)
	}
	return worklogEntry{
		ID: w.id(), IssueKey: issue.Key, Summary: issue.Fields.Summary, ParentKey: issue.parentKey(),
		Started: strings.TrimSpace(w.StartDate + " " + w.startHHMM()), Hours: hoursOf(w.TimeSpentSeconds),
		Comment: w.Description, date: d, seconds: w.TimeSpentSeconds,
	}, nil
}

// --- params ----------------------------------------------------------------

func worklogToday() time.Time {
	return workcal.Civil(time.Now().In(worklogLocation()))
}

func paramDate(params map[string]interface{}, name string) (time.Time, bool, error) {
	s, _ := params[name].(string)
	if s == "" {
		return worklogToday(), false, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return d, true, fmt.Errorf("parameter %s must be a date YYYY-MM-DD", name)
	}
	return d, true, nil
}

func paramHours(params map[string]interface{}) (int, bool, error) {
	v, ok := params["hours"]
	if !ok {
		return 0, false, nil
	}
	h, ok := v.(float64)
	if !ok || h <= 0 || h > 24 {
		return 0, true, fmt.Errorf("parameter hours must be a number above 0 and at most 24")
	}
	minutes := int(math.Round(h * 60))
	if minutes < 1 {
		return 0, true, fmt.Errorf("parameter hours must be at least one minute")
	}
	return minutes * 60, true, nil
}

// scopedIssueKey checks an issue key's shape and that its project is in the
// workspace's jira_projects (an empty list restricts nothing).
func scopedIssueKey(ws *config.Workspace, key string) error {
	m := jiraIssueKeyRe.FindStringSubmatch(key)
	if m == nil {
		return fmt.Errorf("parameter key must be an issue key like THP-123")
	}
	if len(ws.Policy.JiraProjects) > 0 && !containsStr(ws.Policy.JiraProjects, m[1]) {
		return fmt.Errorf("project %s is not in the workspace's jira_projects", m[1])
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- dispatch --------------------------------------------------------------

func (s *Server) executeJiraWorklog(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	c, err := s.newJiraClient(ws, op, params, workspace)
	if err != nil {
		return 0, nil, err
	}
	var out interface{}
	switch op.Path {
	case "report":
		out, err = c.report(ws, params)
	case "issue":
		out, err = c.issueTime(ws, params)
	case "add":
		out, err = c.addWorklog(s, ws, params, workspace)
	case "update":
		out, err = c.updateWorklog(ws, params)
	case "delete":
		out, err = c.deleteWorklog(ws, params)
	case "activities":
		var acts map[string]worklogActivity
		if acts, err = s.readWorklogActivities(workspace); err == nil {
			out = map[string]interface{}{"activities": acts}
		}
	case "activity_set":
		out, err = c.setActivity(s, ws, params, workspace)
	case "activity_remove":
		out, err = s.removeActivity(params, workspace)
	default:
		return 0, nil, fmt.Errorf("operation %s: unknown jira-worklog action %q", op.ID, op.Path)
	}
	if err != nil {
		return 0, nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return 0, nil, err
	}
	return 200, body, nil
}

// --- report ----------------------------------------------------------------

type worklogNorm struct {
	From            string            `json:"from"`
	To              string            `json:"to"`
	WorkingDays     int               `json:"working_days"`
	NormHours       float64           `json:"norm_hours"`
	NormToDateHours float64           `json:"norm_to_date_hours"`
	LoggedHours     float64           `json:"logged_hours"`
	LeftHours       float64           `json:"left_hours"`
	OvertimeHours   float64           `json:"overtime_hours"`
	BalanceToDate   float64           `json:"balance_to_date_hours"`
	Holidays        []worklogHolidays `json:"holidays,omitempty"`
}

type worklogHolidays struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

type worklogDay struct {
	Date        string         `json:"date"`
	Weekday     string         `json:"weekday"`
	WorkingDay  bool           `json:"working_day"`
	Holiday     string         `json:"holiday,omitempty"`
	LoggedHours float64        `json:"logged_hours"`
	Worklogs    []worklogEntry `json:"worklogs"`
}

type worklogIssueTotal struct {
	Key       string  `json:"key"`
	Summary   string  `json:"summary"`
	ParentKey string  `json:"parent_key,omitempty"`
	Hours     float64 `json:"hours"`
}

// periodRange returns the calendar dates of the day, ISO week or month around d.
func periodRange(period string, d time.Time) (time.Time, time.Time) {
	switch period {
	case "week":
		from := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
		return from, from.AddDate(0, 0, 6)
	case "month":
		from := workcal.Date(d.Year(), d.Month(), 1)
		return from, from.AddDate(0, 1, -1)
	}
	return d, d
}

// norm compares the seconds logged in [from, to] with the working-hours norm.
// "To date" counts working days up to and including asOf.
func norm(from, to, asOf time.Time, logged int) worklogNorm {
	days := workcal.WorkingDays(from, to)
	n := worklogNorm{
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), WorkingDays: days,
		NormHours:   float64(days * workcal.HoursPerDay),
		LoggedHours: hoursOf(logged),
	}
	if !asOf.Before(from) {
		end := to
		if asOf.Before(to) {
			end = asOf
		}
		n.NormToDateHours = float64(workcal.WorkingDays(from, end) * workcal.HoursPerDay)
	}
	n.LeftHours = roundHours(math.Max(n.NormHours-n.LoggedHours, 0))
	n.OvertimeHours = roundHours(math.Max(n.LoggedHours-n.NormHours, 0))
	n.BalanceToDate = roundHours(n.LoggedHours - n.NormToDateHours)
	for _, h := range workcal.HolidaysBetween(from, to) {
		n.Holidays = append(n.Holidays, worklogHolidays{h.Date.Format("2006-01-02"), h.Name})
	}
	return n
}

func (c *jiraClient) report(ws *config.Workspace, params map[string]interface{}) (interface{}, error) {
	period, _ := params["period"].(string)
	if period == "" {
		period = "week"
	}
	asOf, _, err := paramDate(params, "date")
	if err != nil {
		return nil, err
	}
	pFrom, pTo := periodRange(period, asOf)
	wFrom, wTo := periodRange("week", asOf)
	mFrom, mTo := periodRange("month", asOf)
	from, to := minDate(pFrom, wFrom, mFrom), maxDate(pTo, wTo, mTo)

	me, err := c.myself()
	if err != nil {
		return nil, err
	}
	entries, err := c.myEntries(ws, me, from, to)
	if err != nil {
		return nil, err
	}

	sum := func(a, b time.Time) int {
		n := 0
		for _, e := range entries {
			if !e.date.Before(a) && !e.date.After(b) {
				n += e.seconds
			}
		}
		return n
	}

	var days []worklogDay
	byIssue := map[string]*worklogIssueTotal{}
	for d := pFrom; !d.After(pTo); d = d.AddDate(0, 0, 1) {
		day := worklogDay{Date: d.Format("2006-01-02"), Weekday: d.Weekday().String()[:3], WorkingDay: workcal.IsWorkingDay(d), Worklogs: []worklogEntry{}}
		day.Holiday, _ = workcal.HolidayName(d)
		secs := 0
		for _, e := range entries {
			if !e.date.Equal(d) {
				continue
			}
			day.Worklogs = append(day.Worklogs, e)
			secs += e.seconds
			t := byIssue[e.IssueKey]
			if t == nil {
				t = &worklogIssueTotal{Key: e.IssueKey, Summary: e.Summary, ParentKey: e.ParentKey}
				byIssue[e.IssueKey] = t
			}
			t.Hours += float64(e.seconds)
		}
		day.LoggedHours = hoursOf(secs)
		days = append(days, day)
	}
	issues := make([]worklogIssueTotal, 0, len(byIssue))
	for _, t := range byIssue {
		t.Hours = hoursOf(int(t.Hours))
		issues = append(issues, *t)
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Hours != issues[j].Hours {
			return issues[i].Hours > issues[j].Hours
		}
		return issues[i].Key < issues[j].Key
	})

	periodNorm := norm(pFrom, pTo, asOf, sum(pFrom, pTo))
	periodNorm.Holidays = nil
	return map[string]interface{}{
		"user":     me.DisplayName,
		"timezone": worklogTimezone,
		"as_of":    asOf.Format("2006-01-02"),
		"projects": ws.Policy.JiraProjects,
		"period": map[string]interface{}{
			"type": period, "norm": periodNorm, "days": days, "by_issue": issues,
		},
		"week":  norm(wFrom, wTo, asOf, sum(wFrom, wTo)),
		"month": norm(mFrom, mTo, asOf, sum(mFrom, mTo)),
	}, nil
}

// myEntries returns the caller's Tempo worklogs on calendar dates from..to, on
// issues in the workspace's jira_projects.
func (c *jiraClient) myEntries(ws *config.Workspace, me jiraUser, from, to time.Time) ([]worklogEntry, error) {
	q := url.Values{"from": {from.Format("2006-01-02")}, "to": {to.Format("2006-01-02")}}
	wls, err := c.tempoWorklogs("/4/worklogs/user/"+url.PathEscape(me.AccountID), q)
	if err != nil {
		return nil, err
	}
	issues, err := c.scopedIssues(ws, wls)
	if err != nil {
		return nil, err
	}
	var entries []worklogEntry
	for _, w := range wls {
		iss, ok := issues[w.issueID()]
		if !ok || w.Author.AccountID != me.AccountID {
			continue
		}
		e, err := toEntry(w, iss)
		if err != nil {
			return nil, err
		}
		if !e.date.Before(from) && !e.date.After(to) {
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Started < entries[j].Started })
	return entries, nil
}

func minDate(ds ...time.Time) time.Time {
	m := ds[0]
	for _, d := range ds[1:] {
		if d.Before(m) {
			m = d
		}
	}
	return m
}

func maxDate(ds ...time.Time) time.Time {
	m := ds[0]
	for _, d := range ds[1:] {
		if d.After(m) {
			m = d
		}
	}
	return m
}

// --- one issue ---------------------------------------------------------------

func (c *jiraClient) issueTime(ws *config.Workspace, params map[string]interface{}) (interface{}, error) {
	key, _ := params["key"].(string)
	if err := scopedIssueKey(ws, key); err != nil {
		return nil, err
	}
	iss, err := c.issue(key, "summary,issuetype,status,parent,timetracking")
	if err != nil {
		return nil, err
	}
	me, err := c.myself()
	if err != nil {
		return nil, err
	}
	wls, err := c.tempoWorklogs("/4/worklogs/issue/"+url.PathEscape(iss.ID), nil)
	if err != nil {
		return nil, err
	}
	names := map[string]string{me.AccountID: me.DisplayName}
	total, mine := 0, 0
	byAuthor := map[string]int{}
	entries := make([]worklogEntry, 0, len(wls))
	for _, w := range wls {
		e, err := toEntry(w, iss)
		if err != nil {
			return nil, err
		}
		name, ok := names[w.Author.AccountID]
		if !ok {
			name = c.userName(w.Author.AccountID)
			names[w.Author.AccountID] = name
		}
		e.Summary, e.ParentKey = "", ""
		e.Author = name
		entries = append(entries, e)
		total += w.TimeSpentSeconds
		byAuthor[name] += w.TimeSpentSeconds
		if w.Author.AccountID == me.AccountID {
			mine += w.TimeSpentSeconds
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Started < entries[j].Started })
	authors := make([]map[string]interface{}, 0, len(byAuthor))
	for name, secs := range byAuthor {
		authors = append(authors, map[string]interface{}{"author": name, "hours": hoursOf(secs)})
	}
	sort.Slice(authors, func(i, j int) bool {
		hi, hj := authors[i]["hours"].(float64), authors[j]["hours"].(float64)
		if hi != hj {
			return hi > hj
		}
		return authors[i]["author"].(string) < authors[j]["author"].(string)
	})
	tt := iss.Fields.TimeTracking
	return map[string]interface{}{
		"key": iss.Key, "summary": iss.Fields.Summary, "issue_type": iss.Fields.IssueType.Name,
		"status": iss.Fields.Status.Name, "parent_key": iss.parentKey(),
		"time_tracking": map[string]interface{}{
			"original_estimate_hours":  hoursOf(tt.OriginalEstimateSeconds),
			"remaining_estimate_hours": hoursOf(tt.RemainingEstimateSeconds),
			"time_spent_hours":         hoursOf(tt.TimeSpentSeconds),
		},
		"logged_hours": hoursOf(total),
		"my_hours":     hoursOf(mine),
		"by_author":    authors,
		"worklogs":     entries,
	}, nil
}

// --- writes ----------------------------------------------------------------

func tempoStartTime(hhmm string) (string, error) {
	if !worklogStartRe.MatchString(hhmm) {
		return "", fmt.Errorf("parameter start must be a time HH:MM")
	}
	return hhmm + ":00", nil
}

func issueIDNumber(iss jiraIssueRef) (int64, error) {
	id, err := strconv.ParseInt(iss.ID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jira returned issue %s without a numeric id", iss.Key)
	}
	return id, nil
}

func dayNote(d time.Time) string {
	if name, ok := workcal.HolidayName(d); ok {
		return d.Format("2006-01-02") + " is a public holiday (" + name + ")"
	}
	if !workcal.IsWorkingDay(d) {
		return d.Format("2006-01-02") + " is a " + d.Weekday().String()
	}
	return ""
}

func (c *jiraClient) addWorklog(s *Server, ws *config.Workspace, params map[string]interface{}, workspace string) (interface{}, error) {
	key, _ := params["key"].(string)
	activity, _ := params["activity"].(string)
	if (key == "") == (activity == "") {
		return nil, fmt.Errorf("give exactly one of key or activity")
	}
	if activity != "" {
		acts, err := s.readWorklogActivities(workspace)
		if err != nil {
			return nil, err
		}
		a, ok := acts[activity]
		if !ok {
			return nil, fmt.Errorf("activity %q is not in the activity map; see jira.worklog_activities", activity)
		}
		key = a.Key
	}
	if err := scopedIssueKey(ws, key); err != nil {
		return nil, err
	}
	secs, ok, err := paramHours(params)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("missing required parameter: hours")
	}
	d, _, err := paramDate(params, "date")
	if err != nil {
		return nil, err
	}
	start, _ := params["start"].(string)
	if start == "" {
		start = worklogDefaultStart
	}
	startTime, err := tempoStartTime(start)
	if err != nil {
		return nil, err
	}
	iss, err := c.issue(key, "summary")
	if err != nil {
		return nil, err
	}
	issueID, err := issueIDNumber(iss)
	if err != nil {
		return nil, err
	}
	me, err := c.myself()
	if err != nil {
		return nil, err
	}
	req := map[string]interface{}{
		"issueId": issueID, "authorAccountId": me.AccountID,
		"startDate": d.Format("2006-01-02"), "startTime": startTime, "timeSpentSeconds": secs,
	}
	if comment, _ := params["comment"].(string); comment != "" {
		req["description"] = comment
	}
	var created tempoWorklog
	if _, err := c.tempo.do("POST", "/4/worklogs", req, &created); err != nil {
		return nil, err
	}
	e, err := toEntry(created, jiraIssueRef{Key: iss.Key})
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{"created": true, "worklog": e}
	if activity != "" {
		out["activity"] = activity
	}
	if note := dayNote(d); note != "" {
		out["note"] = note
	}
	return out, nil
}

// ownWorklog fetches a Tempo worklog and refuses it unless the caller wrote it and
// it is on the issue named by key.
func (c *jiraClient) ownWorklog(ws *config.Workspace, params map[string]interface{}) (jiraIssueRef, tempoWorklog, error) {
	key, _ := params["key"].(string)
	id, _ := params["worklog_id"].(string)
	var iss jiraIssueRef
	var w tempoWorklog
	if err := scopedIssueKey(ws, key); err != nil {
		return iss, w, err
	}
	if !worklogIDRe.MatchString(id) {
		return iss, w, fmt.Errorf("parameter worklog_id must be a number")
	}
	me, err := c.myself()
	if err != nil {
		return iss, w, err
	}
	if _, err := c.tempo.do("GET", "/4/worklogs/"+id, nil, &w); err != nil {
		return iss, w, err
	}
	if iss, err = c.issue(key, "summary"); err != nil {
		return iss, w, err
	}
	if iss.ID != w.issueID() {
		return iss, w, fmt.Errorf("worklog %s is not on %s", id, key)
	}
	if w.Author.AccountID != me.AccountID {
		return iss, w, fmt.Errorf("worklog %s belongs to %s; only your own worklogs can be changed", id, c.userName(w.Author.AccountID))
	}
	return iss, w, nil
}

func (c *jiraClient) updateWorklog(ws *config.Workspace, params map[string]interface{}) (interface{}, error) {
	iss, old, err := c.ownWorklog(ws, params)
	if err != nil {
		return nil, err
	}
	before, err := toEntry(old, iss)
	if err != nil {
		return nil, err
	}
	secs, hoursSet, err := paramHours(params)
	if err != nil {
		return nil, err
	}
	d, dateSet, err := paramDate(params, "date")
	if err != nil {
		return nil, err
	}
	start, _ := params["start"].(string)
	comment, commentSet := params["comment"].(string)
	if !hoursSet && !dateSet && start == "" && !commentSet {
		return nil, fmt.Errorf("nothing to change: give hours, date, start or comment")
	}

	if !dateSet {
		d = before.date
	}
	if start == "" {
		start = old.startHHMM()
	}
	if start == "" {
		start = worklogDefaultStart
	}
	startTime, err := tempoStartTime(start)
	if err != nil {
		return nil, err
	}
	if !hoursSet {
		secs = old.TimeSpentSeconds
	}
	billable := old.BillableSeconds
	if billable == old.TimeSpentSeconds {
		billable = secs
	}
	if !commentSet {
		comment = old.Description
	}
	req := map[string]interface{}{
		"authorAccountId": old.Author.AccountID, "startDate": d.Format("2006-01-02"), "startTime": startTime,
		"timeSpentSeconds": secs, "billableSeconds": billable, "description": comment,
	}
	if len(old.Attributes.Values) > 0 {
		req["attributes"] = old.Attributes.Values
	}
	var updated tempoWorklog
	if _, err := c.tempo.do("PUT", "/4/worklogs/"+old.id(), req, &updated); err != nil {
		return nil, err
	}
	after, err := toEntry(updated, iss)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{"updated": true, "before": before, "worklog": after}
	if note := dayNote(d); note != "" {
		out["note"] = note
	}
	return out, nil
}

func (c *jiraClient) deleteWorklog(ws *config.Workspace, params map[string]interface{}) (interface{}, error) {
	iss, old, err := c.ownWorklog(ws, params)
	if err != nil {
		return nil, err
	}
	e, err := toEntry(old, iss)
	if err != nil {
		return nil, err
	}
	if _, err := c.tempo.do("DELETE", "/4/worklogs/"+old.id(), nil, nil); err != nil {
		return nil, err
	}
	return map[string]interface{}{"deleted": true, "worklog": e}, nil
}

// --- activity map ------------------------------------------------------------

// worklogActivity maps a non-ticket activity (code review, general support, ...)
// to the issue, usually an epic, its time is logged on.
type worklogActivity struct {
	Key       string `json:"key"`
	Summary   string `json:"summary"`
	IssueType string `json:"issue_type"`
	Updated   string `json:"updated"`
}

// worklogActivitiesPath keeps the map next to the deployed config, one file per
// workspace, so it survives restarts and config deploys.
func (s *Server) worklogActivitiesPath(workspace string) string {
	return filepath.Join(filepath.Dir(s.cfg().ConfigPath), "worklog-activities."+workspace+".json")
}

// readWorklogActivities loads the map under the lock writers hold: on Windows a
// reader with the file open makes the writer's rename fail.
func (s *Server) readWorklogActivities(workspace string) (map[string]worklogActivity, error) {
	worklogActivitiesMu.Lock()
	defer worklogActivitiesMu.Unlock()
	return s.loadWorklogActivities(workspace)
}

// loadWorklogActivities reads the map; the caller holds worklogActivitiesMu.
func (s *Server) loadWorklogActivities(workspace string) (map[string]worklogActivity, error) {
	acts := map[string]worklogActivity{}
	data, err := os.ReadFile(s.worklogActivitiesPath(workspace))
	if os.IsNotExist(err) {
		return acts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading activity map: %w", err)
	}
	if err := json.Unmarshal(data, &acts); err != nil {
		return nil, fmt.Errorf("activity map %s is not valid JSON: %w", filepath.Base(s.worklogActivitiesPath(workspace)), err)
	}
	return acts, nil
}

func (s *Server) saveWorklogActivities(workspace string, acts map[string]worklogActivity) error {
	path := s.worklogActivitiesPath(workspace)
	data, err := json.MarshalIndent(acts, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing activity map: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing activity map: %w", err)
	}
	return nil
}

func paramActivity(params map[string]interface{}) (string, error) {
	name, _ := params["name"].(string)
	if !worklogActivityRe.MatchString(name) {
		return "", fmt.Errorf("parameter name must match %s", worklogActivityRe)
	}
	return name, nil
}

func (c *jiraClient) setActivity(s *Server, ws *config.Workspace, params map[string]interface{}, workspace string) (interface{}, error) {
	name, err := paramActivity(params)
	if err != nil {
		return nil, err
	}
	key, _ := params["key"].(string)
	if err := scopedIssueKey(ws, key); err != nil {
		return nil, err
	}
	iss, err := c.issue(key, "summary,issuetype")
	if err != nil {
		return nil, err
	}
	worklogActivitiesMu.Lock()
	defer worklogActivitiesMu.Unlock()
	acts, err := s.loadWorklogActivities(workspace)
	if err != nil {
		return nil, err
	}
	acts[name] = worklogActivity{Key: iss.Key, Summary: iss.Fields.Summary, IssueType: iss.Fields.IssueType.Name, Updated: time.Now().In(worklogLocation()).Format("2006-01-02 15:04")}
	if err := s.saveWorklogActivities(workspace, acts); err != nil {
		return nil, err
	}
	return map[string]interface{}{"set": name, "activities": acts}, nil
}

func (s *Server) removeActivity(params map[string]interface{}, workspace string) (interface{}, error) {
	name, err := paramActivity(params)
	if err != nil {
		return nil, err
	}
	worklogActivitiesMu.Lock()
	defer worklogActivitiesMu.Unlock()
	acts, err := s.loadWorklogActivities(workspace)
	if err != nil {
		return nil, err
	}
	if _, ok := acts[name]; !ok {
		return nil, fmt.Errorf("activity %q is not in the activity map", name)
	}
	delete(acts, name)
	if err := s.saveWorklogActivities(workspace, acts); err != nil {
		return nil, err
	}
	return map[string]interface{}{"removed": name, "activities": acts}, nil
}
