package worker

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlanningPromptKeepsSelectedGoalOnceAndExcludesUnrelatedBodies(t *testing.T) {
	c, s, j := handoffFixture(t)
	s.Goals[0].Body = "SELECTED_GOAL_" + strings.Repeat("x", 11000)
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	s.Events[0].Body = s.Goals[0].Body
	other := s.Goals[0]
	other.URL = "https://github.com/orgs/planning/discussions/2"
	other.Body = "UNRELATED_PROPOSAL_" + strings.Repeat("y", 4759)
	other.BodySHA = BodyHash(other.Body)
	other.Decisions = nil
	s.Goals = append(s.Goals, other)
	before, _ := json.Marshal(s)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prompt, "SELECTED_GOAL_") != 1 || strings.Contains(prompt, "UNRELATED_PROPOSAL_") {
		t.Fatal("goal descriptions duplicated or unrelated body included")
	}
	for _, required := range []string{other.URL, other.BodySHA, s.Goals[0].BodySHA, s.Events[0].ID, j.ID} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing required identity %s", required)
		}
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("prompt construction mutated authority snapshot")
	}
}

func TestPromptOmitsRuntimeBookkeepingButKeepsCheckoutAndScope(t *testing.T) {
	c, s, j := handoffFixture(t)
	j.Checkout, j.Branch, j.ScopeSHA = "/workspace/worker/own", "agent/own", "scope-identity"
	j.SessionID = strings.Repeat("SESSION_BOOKKEEPING", 3000)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "SESSION_BOOKKEEPING") {
		t.Fatal("runtime session bookkeeping was sent to the model")
	}
	for _, required := range []string{j.Checkout, j.Branch, j.ScopeSHA} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing owned checkout/scope %s", required)
		}
	}
}

func TestWakePromptCanonicalizesFullGoalRootSource(t *testing.T) {
	c, s, _ := policyFixture(t)
	r := testWakeRequest(c)
	s.Goals[0].Body = "ROOT_SOURCE_" + strings.Repeat("z", 16000)
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	r.Source.DiscussionURL = s.Goals[0].URL
	r.Source.NodeID, r.Source.DiscussionID, r.Source.RootID = "goal", "goal", "goal"
	r.Source.Body = s.Goals[0].Body
	prompt, err := BuildWakePrompt(c, r, r.Source, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prompt, "ROOT_SOURCE_") != 1 || !strings.Contains(prompt, s.Goals[0].BodySHA) {
		t.Fatal("full root source not represented by a canonical goal")
	}
}

func TestPromptBudgetCountsFinalBytesAndDoesNotTruncate(t *testing.T) {
	c, s, now := policyFixture(t)
	j := Select(c, s, State{}, now)[0]
	s.Tasks[0].Body = "private-sentinel 鸟 café <>&\"\\\n"
	base, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	s.Tasks[0].Body += strings.Repeat("x", 32768-len(base))
	prompt, err := BuildPrompt(c, j, s)
	if err != nil || len(prompt) != 32768 {
		t.Fatalf("exact byte boundary rejected: bytes=%d err=%v", len(prompt), err)
	}
	var input struct{ Task Task }
	parts := strings.SplitN(prompt, "Quoted GitHub input and verified job identity:\n", 2)
	if json.Unmarshal([]byte(parts[1]), &input) != nil || input.Task.Body != s.Tasks[0].Body {
		t.Fatal("escaping or UTF-8 content changed at boundary")
	}
	prompt, err = buildPrompt(c, j, s, "!", now)
	var size *PromptSizeError
	if prompt != "" || !errors.As(err, &size) || size.ActualBytes != 32769 || size.LimitBytes != 32768 {
		t.Fatalf("post-build policy escaped budget: prompt bytes=%d err=%v", len(prompt), err)
	}
	if validatePromptFailure(size) != nil || errorCategory(err) != "prompt_size" {
		t.Fatal("missing valid size diagnostics")
	}
	for _, forbidden := range []string{"private-sentinel", "café", s.Tasks[0].Body} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("size diagnostic disclosed quoted input")
		}
	}
}

func TestPromptRejectsOversizedRequiredSections(t *testing.T) {
	for _, section := range []string{"Goal", "Event", "PlanningGoals"} {
		t.Run(section, func(t *testing.T) {
			c, s, j := handoffFixture(t)
			switch section {
			case "Goal":
				s.Goals[0].Body = strings.Repeat("鸟", 14000)
			case "Event":
				s.Events[0].Body = strings.Repeat("<", 6000)
			case "PlanningGoals":
				for range 100 {
					s.Goals = append(s.Goals, s.Goals[0])
				}
			}
			prompt, err := BuildPrompt(c, j, s)
			var size *PromptSizeError
			if prompt != "" || !errors.As(err, &size) {
				t.Fatalf("required context was silently dropped: %v", err)
			}
			found := false
			for _, s := range size.Sections {
				if s.Name == section && s.Bytes > 20000 {
					found = true
				}
			}
			if !found || validatePromptFailure(size) != nil {
				t.Fatalf("missing responsible-section counts: %+v", size)
			}
		})
	}
}

func TestPlanningIndexCannotRetargetSelectedTurnToAnotherApprovedGoal(t *testing.T) {
	c, s, j := handoffFixture(t)
	other := s.Goals[0]
	other.URL = "https://github.com/orgs/planning/discussions/2"
	other.Body = "Another approved goal"
	other.BodySHA = BodyHash(other.Body)
	s.Goals = append(s.Goals, other)
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Goal          Goal
		Event         Event
		PlanningGoals []struct {
			Goal     Goal
			Approval Approval
		}
	}
	parts := strings.SplitN(prompt, "Quoted GitHub input and verified job identity:\n", 2)
	if json.Unmarshal([]byte(parts[1]), &input) != nil || input.Goal.URL != j.GoalURL || input.Event.URL != j.GoalURL || len(input.PlanningGoals) != 2 {
		t.Fatal("selected planning authority changed")
	}
	if input.PlanningGoals[1].Approval.State != "approved" || input.PlanningGoals[1].Goal.Body != "" || input.PlanningGoals[1].Goal.BodySHA != other.BodySHA {
		t.Fatal("approved discovery index lost its decision or included unrelated body")
	}
	j.GoalURL = other.URL
	if ok, _ := Admit(c, j, s, time.Now()); ok {
		t.Fatal("discovery index retargeted an existing event's planning authority")
	}
}
