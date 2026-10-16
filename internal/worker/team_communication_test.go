package worker

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func teamCommunicationFixture(t *testing.T) (Config, *FileWakeStore) {
	t.Helper()
	c, store := wakeFixture(t)
	p := teamPolicyFixture()
	p.Members = append(p.Members, TeamMember{Name: "fourth", Actor: Actor{ID: 14, Login: "actual-fourth[bot]", Kind: "Bot"}, Capabilities: []string{"implement"}})
	p.EngineeringPeers["fourth"] = "engineer"
	c.Version, c.Team, c.Role = 2, &p, ""
	c.Implementer, c.Reviewer = Actor{}, Actor{}
	c.Name, c.Self = p.Members[3].Name, p.Members[3].Actor
	return c, store
}

func TestTeamCommunicationTargetsImmutableRosterActors(t *testing.T) {
	c, _ := teamCommunicationFixture(t)
	now := time.Now()
	qa := c.Team.Members[2].Actor
	renamed := qa
	renamed.Login = "renamed-qa[bot]"
	for _, row := range []struct {
		name, body     string
		author, parent Actor
		want           []Actor
	}{
		{"all members", "@planner @engineer @qa @actual-fourth please explain", c.Owner, Actor{}, wakeParticipants(c)},
		{"renamed trusted author", "@actual-fourth explain the evidence", renamed, Actor{}, []Actor{c.Self}},
		{"implicit immutable parent", "Please explain the evidence", c.Owner, Actor{ID: c.Self.ID, Kind: "Bot", Login: "renamed"}, []Actor{c.Self}},
		{"explicit overrides parent", "@qa explain the evidence", c.Owner, c.Self, []Actor{qa}},
		{"self suppressed", "@actual-fourth explain the evidence", c.Self, Actor{}, nil},
		{"outsider login", "@actual-fourth explain the evidence", Actor{ID: 999, Kind: "Bot", Login: qa.Login}, Actor{}, nil},
		{"owner impersonation", "@actual-fourth explain the evidence", Actor{ID: 999, Kind: c.Owner.Kind, Login: c.Owner.Login}, Actor{}, nil},
		{"wrong actor kind", "@actual-fourth explain the evidence", Actor{ID: qa.ID, Kind: "User", Login: qa.Login}, Actor{}, nil},
		{"member alias not address", "@fourth explain the evidence", c.Owner, Actor{}, nil},
		{"quoted address", "> @actual-fourth explain the evidence", c.Owner, Actor{}, nil},
		{"acknowledgement", "@actual-fourth thanks", qa, Actor{}, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := sourceFixture(c, "source", row.body, row.author, now)
			if row.parent.ID != 0 {
				s.ParentID, s.ParentAuthor = "parent", row.parent
			}
			if got := ConversationTargets(c, s); !reflect.DeepEqual(got, row.want) {
				t.Fatalf("targets = %+v, want %+v", got, row.want)
			}
		})
	}
}

func TestTeamCommunicationWakeRejectsUnboundSelf(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Team = nil },
		func(c *Config) { c.Self.ID = 999 },
		func(c *Config) { c.Self.Kind = "User" },
		func(c *Config) { c.Name = "qa" },
	} {
		c, _ := teamCommunicationFixture(t)
		change(&c)
		r := testWakeRequest(c)
		if prompt, err := BuildWakePrompt(c, r, r.Source, Snapshot{}); prompt != "" || err == nil {
			t.Fatal("unbound wake identity received prompt")
		}
	}
}

func TestTeamCommunicationRoutingBudgetRestartAndRosterBaseline(t *testing.T) {
	c, store := teamCommunicationFixture(t)
	now := time.Now().UTC()
	s := Snapshot{Complete: true, WakeSources: []ConversationSource{sourceFixture(c, "history", "@actual-fourth historical question", c.Owner, now)}}
	ingest := func() {
		t.Helper()
		if err := IngestDiscussionWakes(c, s, NewWakeStore(store.Dir), now); err != nil {
			t.Fatal(err)
		}
	}
	ingest()
	for i, actor := range []Actor{c.Owner, c.Team.Members[2].Actor, c.Team.Members[1].Actor, c.Team.Members[0].Actor} {
		s.WakeSources = append(s.WakeSources, sourceFixture(c, string(rune('a'+i)), "@actual-fourth explain the evidence", actor, now.Add(time.Duration(i+1)*time.Second)))
		ingest()
		ingest()
	}
	requests, err := store.Requests()
	if err != nil || len(requests) != 3 {
		t.Fatalf("baseline/dedup/two-hop budget: requests=%d err=%v", len(requests), err)
	}
	for i, r := range requests {
		if r.PeerHop != i || r.ChainID != requests[0].ChainID {
			t.Fatal("roster peers reset the shared chain")
		}
	}
	s.WakeSources = append(s.WakeSources, sourceFixture(c, "reset", "@actual-fourth continue", c.Owner, now.Add(time.Minute)))
	ingest()
	requests, _ = store.Requests()
	if len(requests) != 4 || requests[3].PeerHop != 0 || requests[3].ChainID == requests[0].ChainID {
		t.Fatal("owner did not reset chain")
	}
	c.Team.Members[2].Actor.ID = 99
	s.WakeSources = append(s.WakeSources, sourceFixture(c, "new-roster-history", "@actual-fourth question", c.Owner, now.Add(2*time.Minute)))
	ingest()
	requests, _ = store.Requests()
	if len(requests) != 4 {
		t.Fatal("changed roster replayed history instead of baselining")
	}
}

