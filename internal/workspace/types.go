// Package workspace owns identities and persistent lifecycle rules shared by
// mounted interactive Coops and self-contained agent machines.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

type Mode string

const (
	Interactive Mode = "interactive"
	Agent       Mode = "agent"
)

var ErrMaintenanceRequired = errors.New("workspace maintenance required")
var ErrUnsupported = errors.New("operation unsupported by this runtime")

type Spec struct {
	Mode                                                              Mode
	Identity, RuntimeName, Project, Home, Workdir, Image, Fingerprint string
	Config                                                            config.Config
	Profile                                                           config.Profile
}
type Info struct {
	Name                                 string
	Mode                                 Mode
	State                                runtime.State
	Fingerprint, Image, ChildContainerID string
	RuntimeInstance, BootIdentity        string
}
type Runner interface {
	Exec(context.Context, string, string, []string, io.Reader, io.Writer, io.Writer) error
	Attach(context.Context, string, string, []string) error
}
type Backend interface {
	Runner
	Inspect(context.Context, Spec) (Info, error)
	Create(context.Context, Spec) error
	Start(context.Context, Spec) error
	Stop(context.Context, Spec) error
	Remove(context.Context, Spec) error
	Apply(context.Context, Spec) error
}
type AppliedSpec struct {
	Project   string                    `json:"project,omitempty"`
	Home      string                    `json:"home"`
	Workdir   string                    `json:"workdir"`
	Resources config.Resources          `json:"resources"`
	Mounts    []config.Mount            `json:"mounts,omitempty"`
	Volumes   []config.Volume           `json:"volumes,omitempty"`
	Publishes []config.Publish          `json:"publishes,omitempty"`
	SSH       bool                      `json:"ssh"`
	Command   []string                  `json:"command,omitempty"`
	Orgs      []string                  `json:"orgs,omitempty"`
	Restart   string                    `json:"restart,omitempty"`
	Services  map[string]config.Service `json:"services,omitempty"`
}
type Record struct {
	Version     int         `json:"version"`
	Mode        Mode        `json:"mode"`
	Identity    string      `json:"identity"`
	RuntimeName string      `json:"runtime_name"`
	Fingerprint string      `json:"fingerprint"`
	Image       string      `json:"image"`
	Applied     AppliedSpec `json:"applied"`
}
type Store interface {
	Load(Mode, string) (Record, error)
	Save(Record) error
	Remove(Mode, string) error
	List() ([]Record, error)
}

func CheckSpecification(state runtime.State, have, want string) error {
	if state != runtime.StateAbsent && have != want {
		return fmt.Errorf("%w: configuration changed; guest disk preserved; run coop apply after coop rebuild if an image update is needed", ErrMaintenanceRequired)
	}
	return nil
}
func (s Spec) Record() Record {
	return Record{Version: 1, Mode: s.Mode, Identity: s.Identity, RuntimeName: s.RuntimeName, Fingerprint: s.Fingerprint, Image: s.Image, Applied: AppliedSpec{
		Project: s.Project, Home: s.Home, Workdir: s.Workdir, Resources: s.Config.Resources, Mounts: s.Config.Mounts, Volumes: s.Config.Volumes, Publishes: s.Config.Publishes, SSH: s.Config.SSH, Command: s.Profile.Command, Orgs: s.Profile.Orgs, Restart: s.Profile.Restart, Services: s.Profile.Services,
	}}
}
