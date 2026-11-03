package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type portableRuntime struct {
	rows    map[string]runtime.NativeContainer
	volumes map[string]string
	writes  []string
	fail    string
	machine bool
	ready   string
}

func (r *portableRuntime) command(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
	writeJSON := func(value any) error { return json.NewEncoder(out).Encode(value) }
	switch args[0] {
	case "exec":
		ready := r.ready
		if ready == "" {
			ready = "running"
		}
		_, err := io.WriteString(out, ready+"\n")
		return err
	case "machine":
		if r.machine {
			return writeJSON([]map[string]string{{"id": "coop-agent-robin"}})
		}
		return writeJSON([]any{})
	case "list":
		rows := []runtime.NativeContainer{}
		for _, row := range r.rows {
			rows = append(rows, row)
		}
		return writeJSON(rows)
	case "volume":
		if args[1] == "ls" {
			rows := []map[string]string{}
			for name := range r.volumes {
				rows = append(rows, map[string]string{"id": name})
			}
			return writeJSON(rows)
		}
		r.writes = append(r.writes, strings.Join(args, " "))
		if args[1] == "create" {
			r.volumes[args[2]] = "initial data"
		} else if args[1] == "rm" {
			delete(r.volumes, args[2])
		} else {
			return fmt.Errorf("unexpected volume command %v", args)
		}
		return nil
	case "create":
		r.writes = append(r.writes, "create")
		if r.fail == "create-before" {
			r.fail = ""
			return errors.New("lost create request")
		}
		var row runtime.NativeContainer
		row.Configuration.Labels = map[string]string{}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--name":
				i++
				row.Configuration.ID = args[i]
			case "--cpus":
				i++
				row.Configuration.Resources.CPUs, _ = strconv.Atoi(args[i])
			case "--memory":
				i++
				row.Configuration.Resources.MemoryInBytes = memoryBytes(args[i])
			case "--cap-add", "--masked-path", "--read-only-path":
				i++
				if args[i] != "ALL" && args[i] != "NONE" {
					return errors.New("unexpected guest administration settings")
				}
			case "-l":
				i++
				k, v, _ := strings.Cut(args[i], "=")
				row.Configuration.Labels[k] = v
			case "-v":
				i++
				name, target, _ := strings.Cut(args[i], ":")
				var mount struct {
					Destination string `json:"destination"`
					Type        struct {
						Volume *struct {
							Name string `json:"name"`
						} `json:"volume"`
					} `json:"type"`
				}
				mount.Destination = target
				mount.Type.Volume = &struct {
					Name string `json:"name"`
				}{Name: name}
				row.Configuration.Mounts = append(row.Configuration.Mounts, mount)
			case "--ssh", "--mount":
				return errors.New("portable agent unexpectedly shared host state")
			default:
				row.Configuration.Image.Reference = args[i]
				if i+1 >= len(args) || args[i+1] != "/opt/coop-machine/boot" {
					return errors.New("wrong portable boot entrypoint")
				}
				i = len(args)
			}
		}
		row.Status.State = "stopped"
		r.rows[row.Configuration.ID] = row
		if r.fail == "create-after" {
			r.fail = ""
			return errors.New("lost create acknowledgement")
		}
		return nil
	case "rm", "start", "stop":
		r.writes = append(r.writes, strings.Join(args, " "))
		if r.fail == args[0] {
			r.fail = ""
			return errors.New("synthetic runtime failure")
		}
		row, ok := r.rows[args[1]]
		if !ok {
			return errors.New("absent container")
		}
		if args[0] == "rm" {
			delete(r.rows, args[1])
			return nil
		}
		row.Status.State = "stopped"
		if args[0] == "start" {
			row.Status.State = "running"
			row.Status.StartedDate = "2026-11-03T00:00:00Z"
		}
		r.rows[args[1]] = row
		return nil
	default:
		return fmt.Errorf("unexpected command %v", args)
	}
}

func portableFixture(t *testing.T) (Spec, ContainerBackend, *portableRuntime) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Spec{Mode: Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Home: "/root", Workdir: "/workspace", Image: "coop-machine:old", Config: config.Config{Resources: config.Resources{CPUs: 2, Memory: "2G"}}, Profile: config.Profile{Runtime: "container", Command: []string{"opencode"}}}
	s.Fingerprint = Fingerprint(s)
	r := &portableRuntime{rows: map[string]runtime.NativeContainer{}, volumes: map[string]string{}}
	b := ContainerBackend{Runtime: runtime.AppleMachine{Command: r.command}, Store: FileStore{Root: root}}
	return s, b, r
}

