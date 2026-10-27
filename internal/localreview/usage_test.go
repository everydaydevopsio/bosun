package localreview

import (
	"encoding/json"
	"testing"
)

func TestResultPreservesUsageAndUnknownBilling(t *testing.T) {
	// Use the on-disk representation to cover loading records written by the
	// monitor as well as compatibility with records predating usage capture.
	for _, raw := range []string{
		`{"Job":"review","Outcome":"completed","Usage":"{\"schema_version\":1,\"run_id\":\"review\",\"actual_cost_usd\":null,\"usage\":{\"source\":\"codex\",\"status\":\"available\",\"input_tokens\":120,\"output_tokens\":10}}"}`,
		`{"Job":"review","Outcome":"completed"}`,
	} {
		var r record
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(document(r))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		usage, exists := got["usage"]
		if !exists {
			t.Fatalf("usage field missing: %s", data)
		}
		if len(raw) > 100 {
			m, ok := usage.(map[string]any)
			if !ok || m["run_id"] != "review" || m["actual_cost_usd"] != nil {
				t.Fatalf("usage lost: %s", data)
			}
		} else if usage != nil {
			t.Fatal("old record acquired invented usage")
		}
	}
}

func TestMalformedStoredUsageDoesNotHideReview(t *testing.T) {
	r := record{Job: "review", Result: "Useful review.", Usage: `{"schema_version":99}`}
	d := document(r)
	if d.Usage != nil || d.UsageNote == "" || d.Markdown != r.Result {
		t.Fatalf("bad fallback: %+v", d)
	}
}
