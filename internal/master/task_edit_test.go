package master

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type editableTasks struct {
	*testTasks
	issue     taskstore.Issue
	actors    []string
	updates   []c.TaskUpdateInput
	created   map[string]c.ProviderTask
	lost      bool
	onInspect func()
}

func (b *editableTasks) InspectIssue(context.Context, string) (taskstore.Issue, error) {
	if b.onInspect != nil {
		b.onInspect()
	}
	return b.issue, nil
}
func (b *editableTasks) CreateIssue(_ context.Context, p c.Principal, in c.TaskCreateInput) (c.ProviderTask, error) {
	b.actors = append(b.actors, p.ID)
	key := p.ID + in.RequestID
	if task, ok := b.created[key]; ok {
		return task, nil
	}
	out := c.ProviderTask{Reference: conversationIssue, IssueUID: conversationIssue, ProjectUID: b.Capabilities().ProjectUID, Title: in.Title, Body: in.Body, State: "open", Revision: 1}
	b.created[key] = out
	if b.lost {
		b.lost = false
		return c.ProviderTask{}, c.ErrUnavailable
	}
	return out, nil
}
func (b *editableTasks) ListIssues(context.Context, c.TaskListInput) (c.ProviderTaskPage, error) {
	return c.ProviderTaskPage{Tasks: []c.ProviderTask{{Reference: b.issue.UID, Title: b.issue.Title}}, Next: "next-page"}, nil
}
func (b *editableTasks) UpdateIssue(_ context.Context, p c.Principal, in c.TaskUpdateInput) (c.ProviderTask, error) {
	b.actors = append(b.actors, p.ID)
	b.updates = append(b.updates, in)
	if in.Operation != "comment" && in.ExpectedRevision != b.issue.Revision {
		return c.ProviderTask{}, c.ErrConflict
	}
	b.issue.Revision++
	return c.ProviderTask{Reference: b.issue.UID, IssueUID: b.issue.UID, ProjectUID: b.issue.ProjectUID, Revision: b.issue.Revision}, nil
}
func editableFixture(t *testing.T) (*Service, []c.Principal, *editableTasks, c.Thread) {
	t.Helper()
	ctx := t.Context()
	store, err := OpenStore(ctx, StoreConfig{Path: filepath.Join(t.TempDir(), "tasks.db"), MasterID: "task-edit"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"team"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	principals := []c.Principal{owner}
	members := []string{owner.ID}
	for _, name := range []string{"rook", "robin", "hawk", "wren"} {
		principals = append(principals, c.Principal{ID: name, Alias: name, Kind: "agent", Engagements: []string{"team"}, Capabilities: []string{"read", "send"}})
		members = append(members, name)
	}
	if err = store.Configure(ctx, principals, nil); err != nil {
		t.Fatal(err)
	}
	thread, err := store.CreateThread(ctx, owner, c.CreateThread{Title: "Team", Engagement: "team", Members: members, RequestID: "team-thread"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &editableTasks{testTasks: &testTasks{}, created: map[string]c.ProviderTask{}}
	backend.issue = taskstore.Issue{UID: conversationIssue, ProjectUID: backend.Capabilities().ProjectUID, Title: "Existing task", Status: "open", Revision: 3}
	store.conversations = ConversationConfig{TeamThreadID: thread.ID, ProjectUID: backend.Capabilities().ProjectUID}
	return &Service{Store: store, Tasks: backend}, principals, backend, thread
}
func TestEveryBirdCanMaintainKataWithoutPlannerOrGoal(t *testing.T) {
	svc, principals, backend, _ := editableFixture(t)
	for _, p := range principals[1:] {
		t.Run(p.ID, func(t *testing.T) {
			created, err := svc.TaskCreate(t.Context(), p, c.TaskCreateInput{Title: "Task for " + p.ID, Body: "Agreed scope", RequestID: "create-" + p.ID})
			if err != nil || created.Binding != nil || created.Goal != (c.GoalRef{}) {
				t.Fatal("ordinary task required engineering authority", created, err)
			}
			read, err := svc.TaskRead(t.Context(), p, c.TaskReadInput{Reference: backend.issue.UID})
			if err != nil || read.Title != backend.issue.Title || read.Binding != nil {
				t.Fatal(read, err)
			}
			page, err := svc.TaskList(t.Context(), p, c.TaskListInput{})
			if err != nil || len(page.Tasks) != 1 || page.Next != "next-page" {
				t.Fatal(page, err)
			}
			for _, op := range []string{"edit", "claim", "assign", "comment", "close", "reopen"} {
				in := c.TaskUpdateInput{Reference: backend.issue.UID, Operation: op, ExpectedRevision: backend.issue.Revision, RequestID: p.ID + "-" + op}
				switch op {
				case "edit":
					title := "Useful title"
					in.Title = &title
				case "assign":
					name := p.ID
					in.Owner = &name
				case "comment":
					in.ExpectedRevision, in.Message = 0, "An actual response"
				case "close":
					in.Reason, in.Message = "wontfix", "Disposable task is no longer needed"
				}
				if _, err = svc.TaskUpdate(t.Context(), p, in); err != nil {
					t.Fatalf("%s required a permission relay: %v", op, err)
				}
				if backend.actors[len(backend.actors)-1] != p.ID {
					t.Fatal("borrowed identity")
				}
			}
		})
	}
	var goals, artifacts int
	if err := svc.Store.db.QueryRow("SELECT count(*) FROM goals").Scan(&goals); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.db.QueryRow("SELECT count(*) FROM artifacts").Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if goals != 0 || artifacts != 0 || backend.binds != 0 || backend.closes != 0 {
		t.Fatal("task maintenance manufactured execution authority")
	}
}
func TestTaskEditsRejectNonmembersServicesAndReadOnlyActors(t *testing.T) {
	for _, fault := range []string{"outsider", "service", "read-only", "wrong-engagement", "revoked"} {
		t.Run(fault, func(t *testing.T) {
			svc, people, backend, thread := editableFixture(t)
			p := people[1]
			switch fault {
			case "outsider":
				p.ID, p.Alias = "outside", "outside"
			case "service":
				p.ID, p.Alias, p.Kind = "service", "service", "service"
			case "read-only":
				p.Capabilities = []string{"read"}
			case "wrong-engagement":
				p.Engagements = []string{"other"}
			case "revoked":
				if _, err := svc.Store.db.Exec("DELETE FROM members WHERE thread_id=? AND principal_id=?", thread.ID, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			if fault != "revoked" {
				if err := svc.Store.Configure(t.Context(), []c.Principal{p}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "service" {
				if _, err := svc.Store.db.Exec("INSERT INTO members(thread_id,principal_id) VALUES(?,?)", thread.ID, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.TaskCreate(t.Context(), p, c.TaskCreateInput{Title: "Not allowed", RequestID: "denied"}); !errors.Is(err, c.ErrForbidden) {
				t.Fatal(err)
			}
			if _, err := svc.TaskUpdate(t.Context(), p, c.TaskUpdateInput{Reference: backend.issue.UID, Operation: "comment", Message: "No", RequestID: "denied-update"}); !errors.Is(err, c.ErrForbidden) {
				t.Fatal(err)
			}
			if len(backend.actors) != 0 {
				t.Fatal("provider entered before authorization")
			}
		})
	}
}
func TestTaskEditRequestsPreserveUnknownInputAndSavedSuccess(t *testing.T) {
	svc, people, backend, _ := editableFixture(t)
	p := people[2]
	in := c.TaskCreateInput{Title: "One task", Body: "Original", RequestID: "lost-create"}
	backend.lost = true
	if _, err := svc.TaskCreate(t.Context(), p, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	changed := in
	changed.Body = "Changed"
	if _, err := svc.TaskCreate(t.Context(), p, changed); !errors.Is(err, c.ErrConflict) {
		t.Fatal("changed unknown input accepted", err)
	}
	out, err := svc.TaskCreate(t.Context(), p, in)
	if err != nil || len(backend.created) != 1 {
		t.Fatal("lost response duplicated task", err)
	}
	calls := len(backend.actors)
	again, err := svc.TaskCreate(t.Context(), p, in)
	if err != nil || again.Reference != out.Reference || len(backend.actors) != calls {
		t.Fatal("known success was repeated", err)
	}
	update := c.TaskUpdateInput{Reference: backend.issue.UID, Operation: "reopen", ExpectedRevision: backend.issue.Revision, RequestID: "same-update"}
	first, err := svc.TaskUpdate(t.Context(), p, update)
	if err != nil {
		t.Fatal(err)
	}
	again, err = svc.TaskUpdate(t.Context(), p, update)
	if err != nil || first.Revision != again.Revision || len(backend.updates) != 1 {
		t.Fatal("update replay changed result", err)
	}
	update.ExpectedRevision++
	if _, err = svc.TaskUpdate(t.Context(), p, update); !errors.Is(err, c.ErrConflict) {
		t.Fatal("unknown update could change revision", err)
	}
	// The ordinary create namespace must not be mistaken for a legacy engineering
	// creation intent when the existing conversation service starts again.
	if err = svc.Store.initializeTaskCreationCorrelation(t.Context(), backend.Capabilities().ProjectUID); err != nil {
		t.Fatal(err)
	}
}
func TestTaskReadRechecksAccessAndNativeProject(t *testing.T) {
	for _, fault := range []string{"project", "revocation"} {
		t.Run(fault, func(t *testing.T) {
			svc, people, backend, thread := editableFixture(t)
			p := people[1]
			if fault == "project" {
				backend.issue.ProjectUID = conversationIssue
			} else {
				backend.onInspect = func() {
					_, err := svc.Store.db.Exec("DELETE FROM members WHERE thread_id=? AND principal_id=?", thread.ID, p.ID)
					if err != nil {
						t.Error(err)
					}
				}
			}
			if out, err := svc.TaskRead(t.Context(), p, c.TaskReadInput{Reference: backend.issue.UID}); err == nil || out.Body != "" || out.Title != "" {
				t.Fatal("disclosed unauthorized issue", out, err)
			}
		})
	}
	svc, people, backend, _ := editableFixture(t)
	wrong, _ := c.KataTaskReference(conversationIssue, backend.issue.UID, "code/repo")
	_, err := svc.TaskUpdate(t.Context(), people[1], c.TaskUpdateInput{Reference: wrong, Operation: "comment", Message: "No", RequestID: "wrong-project"})
	if !errors.Is(err, c.ErrForbidden) {
		t.Fatal("wrong project accepted", err)
	}
}

func TestUnresolvedTaskCreateExpiresWithoutReplay(t *testing.T) {
	svc, people, backend, _ := editableFixture(t)
	clock := time.Now().UTC()
	svc.Store.now = func() time.Time { return clock }
	p := people[2]
	in := c.TaskCreateInput{Title: "One actual task", RequestID: "lost-before-expiry"}
	backend.lost = true
	if _, err := svc.TaskCreate(t.Context(), p, in); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal(err)
	}
	if len(backend.created) != 1 {
		t.Fatal("fixture did not commit the unknown effect")
	}
	calls := len(backend.actors)
	clock = clock.Add(7 * 24 * time.Hour)
	if _, err := svc.TaskCreate(t.Context(), p, in); !errors.Is(err, c.ErrConflict) || len(backend.actors) != calls {
		t.Fatal("expired unknown create was repeated", err)
	}
	known := c.TaskCreateInput{Title: "Known completed task", RequestID: "saved-success"}
	first, err := svc.TaskCreate(t.Context(), p, known)
	if err != nil {
		t.Fatal(err)
	}
	calls = len(backend.actors)
	clock = clock.Add(8 * 24 * time.Hour)
	again, err := svc.TaskCreate(t.Context(), p, known)
	if err != nil || first.Reference != again.Reference || len(backend.actors) != calls {
		t.Fatal("known result was expired or replayed", err)
	}
}
