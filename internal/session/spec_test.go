package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type hookOnlyVolumeRuntime struct {
	runtime.Runtime
	t *testing.T
}

func (r hookOnlyVolumeRuntime) EnsureVolume(name string) error {
	r.t.Fatalf("RT.EnsureVolume(%q) called instead of preparation hook", name)
	return nil
}

func TestRunSpecWithVolumesUsesPreparationHook(t *testing.T) {
	for _, fail := range []string{"", "agent", "project"} {
		t.Run("failure="+fail, func(t *testing.T) {
			m := runtime.NewMock()
			s := testSession(t, m)
			s.RT = hookOnlyVolumeRuntime{Runtime: m, t: t}
			s.Project = t.TempDir()
			if err := os.Mkdir(filepath.Join(s.Project, "web"), 0700); err != nil {
				t.Fatal(err)
			}
			s.Cfg.Agents = map[string]config.Agent{"opencode": {State: "~/.local/share/opencode"}}
			s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}
			agent := s.Name + "--opencode"
			project := projectVolumeName(s.Name, "web/node_modules")
			injected := errors.New("injected checkpoint failure")
			var calls []string
			spec, err := s.RunSpecWithVolumes(context.Background(), func(name string) error {
				calls = append(calls, name)
				if fail == "agent" && name == agent || fail == "project" && name == project {
					return injected
				}
				return nil
			})
			wantCalls := []string{agent, project}
			if fail == "agent" {
				wantCalls = []string{agent}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("preparation hook calls = %q; want %q", calls, wantCalls)
			}
			if fail != "" {
				if !errors.Is(err, injected) || len(spec.Volumes) != 0 {
					t.Fatalf("failed preparation returned spec=%#v, error=%v", spec, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []runtime.Volume{
				{Name: agent, Target: filepath.Join(s.GuestHome, ".local/share/opencode")},
				{Name: project, Target: filepath.Join(s.Project, "web/node_modules")},
			}
			sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
			if !reflect.DeepEqual(spec.Volumes, want) {
				t.Fatalf("volumes = %#v; want %#v", spec.Volumes, want)
			}
		})
	}
}
