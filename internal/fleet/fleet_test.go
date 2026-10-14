package fleet

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func TestLegacyInteractiveFleetDoesNotRequireMachineSubcommands(t *testing.T) {
	for _, version := range []string{"container CLI version 1.4.9", "container CLI version 1.5.0", "garbage"} {
		t.Run(version, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			base := runtime.NewMock()
			base.Infos = []runtime.ContainerInfo{{Name: "coop-project", State: "stopped"}}
			f := Runtime{Runtime: base, Store: workspace.FileStore{Root: root}, Machine: runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
				if strings.Join(args, " ") == "--version" {
					_, err := io.WriteString(out, version)
					return err
				}
				return errors.New("machine listing failed")
			}}}
			rows, err := f.Containers()
			if strings.Contains(version, "1.4.9") {
				if err != nil || len(rows) != 1 {
					t.Fatalf("legacy interactive fleet unavailable: %v", err)
				}
			} else if err == nil {
				t.Fatal("unexpected runtime error hidden")
			}
		})
	}
}

func TestMergeSuppressesMachineChildrenAndKeepsModeIdentity(t *testing.T) {
	containers := []runtime.ContainerInfo{{Name: "coop-app-123", State: "stopped"}, {Name: "coop-agent-robin-child", State: "running"}}
	machines := []Machine{{Name: "coop-agent-robin", State: "running", ChildID: "coop-agent-robin-child", CPUs: 2, Memory: 4 << 30}}
	records := []workspace.Record{{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}}
	got, err := Merge(containers, machines, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Profile != "robin" || got[1].Mode != "agent" {
		t.Fatalf("fleet=%+v", got)
	}
}
