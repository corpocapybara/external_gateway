package server

import (
	"context"
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

const (
	defaultSlackFilesBase = "https://files.slack.com"
	slackFileMaxBytes     = 1 << 20
)

var slackFileIDPattern = regexp.MustCompile(`^F[A-Z0-9]+$`)

func (s *Server) addSlackSessionAuth(headers map[string]string, env *config.Environment, workspace string) {
	tWs, tName := parseSecretRef(env.SecretRef)
	if tok, err := secrets.ResolveWithWorkspaceCheck(tWs, tName, workspace); err == nil {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(string(tok))
	} else {
		s.logger.Error().Err(err).Str("secret_ref", env.SecretRef).Msg("failed to resolve slack token secret")
	}
	if env.CookieSecretRef != "" {
		cWs, cName := parseSecretRef(env.CookieSecretRef)
		if cook, err := secrets.ResolveWithWorkspaceCheck(cWs, cName, workspace); err == nil {
			headers["Cookie"] = "d=" + strings.TrimSpace(string(cook))
		} else {
			s.logger.Error().Err(err).Str("cookie_secret_ref", env.CookieSecretRef).Msg("failed to resolve slack cookie secret")
		}
	}
}

func validateSlackFileURL(raw, fileID, filesBase string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("slack returned an unparsable file url")
	}
	base, err := url.Parse(filesBase)
	if err != nil {
		return "", fmt.Errorf("invalid slack files base")
	}
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("file url is not on %s", base.Host)
	}
	pathRe := regexp.MustCompile(`^/files-pri/T[A-Z0-9]+-` + regexp.QuoteMeta(fileID) + `/[^/]+$`)
	if !pathRe.MatchString(u.EscapedPath()) {
		return "", fmt.Errorf("file url path does not belong to file %s", fileID)
	}
	return u.String(), nil
}

func isTextMimetype(m string) bool {
	m = strings.ToLower(strings.TrimSpace(strings.SplitN(m, ";", 2)[0]))
	switch m {
	case "text/html", "":
		return false
	case "application/json", "application/xml", "application/yaml", "application/x-yaml":
		return true
	}
	return strings.HasPrefix(m, "text/")
}

type slackFileInfo struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	File  struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Title      string `json:"title"`
		Mimetype   string `json:"mimetype"`
		Filetype   string `json:"filetype"`
		Size       int64  `json:"size"`
		URLPrivate string `json:"url_private"`
	} `json:"file"`
}

func (s *Server) executeSlackFileContent(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	fileID, _ := params["file"].(string)
	if !slackFileIDPattern.MatchString(fileID) {
		return 0, nil, fmt.Errorf("parameter file must match %s", slackFileIDPattern)
	}
	site, ok := ws.Sites[op.Site]
	if !ok {
		return 0, nil, fmt.Errorf("site %s not found in workspace", op.Site)
	}
	var env *config.Environment
	if name, ok := params["env"].(string); ok {
		if e, ok := site.Environments[name]; ok {
			env = &e
		}
	}
	if env == nil {
		for name := range site.Environments {
			e := site.Environments[name]
			env = &e
			break
		}
	}
	if env == nil || env.BaseURL == "" {
		return 0, nil, fmt.Errorf("no environment found for site %s", op.Site)
	}
	if env.Auth.Type != "slack-session" {
		return 0, nil, fmt.Errorf("site %s does not use slack-session auth", op.Site)
	}

	headers := map[string]string{"Accept": "application/json"}
	s.addSlackSessionAuth(headers, env, workspace)

	infoURL := strings.TrimSuffix(env.BaseURL, "/") + "/files.info?" + url.Values{"file": {fileID}}.Encode()
	infoResp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
		Method: "GET", Path: infoURL, Headers: headers, MaxBodyBytes: slackFileMaxBytes,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("slack files.info failed: %w", err)
	}
	var info slackFileInfo
	if err := json.Unmarshal(infoResp.Body, &info); err != nil {
		return 0, nil, fmt.Errorf("slack files.info returned an unreadable response (status %d)", infoResp.StatusCode)
	}
	if !info.OK {
		return 0, nil, fmt.Errorf("slack files.info error: %s", info.Error)
	}
	if info.File.ID != fileID {
		return 0, nil, fmt.Errorf("slack files.info returned a different file")
	}
	if !isTextMimetype(info.File.Mimetype) {
		return 0, nil, fmt.Errorf("file mimetype %q is not a text type; only text files can be read", info.File.Mimetype)
	}
	if info.File.Size > slackFileMaxBytes {
		return 0, nil, fmt.Errorf("file is %d bytes; the limit is %d", info.File.Size, slackFileMaxBytes)
	}

	filesBase := s.slackFilesBase
	if filesBase == "" {
		filesBase = defaultSlackFilesBase
	}
	fileURL, err := validateSlackFileURL(info.File.URLPrivate, fileID, filesBase)
	if err != nil {
		return 0, nil, err
	}

	delete(headers, "Accept")
	resp, err := s.httpClient.Execute(context.Background(), &connectors.Request{
		Method: "GET", Path: fileURL, Headers: headers, MaxBodyBytes: slackFileMaxBytes,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("slack file download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("slack file download returned status %d", resp.StatusCode)
	}
	if ct := resp.Headers["Content-Type"]; ct != "" && !isTextMimetype(ct) {
		return 0, nil, fmt.Errorf("slack file download returned content type %q, expected text", ct)
	}

	content := string(resp.Body)
	if !utf8.ValidString(content) {
		content = strings.ToValidUTF8(content, "�")
	}
	out, err := json.Marshal(map[string]interface{}{
		"ok": true,
		"file": map[string]interface{}{
			"id":       info.File.ID,
			"name":     info.File.Name,
			"title":    info.File.Title,
			"mimetype": info.File.Mimetype,
			"filetype": info.File.Filetype,
			"size":     info.File.Size,
		},
		"content": content,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("encoding file content: %w", err)
	}
	return http.StatusOK, out, nil
}
