package worker

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// mentionText discards quoted examples and code rather than treating them as addresses.
func mentionText(body string) string {
	var result strings.Builder
	fence := ""
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			mark := trim[:3]
			switch fence {
			case "":
				fence = mark
			case mark:
				fence = ""
			}
			continue
		}
		if fence != "" || strings.HasPrefix(trim, ">") {
			continue
		}
		inCode := false
		for _, r := range line {
			if r == '`' {
				inCode = !inCode
				result.WriteByte(' ')
			} else if !inCode {
				result.WriteRune(r)
			}
		}
		result.WriteByte('\n')
	}
	return result.String()
}
func wakeParticipants(c Config) []Actor {
	if c.Version == 2 {
		var actors []Actor
		if c.Team != nil {
			for _, member := range c.Team.Members {
				actors = append(actors, member.Actor)
			}
		}
		return actors
	}
	return []Actor{c.Implementer, c.Reviewer}
}

func trustedWakeAuthor(c Config, author Actor) bool {
	if sameActor(author, c.Owner) {
		return true
	}
	for _, actor := range wakeParticipants(c) {
		if sameActor(author, actor) {
			return true
		}
	}
	return false
}

func ConversationTargets(c Config, s ConversationSource) []Actor {
	if !trustedWakeAuthor(c, s.Author) {
		return nil
	}
	if !sameActor(s.Author, c.Owner) && !substantiveWakeText(s.Body) {
		return nil
	}
	body := " " + mentionText(s.Body) + " "
	var targets []Actor
	for _, a := range wakeParticipants(c) {
		name := strings.TrimSuffix(strings.ToLower(a.Login), "[bot]")
		// Test each configured name independently so adjacent mentions can share a delimiter.
		rx := regexp.MustCompile(`(?i)(^|[^a-z0-9_@.\-])@` + regexp.QuoteMeta(name) + `(\[bot\])?($|[^a-z0-9_.\[\]-])`)
		if rx.MatchString(body) {
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 && s.ParentID != "" {
		for _, a := range wakeParticipants(c) {
			if sameActor(s.ParentAuthor, a) {
				targets = append(targets, a)
			}
		}
	}
	var out []Actor
	for _, a := range targets {
		if !sameActor(s.Author, a) {
			out = append(out, a)
		}
	}
	return out
}
func canonicalConversation(s ConversationSource) ConversationSource {
	s.Revision = ""
	s.Author.Login = ""
	s.ParentAuthor.Login = ""
	s.CreatedAt = time.Time{}
	s.EditedAt = time.Time{}
	s.Body = strings.TrimSpace(strings.ReplaceAll(s.Body, "\r\n", "\n"))
	return s
}
func ConversationRevision(s ConversationSource) string {
	raw, _ := json.Marshal(canonicalConversation(s))
	return BodyHash(string(raw))
}
func conversationSource(d graphDiscussion, c Comment) ConversationSource {
	root := c.RootID
	if root == "" {
		root = c.ID
	}
	u := c.URL
	if u == "" {
		u = d.URL
	}
	s := ConversationSource{NodeID: c.NodeID, URL: u, DiscussionID: d.ID, DiscussionURL: d.URL, RootID: root, ParentID: c.ParentID, ParentAuthor: c.ParentAuthor, Body: c.Body, Author: c.Author, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt}
	if s.NodeID == "" {
		s.NodeID = c.ID
	}
	s.Revision = ConversationRevision(s)
	return s
}
func discussionSources(d graphDiscussion, comments []Comment) []ConversationSource {
	all := append([]Comment{{ID: d.ID, NodeID: d.ID, URL: d.URL, RootID: d.ID, Body: d.Body, Author: d.Author.actor(), CreatedAt: d.CreatedAt, EditedAt: d.LastEditedAt}}, comments...)
	out := make([]ConversationSource, 0, len(all))
	for _, c := range all {
		out = append(out, conversationSource(d, c))
	}
	return out
}
func wakeScope(c Config) string {
	raw, _ := json.Marshal(struct {
		Name                               string
		Owner, Self, Implementer, Reviewer Actor
		Repositories                       []string
		Wake                               *WakeConfig
		Team                               *TeamPolicy `json:",omitempty"`
	}{c.Name, c.Owner, c.Self, c.Implementer, c.Reviewer, c.DiscussionRepositories, c.Wake, c.Team})
	return BodyHash(string(raw))
}
func sourceTime(s ConversationSource) time.Time {
	if s.EditedAt.After(s.CreatedAt) {
		return s.EditedAt
	}
	return s.CreatedAt
}
func IngestDiscussionWakes(c Config, snapshot Snapshot, store WakeStore, now time.Time) error {
	c = Defaults(c)
	if !wakeEnabled(c) {
		return nil
	}
	if !snapshot.Complete {
		return errors.New("incomplete wake snapshot")
	}
	if err := validateWakeConfig(c); err != nil {
		return err
	}
	sources := append([]ConversationSource(nil), snapshot.WakeSources...)
	raw, err := json.Marshal(sources)
	if err != nil || len(raw) > MaxSnapshot || len(sources) > MaxConnection {
		return errors.New("wake snapshot exceeds size limit")
	}
	for i := range sources {
		s := &sources[i]
		if !goalInScope(c, Goal{URL: s.DiscussionURL}) || s.NodeID == "" || s.DiscussionID == "" || s.RootID == "" || !validWakeText(s.Body, MaxWakeRecord) {
			return errors.New("invalid conversation source")
		}
		s.Revision = ConversationRevision(*s)
	}
	allowed := sources[:0]
	for _, source := range sources {
		if discussionIntakeAllows(c, source.DiscussionURL) {
			allowed = append(allowed, source)
		}
	}
	sources = allowed
	sort.Slice(sources, func(i, j int) bool {
		a, b := sourceTime(sources[i]), sourceTime(sources[j])
		if a.Equal(b) {
			return sources[i].NodeID < sources[j].NodeID
		}
		return a.Before(b)
	})
	routing, err := store.Routing()
	if err != nil {
		return err
	}
	baseline, err := wakeRoutingBaseline(c, routing)
	if err != nil {
		return err
	}
	if baseline {
		routing.Seen = map[string]string{}
		routing.Chains = map[string]WakeChain{}
		routing.Suppressed = map[string]string{}
	}
	if routing.Suppressed == nil {
		routing.Suppressed = map[string]string{}
	}
	routing.IntakeDeferred = false
intakeLoop:
	for _, s := range sources {
		if routing.Seen[s.NodeID] == s.Revision {
			continue
		}
		targets := ConversationTargets(c, s)
		key := s.DiscussionID + ":" + s.RootID
		chain := routing.Chains[key]
		trusted := trustedWakeAuthor(c, s.Author)
		if trusted {
			if sameActor(s.Author, c.Owner) {
				chain = WakeChain{ID: BodyHash(s.NodeID + ":" + s.Revision), RootID: s.RootID, OwnerRevision: s.Revision}
			} else if len(targets) > 0 {
				if chain.ID == "" {
					chain = WakeChain{ID: BodyHash("peer:" + key), RootID: s.RootID}
				}
				chain.PeerHop++
			}
		}
		if !baseline {
			for _, target := range targets {
				if !sameActor(target, c.Self) {
					continue
				}
				if chain.PeerHop > c.Wake.PeerHopLimit {
					routing.Suppressed[s.NodeID] = "peer_hop_limit"
					continue
				}
				r := WakeRequest{Version: 1, ID: BodyHash(s.NodeID + ":" + s.Revision + ":" + c.Name), Target: c.Name, Kind: "discussion", Source: s, ChainID: chain.ID, PeerHop: chain.PeerHop, CreatedAt: sourceTime(s).UTC()}
				if _, err := store.Enqueue(r); err != nil {
					if errors.Is(err, ErrWakeCapacity) {
						routing.IntakeDeferred = true
						break intakeLoop
					}
					return err
				}
			}
		}
		if trusted {
			routing.Chains[key] = chain
		}
		routing.Seen[s.NodeID] = s.Revision
	}
	routing.Baselined = true
	routing.ScopeSHA = wakeScope(c)
	if c.Version == 2 {
		routing.ContinuitySHA = wakeContinuityScope(c)
	}
	return store.SaveRouting(routing)
}

var wakeHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
var wakeAddress = regexp.MustCompile(`@[A-Za-z0-9][A-Za-z0-9-]*(?:\[bot\])?`)
var wakeAcknowledgement = regexp.MustCompile(`(?i)^(?:acknowledged|ack|ok|okay|received|noted|queued|done|completed|working on it|on it|thanks|thank you)[.!]*$`)
var wakeReplyIntent = regexp.MustCompile(`(?i)^(?:i(?:'ll| will| am going to)|we(?:'ll| will| are going to)) (?:reply|respond|answer)(?: (?:to (?:you|this)|later|soon|shortly))*[.!]*$`)

// Reject empty machine metadata and common acknowledgement-only posts. Semantic
// answer quality still belongs to the agent; a marker alone is never evidence.
func substantiveWakeText(body string) bool {
	text := wakeHTMLComment.ReplaceAllString(body, "")
	text = wakeAddress.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	return text != "" && !wakeAcknowledgement.MatchString(text) && !wakeReplyIntent.MatchString(text)
}
