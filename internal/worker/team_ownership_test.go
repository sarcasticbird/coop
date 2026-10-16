package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamOwnershipV1EncodingOmitsBindings(t *testing.T) {
	s := newState()
	j := Job{Kind: "implement", TaskURL: "https://github.com/code/repo/issues/1"}
	j.ID = JobKey(j)
	s.Jobs[j.ID] = j
	s.Sessions["legacy"] = SessionBinding{Kind: "implement", Role: "implementer"}
	s.SnapshotCache.Tasks = []Task{{Claim: &Claim{Author: Actor{ID: 1, Kind: "Bot"}}}}
	s.SnapshotCache.PRs = []PR{{HeadSHA: "legacy"}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Binding", "PRScopeRevision", "ClaimConflict", "AssignmentSHA", "Contributors", "ContributorsComplete", "ReviewsComplete"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("v1 state gained v2 field %s", field)
		}
	}
}

func ownershipJob(kind string) Job {
	role := "implementer"
	switch kind {
	case "review":
		role = "engineering-review"
	case "plan":
		role = "planner"
	}
	j := Job{Kind: kind, TaskURL: "https://github.com/code/repo/issues/1", GoalURL: "https://github.com/orgs/planning/discussions/1", Phase: "waiting", Binding: &JobBinding{Version: 2, AssignmentSHA: BodyHash("assignment"), ExecutorID: 101, Role: role}}
	j.ID = V2JobKey(j, j.Binding.AssignmentSHA, j.Binding.ExecutorID)
	return j
}

func TestTeamOwnershipBindingKeys(t *testing.T) {
	j := ownershipJob("implement")
	key := BindingKey(Config{}, j)
	for _, kind := range []string{"implement", "fix", "deliver"} {
		next := j
		next.Kind = kind
		if BindingKey(Config{Role: "planner", Name: "renamed"}, next) != key {
			t.Fatal("author ownership changed between turns or mutable configuration")
		}
	}
	for _, change := range []func(*Job){
		func(j *Job) { j.Binding.AssignmentSHA = BodyHash("new assignment") },
		func(j *Job) { j.Binding.ExecutorID++ },
		func(j *Job) { j.Kind, j.Binding.Role = "review", "engineering-review" },
		func(j *Job) { j.Kind, j.Binding.Role = "plan", "planner" },
		func(j *Job) { j.TaskURL += "0" },
		func(j *Job) { j.GoalURL += "0" },
	} {
		next := ownershipJob("implement")
		change(&next)
		if BindingKey(Config{}, next) == key {
			t.Fatal("distinct owners shared binding key")
		}
	}
	legacy := j
	legacy.Binding = nil
	if BindingKey(Config{Role: "implementer"}, legacy) != BodyHash("implementer:task:"+legacy.TaskURL) {
		t.Fatal("v1 binding changed")
	}
}

