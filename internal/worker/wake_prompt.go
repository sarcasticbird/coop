package worker

import (
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

//go:embed prompts/wake.txt
var wakePrompt string

//go:embed prompts/team-wake.txt
var teamWakePrompt string

type wakeMarker struct {
	Request string `json:"request"`
	Chain   string `json:"chain"`
	Hop     int    `json:"hop"`
	Kind    string `json:"kind"`
}

func WakeResultMarker(r WakeRequest) string {
	raw, _ := json.Marshal(wakeMarker{r.ID, r.ChainID, r.PeerHop, "reply"})
	return "<!-- coop-wake:v1 " + string(raw) + " -->"
}
func wakeReplyTarget(s ConversationSource) string {
	if s.NodeID == s.DiscussionID {
		return ""
	}
	return s.RootID
}
func BuildWakePrompt(c Config, r WakeRequest, source ConversationSource, snapshot Snapshot) (string, error) {
	if r.Target != c.Name || !goalInScope(c, Goal{URL: source.DiscussionURL}) || source.DiscussionID == "" || source.RootID == "" {
		return "", errors.New("invalid wake prompt scope")
	}
	identity, _ := json.Marshal(struct {
		Name, Role                         string
		Self, Implementer, Reviewer, Owner Actor
	}{c.Name, c.Role, c.Self, c.Implementer, c.Reviewer, c.Owner})
	instructions := wakePrompt
	if c.Version == 2 {
		if c.Team == nil {
			return "", errors.New("missing wake team roster")
		}
		member, ok := TeamMemberForActor(*c.Team, c.Self)
		if !ok || member.Name != c.Name {
			return "", errors.New("wake self does not match roster")
		}
		role := "team conversation"
		if member.Name == c.Team.Planner {
			role = "planner conversation"
		}
		var projects []ProjectRef
		var projectDefaults *ProjectRef
		if member.Name == c.Team.Planner {
			projects, projectDefaults = plannerProjectContext(c)
		}
		identity, _ = json.Marshal(struct {
			Name, Role      string
			Self, Owner     Actor
			Capabilities    []string
			Team            *TeamPolicy
			Projects        []ProjectRef `json:",omitempty"`
			ProjectDefaults *ProjectRef  `json:",omitempty"`
		}{member.Name, role, c.Self, c.Owner, member.Capabilities, c.Team, projects, projectDefaults})
		instructions = teamWakePrompt
		if member.Name == c.Team.Planner {
			instructions += "\nYou are the configured planner for this conversation. Perform permitted planning and administrative repairs now within verified goal scope; leave coding, engineering review, QA and delivery to their dedicated assigned jobs.\n"
			instructions += taskAdministrationPrompt
		} else {
			instructions += "\nYou are not the configured planner. Answer as Self, report evidence or blockers, and coordinate planning changes with Team.Planner. Do not generate trusted task assignments or administer another author's tracking.\n"
		}
	}
	input := struct {
		RequestID, Kind, Prompt string
		Source                  promptSource
		ReplyToID, ResultMarker string
		Goal                    *Goal
		Approval                Approval
	}{RequestID: r.ID, Kind: r.Kind, Prompt: r.Prompt, Source: promptSource{ConversationSource: source}, ReplyToID: wakeReplyTarget(source), ResultMarker: WakeResultMarker(r)}
	input.Approval = Approval{State: "blocked", Reason: "goal not discovered in a complete snapshot"}
	if g, ok := goalFor(snapshot, source.DiscussionURL); ok && snapshot.Complete {
		input.Approval = EvaluateGoal(c, g, time.Now())
		g.Decisions = nil
		input.Goal = &g
	}
	input.Source.BodyRef = canonicalBody(&input.Source.Body, input.Goal)
	data, err := promptInput(
		promptField{"RequestID", input.RequestID}, promptField{"Kind", input.Kind}, promptField{"Prompt", input.Prompt},
		promptField{"Source", input.Source}, promptField{"ReplyToID", input.ReplyToID}, promptField{"ResultMarker", input.ResultMarker},
		promptField{"Goal", input.Goal}, promptField{"Approval", input.Approval},
	)
	if err != nil {
		return "", err
	}
	sections := []promptSection{
		{"identity", "Trusted runtime identity:\n" + string(identity) + "\n"},
		{"instructions", instructions + "\nBodyRef resolves to the complete supplied Goal.Body at the exact URL and body hash; it is not an excerpt.\n"},
		{"ResultMarker", "Required result marker:\n" + WakeResultMarker(r) + "\n"},
		{"framing", "Quoted wake input:\n"},
	}
	sections = append(sections, data...)
	return assemblePrompt(r.ID, source.DiscussionURL, sections...)
}
