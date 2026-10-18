package worker

import (
	"testing"
)

func TestLegacyMetadataUnchanged(t *testing.T) {
	cfg, task, goal, comment, now := teamScopeFixture(t)
	if err := ApplyTaskMetadata(cfg, &task, []Comment{comment}); err != nil {
		t.Fatal(err)
	}
	sha, err := ValidateTeamScope(cfg, task, goal, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseTeamPRMetadata(teamMarker("pr", TeamPRMetadata{Task: task.URL, Goal: goal.URL, AssignmentSHA: sha}), task.Repo, task.URL, goal.URL, sha); err != nil {
		t.Fatal("v2 round trip changed", err)
	}
	legacy := configFixture(t)
	old := Task{URL: task.URL, Repo: task.Repo, Title: task.Title, Body: task.Body, BodySHA: task.BodySHA}
	body := encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: old.BodySHA})
	if err = ApplyTaskMetadata(legacy, &old, []Comment{{ID: "legacy", Author: legacy.Owner, Body: body, CreatedAt: now}}); err != nil || old.GoalURL != goal.URL || old.MetadataAuthor.ID != legacy.Owner.ID {
		t.Fatal("v1 owner authority changed", err)
	}
	native, _ := nativeBindingFixture()
	native.TaskURL = task.URL
	native.TaskScopeSHA = task.BodySHA
	if _, err = ParseTeamPRMetadata(NativePRBody(NativePRMetadata{Version: 3, Goal: native.Goal, TaskURL: task.URL, AssignmentSHA: NativeAssignmentSHA(native)}), task.Repo, task.URL, goal.URL, sha); err == nil {
		t.Fatal("native metadata accepted by legacy parser")
	}
}

func TestNativeMetadataRoundTrips(t *testing.T) {
	b, _ := nativeBindingFixture()
	got, err := ParseNativeTaskMetadata(NativeTaskMetadata(b))
	if err != nil || NativeAssignmentSHA(got) != NativeAssignmentSHA(b) {
		t.Fatal("native task round trip changed binding", err)
	}
	pr := NativePRMetadata{Version: 3, TaskURL: b.TaskURL, Goal: b.Goal, AssignmentSHA: NativeAssignmentSHA(b)}
	parsed, err := ParseNativePRMetadata(NativePRBody(pr), "code/repo", b.TaskURL, b.Goal, NativeAssignmentSHA(b))
	if err != nil || parsed.Goal != pr.Goal || parsed.AssignmentSHA != pr.AssignmentSHA {
		t.Fatal("native PR round trip changed binding", err)
	}
}
