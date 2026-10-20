package hostrouter

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/sarcasticbird/coop/internal/coordination"
)

type TargetKind string

const (
	Agent       TargetKind = "agent"
	Interactive TargetKind = "interactive"
)

// Target is comparable; names alone are never routing identities. ID is the
// profile for agents and the runtime name for interactive/project Coops.
type Target struct {
	Host Host       `json:"host"`
	Kind TargetKind `json:"kind"`
	ID   string     `json:"id"`
}

type InventoryEntry struct {
	Diagnostics *coordination.DiagnosticBinding `json:"diagnostics,omitempty"`
	Target      Target                          `json:"target"`
	Name        string                          `json:"name"`
	State       string                          `json:"state"`
	Project     string                          `json:"project,omitempty"` // a path on Target.Host only
	Owned       bool                            `json:"owned"`             // display information, never mutation authorization
}

type HostInventory struct {
	Host    Host             `json:"host"`
	Entries []InventoryEntry `json:"entries"`
	Error   string           `json:"error,omitempty"` // bounded safe code, not remote stderr
}

type Snapshot struct {
	Version       int             `json:"version"`
	Hosts         []HostInventory `json:"hosts"`
	RegistryError string          `json:"registry_error,omitempty"`
}

type InventoryClient interface {
	Inventory(context.Context, Host) ([]InventoryEntry, error)
}

// Action and terminal boundaries are shared with CLI/TUI consumers. Concrete
// action routing and PTY transport are deliberately separate from inventory.
type ActionRequest struct {
	Target    Target
	Operation string
	Args      []string // full CLI argv, including existing command-specific --request-id
	Directory string   // absolute path on the selected host, never the controller cwd
}
type ActionResult struct {
	Target   Target
	ExitCode int
}
type ActionClient interface {
	Execute(context.Context, ActionRequest, io.Reader, io.Writer, io.Writer) (ActionResult, error)
}
type TerminalSize struct{ Rows, Columns uint16 }
type Attachment interface {
	io.ReadWriteCloser
	Resize(TerminalSize) error
	Wait() error
}
type TerminalClient interface {
	Attach(context.Context, Target, TerminalSize) (Attachment, error)
}

type Router struct {
	Local   InventoryClient
	Remote  InventoryClient
	Timeout time.Duration
}

// Watch emits local inventory independently of remote completion. Clients must
// honor context cancellation. Remote concurrency is bounded; local never queues
// behind a remote slot. A buffered channel also permits callers to stop reading.
func (r Router) Watch(ctx context.Context, hosts []Host) <-chan HostInventory {
	out := make(chan HostInventory, len(hosts))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, h := range hosts {
		wg.Go(func() {
			result := HostInventory{Host: h, Entries: []InventoryEntry{}}
			timeout := r.Timeout
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			client := r.Local
			if h != Local {
				client = r.Remote
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					result.Error = "timeout"
					out <- result
					return
				}
			}
			if h.Validate() != nil || client == nil {
				result.Error = "invalid_host"
			} else {
				rows, err := client.Inventory(ctx, h)
				if err != nil {
					result.Error = "unreachable"
					if h == Local {
						result.Error = "inventory_unavailable"
					}
					if ctx.Err() != nil {
						result.Error = "timeout"
					}
				} else if ValidateEntries(rows) != nil {
					result.Error = "invalid_inventory"
				} else {
					for _, row := range rows {
						row.Target.Host = h // trust the selected transport, not remote host claims
						result.Entries = append(result.Entries, row)
					}
					sort.Slice(result.Entries, func(i, j int) bool {
						a, b := result.Entries[i].Target, result.Entries[j].Target
						if a.Kind != b.Kind {
							return a.Kind < b.Kind
						}
						return a.ID < b.ID
					})
				}
			}
			out <- result
		})
	}
	go func() { wg.Wait(); close(out) }()
	return out
}

func (r Router) Inventory(ctx context.Context, hosts []Host) Snapshot {
	s := Snapshot{Version: 1, Hosts: []HostInventory{}}
	for h := range r.Watch(ctx, hosts) {
		s.Hosts = append(s.Hosts, h)
	}
	sort.Slice(s.Hosts, func(i, j int) bool { return s.Hosts[i].Host.Name < s.Hosts[j].Host.Name })
	return s
}

func ValidateEntries(rows []InventoryEntry) error {
	seen := map[Target]bool{}
	bindings := map[coordination.DiagnosticBinding]bool{}
	for _, row := range rows {
		t := row.Target
		t.Host = Local
		if (t.Kind != Agent && t.Kind != Interactive) || !targetID.MatchString(t.ID) || !safeText(row.Name, 255) || !safeText(row.Project, 4096) || seen[t] {
			return errors.New("invalid inventory identity")
		}
		if row.Diagnostics != nil {
			if t.Kind != Agent || coordination.ValidateDiagnosticBinding(*row.Diagnostics) != nil || bindings[*row.Diagnostics] {
				return errors.New("invalid or ambiguous diagnostic binding")
			}
			bindings[*row.Diagnostics] = true
		}
		switch row.State {
		case "running", "stopped", "absent":
		default:
			return errors.New("invalid inventory state")
		}
		seen[t] = true
	}
	return nil
}

// ResolveTarget rejects stale registration addresses before any action/attach
// adapter is allowed to choose a transport. It never resolves local profiles.
func (r Registry) ResolveTarget(t Target) (Host, error) {
	h, err := r.Resolve(t.Host.Name)
	if err != nil {
		return Host{}, err
	}
	if h != t.Host || !targetID.MatchString(t.ID) || (t.Kind != Agent && t.Kind != Interactive) {
		return Host{}, errors.New("stale or invalid target; refresh inventory")
	}
	return h, nil
}
