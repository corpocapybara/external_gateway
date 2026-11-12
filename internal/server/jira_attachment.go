package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/connectors"
	"github.com/external_gateway/internal/policy"
	"github.com/external_gateway/internal/secrets"
)

// jiraAttachmentMaxBytes caps the attachment (listed size and body) and the issue
// JSON alike (owner, T0008).
const jiraAttachmentMaxBytes = 1 << 20

var jiraAttachmentIDPattern = regexp.MustCompile(`^[0-9]+$`)

// normMime lowercases a MIME type and drops its parameters ("IMAGE/PNG; x=y" -> "image/png").
func normMime(m string) string {
	return strings.TrimSpace(strings.ToLower(strings.SplitN(m, ";", 2)[0]))
}

// attachmentClass maps a normalised MIME type to the class the op serves:
// "image" and "pdf" go out base64, "text" as a string, "" is not served.
func attachmentClass(m string) string {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "image"
	case "application/pdf":
		return "pdf"
	}
	if isTextMimetype(m) {
		return "text"
	}
	return ""
}

// jiraAuthHeader returns the Authorization value and the credentials to taint
// for a Jira site env. Only basic (with user) and bearer with a secret_ref are
// accepted; anything else fails before a request is made.
func jiraAuthHeader(env *config.Environment, workspace string) (string, *connectors.Credentials, error) {
	if env.Auth.Type != "basic" && env.Auth.Type != "bearer" {
		return "", nil, fmt.Errorf("jira_attachment_content needs basic or bearer auth, site uses %q", env.Auth.Type)
	}
	if env.SecretRef == "" {
		return "", nil, fmt.Errorf("jira_attachment_content needs a secret_ref")
	}
	if env.Auth.Type == "basic" && env.User == "" {
		return "", nil, fmt.Errorf("jira_attachment_content with basic auth needs a user")
	}
	wsName, secretName := parseSecretRef(env.SecretRef)
	raw, err := secrets.ResolveWithWorkspaceCheck(wsName, secretName, workspace)
	if err != nil {
		return "", nil, fmt.Errorf("resolving credentials for %s: %w", env.SecretRef, err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", nil, fmt.Errorf("secret %s is empty", env.SecretRef)
	}
	if env.Auth.Type == "basic" {
		return "Basic " + base64Auth(env.User, secret), &connectors.Credentials{Username: env.User, Password: secret}, nil
	}
	return "Bearer " + secret, &connectors.Credentials{Token: secret}, nil
}

type jiraAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

type jiraIssueAttachments struct {
	Key    string `json:"key"`
	Fields struct {
		Attachment []jiraAttachment `json:"attachment"`
	} `json:"fields"`
}

// executeJiraAttachmentContent serves one attachment of one issue (T0008, design
// docs/design/T0008-jira-attachment-content.md). Scope: the attachment must be
// listed on the requested issue; both URLs are built from the site's base_url.
// Refusals are *policy.ConstraintDenied (403); other failures are plain errors.
func (s *Server) executeJiraAttachmentContent(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	key, _ := params["key"].(string)
	id, _ := params["id"].(string)
	if !policy.JiraIssueKeyRe.MatchString(key) {
		return 0, nil, fmt.Errorf("parameter key must be a Jira issue key")
	}
	if !jiraAttachmentIDPattern.MatchString(id) {
		return 0, nil, fmt.Errorf("parameter id must match %s", jiraAttachmentIDPattern)
	}
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found in workspace", op.Site)
	}
	envName, env, err := resolveEnv(site, op.Site, params)
	if err != nil {
		return 0, nil, err
	}
	if env.ProxyHost != "" {
		return 0, nil, fmt.Errorf("jira_attachment_content does not support proxy_host (environment %q)", envName)
	}
	if env.BaseURL == "" || env.BaseURL == "-" {
		return 0, nil, fmt.Errorf("no base_url configured for environment %q", envName)
	}
	base := strings.TrimSuffix(env.BaseURL, "/")
	auth, creds, err := jiraAuthHeader(env, workspace)
	if err != nil {
		return 0, nil, err
	}

	issueResp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
		Method: "GET", Path: base + "/rest/api/2/issue/" + key + "?fields=attachment",
		Headers:     map[string]string{"Accept": "application/json", "Authorization": auth},
		Credentials: creds, Insecure: env.InsecureSkipVerify, MaxBodyBytes: jiraAttachmentMaxBytes,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("jira issue %s: %w", key, err)
	}
	if issueResp.StatusCode != http.StatusOK {
		return issueResp.StatusCode, nil, fmt.Errorf("jira issue %s returned status %d", key, issueResp.StatusCode)
	}
	var issue jiraIssueAttachments
	if err := json.Unmarshal(issueResp.Body, &issue); err != nil {
		return issueResp.StatusCode, nil, fmt.Errorf("jira issue %s returned an unreadable response", key)
	}
	if issue.Key != key {
		return 0, nil, &policy.ConstraintDenied{Detail: fmt.Sprintf("issue %s answered as %q", key, issue.Key)}
	}
	var att *jiraAttachment
	for i := range issue.Fields.Attachment {
		if issue.Fields.Attachment[i].ID == id {
			att = &issue.Fields.Attachment[i]
			break
		}
	}
	if att == nil {
		return 0, nil, &policy.ConstraintDenied{Detail: fmt.Sprintf("attachment %s is not on issue %s", id, key)}
	}
	class := attachmentClass(normMime(att.MimeType))
	if class == "" {
		return 0, nil, &policy.ConstraintDenied{Detail: fmt.Sprintf("attachment type %q is not served (images, PDF and text only)", att.MimeType)}
	}
	if att.Size > jiraAttachmentMaxBytes {
		return 0, nil, &policy.ConstraintDenied{Detail: fmt.Sprintf("attachment is %d bytes; the limit is %d", att.Size, jiraAttachmentMaxBytes)}
	}
	if att.Filename == "" || att.Filename == "." || att.Filename == ".." {
		return 0, nil, fmt.Errorf("attachment %s has an unusable filename %q", id, att.Filename)
	}

	resp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
		Method: "GET", Path: base + "/secure/attachment/" + id + "/" + url.PathEscape(att.Filename),
		Headers:     map[string]string{"Authorization": auth},
		Credentials: creds, Insecure: env.InsecureSkipVerify, MaxBodyBytes: jiraAttachmentMaxBytes,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("attachment %s download: %w", id, err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, fmt.Errorf("attachment %s download returned status %d", id, resp.StatusCode)
	}
	if ct := headerLookup(resp.Headers, "Content-Type"); ct != "" && attachmentClass(normMime(ct)) != class {
		return resp.StatusCode, nil, fmt.Errorf("attachment %s download returned content type %q, expected %s", id, ct, class)
	}

	encoding, content := "base64", base64.StdEncoding.EncodeToString(resp.Body)
	if class == "text" {
		encoding, content = "text", string(resp.Body)
		if !utf8.ValidString(content) {
			content = strings.ToValidUTF8(content, "�")
		}
	}
	out, err := json.Marshal(map[string]interface{}{
		"attachment": map[string]interface{}{
			"id": att.ID, "filename": att.Filename, "mimeType": att.MimeType, "size": att.Size,
		},
		"encoding": encoding,
		"content":  content,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("encoding attachment: %w", err)
	}
	return http.StatusOK, out, nil
}
