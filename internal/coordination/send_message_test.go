package coordination

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSendMessageMembershipWireSemantics(t *testing.T) {
	for _, expected := range [][]string{nil, {}, {"owner", "hawk"}} {
		in := SendMessage{ThreadID: "thread", Body: "synthetic body", RequestID: "request", ExpectedMembers: expected}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if expected == nil && string(raw) != `{"thread_id":"thread","body":"synthetic body","request_id":"request"}` {
			t.Fatalf("legacy nil request bytes changed: %s", raw)
		}
		var decoded SendMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded.ExpectedMembers, expected) {
			t.Fatalf("membership guard changed over JSON: got %v (nil=%t), want %v (nil=%t)", decoded.ExpectedMembers, decoded.ExpectedMembers == nil, expected, expected == nil)
		}
	}
}
