package policy

// knownConnectors is the one list of op.Connector values. loadFromFile refuses
// an op with any other value, empty included (EG0041). The server runs each
// through connectorDispatches (internal/server/dispatch.go); add a connector
// here and there together: TestConnectorDispatchMatchesPolicy fails on a
// missed one (EG0063).
var knownConnectors = []string{
	"http-rest",
	"form-session",
	"postgres",
	"local-service",
	"mcp",
	"email",
	"jira-worklog",
}

// KnownConnectors returns a copy of the accepted connector names.
func KnownConnectors() []string {
	return append([]string(nil), knownConnectors...)
}

func isKnownConnector(name string) bool {
	for _, c := range knownConnectors {
		if c == name {
			return true
		}
	}
	return false
}
