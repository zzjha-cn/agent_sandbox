// Package governance applies administrator ceilings after user configuration.
// It does not protect against administrators or direct Docker API access.
package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"sandx/internal/config"
	"sandx/internal/execution"
	"sandx/internal/proxy"
)

const PolicyPath = "/etc/sbx/organization.toml"

type Resource struct {
	Branch           string `toml:"branch" json:"branch"`
	Kind             string `toml:"kind" json:"kind"`
	Path             string `toml:"path" json:"path"`
	AllowReplace     bool   `toml:"allow_replace" json:"allow_replace"`
	AllowWorkingTree bool   `toml:"allow_working_tree" json:"allow_working_tree"`
	AllowUntracked   bool   `toml:"allow_untracked" json:"allow_untracked"`
}
type Tool struct {
	Endpoint   string   `toml:"endpoint" json:"endpoint"`
	Targets    []string `toml:"targets" json:"targets"`
	Fields     []string `toml:"fields" json:"fields"`
	ReadFields []string `toml:"read_fields" json:"read_fields"`
	Actions    []string `toml:"actions" json:"actions"`
}
type Events struct {
	Endpoint string `toml:"endpoint" json:"endpoint"`
}
type Policy struct {
	SchemaVersion    string              `toml:"schema_version" json:"schema_version"`
	Organization     string              `toml:"organization" json:"organization"`
	Revision         string              `toml:"revision" json:"revision"`
	Enabled          bool                `toml:"enabled" json:"enabled"`
	ExpiresAt        time.Time           `toml:"expires_at" json:"expires_at"`
	NetworkHosts     []string            `toml:"network_hosts" json:"network_hosts"`
	Forward          []string            `toml:"forward" json:"forward"`
	AllowEmptySource bool                `toml:"allow_empty_source" json:"allow_empty_source"`
	Sources          map[string]Resource `toml:"sources" json:"sources"`
	Sinks            map[string]Resource `toml:"sinks" json:"sinks"`
	Tools            map[string]Tool     `toml:"tools" json:"tools"`
	Events           Events              `toml:"events" json:"events"`
}
type Snapshot struct {
	Managed bool   `json:"managed"`
	Path    string `json:"path"`
	Digest  string `json:"digest,omitempty"`
	Policy  Policy `json:"policy"`
}

