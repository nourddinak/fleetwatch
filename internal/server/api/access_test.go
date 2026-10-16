package api

import (
	"encoding/json"
	"testing"
)

func TestScopeFiltersNested(t *testing.T) {
	sc := &scope{servers: []string{"a"}, appSrv: map[string]string{}}
	var v any
	json.Unmarshal([]byte(`{"handoff":{"summary":{"incidents":[{"serverId":"a","title":"ok"},{"serverId":"b","title":"secret"}]}},
		"pages":[{"serverId":"b","title":"secret"},{"serverId":"a","title":"ok","inner":{"list":[{"serverId":"b"}]}}],"servers":{"a":1,"b":2}}`), &v)
	out, _ := json.Marshal(sc.filter(v, false))
	want := `{"handoff":{"summary":{"incidents":[{"serverId":"a","title":"ok"}]}},"pages":[{"inner":{"list":[]},"serverId":"a","title":"ok"}],"servers":{"a":1}}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
}
