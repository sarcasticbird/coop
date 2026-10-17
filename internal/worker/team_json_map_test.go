package worker

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTeamMigrationPreservesCaseDistinctOpaqueMapKeys(t *testing.T) {
	for _, record := range []string{"routing", "state snapshot"} {
		t.Run(record, func(t *testing.T) {
			c, s, state, w, _, _ := legacyCompletedWakeFixture(t, "discussion")
			rel := "state.json"
			want := map[string]string{"DC_opaque_a": "first revision", "DC_opaque_A": "second revision", "DC_opaque_s": "third revision", "DC_opaque_\u017f": "fourth revision", "DC_opaque_k": "fifth revision", "DC_opaque_\u212a": "sixth revision"}
			if record == "routing" {
				routing, err := w.Routing()
				if err != nil {
					t.Fatal(err)
				}
				routing.Seen = want
				if err := w.SaveRouting(routing); err != nil {
					t.Fatal(err)
				}
				rel = "wake/v1/routing.json"
			} else {
				state.SnapshotCache.Cursor = want
				if err := s.Save(state); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(filepath.Join(s.Dir, rel))
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(s.Dir, "migration-backup.json"))
			var backup map[string][]byte
			if err != nil || json.Unmarshal(raw, &backup) != nil || !bytes.Equal(backup[rel], before) {
				t.Fatal("opaque map backup bytes changed", err)
			}
			for range 2 {
				e := &Engine{Config: c, Store: NewVersionedFileStore(s.Dir, 2)}
				if err := e.initialize(); err != nil {
					t.Fatal(err)
				}
				got := e.state.SnapshotCache.Cursor
				if record == "routing" {
					routing, err := e.Wakes.Store.Routing()
					if err != nil {
						t.Fatal(err)
					}
					got = routing.Seen
					after, err := os.ReadFile(filepath.Join(s.Dir, rel))
					if err != nil || !bytes.Equal(after, before) {
						t.Fatal("opaque routing bytes changed", err)
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("case-distinct opaque IDs were conflated")
				}
			}
		})
	}
}

func TestStrictTeamJSONRetainsSchemaAndMapDuplicateChecks(t *testing.T) {
	for _, row := range []struct {
		name  string
		raw   string
		value func() any
	}{
		{"outer schema alias", `{"Version":1,"version":1}`, func() any { return new(WakeRoutingState) }},
		{"exact opaque map key", `{"Seen":{"DC_same":"one","DC_same":"two"}}`, func() any { return new(WakeRoutingState) }},
		{"nested map value schema alias", `{"Bindings":{"DC_opaque":{"SessionID":"ses_one","sessionid":"ses_two"}}}`, func() any { return new(WakeRoutingState) }},
		{"slice value schema alias", `{"SnapshotCache":{"Tasks":[{"URL":"one","url":"two"}]}}`, func() any { return new(State) }},
		{"pointer value schema alias", `{"Schedule":{"Version":2,"version":2}}`, func() any { return new(State) }},
		{"tagged schema alias", `{"base_branches":{},"BASE_BRANCHES":{}}`, func() any { return new(TeamGoalMetadata) }},
		{"unicode config schema alias", `{"state_dir":"/safe","\u017ftate_dir":"/var/lib/coop/worker/state"}`, func() any { return new(Config) }},
		{"unicode nested schema alias", `{"Bindings":{"DC_opaque":{"SessionID":"ses_one","\u017fessionID":"ses_two"}}}`, func() any { return new(WakeRoutingState) }},
		{"unicode custom limits wire alias", `{"limits":{"max_attempts":1,"max_attempt\u017f":2}}`, func() any { return new(Config) }},
		{"unicode custom opencode wire alias", `{"opencode":{"password_file":"/safe","pa\u017fsword_file":"/foreign"}}`, func() any { return new(Config) }},
		{"unicode Kelvin schema alias", `{"checkout_modes":{},"chec\u212aout_modes":{}}`, func() any { return new(Config) }},
		{"unicode untyped fallback alias", `{"state_dir":"/safe","\u017ftate_dir":"/foreign"}`, func() any { return new(any) }},
		{"unknown field", `{"Unknown":true}`, func() any { return new(WakeRoutingState) }},
		{"trailing document", `{"Version":1}{"Version":1}`, func() any { return new(WakeRoutingState) }},
		{"untyped folded fallback", `{"DC_opaque_a":1,"DC_opaque_A":2}`, func() any { return new(any) }},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := strictTeamJSON([]byte(row.raw), row.value()); err == nil {
				t.Fatal("ambiguous or malformed JSON accepted")
			}
		})
	}
}

func TestTeamMapKeysStillRequireCanonicalAuthority(t *testing.T) {
	body := `<!-- coop-goal:v2 {"repositories":["code/repo"],"base_branches":{"code/Repo":"main"},"qa":{"required":false}} -->`
	if _, err := ParseTeamGoalMetadata(body); err == nil {
		t.Fatal("case-variant repository key granted base branch authority")
	}
	p := teamPolicyFixture()
	p.EngineeringPeers = map[string]string{"Engineer": "planner"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TeamPolicy
	if err := strictTeamJSON(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTeamPolicy(decoded); err == nil {
		t.Fatal("case-variant member key granted engineering authority")
	}
}
