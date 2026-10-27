package isolation

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"sandx/internal/docker"
)

func TestRealDockerLegacyAdoptionAndOwnershipRace(t *testing.T) {
	if os.Getenv("SBX_REAL_DOCKER_E2E") != "1" {
		t.Skip("set SBX_REAL_DOCKER_E2E=1")
	}
	ctx := context.Background()
	c := docker.New(false)
	prefix := fmt.Sprintf("sbx-r6-%d", time.Now().UnixNano())
	home, marker, race := prefix+"-home", prefix+"-owner", prefix+"-race"
	t.Cleanup(func() {
		for _, n := range []string{home, marker, race} {
			c.VolumeRm(n)
		}
	})
	if _, err := c.VolumeCreate(home, map[string]string{"sbx.kind": "shared"}); err != nil {
		t.Fatal(err)
	}
	volumeCmd := func(command string) string {
		t.Helper()
		out, err := c.Run("run", "--rm", "--pull=never", "--network=none", "--mount", "type=volume,source="+home+",target=/v", "busybox", "sh", "-c", command)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	volumeCmd("printf SYNTHETIC_AUTH_SENTINEL > /v/sentinel; chmod 600 /v/sentinel")
	before, _, err := c.VolumeLabels(home)
	if err != nil {
		t.Fatal(err)
	}
	g := New(c, Policy{Owner: "operator-a"})
	g.marker, g.shared = marker, []string{home}
	if err := g.Check(ctx); !IsRejection(err) {
		t.Fatalf("legacy silently accepted: %v", err)
	}
	if _, found, err := c.VolumeLabels(marker); err != nil || found {
		t.Fatalf("admission mutated daemon: %v %t", err, found)
	}
	s, err := g.Adopt(ctx)
	if err != nil || !s.Allowed {
		t.Fatalf("%+v %v", s, err)
	}
	after, _, err := c.VolumeLabels(home)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy labels changed: %v %v %v", before, after, err)
	}
	if out := volumeCmd("cat /v/sentinel; stat -c ':%a' /v/sentinel"); out != "SYNTHETIC_AUTH_SENTINEL:600" {
		t.Fatalf("credential fixture changed: %q", out)
	}
	g.Policy.Owner = "operator-b"
	if _, err := g.Adopt(ctx); !IsRejection(err) {
		t.Fatalf("foreign adoption accepted: %v", err)
	}
	g.Policy.Owner = "operator-a"
	if err := g.Check(ctx); err != nil {
		t.Fatal("original owner could not recover", err)
	}

	// Two operators contend for one empty daemon record. Only the winner may
	// proceed; actual Docker labels survive the losing claimant unchanged.
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, owner := range []string{"operator-a", "operator-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			g := New(c, Policy{Owner: owner})
			g.marker, g.shared = race, nil
			if _, err := g.Ensure(ctx); err == nil {
				results <- owner
			}
		}(owner)
	}
	wg.Wait()
	close(results)
	winners := []string{}
	for owner := range results {
		winners = append(winners, owner)
	}
	if len(winners) != 1 {
		t.Fatalf("claim race winners: %v", winners)
	}
	labels, _, err := c.VolumeLabels(race)
	if err != nil || labels["sbx.owner"] != winners[0] {
		t.Fatalf("winner record overwritten: %v %v", labels, err)
	}
	t.Logf("legacy sentinel/permissions preserved; original owner recovered; single claim winner=%s", winners[0])
}
