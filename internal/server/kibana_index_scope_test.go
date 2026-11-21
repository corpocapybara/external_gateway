package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/external_gateway/internal/config"
	"github.com/external_gateway/internal/policy"
)

// kibana_index_scope was never enforced (EG0021): kibana.search reached any
// index. A refused index is now a 403 constraint deny before any upstream call.
func TestKibanaIndexScopeServer(t *testing.T) {
	f := newEnvFixture(t)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"hits":{"hits":[]}}`))
	}))
	t.Cleanup(upstream.Close)
	f.sites["kibana"] = config.Site{Environments: map[string]config.Environment{"prod": {BaseURL: upstream.URL}}}
	f.wsPolicy = config.WorkspacePolicy{KibanaIndices: []string{"logstash-nth-stg*"}}
	f.ops["kibana.search"] = &policy.Operation{ID: "kibana.search", Connector: "http-rest", Site: "kibana", Method: "POST",
		Path:   "/api/console/proxy?path={index}/_search&method=POST",
		Params: map[string]policy.ParamDef{"index": {Type: "string", Default: "logstash-nth-stg-1"}},
		Constraints: []map[string]interface{}{
			{"type": "kibana_index_scope", "indices": "$workspace.kibana_indices"},
		},
	}

	for _, dry := range []bool{false, true} {
		code, out := f.send(t, "kibana.search", `{"index":"secret-*"}`, dry)
		if code != http.StatusForbidden || out["error"] != "denied" || out["rule"] != "constraint" {
			t.Errorf("dry=%v secret-*: status %d, body %v; want 403 denied/constraint", dry, code, out)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("refused index reached upstream %d times", n)
	}
	code, out := f.send(t, "kibana.search", `{"index":"logstash-nth-stg-2026.11"}`, false)
	if code != http.StatusOK || out["status"] == "error" {
		t.Fatalf("allowed index: status %d, body %v", code, out)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("allowed index: upstream hits = %d, want 1", n)
	}
}
