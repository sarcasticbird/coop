package runtime

import (
	"encoding/json"
	"strings"
	"time"
)

// ContainerInfo is the parsed state of one container.
type ContainerInfo struct {
	Name    string
	State   string
	IP      string
	CPUs    int
	Memory  int64 // bytes
	Started time.Time
	Mounts  []MountInfo
}

// MountInfo describes one mount. Bind distinguishes virtiofs bind
// mounts from named volumes (whose sources are volume images under the
// apiserver's storage dir).
type MountInfo struct {
	Source      string
	Destination string
	Bind        bool
}

// ProjectMount returns the coop's project path: the bind mount whose
// source equals its destination (the identical-path invariant). Agent
// state volumes also live under HOME, so destination alone is not
// enough.
func (c ContainerInfo) ProjectMount() string {
	for _, m := range c.Mounts {
		if m.Bind && m.Source == m.Destination {
			return m.Destination
		}
	}
	return ""
}

// listEntry mirrors the fields we consume from
// `container list --all --format json`. Parsed defensively — the
// schema is Apple's, not ours.
type listEntry struct {
	Configuration struct {
		ID        string `json:"id"`
		Resources struct {
			CPUs          int   `json:"cpus"`
			MemoryInBytes int64 `json:"memoryInBytes"`
		} `json:"resources"`
		Mounts []struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
			Type        struct {
				Virtiofs *struct{} `json:"virtiofs"`
			} `json:"type"`
		} `json:"mounts"`
	} `json:"configuration"`
	Status struct {
		State       string `json:"state"`
		StartedDate string `json:"startedDate"`
		Networks    []struct {
			IPv4Address string `json:"ipv4Address"`
		} `json:"networks"`
	} `json:"status"`
}

// Containers returns all containers whose name has the coop- prefix.
func (a *Apple) Containers() ([]ContainerInfo, error) {
	out, err := a.output("list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseList(out)
}

func parseList(data []byte) ([]ContainerInfo, error) {
	var entries []listEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	var infos []ContainerInfo
	for _, e := range entries {
		if !strings.HasPrefix(e.Configuration.ID, "coop-") {
			continue
		}
		info := ContainerInfo{
			Name:   e.Configuration.ID,
			State:  e.Status.State,
			CPUs:   e.Configuration.Resources.CPUs,
			Memory: e.Configuration.Resources.MemoryInBytes,
		}
		if len(e.Status.Networks) > 0 {
			info.IP = strings.Split(e.Status.Networks[0].IPv4Address, "/")[0]
		}
		if t, err := time.Parse(time.RFC3339, e.Status.StartedDate); err == nil {
			info.Started = t
		}
		for _, m := range e.Configuration.Mounts {
			info.Mounts = append(info.Mounts, MountInfo{
				Source:      m.Source,
				Destination: m.Destination,
				Bind:        m.Type.Virtiofs != nil,
			})
		}
		infos = append(infos, info)
	}
	return infos, nil
}