func TestTeamOwnershipStateValidation(t *testing.T) {
	fixture := func() State {
		s := newState()
		s.Version = 2
		j := ownershipJob("implement")
		j.SessionID, j.Checkout, j.Branch = "ses_owned", "/workspace/owned/repo", "agent/owned"
		s.Jobs[j.ID] = j
		binding := *j.Binding
		s.Sessions[BindingKey(Config{}, j)] = SessionBinding{TaskURL: j.TaskURL, GoalURL: j.GoalURL, Role: binding.Role, Kind: j.Kind, Checkout: j.Checkout, Branch: j.Branch, SessionID: j.SessionID, Binding: &binding}
		return s
	}
	store := NewVersionedFileStore(storeFixture(t).Dir, 2)
	if err := store.Save(fixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"plan", "implement", "fix", "deliver", "review"} {
		s := newState()
		s.Version = 2
		j := ownershipJob(kind)
		s.Jobs[j.ID] = j
		if err := validateState(s); err != nil {
			t.Fatalf("valid %s rejected: %v", kind, err)
		}
	}
	for _, mutation := range []string{"version", "hash", "executor", "role", "job ID", "session binding", "session key", "session ID", "session checkout", "session branch", "session goal", "session role", "missing session", "duplicate session", "duplicate checkout", "unbound job", "v1 state"} {
		t.Run(mutation, func(t *testing.T) {
			s := fixture()
			j := ownershipJob("implement")
			key := BindingKey(Config{}, j)
			j = s.Jobs[j.ID]
			b := s.Sessions[key]
			switch mutation {
			case "version":
				j.Binding.Version = 1
			case "hash":
				j.Binding.AssignmentSHA = "bad"
			case "executor":
				j.Binding.ExecutorID = 0
			case "role":
				j.Binding.Role = "engineering-review"
			case "job ID":
				delete(s.Jobs, j.ID)
				legacy := j
				legacy.Binding = nil
				j.ID = JobKey(legacy)
			case "session binding":
				b.Binding.ExecutorID++
			case "session key":
				delete(s.Sessions, key)
				key = BodyHash("foreign")
			case "session ID":
				b.SessionID = "ses_foreign"
			case "session checkout":
				b.Checkout += "-foreign"
			case "session branch":
				b.Branch += "-foreign"
			case "session goal":
				b.GoalURL += "0"
			case "session role":
				b.Role = "engineering-review"
			case "missing session":
				b.Binding = nil
			case "duplicate session", "duplicate checkout":
				other := b
				binding := *b.Binding
				binding.ExecutorID++
				other.Binding = &binding
				if mutation == "duplicate session" {
					other.Checkout += "-other"
				} else {
					other.SessionID += "_other"
				}
				s.Sessions[BindingKey(Config{}, Job{TaskURL: other.TaskURL, GoalURL: other.GoalURL, Binding: other.Binding})] = other
			case "unbound job":
				j.Binding = nil
			case "v1 state":
				s.Version = 1
			}
			s.Jobs[j.ID], s.Sessions[key] = j, b
			if err := validateState(s); err == nil {
				t.Fatal("invalid ownership accepted")
			}
		})
	}
	// A migrated v1 record remains historical even in a v2 state.
	s := newState()
	s.Version = 2
	j := ownershipJob("implement")
	j.Binding = nil
	j.ID = JobKey(j)
	s.Jobs[j.ID] = j
	s.Sessions["historical"] = SessionBinding{SessionID: "ses_old", Checkout: "/workspace/old"}
	s.Sessions["historical-alias"] = s.Sessions["historical"]
	if err := validateState(s); err != nil {
		t.Fatal("historical migration rejected", err)
	}
}

func TestTeamOwnershipSessionHistory(t *testing.T) {
	for _, kinds := range [][]string{{"implement", "fix", "fix", "deliver"}, {"review", "review"}} {
		t.Run(kinds[0], func(t *testing.T) {
			store := NewVersionedFileStore(storeFixture(t).Dir, 2)
			s, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			for i, kind := range kinds {
				j := ownershipJob(kind)
				j.InputRevision = stringID(i)
				j.HeadSHA = BodyHash(j.InputRevision)
				j.ID = JobKey(j)
				j.Phase = "preparing"
				s.Jobs[j.ID] = j
				if err := store.Save(s); err != nil {
					t.Fatal("preparation without a session failed", err)
				}
				// Dispatch publishes the session binding after EnsureSession and
				// before the next save of the prepared job.
				j.SessionID, j.Checkout, j.Branch = "ses_shared", "/workspace/shared/repo", "agent/shared"
				s.Sessions[BindingKey(Config{}, j)] = SessionBinding{TaskURL: j.TaskURL, GoalURL: j.GoalURL, Role: j.Binding.Role, Kind: j.Kind, Checkout: j.Checkout, Branch: j.Branch, SessionID: j.SessionID, Binding: j.Binding}
				j.Phase = "completed"
				s.Jobs[j.ID] = j
				if err := store.Save(s); err != nil {
					t.Fatalf("latest %s session binding rejected historical jobs: %v", kind, err)
				}
				s, err = store.Load()
				if err != nil {
					t.Fatal(err)
				}
				if len(s.Jobs) != i+1 {
					t.Fatal("historical jobs lost")
				}
				for _, saved := range s.Jobs {
					if saved.SessionID != "ses_shared" {
						t.Fatal("historical session identity changed")
					}
				}
			}
		})
	}
}