func TestPortableAgentReplacementPreservesAllDataVolumes(t *testing.T) {
	s, b, r := portableFixture(t)
	c := Controller{Backend: b, Store: b.Store}
	if err := c.Ensure(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	old := r.rows[s.RuntimeName].Configuration.Labels[incarnationLabel]
	for name := range r.volumes {
		r.volumes[name] = "durable memory, sessions, work and action receipts"
	}
	before := map[string]string{}
	for name, data := range r.volumes {
		before[name] = data
	}
	next := s
	next.Image = "coop-machine:new"
	next.Fingerprint = Fingerprint(next)
	if err := c.Ensure(t.Context(), next); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("ordinary up replaced root", err)
	}
	if err := c.Apply(t.Context(), next); err == nil {
		t.Fatal("running agent replaced")
	}
	if err := c.Down(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	row := r.rows[s.RuntimeName]
	if row.Status.State != "stopped" || row.Configuration.Image.Reference != next.Image || row.Configuration.Labels[incarnationLabel] == old {
		t.Fatal("image/incarnation did not change while cold", row)
	}
	if !reflect.DeepEqual(before, r.volumes) {
		t.Fatal("replacement modified durable data")
	}
	if err := c.Ensure(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if got := r.rows[s.RuntimeName].Status.State; got != "running" {
		t.Fatal(got)
	}
	for _, write := range r.writes {
		if strings.HasPrefix(write, "volume rm") {
			t.Fatal("replacement deleted a data volume")
		}
	}
}

func TestPortableAgentReplacementResumesWithoutRecreatingAcknowledgedRoot(t *testing.T) {
	for _, fault := range []string{"rm", "create-before", "create-after"} {
		t.Run(fault, func(t *testing.T) {
			s, b, r := portableFixture(t)
			c := Controller{Backend: b, Store: b.Store}
			if err := b.Create(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			next := s
			next.Image = "coop-machine:new"
			next.Fingerprint = Fingerprint(next)
			r.fail = fault
			if err := c.Apply(t.Context(), next); err == nil {
				t.Fatal("failure not exercised")
			}
			before := len(r.writes)
			if err := c.Ensure(t.Context(), next); err == nil || len(r.writes) != before {
				t.Fatal("ordinary startup crossed pending replacement")
			}
			changed := next
			changed.Image = "unrelated"
			changed.Fingerprint = Fingerprint(changed)
			if err := c.Apply(t.Context(), changed); err == nil || len(r.writes) != before {
				t.Fatal("replacement intent changed")
			}
			if err := c.Apply(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			if fault == "create-after" && len(r.writes) != before {
				t.Fatal("acknowledged create replayed")
			}
			if _, err := b.Store.LoadRecovery(Agent, s.Identity); err == nil {
				t.Fatal("completed journal retained")
			}
		})
	}
}

func TestPortableAgentRefusesUnsafeReplacementBeforeMutation(t *testing.T) {
	for _, fault := range []string{"guard", "late-guard", "missing-volume", "foreign-reference", "foreign-mount", "image-drift", "resource-drift", "running"} {
		t.Run(fault, func(t *testing.T) {
			s, b, r := portableFixture(t)
			if err := b.Create(t.Context(), s); err != nil {
				t.Fatal(err)
			}
			next := s
			next.Image = "coop-machine:new"
			next.Fingerprint = Fingerprint(next)
			row := r.rows[s.RuntimeName]
			switch fault {
			case "guard":
				b.Guard = func(context.Context, *Record, Spec) error { return errors.New("revoked placement") }
			case "late-guard":
				calls := 0
				b.Guard = func(context.Context, *Record, Spec) error {
					calls++
					if calls >= 3 {
						return errors.New("revoked before root removal")
					}
					return nil
				}
			case "missing-volume":
				delete(r.volumes, AgentVolumes(s.RuntimeName)[0].Name)
			case "foreign-reference":
				copy := row
				copy.Configuration.ID = "foreign"
				r.rows["foreign"] = copy
			case "foreign-mount":
				row.Configuration.Mounts[0].Type.Volume.Name = "foreign"
			case "image-drift":
				row.Configuration.Image.Reference = "foreign"
			case "resource-drift":
				row.Configuration.Resources.CPUs++
			case "running":
				row.Status.State = "running"
			}
			r.rows[s.RuntimeName] = row
			before := len(r.writes)
			if err := b.Apply(t.Context(), next); err == nil || len(r.writes) != before {
				t.Fatal("unsafe replacement crossed mutation boundary", err, r.writes[before:])
			}
		})
	}
}

func TestPortableAgentCreationDoesNotAdoptOldMachineOrOrphanedVolumes(t *testing.T) {
	for _, fault := range []string{"machine", "volume", "record"} {
		t.Run(fault, func(t *testing.T) {
			s, b, r := portableFixture(t)
			switch fault {
			case "machine":
				r.machine = true
			case "volume":
				r.volumes[AgentVolumes(s.RuntimeName)[0].Name] = "user work"
			case "record":
				if err := b.Store.Save(s.Record()); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.Create(t.Context(), s); err == nil || len(r.writes) != 0 {
				t.Fatal("foreign state adopted", err, r.writes)
			}
		})
	}
}

func TestPortableAgentInterruptedCreationRequiresExplicitApply(t *testing.T) {
	s, b, r := portableFixture(t)
	r.fail = "create-after"
	if err := b.Create(t.Context(), s); err == nil {
		t.Fatal("lost acknowledgement not exercised")
	}
	c := Controller{Backend: b, Store: b.Store}
	before := len(r.writes)
	if err := c.Ensure(t.Context(), s); err == nil || len(r.writes) != before {
		t.Fatal("unpublished creation started")
	}
	if err := c.Apply(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if len(r.writes) != before {
		t.Fatal("creation replayed")
	}
}

func TestPortableRestoreTargetRemainsUnownedUntilColdPublication(t *testing.T) {
	s, b, r := portableFixture(t)
	if err := b.CreateRestoreTarget(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if r.rows[s.RuntimeName].Status.State != "stopped" {
		t.Fatal("restore target running")
	}
	if _, err := b.Store.Load(Agent, s.Identity); err == nil {
		t.Fatal("restore published ownership early")
	}
	c := Controller{Backend: b, Store: b.Store}
	before := len(r.writes)
	if err := c.Ensure(t.Context(), s); err == nil || len(r.writes) != before {
		t.Fatal("unfinished restore started")
	}
}

type interruptedPublicationStore struct {
	RecoveryStore
	save, clear bool
}

func (s *interruptedPublicationStore) Save(r Record) error {
	if s.save {
		s.save = false
		return errors.New("publication interrupted")
	}
	return s.RecoveryStore.Save(r)
}
func (s *interruptedPublicationStore) ClearRecovery(mode Mode, name string) error {
	if s.clear {
		s.clear = false
		return errors.New("journal cleanup interrupted")
	}
	return s.RecoveryStore.ClearRecovery(mode, name)
}

func TestPortableReplacementPublicationIsRestartSafe(t *testing.T) {
	for _, fault := range []string{"save", "clear"} {
		t.Run(fault, func(t *testing.T) {
			spec, b, r := portableFixture(t)
			if err := b.Create(t.Context(), spec); err != nil {
				t.Fatal(err)
			}
			store := &interruptedPublicationStore{RecoveryStore: b.Store, save: fault == "save", clear: fault == "clear"}
			b.Store = store
			next := spec
			next.Image = "new-image"
			next.Fingerprint = Fingerprint(next)
			c := Controller{Backend: b, Store: store}
			if err := c.Apply(t.Context(), next); err == nil {
				t.Fatal("publication interruption not exercised")
			}
			before := len(r.writes)
			if err := c.Ensure(t.Context(), next); err == nil || len(r.writes) != before {
				t.Fatal("pending publication started execution")
			}
			if err := c.Apply(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			if len(r.writes) != before {
				t.Fatal("publication retry recreated runtime or volumes")
			}
		})
	}
}

func TestPortableAgentDoesNotDuplicateAnotherProfilesExecution(t *testing.T) {
	spec, b, r := portableFixture(t)
	spec.Profile.Execution = &config.AgentExecution{ExecutionBinding: c.ExecutionBinding{MasterID: "master", Placement: c.Placement{AgentID: "robin", HostID: "local", InstanceID: strings.Repeat("a", 64), Generation: 1}}}
	spec.Fingerprint = Fingerprint(spec)
	other := spec.Record()
	other.Identity = "robin-copy"
	other.RuntimeName = "coop-agent-robin-copy"
	if err := b.Store.Save(other); err != nil {
		t.Fatal(err)
	}
	b.Guard = func(context.Context, *Record, Spec) error { return nil }
	if err := b.Create(t.Context(), spec); err == nil || len(r.writes) != 0 {
		t.Fatal("execution duplicated across profiles", err, r.writes)
	}
}

func TestPortableAgentDoesNotAdmitServicesBeforeReadySystemd(t *testing.T) {
	spec, b, r := portableFixture(t)
	if err := b.Create(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	r.ready = "starting"
	c := Controller{Backend: b, Store: b.Store}
	if err := c.Ensure(t.Context(), spec); err == nil {
		t.Fatal("unready boot admitted")
	}
	writes := len(r.writes)
	if err := c.Ensure(t.Context(), spec); err == nil || len(r.writes) != writes {
		t.Fatal("running process bypassed readiness or repeated start", err)
	}
	r.ready = "running"
	if err := c.Ensure(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	r.ready = "degraded"
	if err := c.Ensure(t.Context(), spec); err != nil {
		t.Fatal("a ready but degraded manager blocked administrative repair", err)
	}
}
