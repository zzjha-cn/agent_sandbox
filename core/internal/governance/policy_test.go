package governance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sandx/internal/config"
	"sandx/internal/execution"
	"sandx/internal/external"
	"sandx/internal/sink"
	"sandx/internal/source"
)

func snapshot() Snapshot {
	return Snapshot{Managed: true, Digest: strings.Repeat("a", 64), Policy: Policy{SchemaVersion: "sbx.organization/v1", Organization: "team", Revision: "1", Enabled: true, ExpiresAt: time.Now().Add(time.Hour), NetworkHosts: []string{"api.example.com"}, AllowEmptySource: true}}
}
func TestCeilingsOnlyTighten(t *testing.T) {
	s := snapshot()
	path := t.TempDir()
	s.Policy.Sources = map[string]Resource{"input": {Kind: "files", Path: path}}
	s.Policy.Sinks = map[string]Resource{"out": {Kind: "local_directory", Path: filepath.Join(path, "new-target")}}
	s.Policy.Tools = map[string]Tool{"read": {Endpoint: "https://records.example.com", Targets: []string{"r1"}, Fields: []string{"status"}, ReadFields: []string{"status"}, Actions: []string{"read", "lookup"}}}
	p := execution.DefaultPipelinePolicy()
	p.AllowUnverified = true
	p.Sources = map[string]source.Grant{"input": {Kind: "files", Path: path, AllowWorkingTree: true, AllowUntracked: true}, "other": {Kind: "files", Path: path}}
	p.Sinks = map[string]sink.Grant{"out": {Kind: "local_directory", Path: filepath.Join(path, "new-target"), AllowReplace: true}, "other": {Kind: "local_directory", Path: path}}
	p.Tools = map[string]external.Grant{"read": {Connector: "record_http", Endpoint: "https://records.example.com", Targets: []string{"r1", "r2"}, Fields: []string{"status", "secret"}, ReadFields: []string{"status", "secret"}, Actions: []string{"read", "write", "lookup"}}, "other": {Connector: "record_http"}}
	if err := s.ConstrainPipeline(&p); err != nil {
		t.Fatal(err)
	}
	if len(p.Sources) != 1 || len(p.Sinks) != 1 || len(p.Tools) != 1 || p.AllowUnverified || p.Sources["input"].AllowWorkingTree || p.Sinks["out"].AllowReplace {
		t.Fatalf("expanded capability: %+v", p)
	}
	g := p.Tools["read"]
	if !reflect.DeepEqual(g.Targets, []string{"r1"}) || !reflect.DeepEqual(g.Fields, []string{"status"}) || !reflect.DeepEqual(g.ReadFields, []string{"status"}) || !reflect.DeepEqual(g.Actions, []string{"read", "lookup"}) {
		t.Fatal(g)
	}
	// Organization grants alone cannot create a personal grant.
	empty := execution.DefaultPipelinePolicy()
	if err := s.ConstrainPipeline(&empty); err != nil || len(empty.Tools) != 0 || len(empty.Sources) != 0 || len(empty.Sinks) != 0 {
		t.Fatal(empty, err)
	}
	cfg := config.Default()
	cfg.Network.Allow = []string{"evil.example.com", ".example.com", "api.example.com"}
	cfg.Network.CloudMCP = true
	cfg.Git.Protect = false
	if err := s.Constrain(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Mode != "allowlist" || cfg.Network.Proxy != "dedicated" || cfg.Network.CloudMCP || !cfg.Git.Protect || !reflect.DeepEqual(cfg.Network.Allow, []string{"api.example.com"}) {
		t.Fatal(cfg)
	}
}
func TestPolicyDoesNotAuthorizeRedirectedResources(t *testing.T) {
	s := snapshot()
	a, b := t.TempDir(), t.TempDir()
	s.Policy.Sources = map[string]Resource{"input": {Kind: "files", Path: a}}
	p := execution.DefaultPipelinePolicy()
	p.Sources = map[string]source.Grant{"input": {Kind: "files", Path: b}}
	if err := s.ConstrainPipeline(&p); err != nil || len(p.Sources) != 0 {
		t.Fatal(p, err)
	}
	link := filepath.Join(b, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	if !samePath(a, link) || samePath(a, b) {
		t.Fatal("canonical paths not enforced")
	}
}
func TestManagedProfileRejectsBypassesAndRevocation(t *testing.T) {
	s := snapshot()
	for _, mutate := range []func(*config.Config){func(c *config.Config) { c.Network.Upstream = "http://localhost:7890" }, func(c *config.Config) { c.Forward = []config.Forward{{Port: 1234, To: config.Target{S: "1234"}}} }, func(c *config.Config) { c.Services = []config.Service{{Name: "db"}} }, func(c *config.Config) { c.OnExit = "curl secret" }, func(c *config.Config) { c.Prep.OnDrift = "build" }} {
		c := config.Default()
		mutate(&c)
		if s.Constrain(&c) == nil {
			t.Fatal("accepted bypass", c)
		}
	}
	req := execution.Request{Mode: execution.Headless, Pipeline: &execution.PipelinePlan{}}
	if err := s.CheckRequest(req); err != nil {
		t.Fatal(err)
	}
	for _, r := range []execution.Request{{Mode: execution.Interactive}, {Mode: execution.Headless}, {Mode: execution.Headless, Detach: true, Pipeline: req.Pipeline}} {
		if s.CheckRequest(r) == nil {
			t.Fatal("unsupervised entry accepted")
		}
	}
	s.Policy.AllowEmptySource = false
	if s.CheckRequest(req) == nil {
		t.Fatal("empty source accepted")
	}
	s.Policy.Enabled = false
	if s.Active() == nil {
		t.Fatal("disabled accepted")
	}
	s.Policy.Enabled = true
	s.Policy.ExpiresAt = time.Now().Add(-time.Second)
	if s.Active() == nil {
		t.Fatal("expired accepted")
	}
}
func TestWatchCancelsOnUpdateRemovalInvalidAndExpiry(t *testing.T) {
	for _, kind := range []string{"update", "removal", "invalid", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			initial := snapshot()
			read := func() (Snapshot, error) {
				s := initial
				switch kind {
				case "update":
					s.Digest = "new"
				case "removal":
					s.Managed = false
				case "invalid":
					return s, errors.New("bad")
				case "expiry":
					s.Policy.ExpiresAt = time.Now().Add(-time.Second)
				}
				return s, nil
			}
			ctx, stop := Watch(context.Background(), initial, read)
			defer stop()
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("revocation not observed")
			}
		})
	}
}
func TestRootPolicyInstallationIntegrity(t *testing.T) {
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.MkdirTemp(home, ".sbx-g8-test-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	name := filepath.Join(root, "policy.toml")
	body := "schema_version = 'sbx.organization/v1'\norganization = 'team'\nrevision = '1'\nenabled = true\nexpires_at = " + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := load(name, uint32(os.Getuid()))
	if err != nil || !s.Managed {
		t.Fatal(s, err)
	}
	if os.Getuid() != 0 {
		if _, err = load(name, 0); err == nil {
			t.Fatal("non-root policy accepted by production trust")
		}
	}
	for _, suffix := range []string{"unknown = true\n", "enabled = false\n"} {
		os.WriteFile(name, []byte(body+suffix), 0600)
		if _, err = load(name, uint32(os.Getuid())); err == nil {
			t.Fatal("unknown/duplicate accepted")
		}
	}
	os.WriteFile(name, []byte(body), 0600)
	os.Chmod(name, 0666)
	if _, err = load(name, uint32(os.Getuid())); err == nil {
		t.Fatal("writable accepted")
	}
	os.Chmod(name, 0600)
	link := filepath.Join(root, "link")
	os.Symlink(name, link)
	if _, err = load(link, uint32(os.Getuid())); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Chmod(root, 0777)
	if _, err = load(name, uint32(os.Getuid())); err == nil {
		t.Fatal("writable parent accepted")
	}
}

func TestPersonalAllowlistAndForwardRemainNarrower(t *testing.T) {
	s := snapshot()
	s.Policy.NetworkHosts = []string{"api.example.com", "extra.example.com"}
	c := config.Default()
	c.Network.Mode = "allowlist"
	c.Network.Allow = []string{"api.example.com"}
	f := config.Forward{Port: 5432, To: config.Target{S: "db.internal:5432"}, Host: "db.internal"}
	c.Forward = []config.Forward{f}
	s.Policy.Forward = []string{ForwardKey(f)}
	if err := s.Constrain(&c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Network.Allow, []string{"api.example.com"}) {
		t.Fatal("organization expanded personal allowlist", c.Network.Allow)
	}
	c.Forward[0].To.S = "other.internal:5432"
	if s.Constrain(&c) == nil {
		t.Fatal("redirected forward accepted")
	}
}
func TestWriteRevocationPreservesBoundLookupAuthority(t *testing.T) {
	s := snapshot()
	s.Policy.Tools = map[string]Tool{"business": {Endpoint: "https://records.example.com", Targets: []string{"r1"}, ReadFields: []string{"status"}, Actions: []string{"read", "lookup"}}}
	grant := external.Grant{Connector: "record_http", Endpoint: "https://records.example.com", CredentialFile: filepath.Join(t.TempDir(), "token"), Targets: []string{"r1"}, Fields: []string{"status"}, ReadFields: []string{"status"}, Actions: []string{"read", "write", "lookup"}, ExpiresAt: time.Now().Add(time.Hour), TimeoutSeconds: 5, MaxResponseBytes: 1024}
	p := execution.DefaultPipelinePolicy()
	p.Tools = map[string]external.Grant{"business": grant}
	if err := s.ConstrainPipeline(&p); err != nil {
		t.Fatal(err)
	}
	current := p.Tools["business"]
	call := external.Call{ID: "change", GrantRef: "business", Target: "r1", Value: map[string]string{"status": "done"}}
	if current.Binding() != grant.Binding() {
		t.Fatal("revocation redirected receipt binding")
	}
	if current.Authorize(call, "write") == nil || current.Authorize(call, "lookup") != nil || current.Authorize(call, "read") != nil {
		t.Fatal("unknown-result reconciliation authority broken")
	}
}
