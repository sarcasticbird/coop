package hostrouter

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestWorkUIEntryUsesPinnedMasterAndProject(t *testing.T) {
	req := WorkUIRequest{MasterID: "master-a", ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	encoded, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkUIRequest(encoded)
	if err != nil || decoded != req {
		t.Fatalf("round trip: %#v %v", decoded, err)
	}
	for _, bad := range []WorkUIRequest{
		{MasterID: "", ProjectUID: req.ProjectUID},
		{MasterID: req.MasterID, ProjectUID: "display name"},
		{MasterID: req.MasterID, ProjectUID: req.ProjectUID, IssueUID: "; touch /tmp/injected"},
	} {
		if _, err := bad.Encode(); err == nil {
			t.Fatalf("accepted %#v", bad)
		}
	}
	if _, err := DecodeWorkUIRequest(strings.Repeat("A", 9000)); err == nil {
		t.Fatal("accepted oversized request")
	}
	if _, err := DecodeWorkUIRequest("eyJtYXN0ZXJfaWQiOiJtYXN0ZXItYSIsIm1hc3Rlcl9pZCI6Im1hc3Rlci1iIn0"); err == nil {
		t.Fatal("accepted ambiguous request")
	}
}

func TestWorkUISSHTerminalReturns(t *testing.T) {
	_, ctx := terminalFixture(t, Agent)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	req := WorkUIRequest{MasterID: "master-a", ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	action, err := req.Action(registry, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	command, err := actionCommand(action)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOP_TERMINAL_COMMAND", command)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := (SSH{}).AttachCommand(ctx, action, TerminalSize{Rows: 24, Columns: 80})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	reader := bufio.NewReader(terminal)
	terminalLine(t, reader, "READY")
	if _, err := io.WriteString(terminal, "exit\n"); err != nil {
		t.Fatal(err)
	}
	terminalLine(t, reader, "BYE")
	if err := terminal.Wait(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Getwd(); after != cwd {
		t.Fatal("SSH work entry changed controller cwd")
	}
}

func TestWorkUIStaleHostRefusesBeforeSSH(t *testing.T) {
	// Use the normal installed registry so transport must revalidate this capture.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Change("add", "rack", "rack-old"); err != nil {
		t.Fatal(err)
	}
	req := WorkUIRequest{MasterID: "master-a", ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	action, err := req.Action(registry, "rack")
	if err != nil {
		t.Fatal(err)
	}
	if action.Target.Host.Address != "rack-old" || action.Directory != "" || len(action.Args) != 2 || action.Args[0] != "work-ui-exec" {
		t.Fatalf("bad action %#v", action)
	}
	command, err := actionCommand(action)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, "coop host-exec ") || strings.Contains(command, "master-a") {
		t.Fatalf("unsafe shell command %q", command)
	}
	if err := registry.Change("update", "rack", "rack-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := (SSH{}).Execute(context.Background(), action, strings.NewReader(""), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("changed host accepted: %v", err)
	}
}
