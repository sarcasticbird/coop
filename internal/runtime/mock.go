package runtime

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// Mock is an in-memory Runtime for tests (hosted CI cannot run Apple
// containers — no nested virtualization).
type Mock struct {
	Existing        map[string]bool
	Run_            []RunSpec
	Started         []string
	Stopped         []string
	Removed         []string
	Volumes         map[string]bool
	RemovedVol      []string
	ExecCalls       []ExecCall
	Interactive     []ExecCall
	GuestFiles      map[string]string // path -> content
	GuestModes      map[string]string // path -> octal mode
	Infos           []ContainerInfo
	Images          map[string]bool
	ImageErr        error
	ContainerImages map[string]string
	ContainerLabels map[string]map[string]string
	LabelErr        error
	ExistsErr       error
	StateErr        error
}

type ExecCall struct {
	Name    string
	Argv    []string
	Stdin   string
	Workdir string
}

func NewMock() *Mock {
	return &Mock{
		Existing:   map[string]bool{},
		Volumes:    map[string]bool{},
		GuestFiles: map[string]string{},
		GuestModes: map[string]string{},
	}
}

// State implements the interface; StateErr simulates runtime failure.
func (m *Mock) State(name string) (State, error) {
	if m.StateErr != nil {
		return StateAbsent, m.StateErr
	}
	if !m.Existing[name] {
		return StateAbsent, nil
	}
	if slices.Contains(m.Started, name) {
		return StateRunning, nil
	}
	return StateStopped, nil
}

// Running is a test convenience (not part of the Runtime interface).
func (m *Mock) Running(name string) bool { return slices.Contains(m.Started, name) }

func (m *Mock) Start(name string) error {
	m.Started = append(m.Started, name)
	return nil
}

func (m *Mock) Run(spec RunSpec) error {
	m.Run_ = append(m.Run_, spec)
	m.Existing[spec.Name] = true
	m.Started = append(m.Started, spec.Name)
	if m.ContainerImages == nil {
		m.ContainerImages = map[string]string{}
	}
	m.ContainerImages[spec.Name] = spec.Image
	if m.ContainerLabels == nil {
		m.ContainerLabels = map[string]map[string]string{}
	}
	m.ContainerLabels[spec.Name] = spec.Labels
	return nil
}

func (m *Mock) ContainerImage(name string) (string, error) {
	if img, ok := m.ContainerImages[name]; ok {
		return img, nil
	}
	return "", fmt.Errorf("no image recorded for %s", name)
}

func (m *Mock) ContainerLabel(name, key string) (string, error) {
	if m.LabelErr != nil {
		return "", m.LabelErr
	}
	return m.ContainerLabels[name][key], nil
}

func (m *Mock) Stop(name string) error {
	m.Stopped = append(m.Stopped, name)
	m.Started = slices.DeleteFunc(m.Started, func(s string) bool { return s == name })
	return nil
}

func (m *Mock) Remove(name string) error {
	m.Removed = append(m.Removed, name)
	delete(m.Existing, name)
	return nil
}

func (m *Mock) List() (string, error) { return "", nil }

func (m *Mock) Containers() ([]ContainerInfo, error) { return m.Infos, nil }

func (m *Mock) EnsureVolume(name string) error {
	m.Volumes[name] = true
	return nil
}

func (m *Mock) RemoveVolume(name string) error {
	m.RemovedVol = append(m.RemovedVol, name)
	delete(m.Volumes, name)
	return nil
}

func (m *Mock) ListVolumes() ([]string, error) {
	names := make([]string, 0, len(m.Volumes))
	for name := range m.Volumes {
		names = append(names, name)
	}
	return names, nil
}

func (m *Mock) Exec(name string, argv []string, stdin io.Reader) error {
	call := ExecCall{Name: name, Argv: argv}
	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		call.Stdin = string(b)
	}
	m.ExecCalls = append(m.ExecCalls, call)

	// Emulate the atomic seed-write script (positional dest/mode/parent).
	if len(argv) == 7 && argv[0] == "sh" && argv[1] == "-c" &&
		strings.Contains(argv[2], `cat > "$t"`) {
		m.GuestFiles[argv[4]] = call.Stdin
		m.GuestModes[argv[4]] = argv[5]
	}
	return nil
}

func (m *Mock) ExecInteractive(name, workdir string, argv []string) error {
	m.Interactive = append(m.Interactive, ExecCall{Name: name, Argv: argv, Workdir: workdir})
	return nil
}

func (m *Mock) GuestFileExists(name, path string) (bool, error) {
	if m.ExistsErr != nil {
		return false, m.ExistsErr
	}
	_, ok := m.GuestFiles[path]
	return ok, nil
}

func (m *Mock) ImageExists(name string) (bool, error) {
	if m.ImageErr != nil {
		return false, m.ImageErr
	}
	return m.Images[name], nil
}

// ExecString renders exec calls for assertions.
func (m *Mock) ExecString() string {
	var b strings.Builder
	for _, c := range m.ExecCalls {
		fmt.Fprintf(&b, "%s: %s\n", c.Name, strings.Join(c.Argv, " "))
	}
	return b.String()
}
