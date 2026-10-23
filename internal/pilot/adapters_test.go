package pilot

import (
	"testing"
)

func TestPilotRejectsLiveConfiguration(t *testing.T) {
	cfg := Config{Fixture: true, MasterURL: "http://127.0.0.1:1", KataURL: "http://127.0.0.1:2", KataProject: "coop-kenn-spike", ThreadID: "fixture", SessionID: "opencode:fixture", OwnerTokenFile: "/tmp/owner", BirdTokenFile: "/tmp/bird", StateDir: "/tmp/state", KataHome: "/tmp/kata", KataBinary: "/tmp/kata-cli", AgentsViewBinary: "/tmp/archive", AgentsViewData: "/tmp/archive-data", OpenCodeDir: "/tmp/fixture"}
	if e := cfg.validate(); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Fixture = false }, func(c *Config) { c.MasterURL = "https://rack-mini.example" }, func(c *Config) { c.KataURL = "http://localhost:64682" }, func(c *Config) { c.KataProject = "production" }} {
		bad := cfg
		change(&bad)
		if bad.validate() == nil {
			t.Fatal("live configuration accepted", bad)
		}
	}
}
func TestPilotEvidenceRequiresResultNotCommandInput(t *testing.T) {
	if verifyPublicFailure([]byte(`{"messages":[{"tool_calls":[{"input_json":"exit 7"}]}]}`)) == nil {
		t.Fatal("tool input counted as execution")
	}
	raw := []byte(`{"messages":[{"tool_calls":[{"result_content":"Command exited with code 7.","result_events":[{"status":"errored","content":"Command exited with code 7."}]}]}]}`)
	if e := verifyPublicFailure(raw); e != nil {
		t.Fatal(e)
	}
}
