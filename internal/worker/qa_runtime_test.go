package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func qaRuntimeJob() Job {
	j := ownershipJob("qa")
	j.Binding.Role = "customer-qa"
	j.Binding.ExecutorID = 303
	j.PRURL = "https://github.com/code/repo/pull/2"
	j.ID = V2JobKey(j, j.Binding.AssignmentSHA, j.Binding.ExecutorID)
	return j
}

func TestQARuntimeOwnership(t *testing.T) {
	j := qaRuntimeJob()
	if err := validateOwnershipBinding(j.Binding, j.Kind); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"implement", "fix", "deliver", "review", "plan", "unknown"} {
		if validateOwnershipBinding(j.Binding, kind) == nil {
			t.Fatalf("QA binding accepted for %s", kind)
		}
		if BindingKey(Config{}, j) == BindingKey(Config{}, ownershipJob(kind)) {
			t.Fatalf("QA shares %s checkout", kind)
		}
	}
	j.SessionID, j.Checkout, j.Branch = "ses_qa", "/workspace/qa/repo", "agent/qa"
	s := newState()
	s.Version = 2
	s.Jobs[j.ID] = j
	s.Sessions[BindingKey(Config{}, j)] = SessionBinding{Binding: j.Binding, TaskURL: j.TaskURL, GoalURL: j.GoalURL, Kind: j.Kind, Role: j.Binding.Role, SessionID: j.SessionID, Checkout: j.Checkout, Branch: j.Branch}
	store := NewVersionedFileStore(storeFixture(t).Dir, 2)
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	s.Version = 1
	if validateState(s) == nil {
		t.Fatal("QA binding accepted in v1 ledger")
	}
}

func TestQARuntimeClaimParser(t *testing.T) {
	j := qaRuntimeJob()
	actor := Actor{ID: j.Binding.ExecutorID, Kind: "Bot", Login: "qa[bot]"}
	for _, role := range []string{"customer-qa", "engineering-review", "implementer", "planner", "qa"} {
		binding := *j.Binding
		binding.Role = role
		body := marker(j, "claim") + "\n" + teamMarker("claim", struct {
			Instance string     `json:"instance"`
			Binding  JobBinding `json:"binding"`
		}{BodyHash("instance"), binding})
		claim := claimFromComment(Comment{ID: "1", Author: actor, Body: body})
		want := role == "customer-qa" || role == "engineering-review" || role == "implementer"
		if (claim != nil) != want {
			t.Fatalf("role %s accepted=%v", role, claim != nil)
		}
		actor.ID++
		if claimFromComment(Comment{Author: actor, Body: body}) != nil {
			t.Fatal("foreign executor claim accepted")
		}
		actor.ID--
	}
}

func TestQARuntimeCheckout(t *testing.T) {
	for _, mutation := range []string{"none", "dirty", "head", "origin", "missing-push", "changed-push", "extra-push", "rewritten-push"} {
		t.Run(mutation, func(t *testing.T) {
			c, _, _ := policyFixture(t)
			c.Version = 2
			origin := filepath.Join(t.TempDir(), "origin")
			if err := os.Mkdir(origin, 0700); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "init", "-b", "main")
			localGit(t, origin, "commit", "--allow-empty", "-m", "fixture")
			head := localGit(t, origin, "rev-parse", "HEAD")
			localGit(t, origin, "update-ref", "refs/pull/2/head", head)
			j := qaRuntimeJob()
			j.HeadSHA = head
			checkouts := NewCheckouts(c, ExecRunner{})
			checkouts.origin = func(string) string { return origin }
			owned, err := checkouts.Prepare(context.Background(), j)
			if err != nil {
				t.Fatal(err)
			}
			if localGit(t, owned.Checkout, "rev-parse", "HEAD") != head || localGit(t, owned.Checkout, "remote", "get-url", "origin") != origin {
				t.Fatal("QA checkout changed native head or read origin")
			}
			cmd := exec.Command("git", "-C", owned.Checkout, "push", "origin", "HEAD:refs/heads/qa-must-not-push")
			if err := cmd.Run(); err == nil {
				t.Fatal("QA feature push succeeded")
			}
			switch mutation {
			case "dirty":
				if err := os.WriteFile(filepath.Join(owned.Checkout, "dirty"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "head":
				localGit(t, origin, "commit", "--allow-empty", "-m", "new head")
				localGit(t, origin, "update-ref", "refs/pull/2/head", "HEAD")
			case "origin":
				localGit(t, owned.Checkout, "config", "remote.origin.url", origin+"-foreign")
			case "missing-push":
				localGit(t, owned.Checkout, "config", "--unset-all", "remote.origin.pushurl")
			case "changed-push":
				localGit(t, owned.Checkout, "config", "remote.origin.pushurl", origin)
			case "extra-push":
				localGit(t, owned.Checkout, "config", "--add", "remote.origin.pushurl", origin)
			case "rewritten-push":
				localGit(t, owned.Checkout, "config", "url."+origin+".insteadOf", "/dev/null/coop-qa-push-disabled")
			}
			_, err = checkouts.Prepare(context.Background(), owned)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("reuse error=%v", err)
			}
			if localGit(t, owned.Checkout, "rev-parse", "HEAD") != head {
				t.Fatal("rejected reuse changed HEAD")
			}
			if mutation == "dirty" {
				if raw, err := os.ReadFile(filepath.Join(owned.Checkout, "dirty")); err != nil || string(raw) != "preserve" {
					t.Fatal("dirty QA work erased")
				}
			}
		})
	}
}

