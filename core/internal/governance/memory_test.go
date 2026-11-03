package governance

import (
	"github.com/BurntSushi/toml"
	"testing"
	"time"
)

func TestMemoryReviewGrantUsesHostUIDAndExactProjects(t *testing.T) {
	s := snapshot()
	s.Policy.MemoryReviewers = map[string]MemoryReviewer{"501": {Subject: "alice", Projects: []string{"demo"}}}
	store, e := s.MemoryStore(t.TempDir(), 501, true)
	if e != nil || store.Actor.Subject != "alice" || store.Actor.PolicyDigest != s.Digest || len(store.AllowedProjects) != 1 || store.AllowedProjects[0] != "demo" {
		t.Fatal(store, e)
	}
	if _, e = s.MemoryStore(store.Home, 502, true); e == nil {
		t.Fatal("ungranted UID accepted")
	}
	if _, e = s.MemoryStore(store.Home, 0, true); e == nil {
		t.Fatal("root bypassed explicit grants")
	}
	if _, e = s.MemoryStore(store.Home, 502, false); e != nil {
		t.Fatal("local read unexpectedly requires write grant", e)
	}
	s.Policy.ExpiresAt = time.Now().Add(-time.Second)
	if _, e = s.MemoryStore(store.Home, 501, true); e == nil {
		t.Fatal("expired policy accepted")
	}
	s = snapshot()
	for _, grant := range []map[string]MemoryReviewer{
		{"user": {Subject: "alice", Projects: []string{"demo"}}},
		{"0501": {Subject: "alice", Projects: []string{"demo"}}},
		{"501": {Subject: "", Projects: []string{"demo"}}},
		{"501": {Subject: "alice", Projects: []string{"*"}}},
		{"501": {Subject: "alice"}},
	} {
		s.Policy.MemoryReviewers = grant
		if s.Policy.Validate() == nil {
			t.Fatal("invalid identity accepted", grant)
		}
	}
}

func TestMemoryReviewerPolicyTOML(t *testing.T) {
	body := `schema_version = "sbx.organization/v1"
organization = "team"
revision = "2"
enabled = true
expires_at = 2099-01-01T00:00:00Z
[memory_reviewers."501"]
subject = "alice"
projects = ["demo"]
`
	var p Policy
	md, e := toml.Decode(body, &p)
	if e != nil || len(md.Undecoded()) != 0 {
		t.Fatal(md, e)
	}
	if e = p.Validate(); e != nil {
		t.Fatal(e)
	}
	s := Snapshot{Managed: true, Digest: "checked-admin-installation", Policy: p}
	store, e := s.MemoryStore(t.TempDir(), 501, true)
	if e != nil || store.Actor.Subject != "alice" || store.AllowedProjects[0] != "demo" {
		t.Fatal(store, e)
	}
}
