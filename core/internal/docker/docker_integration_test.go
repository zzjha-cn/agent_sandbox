//go:build docker

package docker

import (
	"strings"
	"testing"
)

func TestIntegrationLifecycle(t *testing.T) {
	c := New(testing.Verbose())
	const name = "sbx-it-docker"
	labels := map[string]string{"sbx.kind": "it", "sbx.it": "docker"}
	c.Rm(name)
	c.NetworkRm(name + "-net")
	c.VolumeRm(name + "-vol")

	if err := c.NetworkCreate(name+"-net", true, labels); err != nil {
		t.Fatal(err)
	}
	defer c.NetworkRm(name + "-net")
	created, err := c.VolumeCreate(name+"-vol", labels)
	if err != nil || !created {
		t.Fatalf("volume create: %v %v", created, err)
	}
	defer c.VolumeRm(name + "-vol")
	if again, _ := c.VolumeCreate(name+"-vol", labels); again {
		t.Fatal("second create should report existing")
	}

	_, err = c.RunContainer(RunSpec{Name: name, Image: "busybox", Labels: labels, Network: name + "-net",
		Mounts: []Mount{{Source: name + "-vol", Target: "/v", Volume: true}}, Cmd: []string{"sleep", "300"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Rm(name)

	st, ok, err := c.Inspect(name)
	if err != nil || !ok || !st.Running {
		t.Fatalf("inspect: %+v %v %v", st, ok, err)
	}
	out, err := c.Exec(name, ExecOpts{}, "sh", "-c", "echo hi > /v/f && cat /v/f")
	if err != nil || out != "hi" {
		t.Fatalf("exec: %q %v", out, err)
	}
	items, err := c.ListByLabel("container", "sbx.it=docker")
	if err != nil || len(items) != 1 || items[0].Str("Names") != name {
		t.Fatalf("list: %v %v", items, err)
	}
	if err := c.Rm(name); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Inspect(name); ok || err != nil {
		t.Fatalf("after rm: %v %v", ok, err)
	}
	_, err = c.Run("run", "--definitely-bogus-flag", "busybox")
	if err == nil || !strings.Contains(err.Error(), "--definitely-bogus-flag") || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error should include command and stderr: %v", err)
	}
}
