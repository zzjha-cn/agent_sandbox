package docker

import (
	"testing"
)

func TestResourceScopeCannotBeOverriddenByCallerLabels(t *testing.T) {
	c := &Client{ResourceLabels: map[string]string{"sbx.owner": "a", "sbx.namespace": "n", "sbx.isolation": "v1"}}
	input := map[string]string{"sbx.kind": "dep", "sbx.owner": "b"}
	labels := c.resourceLabels(input)
	if labels["sbx.owner"] != "a" || labels["sbx.kind"] != "dep" || input["sbx.owner"] != "b" {
		t.Fatal(labels, input)
	}
}

func TestVolumeScopeAllowsLegacyButRejectsForeignOrPartialLabels(t *testing.T) {
	c := &Client{ResourceLabels: map[string]string{"sbx.owner": "a", "sbx.namespace": "n", "sbx.isolation": "v1"}}
	for _, tc := range []struct {
		labels map[string]string
		reject bool
	}{
		{nil, false},
		{map[string]string{"sbx.kind": "shared"}, false},
		{map[string]string{"sbx.owner": "a"}, true},
		{map[string]string{"sbx.owner": "b", "sbx.namespace": "n", "sbx.isolation": "v1"}, true},
		{map[string]string{"sbx.owner": "a", "sbx.namespace": "n", "sbx.isolation": "v1"}, false},
	} {
		if err := c.checkVolumeScope("test", tc.labels); (err != nil) != tc.reject {
			t.Fatalf("%v: %v", tc.labels, err)
		}
	}
}
