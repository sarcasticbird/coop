package coordination

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHandoffSupplementPreservesHistoricalRequestBytes(t *testing.T) {
	in := HandoffInput{Kind: "release", TaskURL: "task", RequestID: "original"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// This exact field order and omission predates retained/tag-only authority.
	want := `{"kind":"release","goal":{"source":"","master_id":"","goal_id":"","revision_sha256":""},"task_url":"task","pr_url":"","assignment_sha256":"","head_sha":"","artifact_sha256":"","evidence_message_id":"","request_id":"original"}`
	if string(raw) != want {
		t.Fatalf("historical request bytes changed: %s", raw)
	}
	in.Retained.SourcePolicySHA = "source"
	raw, err = json.Marshal(in)
	if err != nil || !bytes.Contains(raw, []byte(`"retained":`)) || bytes.Contains(raw, []byte(`"tag_only":`)) {
		t.Fatal("retained supplement serialization", err)
	}
	var decoded HandoffInput
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded != in {
		t.Fatal("supplement did not round trip", err)
	}
}
