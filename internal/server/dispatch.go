package server

import (
	"fmt"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// connectorDispatch is how executeOp runs one connector (EG0063). The names are
// policy.KnownConnectors(): loading an ops file refuses any other name, and
// TestConnectorDispatchMatchesPolicy fails when the two drift apart.
type connectorDispatch struct {
	// pickEnv: the connector chooses its environment with resolveEnv, so
	// checkEnvSelection refuses a missing-but-ambiguous or unknown env first.
	pickEnv bool
	run     func(s *Server, ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error)
}

var connectorDispatches = map[string]connectorDispatch{
	"http-rest": {pickEnv: true, run: func(s *Server, ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
		switch {
		case op.SlackFileContent:
			return s.executeSlackFileContent(ws, op, params, workspace)
		case op.JiraAttachmentContent:
			return s.executeJiraAttachmentContent(ws, op, params, workspace)
		default:
			return s.executeHttpConnector(ws, op, params, workspace)
		}
	}},
	"form-session": {pickEnv: true, run: (*Server).executeHttpConnector},
	"postgres":     {run: (*Server).executePostgresConnector},
	"local-service": {run: func(s *Server, ws *config.Workspace, op *policy.Operation, params map[string]interface{}, _ string) (int, []byte, error) {
		body, err := s.executeLocalServiceConnector(ws, op, params)
		return 200, body, err
	}},
	"mcp":          {pickEnv: true, run: (*Server).executeMcpConnector},
	"email":        {pickEnv: true, run: (*Server).executeEmailConnector},
	"jira-worklog": {pickEnv: true, run: (*Server).executeJiraWorklog},
}

// dispatchConnector runs op through its connector. A name with no entry is an
// error, never a silent empty result.
func (s *Server) dispatchConnector(ws *config.Workspace, op *policy.Operation, params map[string]interface{}, workspace string) (int, []byte, error) {
	d, ok := connectorDispatches[op.Connector]
	if !ok {
		return 0, nil, fmt.Errorf("connector %q has no dispatch entry", op.Connector)
	}
	return d.run(s, ws, op, params, workspace)
}