func TestTeamOwnershipCheckoutIsolationAndRecovery(t *testing.T) {
	c, _, _ := policyFixture(t)
	c.Version, c.Role = 2, "implementer"
	origin := filepath.Join(filepath.Dir(c.StateDir), "origin")
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "add", "code.txt")
	localGit(t, origin, "commit", "-m", "base")
	base := localGit(t, origin, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("review head"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "commit", "-am", "review")
	head := localGit(t, origin, "rev-parse", "HEAD")
	localGit(t, origin, "update-ref", "refs/pull/2/head", head)
	localGit(t, origin, "reset", "--hard", base)
	checkouts := NewCheckouts(c, ExecRunner{})
	checkouts.origin = func(string) string { return origin }
	ctx := context.Background()
	author, err := checkouts.Prepare(ctx, ownershipJob("implement"))
	if err != nil {
		t.Fatal(err)
	}
	writeDirty := func(j Job) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(j.Checkout, "code.txt"), []byte("dirty work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeDirty(author)
	checkouts.config.Role = "planner"
	for _, kind := range []string{"fix", "deliver"} {
		next := author
		next.Kind, next.PRURL, next.HeadSHA = kind, "https://github.com/code/repo/pull/2", head
		if recovered, err := checkouts.Prepare(ctx, next); err != nil || recovered.Checkout != author.Checkout {
			t.Fatal("author checkout not recovered", err)
		}
	}
	checkouts.config.Role = "implementer"
	review := ownershipJob("review")
	review.PRURL, review.HeadSHA = "https://github.com/code/repo/pull/2", head
	reviewer, err := checkouts.Prepare(ctx, review)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.Checkout == author.Checkout || localGit(t, reviewer.Checkout, "rev-parse", "HEAD") != head {
		t.Fatal("review did not use isolated exact-head checkout")
	}
	borrowed := author
	borrowed.Checkout, borrowed.Branch = reviewer.Checkout, reviewer.Branch
	if _, err := checkouts.Prepare(ctx, borrowed); err == nil {
		t.Fatal("author adopted review tree")
	}
	if raw, _ := os.ReadFile(filepath.Join(author.Checkout, "code.txt")); string(raw) != "dirty work" {
		t.Fatal("review overwrote author work")
	}
	writeDirty(reviewer)
	if _, err := checkouts.Prepare(ctx, reviewer); err == nil {
		t.Fatal("dirty review checkout reset")
	}
	localGit(t, reviewer.Checkout, "restore", "code.txt")
	localGit(t, origin, "update-ref", "refs/pull/2/head", base)
	if _, err := checkouts.Prepare(ctx, reviewer); err == nil {
		t.Fatal("changed PR head accepted")
	}
	if localGit(t, reviewer.Checkout, "rev-parse", "HEAD") != head {
		t.Fatal("head mismatch changed checkout")
	}
	for _, mutation := range []string{"assignment", "executor", "review", "unbound"} {
		next := author
		binding := *author.Binding
		next.Binding = &binding
		switch mutation {
		case "assignment":
			next.Binding.AssignmentSHA = BodyHash("reassigned")
		case "executor":
			next.Binding.ExecutorID++
		case "review":
			next.Kind, next.Binding.Role = "review", "engineering-review"
		case "unbound":
			next.Binding = nil
		}
		if _, err := checkouts.Prepare(ctx, next); err == nil {
			t.Fatalf("%s adopted author checkout", mutation)
		}
	}
	localGit(t, author.Checkout, "remote", "set-url", "origin", origin+"-foreign")
	if _, err := checkouts.Prepare(ctx, author); err == nil {
		t.Fatal("wrong origin accepted")
	}
	localGit(t, author.Checkout, "remote", "set-url", "origin", origin)
	marker := filepath.Join(filepath.Dir(author.Checkout), "owner.json")
	raw, err := readPrivate(marker, MaxConfig)
	if err != nil {
		t.Fatal(err)
	}
	var owner checkoutOwner
	if err := json.Unmarshal(raw, &owner); err != nil {
		t.Fatal(err)
	}
	owner.Version, owner.AssignmentSHA, owner.ExecutorID, owner.Role = 0, "", 0, ""
	raw, _ = json.Marshal(owner)
	if err := atomicPrivate(marker, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := checkouts.Prepare(ctx, author); err == nil {
		t.Fatal("legacy ownership marker adopted by v2")
	}
}