func qaRuntimePromptFixture(t *testing.T, reverse bool) (Config, Snapshot, Job, time.Time) {
	t.Helper()
	c, s, now := engineeringFixture(t, reverse)
	task, goal, pr := &s.Tasks[0], &s.Goals[0], &s.PRs[0]
	task.QA = QAPolicy{Required: true, Reviewer: "qa", AcceptanceSHA: BodyHash("acceptance"), Environment: "staging", EnvironmentRevision: "runtime-v2"}
	goal.Body = teamMarker("goal", TeamGoalMetadata{Repositories: goal.Repositories, BaseBranches: map[string]string{task.Repo: task.Assignment.BaseBranch}, QA: task.QA})
	goal.BodySHA = BodyHash(goal.Body)
	task.Assignment.QA, task.Assignment.GoalBodySHA = "qa", goal.BodySHA
	sha, err := ValidateTeamScope(c, *task, *goal, now)
	if err != nil {
		t.Fatal(err)
	}
	pr.AssignmentSHA = sha
	pr.QAArtifact = &QAArtifact{Kind: "source", HeadSHA: pr.HeadSHA, SHA256: BodyHash("native archive"), URL: "https://api.github.com/repos/code/repo/tarball/" + pr.HeadSHA, Revision: "native-archive-v1"}
	c.Name, c.Self = c.Team.Members[2].Name, c.Team.Members[2].Actor
	j, err := bindTeamJob(c, Job{Kind: "qa", TaskURL: task.URL, GoalURL: goal.URL, PRURL: pr.URL, HeadSHA: pr.HeadSHA, InputRevision: BodyHash("artifact-bound input"), QAArtifact: pr.QAArtifact}, s, now)
	if err != nil {
		t.Fatal(err)
	}
	return c, s, j, now
}

func TestQARuntimeBuildPrompt(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		c, s, j, now := qaRuntimePromptFixture(t, reverse)
		prompt, err := buildPrompt(c, j, s, "runtime policy", now)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{commonPrompt[:strings.Index(commonPrompt, "Self is")], `"Role":"customer-qa"`, `"Self":{"id":13,`, `"QA":{"id":13,`, `"Author":`, `"Peer":`, `"QAArtifact":`, j.QAArtifact.SHA256, j.QAArtifact.Revision, s.Tasks[0].QA.AcceptanceSHA, s.Tasks[0].QA.EnvironmentRevision, customerQAPrompt} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("QA build prompt missing %q", want)
			}
		}
		if strings.Contains(prompt, "post it once in the existing task or goal thread") {
			t.Fatal("QA prompt leaks generic public blocker instruction")
		}
		for _, mutation := range []string{"self", "missing artifact", "artifact digest", "artifact head", "artifact revision"} {
			t.Run(mutation, func(t *testing.T) {
				c2, j2 := c, j
				artifact := *j.QAArtifact
				j2.QAArtifact = &artifact
				switch mutation {
				case "self":
					c2.Self = s.PRs[0].Author
				case "missing artifact":
					j2.QAArtifact = nil
				case "artifact digest":
					artifact.SHA256 = BodyHash("different artifact")
				case "artifact head":
					artifact.HeadSHA = BodyHash("different head")
				case "artifact revision":
					artifact.Revision = "different revision"
				}
				if _, err := buildPrompt(c2, j2, s, "", now); err == nil {
					t.Fatal("changed QA input accepted")
				}
			})
		}
	}
}

func TestQARuntimePromptContract(t *testing.T) {
	for _, want := range []string{"customer-qa", "contents write", "Do not edit code, commit, push, merge", "Missing environment, tools, test data or permissions means blocked", "Task.QA", "Job.QAArtifact", "native immutable digest", "coop-worker qa-source-digest --archive PATH", "Job.InputRevision", "commit_id Job.HeadSHA", "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "entire review body", "authorized goal repository"} {
		if !strings.Contains(customerQAPrompt, want) {
			t.Fatalf("QA prompt missing %q", want)
		}
	}
	start := strings.Index(customerQAPrompt, "<!-- coop-qa:v1 ")
	end := strings.Index(customerQAPrompt[start:], " -->") + start + len(" -->")
	body := customerQAPrompt[start:end]
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(body, "<!-- coop-qa:v1 "), " -->")), &fields); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(QAResult{})
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(expected) {
		t.Fatal("QA prompt result schema differs from parser")
	}
	for field := range expected {
		if _, ok := fields[field]; !ok {
			t.Fatalf("QA prompt missing result field %s", field)
		}
	}
}
