package worker

import (
	"strings"
	"testing"
)

func TestTeamPlannerPromptSuppliesPublicationRecipe(t *testing.T) {
	c, s, j, now := teamHandoffFixture(t, "engineer")
	c.Projects = []ProjectRef{{Owner: "planning", Number: 4, StatusField: "Phase", ReadyValue: "Queued for build", InProgressValue: "Building", DoneValue: "Delivered"}}
	bound, err := bindTeamJob(c, j, s, now)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := buildPrompt(c, bound, s, "", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--author MEMBER_NAME", "--depends-on ISSUE_URL", "separate issue comment", "Never append", "read back", "Queued for build", "engineering_peers", "Building"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("planner missing %q", want)
		}
	}
	source := ConversationSource{DiscussionURL: s.Goals[0].URL, DiscussionID: "discussion", RootID: "root", NodeID: "root", Body: "Please plan", Author: c.Owner}
	r := WakeRequest{ID: BodyHash("wake"), Target: c.Name, Kind: "ping", ChainID: BodyHash("wake")}
	prompt, err = BuildWakePrompt(c, r, source, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--author MEMBER_NAME", "separate issue comment", "Queued for build", "read back"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("planner conversation missing %q", want)
		}
	}
}
