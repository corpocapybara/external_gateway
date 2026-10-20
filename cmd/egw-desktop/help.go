//go:build windows

package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/external_gateway/internal/tokencheck"
)

type help struct {
	Title       string   `json:"title"`
	What        string   `json:"what"`
	CreateURL   string   `json:"create_url,omitempty"`
	CreateLabel string   `json:"create_label,omitempty"`
	Steps       []string `json:"steps"`
	Pattern     string   `json:"pattern,omitempty"`
	PatternHint string   `json:"pattern_hint,omitempty"`
	StripSpaces bool     `json:"strip_spaces,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	PairLabel   string   `json:"pair_label,omitempty"`
}

type helpInput struct {
	Kind      tokencheck.Kind
	Role      string
	Name      string
	Pair      string
	Workspace string
	Port      int
	BuildDir  string
	User      string
	BaseURL   string
	Tunnel    string
	UsedBy    []string
}

func hostRoot(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return strings.TrimRight(base, "/")
	}
	return u.Scheme + "://" + u.Host
}

func helpFor(in helpInput) help {
	h := help{}
	shared := ""
	if len(in.UsedBy) > 1 {
		shared = "Shared by " + strings.Join(in.UsedBy, ", ") + " - changing it affects all of them."
	}
	tunnel := ""
	if in.Tunnel != "" {
		tunnel = fmt.Sprintf("Checks need the %q tunnel/VPN up; an \"unreachable\" result usually means it is down, not that the secret is wrong.", in.Tunnel)
	}
	user := in.User
	if user == "" {
		user = "(not set in config)"
	}

	switch in.Kind {
	case tokencheck.KindGitLab:
		root := hostRoot(in.BaseURL)
		h = help{
			Title:       "GitLab personal access token",
			What:        "A personal access token of the GitLab account the gateway acts as. That account's MRs are what gitlab.my_mrs returns.",
			CreateURL:   root + "/-/user_settings/personal_access_tokens?name=egw-" + in.Workspace + "&scopes=read_api,read_repository",
			CreateLabel: "Open GitLab token page (name and scopes pre-filled)",
			Steps: []string{
				"Sign in to GitLab as the account the gateway should act as.",
				"Open the token page (button above): name and scopes read_api + read_repository are pre-filled.",
				"Pick an expiry date (GitLab caps it at about a year) and click Create token.",
				"Copy the glpat-... value right away - GitLab shows it only once - and paste it below.",
			},
			Pattern:     `^glpat-[0-9A-Za-z_.\-]{20,}$`,
			PatternHint: "starts with glpat-",
			Notes:       []string{"The check shows the token's expiry date and scopes once it is stored."},
		}
	case tokencheck.KindGitHub:
		h = help{
			Title:       "GitHub personal access token",
			What:        "A GitHub token for the account the gateway acts as.",
			CreateURL:   "https://github.com/settings/tokens/new?scopes=repo,read:org&description=egw-" + in.Workspace,
			CreateLabel: "Open GitHub classic token page (scopes pre-filled)",
			Steps: []string{
				"Open the token page (classic token, scopes repo + read:org pre-filled) or create a fine-grained token with read access to the repos you need.",
				"Set an expiration, click Generate token, copy the value and paste it below.",
			},
			Pattern:     `^(ghp_[0-9A-Za-z]{36}|github_pat_[0-9A-Za-z_]{40,})$`,
			PatternHint: "starts with ghp_ (classic) or github_pat_ (fine-grained)",
			Notes:       []string{"For org repos with SSO, authorize the token for the org after creating it (Configure SSO on the token list)."},
		}
	case tokencheck.KindAtlassian:
		h = help{
			Title:       "Atlassian API token",
			What:        "An Atlassian Cloud API token, sent with basic auth together with the configured user " + user + ".",
			CreateURL:   "https://id.atlassian.com/manage-profile/security/api-tokens",
			CreateLabel: "Open Atlassian API tokens",
			Steps: []string{
				"Sign in to id.atlassian.com as " + user + " - the token must belong to that same account.",
				"Click Create API token (the classic one, without scopes), give it a name such as egw-" + in.Workspace + ", choose an expiry and create it.",
				"Copy the token (starts with ATATT) and paste it below.",
			},
			Pattern:     `^ATATT[0-9A-Za-z_=\-]{20,}$`,
			PatternHint: "starts with ATATT",
			Notes:       []string{"Atlassian API tokens expire (at most one year); Atlassian does not report the expiry through the API, so note the date you chose."},
		}
	case tokencheck.KindJira:
		h = help{
			Title: "Jira Server / Data Center password",
			What:  "The password (or personal access token used as password) for user " + user + " on " + hostRoot(in.BaseURL) + ".",
			Steps: []string{
				"Get the current password for " + user + " (or create a personal access token under your Jira profile > Personal Access Tokens).",
				"Paste it below.",
			},
		}
		if in.BaseURL != "" {
			h.CreateURL = hostRoot(in.BaseURL) + "/secure/ViewProfile.jspa"
			h.CreateLabel = "Open Jira profile"
		}
	case tokencheck.KindJenkins:
		root := strings.TrimRight(in.BaseURL, "/")
		h = help{
			Title:       "Jenkins API token",
			What:        "A Jenkins API token for user " + user + ", used with basic auth.",
			CreateURL:   root + "/me/configure",
			CreateLabel: "Open Jenkins user settings",
			Steps: []string{
				"Sign in to Jenkins as " + user + ".",
				"Open your user settings (button above) > API Token > Add new Token, name it egw-" + in.Workspace + ", click Generate.",
				"Copy the token (shown once) and paste it below.",
			},
			Pattern:     `^[0-9a-f]{32,34}$`,
			PatternHint: "32-34 hex characters",
		}
	case tokencheck.KindBitbucket:
		h = help{
			Title:       "Bitbucket credential",
			What:        "Used with basic auth as user " + user + ".",
			CreateURL:   "https://id.atlassian.com/manage-profile/security/api-tokens",
			CreateLabel: "Open Atlassian API tokens",
			Steps: []string{
				"Create an API token with scopes for Bitbucket (read on repositories and pull requests).",
				"Paste it below.",
			},
			Notes: []string{"Bitbucket replaced app passwords with Atlassian API tokens. With an API token the basic-auth user must be your Atlassian account email, so the configured user (" + user + ") may need changing in config." + " If the check is rejected, verify that first."},
		}
	case tokencheck.KindKibana:
		h = help{
			Title: "Kibana / Elasticsearch password",
			What:  "The password of Kibana user " + user + ".",
			Steps: []string{"Get the current password for " + user + " from whoever administers that Kibana, then paste it below."},
		}
	case tokencheck.KindClockify:
		h = help{
			Title:       "Clockify API key",
			What:        "Your personal Clockify API key.",
			CreateURL:   "https://app.clockify.me/user/preferences#advanced",
			CreateLabel: "Open Clockify profile settings",
			Steps:       []string{"In Clockify open Profile settings > Advanced (API section) and click Generate.", "Copy the key and paste it below."},
		}
	case tokencheck.KindAuthentik:
		root := hostRoot(in.BaseURL)
		h = help{
			Title:       "Authentik API token",
			What:        "An authentik API token. Admin endpoints need a token of a superuser.",
			CreateURL:   root + "/if/user/#/settings",
			CreateLabel: "Open authentik user settings",
			Steps: []string{
				"Open your user settings (button above) > Tokens and App passwords > Create Token, intent API.",
				"Use the copy button on the new token row and paste it below.",
			},
		}
	case tokencheck.KindSlack:
		tool := in.BuildDir + `\tools\slack-provision\Provision-Slack.ps1`
		if in.Role == "secondary" {
			h = help{Title: "Slack d cookie (xoxd)", PairLabel: "xoxc token (" + in.Pair + ") - needed to test the pair", Pattern: `^xoxd-`, PatternHint: "starts with xoxd-"}
		} else {
			h = help{Title: "Slack session token (xoxc)", PairLabel: "d cookie (" + in.Pair + ") - needed to test the pair", Pattern: `^xoxc-`, PatternHint: "starts with xoxc-"}
		}
		h.What = "Slack uses your logged-in session: the per-workspace xoxc token plus the d cookie (xoxd). They are checked together."
		h.CreateURL = "https://app.slack.com/client"
		h.CreateLabel = "Open Slack in the browser"
		h.Steps = []string{
			"Open Slack in the browser (button above) and sign in to the workspace.",
			"Press F12 > Console and run: Object.values(JSON.parse(localStorage.localConfig_v2).teams).map(t => t.name + ' ' + t.token) - copy the xoxc-... token of your workspace.",
			"In DevTools open Application > Cookies > https://app.slack.com and copy the value of the cookie named d (xoxd-...), exactly as shown.",
			"Paste both values below, Test, then Save each one (the token and the cookie are two separate secrets).",
		}
		h.Notes = []string{"Alternative using the Slack desktop app: " + tool + " -Workspace " + in.Workspace + " -Port " + fmt.Sprint(in.Port) + " -SlackDomain <your-slack-subdomain>. It does not pass the admin token yet, so it fails with 401 while the admin API is locked."}
	case tokencheck.KindTrello:
		if in.Role == "secondary" {
			h = help{
				Title:     "Trello token",
				What:      "A Trello user token authorized for the API key " + in.Pair + ".",
				PairLabel: "API key (" + in.Pair + ") - needed to test",
				Steps: []string{
					"Open https://trello.com/1/authorize?expiration=never&scope=read&response_type=token&name=egw&key=<YOUR_API_KEY> in the browser (put your API key in).",
					"Click Allow and copy the token shown.",
				},
				Pattern:     `^(ATTA[0-9A-Za-z]+|[0-9a-f]{64})$`,
				PatternHint: "starts with ATTA",
			}
		} else {
			h = help{
				Title:       "Trello API key",
				What:        "The API key of your Trello Power-Up.",
				CreateURL:   "https://trello.com/power-ups/admin",
				CreateLabel: "Open Trello Power-Up admin",
				PairLabel:   "Trello token (" + in.Pair + ") - needed to test",
				Steps:       []string{"Open the Power-Up admin page, pick your Power-Up > API key, copy the key."},
				Pattern:     `^[0-9a-f]{32}$`,
				PatternHint: "32 hex characters",
			}
		}
	case tokencheck.KindPostgres:
		h = help{
			Title: "PostgreSQL password",
			What:  "The password of database user " + user + ".",
			Steps: []string{"Get the current password for " + user + " from the DBA / password vault and paste it below."},
		}
	case tokencheck.KindIMAP:
		h = help{
			Title:       "Mail app password",
			What:        "An app password for mailbox " + user + " (IMAP/SMTP).",
			CreateURL:   "https://myaccount.google.com/apppasswords",
			CreateLabel: "Open Google app passwords",
			Steps: []string{
				"Sign in as " + user + " (2-step verification must be on).",
				"Create an app password named egw-" + in.Workspace + " and paste the 16 letters below (spaces are removed automatically).",
			},
			Pattern:     `^[a-z]{16}$`,
			PatternHint: "16 lowercase letters",
			StripSpaces: true,
		}
	case tokencheck.KindOAuth:
		doc := "docs\\GDOCS.md"
		if strings.Contains(in.Name, "onedrive") {
			doc = "docs\\ONEDRIVE.md"
		}
		h = help{
			Title: "OAuth credential (" + in.Role + ")",
			What:  "Part of an OAuth setup: the gateway refreshes the access token itself using the refresh token and client secret.",
			Steps: []string{
				"Usually you only need to replace the refresh token, after it was revoked or expired.",
				"Follow " + in.BuildDir + "\\" + doc + " to run the consent flow again, then paste the new value below.",
			},
			Notes: []string{"There is no automatic check for OAuth secrets here; after saving, run one of the workspace's operations to confirm."},
		}
	case tokencheck.KindMCP:
		h = help{Title: "Atlassian MCP credential", What: "Uses the Atlassian API token.", Steps: []string{"Change it from the Jira entry that shares this secret."}}
	default:
		h = help{Title: "Secret", What: "No specific guidance for this credential type.", Steps: []string{"Paste the new value below."}}
	}
	if shared != "" {
		h.Notes = append(h.Notes, shared)
	}
	if tunnel != "" {
		h.Notes = append(h.Notes, tunnel)
	}
	return h
}
