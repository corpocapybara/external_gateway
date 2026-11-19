package server

import (
	"sort"
	"strings"
	"testing"

	"github.com/external_gateway/internal/policy"
)

// Every connector name policy accepts has a dispatch entry and every entry is a
// name policy accepts (EG0063); the environment-picking set is pinned, and a
// name with no entry is an error.
func TestConnectorDispatchMatchesPolicy(t *testing.T) {
	known := policy.KnownConnectors()
	sort.Strings(known)
	var dispatched, pickEnv []string
	for name, d := range connectorDispatches {
		dispatched = append(dispatched, name)
		if d.pickEnv {
			pickEnv = append(pickEnv, name)
		}
	}
	sort.Strings(dispatched)
	sort.Strings(pickEnv)
	if strings.Join(dispatched, ",") != strings.Join(known, ",") {
		t.Errorf("dispatch entries %v, policy.KnownConnectors() %v: add or remove the connector in both", dispatched, known)
	}
	if got, want := strings.Join(pickEnv, ","), "email,form-session,http-rest,jira-worklog,mcp"; got != want {
		t.Errorf("connectors that pick their environment = %s, want %s", got, want)
	}

	s := &Server{}
	if _, _, err := s.dispatchConnector(nil, &policy.Operation{Connector: "nope"}, nil, "w"); err == nil || !strings.Contains(err.Error(), `connector "nope" has no dispatch entry`) {
		t.Errorf("unknown connector: err = %v", err)
	}
}
