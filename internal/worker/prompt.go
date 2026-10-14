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
	identity, err := json.Marshal(struct {
		Name, Role                  string
		Self, Implementer, Reviewer Actor
	}{c.Name, c.Role, c.Self, c.Implementer, c.Reviewer})
	if err != nil {
		return "", errors.New("cannot build worker identity")
	}
	input := struct {
		Job            Job
		Goal           *Goal
		Task           *Task
		PR             *PR
		Event          *Event
		Approval       Approval
		Owner          Actor
		ResultMarker   string
		ReviewHandoffs []ReviewHandoff
		PlanningGoals  []struct {
			Goal     Goal
			Approval Approval
		}
	}{Job: j, Owner: c.Owner}
	if g, ok := goalFor(s, j.GoalURL); ok {
		input.Approval = EvaluateGoal(c, g, time.Now())
		g.Decisions = nil
		input.Goal = &g
	}
	if t, ok := taskFor(s, j.TaskURL); ok {
		input.Task = &t
	}
	if p, ok := prFor(s, j.PRURL); ok {
		input.PR = &p
	}
	if j.Kind == "plan" {
		input.ResultMarker = marker(j, "result")
		input.ReviewHandoffs = reviewHandoffs(c, j, s, time.Now())
		for _, e := range planningEvents(c, s, time.Now()) {
			if e.ID == j.TaskURL {
				copy := e
				input.Event = &copy
			}
		}
		for _, g := range s.Goals {
			approval := EvaluateGoal(c, g, time.Now())
			g.Decisions = nil
			input.PlanningGoals = append(input.PlanningGoals, struct {
				Goal     Goal
				Approval Approval
			}{g, approval})
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", errors.New("cannot build worker input")
	}
	role := implementerPrompt
	if c.Role == "planner" {
		role = plannerPrompt
	}
	prompt := commonPrompt + "\nTrusted runtime identity:\n" + string(identity) + "\n" + role + "\nQuoted GitHub input and verified job identity:\n" + string(data)
	if len(prompt) > 32<<10 {
		return "", errors.New("worker prompt exceeds supported scope size")
	}
	return prompt, nil
}
