// Package hostrouter provides presentation-independent local/SSH fleet routing.
package hostrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sarcasticbird/coop/internal/hostcontrol"
)

// Host is a trusted registry reference, not an identity supplied by a remote.
// Targets retain the address so changing a registration invalidates old targets.
type Host struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
}

var Local = Host{Name: "local"}
var serverName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var sshWord = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)

func (h Host) Validate() error {
	if h == Local {
		return nil
	}
	if !serverName.MatchString(h.Name) || h.Name == "local" || strings.HasSuffix(h.Name, "-") {
		return errors.New("invalid or reserved server name")
	}
	if len(h.Address) > 255 {
		return errors.New("invalid SSH destination; use a trusted SSH alias or [user@]hostname")
	}
	parts := strings.Split(h.Address, "@")
	if len(parts) > 2 || (len(parts) == 2 && !sshWord.MatchString(parts[0])) {
		return errors.New("invalid SSH destination; use a trusted SSH alias or [user@]hostname")
	}
	address := parts[len(parts)-1]
	if !sshWord.MatchString(address) && net.ParseIP(address) == nil {
		return errors.New("invalid SSH destination; use a trusted SSH alias or [user@]hostname")
	}
	return nil
}

type Registry struct{ Path string }

func DefaultRegistry() (Registry, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Registry{}, err
		}
		root = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(root) {
		return Registry{}, errors.New("server configuration root must be absolute")
	}
	return Registry{Path: filepath.Join(root, "coop", "servers", "registry.json")}, nil
}

type registryFile struct {
	Version int    `json:"version"`
	Servers []Host `json:"servers"`
}

func decodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid JSON document")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid JSON document")
	}
	return nil
}

func readRegistry(b []byte) (registryFile, error) {
	var f registryFile
	if len(b) == 0 {
		return f, errors.New("invalid server registry")
	}
	if err := decodeJSON(b, &f); err != nil || f.Version != 1 || f.Servers == nil || len(f.Servers) > 64 {
		return f, errors.New("invalid server registry")
	}
	seen := map[string]bool{}
	for _, h := range f.Servers {
		if h.Validate() != nil || h.Name == "local" || seen[h.Name] {
			return f, errors.New("invalid server registry")
		}
		seen[h.Name] = true
	}
	sort.Slice(f.Servers, func(i, j int) bool { return f.Servers[i].Name < f.Servers[j].Name })
	return f, nil
}

func (r Registry) List() ([]Host, error) {
	b, err := hostcontrol.ReadPrivateFile(r.Path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return []Host{}, nil
	}
	if err != nil {
		return nil, errors.New("cannot read private server registry (check ownership, permissions and path)")
	}
	f, err := readRegistry(b)
	return f.Servers, err
}

// Resolve never consults the registry for local, preserving local-only operation
// even when remote configuration is unavailable or malformed.
func (r Registry) Resolve(name string) (Host, error) {
	if name == "local" {
		return Local, nil
	}
	hosts, err := r.List()
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.Name == name {
			return h, nil
		}
	}
	return Host{}, errors.New("unknown server; use coop server list")
}

// Change edits references only. Registration never connects or reads SSH keys.
func (r Registry) Change(operation, name, address string) error {
	h := Host{Name: name, Address: address}
	if operation == "remove" {
		h.Address = "unused"
	}
	if err := h.Validate(); err != nil || name == "local" {
		return errors.New("invalid server name or SSH destination (local is reserved)")
	}
	return hostcontrol.UpdatePrivateFile(r.Path, 64<<10, func(b []byte) ([]byte, error) {
		f := registryFile{Version: 1, Servers: []Host{}}
		var err error
		if b != nil {
			f, err = readRegistry(b)
			if err != nil {
				return nil, err
			}
		}
		index := -1
		for i, old := range f.Servers {
			if old.Name == name {
				index = i
			}
		}
		switch operation {
		case "add":
			if index >= 0 {
				if f.Servers[index] != h {
					return nil, errors.New("server already exists; use coop server update")
				}
			} else {
				if len(f.Servers) >= 64 {
					return nil, errors.New("server registry limit reached (64)")
				}
				f.Servers = append(f.Servers, h)
			}
		case "update", "remove":
			if index < 0 {
				return nil, errors.New("unknown server; use coop server list")
			}
			if operation == "update" {
				f.Servers[index] = h
			} else {
				f.Servers = append(f.Servers[:index], f.Servers[index+1:]...)
			}
		default:
			return nil, errors.New("unsupported registry operation")
		}
		sort.Slice(f.Servers, func(i, j int) bool { return f.Servers[i].Name < f.Servers[j].Name })
		return json.MarshalIndent(f, "", "  ")
	})
}
