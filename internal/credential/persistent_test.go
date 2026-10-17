package credential

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type persistentExecutor struct {
	calls     [][]string
	fail      bool
	ambiguous bool
	published string
	deleted   bool
}

func (e *persistentExecutor) ExecContext(_ context.Context, _ string, args []string, input io.Reader) error {
	e.calls = append(e.calls, append([]string(nil), args...))
	if input != nil {
		_, _ = io.Copy(io.Discard, input)
	}
	if e.fail {
		return errors.New("transport unavailable")
	}
	if e.ambiguous && strings.Contains(strings.Join(args, " "), "current-credentials") {
		e.published = args[len(args)-1]
		return errors.New("publication committed but response lost")
	}
	if len(args) > 0 && args[0] == "rm" {
		e.deleted = true
	}
	return nil
}

func TestUncertainPublicationRetainsPotentiallyActiveGeneration(t *testing.T) {
	e := &persistentExecutor{ambiguous: true}
	lease, err := (PersistentManager{Executor: e}).Stage(context.Background(), "robin", nil)
	if err == nil {
		t.Fatal("transport error lost")
	}
	if e.published != lease.Dir {
		t.Fatal("fixture did not commit publication")
	}
	if e.deleted || len(e.calls) != 2 {
		t.Fatal("possibly active generation deleted after ambiguous publication")
	}
}
func TestPersistentCredentialRefreshRetainsPreviousGeneration(t *testing.T) {
	e := &persistentExecutor{}
	m := PersistentManager{Executor: e}
	one, err := m.Stage(context.Background(), "robin", nil)
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.Stage(context.Background(), "robin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if one.Dir == two.Dir {
		t.Fatal("refresh reused credential generation")
	}
	for _, args := range e.calls {
		if len(args) > 0 && args[0] == "rm" {
			t.Fatal("refresh deleted a potentially active generation")
		}
	}
	if len(e.calls) != 4 {
		t.Fatalf("stage/publication count=%d", len(e.calls))
	}
	if !strings.Contains(strings.Join(e.calls[1], " "), one.Dir) {
		t.Fatal("first generation never published")
	}
}
func TestPersistentCredentialStageFailureNeverPublishes(t *testing.T) {
	e := &persistentExecutor{fail: true}
	m := PersistentManager{Executor: e}
	if _, err := m.Stage(context.Background(), "robin", nil); err == nil {
		t.Fatal("transport failure ignored")
	}
	for _, args := range e.calls {
		if strings.Contains(strings.Join(args, " "), "current-credentials") {
			t.Fatal("failed generation published")
		}
	}
}