func TestTeamCommunicationWakePromptRolesAndByteBoundary(t *testing.T) {
	for _, name := range []string{"planner", "engineer", "qa", "fourth"} {
		t.Run(name, func(t *testing.T) {
			c, _ := teamCommunicationFixture(t)
			c = qaGateActor(c, name)
			r := testWakeRequest(c)
			r.Source.Body = "private-sentinel <>&\"\\\n"
			prompt, err := BuildWakePrompt(c, r, r.Source, Snapshot{})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"Self":`, `"Owner":`, `"Capabilities":`, `"Team":`, c.Self.Login, r.Source.DiscussionID, r.Source.RootID, `"State":"blocked"`, "coop-pr:v2", "not a coding"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("missing %s", want)
				}
			}
			for _, forbidden := range []string{"Hawk", "Robin", "coop-pr:v1", `"Implementer":`, `"Reviewer":`} {
				if strings.Contains(prompt, forbidden) {
					t.Fatalf("legacy assumption: %s", forbidden)
				}
			}
			role := "team conversation"
			if name == "planner" {
				role = "planner conversation"
			}
			if !strings.Contains(prompt, `"Role":"`+role+`"`) {
				t.Fatal("wrong conversation role")
			}
			r.Source.Body += strings.Repeat("x", MaxPromptBytes-len(prompt))
			prompt, err = BuildWakePrompt(c, r, r.Source, Snapshot{})
			if err != nil || len(prompt) != MaxPromptBytes {
				t.Fatalf("exact boundary: %d %v", len(prompt), err)
			}
			var input struct{ Source ConversationSource }
			parts := strings.SplitN(prompt, "Quoted wake input:\n", 2)
			if json.Unmarshal([]byte(parts[1]), &input) != nil || input.Source.Body != r.Source.Body {
				t.Fatal("source changed at boundary")
			}
			r.Source.Body += "!"
			prompt, err = BuildWakePrompt(c, r, r.Source, Snapshot{})
			var size *PromptSizeError
			if prompt != "" || !errors.As(err, &size) || size.ActualBytes != MaxPromptBytes+1 || validatePromptFailure(size) != nil || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatalf("invalid size diagnostic: %v", err)
			}
		})
	}
}

func TestTeamCommunicationTaskPromptActorsAndTarget(t *testing.T) {
	for _, kind := range []string{"review", "qa", "deliver"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := qaGateFixture(t)
			switch kind {
			case "review":
				c = qaGateActor(c, s.Tasks[0].Assignment.Peer)
			case "qa":
				c = qaGateActor(c, s.Tasks[0].Assignment.QA)
			}
			j := qaGateJob(t, c, s, now, kind)
			prompt, err := buildPrompt(c, j, s, "", now)
			if err != nil {
				t.Fatal(err)
			}
			for _, actor := range c.Team.Members {
				if !strings.Contains(prompt, actor.Actor.Login) {
					t.Fatalf("missing assigned actor %s", actor.Name)
				}
			}
			for _, want := range []string{`"Role":"` + j.Binding.Role + `"`, `"Self":`, `"Author":`, `"Peer":`, `"QA":`, s.Goals[0].URL, j.TaskURL, j.PRURL, j.HeadSHA, s.Tasks[0].Assignment.BaseBranch, j.Binding.AssignmentSHA} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("missing bound target %s", want)
				}
			}
			policy := strings.Repeat("x", MaxPromptBytes-len(prompt))
			prompt, err = buildPrompt(c, j, s, policy, now)
			if err != nil || len(prompt) != MaxPromptBytes {
				t.Fatalf("exact boundary: %d %v", len(prompt), err)
			}
			prompt, err = buildPrompt(c, j, s, policy+"!", now)
			var size *PromptSizeError
			if prompt != "" || !errors.As(err, &size) || size.ActualBytes != MaxPromptBytes+1 || validatePromptFailure(size) != nil {
				t.Fatalf("invalid size diagnostic: %v", err)
			}
		})
	}
}
