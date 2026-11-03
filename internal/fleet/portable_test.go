package fleet

import (
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"testing"
)

func TestPortableAgentInventoryDoesNotBecomeInteractive(t *testing.T) {
	r := workspace.Record{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Applied: workspace.AppliedSpec{Runtime: "container"}}
	c := runtime.ContainerInfo{Name: r.RuntimeName, State: "running", Labels: map[string]string{"coop.agent": r.Identity, "coop.storage": "agent-volumes-v1"}}
	rows, err := Merge([]runtime.ContainerInfo{c}, nil, []workspace.Record{r})
	if err != nil || len(rows) != 1 || rows[0].Mode != "agent" || rows[0].Profile != "robin" {
		t.Fatal(rows, err)
	}
	c.Labels["coop.agent"] = "foreign"
	if _, err := Merge([]runtime.ContainerInfo{c}, nil, []workspace.Record{r}); err == nil {
		t.Fatal("foreign container reported as owned bird")
	}
	if _, err := Merge(nil, []Machine{{Name: r.RuntimeName, State: "stopped"}}, []workspace.Record{r}); err == nil {
		t.Fatal("machine/container conflict hidden")
	}
}
