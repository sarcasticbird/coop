package pilot

import "testing"

func TestRemotePilotRestrictsHostAndMode(t *testing.T) {
	n := NativeConfig{ContainerID: "coop-native-vm-test", Directory: "/tmp/native-vm", PasswordFile: "/tmp/password", Model: "openrouter/openai/gpt-5-mini", Marker: "PILOT_TESTS_PASS", Host: "rack-mini", Mode: "coding", ProtectedSHA: map[string]string{"go.mod": "hash", "ready_test.go": "hash", "AGENTS.md": "hash", ".gitignore": "hash"}, InitialCodeSHA: "hash"}
	if e := n.validate(); e != nil {
		t.Fatal(e)
	}
	n.Host = "some-other-host"
	if n.validate() == nil {
		t.Fatal("arbitrary host accepted")
	}
	n.Host = "rack-mini"
	n.Mode = "arbitrary"
	if n.validate() == nil {
		t.Fatal("arbitrary mode accepted")
	}
}
func TestCodingProofRequiresTestResultOfOwnedTurn(t *testing.T) {
	raw := []byte(`[{"info":{"role":"assistant","parentID":"msg_owned"},"parts":[{"type":"tool","tool":"bash","callID":"test_call","state":{"status":"completed","input":{"command":"go test ./..."},"output":"ok\tfixture/ready 0.01s","metadata":{"exit":0}}}]}]`)
	if _, e := nativeCodingProof(raw, "msg_owned"); e != nil {
		t.Fatal(e)
	}
	if _, e := nativeCodingProof(raw, "msg_foreign"); e == nil {
		t.Fatal("foreign result accepted")
	}
	if _, e := nativeCodingProof([]byte(`[{"info":{"role":"assistant","parentID":"msg_owned"},"parts":[{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"echo ok"},"output":"ok"}}]}]`), "msg_owned"); e == nil {
		t.Fatal("echo counted as a test")
	}
}
func TestRemoteArgumentQuoting(t *testing.T) {
	got := remoteQuote([]string{"cat", "/tmp/a'b", "$(touch bad)"})
	if got != "'cat' '/tmp/a'\"'\"'b' '$(touch bad)'" {
		t.Fatal(got)
	}
}
func TestCodingProofRejectsFailedOrUnknownExit(t *testing.T) {
	for _, metadata := range []string{``, `,"metadata":{"exit":1}`} {
		raw := []byte(`[{"info":{"role":"assistant","parentID":"msg_owned"},"parts":[{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"go test ./..."},"output":"ok\tfixture/good\nFAIL\tfixture/bad"` + metadata + `}}]}]`)
		if _, e := nativeCodingProof(raw, "msg_owned"); e == nil {
			t.Fatal("unverified exit accepted", metadata)
		}
	}
}
