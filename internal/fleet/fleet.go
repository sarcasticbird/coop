// Package fleet presents owned agent machines and interactive containers as
// distinct workspaces while hiding machines' internal child containers.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type Machine struct {
	Name, State, ChildID, IP string
	CPUs                     int
	Memory                   int64
}

func Merge(containers []runtime.ContainerInfo, machines []Machine, records []workspace.Record) ([]runtime.ContainerInfo, error) {
	children := map[string]bool{}
	owned := map[string]workspace.Record{}
	for _, r := range records {
		if r.Mode == workspace.Agent {
			owned[r.RuntimeName] = r
		}
	}
	for _, m := range machines {
		if m.ChildID != "" {
			children[m.ChildID] = true
		}
	}
	var rows []runtime.ContainerInfo
	for _, c := range containers {
		if !children[c.Name] {
			c.Mode = "interactive"
			rows = append(rows, c)
		}
	}
	for _, m := range machines {
		if r, ok := owned[m.Name]; ok {
			if m.State != "running" && m.State != "stopped" {
				return nil, fmt.Errorf("machine %s state is not authoritative", m.Name)
			}
			rows = append(rows, runtime.ContainerInfo{Name: m.Name, Mode: "agent", Profile: r.Identity, State: m.State, IP: m.IP, CPUs: m.CPUs, Memory: m.Memory})
		}
	}
	return rows, nil
}

type Runtime struct {
	runtime.Runtime
	Machine runtime.AppleMachine
	Store   workspace.Store
}

func New(base runtime.Runtime) runtime.Runtime {
	store, err := workspace.DefaultStore()
	if err != nil {
		return &Runtime{Runtime: base}
	}
	return &Runtime{Runtime: base, Store: store}
}
func (f *Runtime) Containers() ([]runtime.ContainerInfo, error) {
	containers, err := f.Runtime.Containers()
	if err != nil {
		return nil, err
	}
	if f.Store == nil {
		return nil, fmt.Errorf("cannot resolve workspace ownership store")
	}
	records, err := f.Store.List()
	if err != nil {
		return nil, err
	}
	version, err := f.Machine.Output(context.Background(), "--version")
	if err != nil {
		return nil, err
	}
	match := regexp.MustCompile(`\b([0-9]+)\.([0-9]+)\.([0-9]+)\b`).FindStringSubmatch(string(version))
	if len(match) != 4 {
		return nil, fmt.Errorf("cannot establish runtime machine capability")
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major < 1 || (major == 1 && minor < 5) {
		for _, r := range records {
			if r.Mode == workspace.Agent {
				return nil, fmt.Errorf("agent ownership exists but installed runtime does not support machines")
			}
		}
		return Merge(containers, nil, records)
	}
	data, err := f.Machine.Output(context.Background(), "machine", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		IP     string `json:"ipAddress"`
		CPUs   int    `json:"cpus"`
		Memory int64  `json:"memory"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	var machines []Machine
	for _, row := range list {
		if row.ID == "" {
			return nil, fmt.Errorf("machine listing lacks identity")
		}
		data, err := f.Machine.Output(context.Background(), "machine", "inspect", row.ID)
		if err != nil {
			return nil, err
		}
		var info []runtime.MachineInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return nil, err
		}
		if len(info) != 1 || info[0].ID != row.ID {
			return nil, fmt.Errorf("machine child ownership inspection failed")
		}
		machines = append(machines, Machine{Name: row.ID, State: row.Status, ChildID: info[0].ContainerID, IP: row.IP, CPUs: row.CPUs, Memory: row.Memory})
	}
	return Merge(containers, machines, records)
}
func (f *Runtime) List() (string, error) {
	rows, err := f.Containers()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintln(&out, "NAME MODE STATE PROFILE")
	for _, row := range rows {
		fmt.Fprintf(&out, "%s %s %s %s\n", row.Name, row.Mode, row.State, row.Profile)
	}
	return out.String(), nil
}
