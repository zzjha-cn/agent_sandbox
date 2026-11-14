package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/config"
	"sandx/internal/projectmemory"
)

func memoryFixture(t *testing.T) (Request, projectmemory.Store, projectmemory.Entry) {
	t.Helper()
	req := testRequest("fix the tests")
	req.Task.Home = t.TempDir()
	req.Task.WS.Root = t.TempDir()
	req.Config = config.Default()
	req.Config.DefaultAgent = "codex"
	req.Config.Memory = config.ProjectMemory{Mode: "managed", Project: "demo"}
	req.MemoryAttemptID = "attempt1"
	s := projectmemory.Store{Home: req.Task.Home}
	if _, e := s.Create("demo"); e != nil {
		t.Fatal(e)
	}
	v, e := s.Add("demo", projectmemory.Input{Kind: "testing", Title: "Docker", Content: "Check Docker", Source: "operator"})
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.Review("demo", v.ID, v.Digest, "approved", "operator", "verified")
	if e != nil {
		t.Fatal(e)
	}
	return req, s, v
}
func TestMemorySessionFrozenRevokedFreshAndCleanup(t *testing.T) {
	req, s, v := memoryFixture(t)
	first, e := prepareMemory(req, false)
	if e != nil || first.Snapshot == nil {
		t.Fatal(first, e)
	}
	if e = os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("0199a213-81c0-7800-8aa1-bbab2a035a53"), 0600); e != nil {
		t.Fatal(e)
	}
	n, e := s.Add("demo", projectmemory.Input{Kind: "testing", Title: "second", Content: "Other advice", Source: "operator"})
	if e != nil {
		t.Fatal(e)
	}
	s.Review("demo", n.ID, n.Digest, "approved", "operator", "verified")
	req.MemoryAttemptID = "attempt2"
	second, e := prepareMemory(req, false)
	if e != nil || second.Snapshot.Digest != first.Snapshot.Digest {
		t.Fatal(second, e)
	}
	s.Review("demo", v.ID, v.Digest, "archived", "operator", "obsolete")
	req.MemoryAttemptID = "attempt3"
	if _, e = prepareMemory(req, false); e == nil {
		t.Fatal("revoked memory resumed")
	}
	req.Fresh = true
	third, e := prepareMemory(req, false)
	if e != nil || third.Snapshot.Digest == first.Snapshot.Digest {
		t.Fatal(third, e)
	}
	b, e := os.ReadFile(filepath.Join(req.Task.GenDir(), "memory-prompt.txt"))
	if e != nil || !strings.Contains(string(b), "fix the tests") || !strings.Contains(string(b), "Other advice") {
		t.Fatal(string(b), e)
	}
	os.RemoveAll(req.Task.StateDir())
	if _, e = ReadMemorySession(req.Task.Home, req.Task.ID()); e != nil {
		t.Fatal("done lost memory", e)
	}
	if _, e = os.Stat(filepath.Join(MemoryTaskDir(req.Task.Home, req.Task.ID()), "attempt1.json")); e != nil {
		t.Fatal(e)
	}
}

