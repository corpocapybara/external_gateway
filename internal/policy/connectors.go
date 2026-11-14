package policy

// knownConnectors lists the op.Connector values that executeOp in
// internal/server/server.go dispatches. loadFromFile refuses an op with any
// other value, empty included (EG0041). Add a connector here and in the
// dispatch chain together.
var knownConnectors = []string{
	"http-rest",
	"form-session",
	"postgres",
	"local-service",
	"mcp",
	"email",
	"jira-worklog",
}

func isKnownConnector(name string) bool {
	for _, c := range knownConnectors {
		if c == name {
			return true
		}
	}
	return false
}
