package cli

import (
	"reflect"
	"sandx/internal/config"
	"sandx/internal/governance"
	"sandx/internal/task"
	"sandx/internal/workspace"
	"testing"
)

func TestManagedProxyDoesNotUnionBuiltinsOrNotifyHosts(t *testing.T) {
	a := &App{Home: t.TempDir(), Cfg: config.Default(), Organization: governance.Snapshot{Managed: true}}
	a.Cfg.Network.Allow = []string{"api.example.com"}
	a.Cfg.Network.Mode = "allowlist"
	tsk := task.Task{Home: a.Home, Name: "test", WS: workspace.Workspace{ID: "test"}}
	spec := a.proxySpec(tsk, "test")
	if spec.Open || !reflect.DeepEqual(spec.Allow, []string{"api.example.com"}) {
		t.Fatal("managed proxy expanded authority", spec)
	}
}
