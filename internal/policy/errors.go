package policy

// PrecheckJiraIssueLink is the Operation.Precheck value that scopes both ends of
// an issue link by id before the op's own request is sent.
const PrecheckJiraIssueLink = "jira_issue_link"

// ConstraintDenied is returned by a pre-check that refuses a request: the server
// turns it into a 403 with rule "constraint" (and no allow audit record).
type ConstraintDenied struct {
	Detail string
}

func (e *ConstraintDenied) Error() string { return e.Detail }