func TestMemoryQueryAndWatchInvalidateResumeAndPinnedContext(t *testing.T) {
	req, s, _ := memoryFixture(t)
	root := req.Task.Worktree()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module first"), 0600); err != nil {
		t.Fatal(err)
	}
	facts, err := projectmemory.ObserveFiles(root, []string{"go.mod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Add("demo", projectmemory.Input{Kind: "troubleshooting", Title: "GOMODCACHE", Content: "Check prep seed location", Source: "operator", Requires: map[string]string{"file:go.mod": facts["file:go.mod"]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review("demo", v.ID, v.Digest, "approved", "operator", "verified"); err != nil {
		t.Fatal(err)
	}
	req.Config.Memory.Query = "GOMODCACHE"
	req.Config.Memory.Watch = []string{"go.mod"}
	first, err := prepareMemory(req, false)
	if err != nil || len(first.Snapshot.Entries) != 1 {
		t.Fatal(first, err)
	}
	if err = os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("prior-session"), 0600); err != nil {
		t.Fatal(err)
	}
	req.MemoryAttemptID = "attempt2"
	if _, err = prepareMemory(req, false); err != nil {
		t.Fatal("stable observation did not resume", err)
	}
	if err = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module second"), 0600); err != nil {
		t.Fatal(err)
	}
	req.MemoryAttemptID = "attempt3"
	if _, err = prepareMemory(req, false); err == nil {
		t.Fatal("changed file reused old context")
	}
	req.Fresh = true
	req.Config.Memory.Snapshot = first.Snapshot.Digest
	req.Config.Memory.Query = ""
	if _, err = prepareMemory(req, false); err == nil {
		t.Fatal("pinned old conditions accepted")
	}
}
func TestModeChangesAndActiveTaskCannotPretendToSwitch(t *testing.T) {
	req, _, _ := memoryFixture(t)
	if _, e := prepareMemory(req, false); e != nil {
		t.Fatal(e)
	}
	req.MemoryAttemptID = "attempt2"
	req.Config.Memory = config.ProjectMemory{Mode: "off"}
	if _, e := prepareMemory(req, true); e == nil {
		t.Fatal("active mode change accepted")
	}
	req.Fresh = true
	if _, e := prepareMemory(req, true); e == nil {
		t.Fatal("fresh active accepted")
	}
	if _, e := prepareMemory(req, false); e != nil {
		t.Fatal(e)
	}
}
func TestUntrackedHistoryAndManagedOrganizationDefaults(t *testing.T) {
	req, _, _ := memoryFixture(t)
	os.MkdirAll(req.Task.StateDir(), 0700)
	os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("old"), 0600)
	if _, e := prepareMemory(req, false); e == nil {
		t.Fatal("old context accepted")
	}
	req.Config.Memory = config.ProjectMemory{}
	req.OrganizationPolicyDigest = "managed-policy"
	if EffectiveMemoryMode(req) != "off" {
		t.Fatal("managed default")
	}
	req.Config.Memory.Mode = "native"
	if e := validateMemory(req); e == nil {
		t.Fatal("organization native allowed")
	}
}
func TestMemoryValidationDoesNotBlockBusinessContinuation(t *testing.T) {
	req, _, _ := memoryFixture(t)
	req.Config.Memory.Project = "missing"
	if e := validateMemory(req); e == nil {
		t.Fatal("missing project accepted for agent run")
	}
	if e := validateRequest(req); e != nil {
		t.Fatal("memory revoked should not prevent lookup/reconciliation", e)
	}
}

func TestPinnedContextAndCorruptSessionFailClosed(t *testing.T) {
	req, s, _ := memoryFixture(t)
	snapshot, e := s.Select("demo", projectmemory.Selection{})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Pin(snapshot); e != nil {
		t.Fatal(e)
	}
	req.Config.Memory.Snapshot = snapshot.Digest
	v, e := prepareMemory(req, false)
	if e != nil || v.Pinned != snapshot.Digest {
		t.Fatal(v, e)
	}
	b, e := os.ReadFile(filepath.Join(MemoryTaskDir(req.Task.Home, req.Task.ID()), "session.json"))
	if e != nil {
		t.Fatal(e)
	}
	b = []byte(strings.Replace(string(b), "Check Docker", "tampered", 1))
	os.WriteFile(filepath.Join(MemoryTaskDir(req.Task.Home, req.Task.ID()), "session.json"), b, 0600)
	os.WriteFile(filepath.Join(req.Task.StateDir(), "codex-session"), []byte("old"), 0600)
	req.MemoryAttemptID = "attempt2"
	if _, e = prepareMemory(req, false); e == nil {
		t.Fatal("corrupt context resumed")
	}
}
