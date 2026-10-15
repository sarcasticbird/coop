package worker

import (
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

//go:embed prompts/common.txt
var commonPrompt string

//go:embed prompts/implementer.txt
var implementerPrompt string

//go:embed prompts/planner.txt
var plannerPrompt string

func WorkerToolPolicy(paths []string) ([]byte, error) {
	read := map[string]string{}
	for _, path := range append([]string{"*/auth.json", "*/credentials/*", "/run/coop/current-credentials/*", "/dev/shm/coop-credentials/*", "*/github-apps/*.pem"}, paths...) {
		read[path] = "deny"
	}
	bash := map[string]string{}
	for _, pattern := range []string{"*auth.json*", "*current-credentials*", "*coop-credentials*", "*COOP_GITHUB_APP_BUNDLE_FILE*", "*printenv*", "env", "env -0", "export -p", "declare -p*", "typeset -p*", "*gh auth*"} {
		bash[pattern] = "deny"
	}
	return json.Marshal(map[string]any{"permission": map[string]any{"question": "deny", "read": read, "bash": bash}})
}
func BuildPrompt(c Config, j Job, s Snapshot) (string, error) {
	return buildPrompt(c, j, s, "", time.Now())
}

func buildPrompt(c Config, j Job, s Snapshot, policy string, now time.Time) (string, error) {
	identity, err := json.Marshal(struct {
		Name, Role                  string
		Self, Implementer, Reviewer Actor
	}{c.Name, c.Role, c.Self, c.Implementer, c.Reviewer})
	if err != nil {
		return "", errors.New("cannot build worker identity")
	}
	// Only model-relevant identity and checkout fields, never mutable session or
	// checkpoint bookkeeping. Authority continues to come from policy evaluation.
	job := struct {
		ID, Kind, GoalURL, TaskURL, PRURL, HeadSHA, InputRevision, ScopeSHA string
		Checkout, Branch                                                    string
	}{j.ID, j.Kind, j.GoalURL, j.TaskURL, j.PRURL, j.HeadSHA, j.InputRevision, j.ScopeSHA, j.Checkout, j.Branch}
	var goal *Goal
	var task *Task
	var pr *PR
	var event *promptEvent
	var approval Approval
	var resultMarker string
	var handoffs []ReviewHandoff
	var goals []struct {
		Goal     promptGoalRef
		Approval Approval
	}
	if g, ok := goalFor(s, j.GoalURL); ok {
		approval = EvaluateGoal(c, g, now)
		g.Decisions = nil
		goal = &g
	}
	if t, ok := taskFor(s, j.TaskURL); ok {
		task = &t
	}
	if p, ok := prFor(s, j.PRURL); ok {
		pr = &p
	}
	if j.Kind == "plan" {
		resultMarker = marker(j, "result")
		handoffs = reviewHandoffs(c, j, s, now)
		for _, e := range planningEvents(c, s, now) {
			if e.ID == j.TaskURL {
				copy := promptEvent{Event: e}
				copy.BodyRef = canonicalBody(&copy.Body, goal)
				event = &copy
			}
		}
		for _, g := range s.Goals {
			goals = append(goals, struct {
				Goal     promptGoalRef
				Approval Approval
			}{promptGoalRef{g.URL, g.BodySHA, g.Repositories, g.CreatedAt.Format(time.RFC3339Nano), g.LastEditedAt.Format(time.RFC3339Nano), g.Readable}, EvaluateGoal(c, g, now)})
		}
	}
	data, err := promptInput(
		promptField{"Job", job}, promptField{"Goal", goal}, promptField{"Task", task},
		promptField{"PR", pr}, promptField{"Event", event}, promptField{"Approval", approval},
		promptField{"Owner", c.Owner}, promptField{"ResultMarker", resultMarker},
		promptField{"ReviewHandoffs", handoffs}, promptField{"PlanningGoals", goals},
	)
	if err != nil {
		return "", err
	}
	role := implementerPrompt
	if c.Role == "planner" {
		role = plannerPrompt
	}
	sections := []promptSection{
		{"instructions", commonPrompt + "\n" + role + "\n"},
		{"identity", "Trusted runtime identity:\n" + string(identity) + "\n"},
		{"policy", policy},
		{"framing", "\nQuoted GitHub input and verified job identity:\n"},
	}
	sections = append(sections, data...)
	return assemblePrompt(j.ID, j.GoalURL, sections...)
}
