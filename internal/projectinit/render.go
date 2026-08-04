package projectinit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
)

func Render(volumes []string, publishes []config.Publish) ([]byte, error) {
	if len(volumes) == 0 && len(publishes) == 0 {
		return nil, nil
	}
	for _, volume := range volumes {
		if hasControl(volume) {
			return nil, fmt.Errorf("project volume path %q contains a control character", volume)
		}
	}
	volumes = slices.Clone(volumes)
	slices.Sort(volumes)
	publishes = slices.Clone(publishes)
	slices.SortFunc(publishes, func(a, b config.Publish) int {
		if a.HostPort != b.HostPort {
			return a.HostPort - b.HostPort
		}
		return a.GuestPort - b.GuestPort
	})

	var block strings.Builder
	first := true
	separator := func() {
		if !first {
			block.WriteByte('\n')
		}
		first = false
	}
	for _, volume := range volumes {
		separator()
		fmt.Fprintf(&block, "[[volume]]\npath = %s\n", strconv.Quote(volume))
	}
	for _, publish := range publishes {
		separator()
		fmt.Fprintf(&block, "[[publish]]\nguest_port = %d\nhost_port = %d\n", publish.GuestPort, publish.HostPort)
	}
	return []byte(block.String()), nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
