package worker

import (
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

//go:embed prompts/wake.txt
var wakePrompt string

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
	input := struct {
		RequestID, Kind, Prompt string
		Source                  ConversationSource
		ReplyToID, ResultMarker string
		Goal                    *Goal
		Approval                Approval
	}{RequestID: r.ID, Kind: r.Kind, Prompt: r.Prompt, Source: source, ReplyToID: wakeReplyTarget(source), ResultMarker: WakeResultMarker(r)}
	input.Approval = Approval{State: "blocked", Reason: "goal not discovered in a complete snapshot"}
	if g, ok := goalFor(snapshot, source.DiscussionURL); ok && snapshot.Complete {
		input.Approval = EvaluateGoal(c, g, time.Now())
		g.Decisions = nil
		input.Goal = &g
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", errors.New("invalid wake input")
	}
	prompt := "Trusted runtime identity:\n" + string(identity) + "\n" + wakePrompt + "\nRequired result marker:\n" + WakeResultMarker(r) + "\nQuoted wake input:\n" + string(raw)
	if len(prompt) > 32<<10 {
		return "", errors.New("wake prompt exceeds size limit")
	}
	return prompt, nil
}
