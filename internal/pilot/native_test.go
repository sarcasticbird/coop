package pilot

import (
	"testing"
)

func TestPilotNativeConfigCannotSelectABird(t *testing.T) {
	c := NativeConfig{ContainerID: "coop-native-vm-fixture", Directory: "/tmp/native-vm", PasswordFile: "/tmp/private/password", Model: "openrouter/openai/gpt-5-mini", Marker: "unique-marker"}
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	c.ContainerID = "coop-agent-hawk"
	if c.validate() == nil {
		t.Fatal("live bird accepted")
	}
}
func TestNativeArchiveNeedsActualReadAndAssistantResult(t *testing.T) {
	if verifyNativeArchive([]byte(`{"messages":[{"role":"user","source_uuid":"msg_owned","content":"kata#abc delivery1 marker"}]}`), "msg_owned", "kata#abc", "delivery1", "marker") == nil {
		t.Fatal("prompt counted as evidence")
	}
	raw := []byte(`{"messages":[{"role":"user","source_uuid":"msg_owned","content":"kata#abc delivery1"},{"role":"assistant","content":"marker","tool_calls":[{"tool_name":"read","result_content":"marker"}]}]}`)
	if e := verifyNativeArchive(raw, "msg_owned", "kata#abc", "delivery1", "marker"); e != nil {
		t.Fatal(e)
	}
	if verifyNativeArchive(raw, "msg_foreign", "kata#abc", "delivery1", "marker") == nil {
		t.Fatal("foreign turn accepted")
	}
}
func TestNativeReadProofRequiresOwnedSuccessfulOutput(t *testing.T) {
	raw := []byte(`[{"info":{"role":"assistant","parentID":"msg_owned"},"parts":[{"type":"tool","tool":"read","callID":"call_read","state":{"status":"completed","output":"actual-marker"}}]}]`)
	if _, e := nativeReadProof(raw, "msg_owned", "actual-marker"); e != nil {
		t.Fatal(e)
	}
	if _, e := nativeReadProof(raw, "msg_foreign", "actual-marker"); e == nil {
		t.Fatal("foreign native output accepted")
	}
	if _, e := nativeReadProof([]byte(`[{"info":{"role":"assistant","parentID":"msg_owned"},"parts":[{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"actual-marker"}}}]}]`), "msg_owned", "actual-marker"); e == nil {
		t.Fatal("input accepted as output")
	}
}
func TestNativeArchiveAcceptsCompletedEventWithoutBody(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","source_uuid":"msg_owned","content":"kata#abc delivery1"},{"role":"assistant","content":"marker","tool_calls":[{"tool_name":"read","result_events":[{"status":"completed"}]}]}]}`)
	if e := verifyNativeArchive(raw, "msg_owned", "kata#abc", "delivery1", "marker"); e != nil {
		t.Fatal(e)
	}
}