// Load uses a fixed system path. No config, SBX_HOME, env or CLI override exists.
func Load() (Snapshot, error) { return load(PolicyPath, 0) }
func load(name string, owner uint32) (Snapshot, error) {
	s := Snapshot{Path: name}
	_, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		// A dangling symlink is an invalid installation, never an absent policy.
		return s, nil
	}
	if err != nil {
		return s, err
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return s, err
	}
	for p := real; ; p = filepath.Dir(p) {
		info, e := os.Stat(p)
		if e != nil {
			return s, e
		}
		if e = trusted(info, owner); e != nil {
			return s, e
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return s, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return s, err
	}
	if err = trusted(info, owner); err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return s, errors.New("organization policy must be a regular file at most 1 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return s, err
	}
	if len(b) > 1<<20 {
		return s, errors.New("organization policy too large")
	}
	md, err := toml.Decode(string(b), &s.Policy)
	if err != nil {
		return s, errors.New("invalid organization policy TOML")
	}
	if len(md.Undecoded()) > 0 {
		return s, errors.New("unknown organization policy field")
	}
	if err = s.Policy.Validate(); err != nil {
		return s, err
	}
	sum := sha256.Sum256(b)
	s.Digest = hex.EncodeToString(sum[:])
	s.Managed = true
	return s, nil
}
func trusted(info os.FileInfo, owner uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != owner && stat.Uid != 0) || info.Mode().Perm()&0022 != 0 {
		return errors.New("organization installation must be root-owned without group/other write permission (including parent directories)")
	}
	return nil
}
func (p Policy) Validate() error {
	if p.SchemaVersion != "sbx.organization/v1" || p.Organization == "" || len(p.Organization) > 80 || p.Revision == "" || len(p.Revision) > 80 || p.ExpiresAt.IsZero() {
		return errors.New("organization policy requires schema, organization, revision and expiry")
	}
	for _, h := range p.NetworkHosts {
		if !validHost(h) {
			return errors.New("organization network_hosts requires lowercase exact DNS names")
		}
	}
	for _, r := range p.Sources {
		if (r.Kind != "git" && r.Kind != "files") || !filepath.IsAbs(r.Path) {
			return errors.New("invalid organization source")
		}
	}
	for _, r := range p.Sinks {
		if (r.Kind != "git_local" && r.Kind != "local_directory") || !filepath.IsAbs(r.Path) {
			return errors.New("invalid organization sink")
		}
	}
	for _, t := range p.Tools {
		if err := endpoint(t.Endpoint); err != nil {
			return err
		}
	}
	if p.Events.Endpoint != "" {
		if err := endpoint(p.Events.Endpoint); err != nil {
			return err
		}
	}
	return nil
}
func endpoint(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return errors.New("organization endpoint requires HTTPS (HTTP only for loopback), no userinfo/query/fragment")
	}
	return nil
}
func validHost(s string) bool {
	if len(s) < 1 || len(s) > 253 || s != strings.ToLower(s) || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func (s Snapshot) Active() error {
	if s.Managed && (!s.Policy.Enabled || !time.Now().Before(s.Policy.ExpiresAt)) {
		return errors.New("organization policy disabled or expired")
	}
	return nil
}

// Constrain forces network defaults and intersects capabilities; it never grants
// a Source/Sink/tool missing from the personal registry.
func (s Snapshot) Constrain(c *config.Config) error {
	if !s.Managed {
		return nil
	}
	if err := s.Active(); err != nil {
		return err
	}
	wasOpen := c.Network.Mode == "open"
	candidates := proxy.RenderAllow(nil, proxy.BuiltinList("builtin"), proxy.BuiltinList(c.Profile), c.Network.Allow)
	hosts := []string{}
	for _, host := range s.Policy.NetworkHosts {
		allowed := wasOpen
		for _, candidate := range candidates {
			if host == candidate || strings.HasPrefix(candidate, ".") && (host == candidate[1:] || strings.HasSuffix(host, candidate)) {
				allowed = true
			}
		}
		if allowed {
			hosts = append(hosts, host)
		}
	}
	c.Network.Mode = "allowlist"
	c.Network.Proxy = "dedicated"
	c.Network.CloudMCP = false
	c.Network.Allow = hosts
	// A direct upstream, sidecar, notification or build can create another egress
	// path. These are deliberately unavailable in the first managed profile.
	if c.Network.Upstream != "" || len(c.Services) > 0 || c.OnIdle != "" || c.OnExit != "" || c.Prep.OnDrift == "build" {
		return errors.New("managed profile disallows upstream proxy, services, notifications and automatic prep builds")
	}
	for _, f := range c.Forward {
		if !slices.Contains(s.Policy.Forward, ForwardKey(f)) {
			return errors.New("forward exceeds organization ceiling")
		}
	}
	c.Git.Protect = true
	return nil
}
func ForwardKey(f config.Forward) string {
	return fmt.Sprintf("%d|%s|%s|%t", f.Port, f.To.S, f.Host, f.AllowPublic)
}
func intersection(a, b []string) []string {
	out := []string{}
	for _, v := range a {
		if slices.Contains(b, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
func canonical(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", errors.New("path must be absolute")
	}
	name = filepath.Clean(name)
	p := name
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("cannot resolve path")
		}
		p = parent
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, rel), nil
}
func samePath(a, b string) bool {
	a1, e := canonical(a)
	b1, f := canonical(b)
	return e == nil && f == nil && a1 == b1
}

func (s Snapshot) ConstrainPipeline(p *execution.PipelinePolicy) error {
	if !s.Managed {
		return nil
	}
	if err := s.Active(); err != nil {
		return err
	}
	for ref, g := range p.Sources {
		r, ok := s.Policy.Sources[ref]
		if !ok || g.Kind != r.Kind || !samePath(g.Path, r.Path) {
			delete(p.Sources, ref)
			continue
		}
		g.AllowWorkingTree = g.AllowWorkingTree && r.AllowWorkingTree
		g.AllowUntracked = g.AllowUntracked && r.AllowUntracked
		p.Sources[ref] = g
	}
	for ref, g := range p.Sinks {
		r, ok := s.Policy.Sinks[ref]
		if !ok || g.Kind != r.Kind || g.Branch != r.Branch || !samePath(g.Path, r.Path) {
			delete(p.Sinks, ref)
			continue
		}
		g.AllowReplace = g.AllowReplace && r.AllowReplace
		p.Sinks[ref] = g
	}
	for ref, g := range p.Tools {
		r, ok := s.Policy.Tools[ref]
		if !ok || g.Endpoint != r.Endpoint || g.Connector != "record_http" {
			delete(p.Tools, ref)
			continue
		}
		g.Targets = intersection(g.Targets, r.Targets)
		g.Fields = intersection(g.Fields, r.Fields)
		g.ReadFields = intersection(g.ReadFields, r.ReadFields)
		g.Actions = intersection(g.Actions, r.Actions)
		p.Tools[ref] = g
	}
	p.AllowUnverified = false
	return nil
}
func (s Snapshot) CheckSpec(spec execution.TaskSpec) error {
	if !s.Managed {
		return nil
	}
	if err := s.Active(); err != nil {
		return err
	}
	if (spec.Source == nil || spec.Source.Kind == "" || spec.Source.Kind == "empty") && !s.Policy.AllowEmptySource {
		return errors.New("empty source exceeds organization ceiling")
	}
	return nil
}
func (s Snapshot) CheckRequest(req execution.Request) error {
	if !s.Managed {
		return nil
	}
	if err := s.Active(); err != nil {
		return err
	}
	if req.Pipeline == nil || req.Mode != execution.Headless || req.Detach {
		return errors.New("managed profile requires synchronous v2 TaskSpec; use task run --spec")
	}
	return s.CheckSpec(req.Pipeline.Spec)
}

// Watch conservatively cancels on any update, removal, expiry or invalid file.
// Existing external effects still require normal receipt reconciliation.
func Watch(parent context.Context, initial Snapshot, read func() (Snapshot, error)) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s, e := read()
				if e != nil || s.Managed != initial.Managed || s.Digest != initial.Digest || s.Active() != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}
