// Package isolation binds one trusted operator to one Docker daemon. Labels
// prevent accidental reuse; Docker socket access remains the security boundary.
package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"sandx/internal/docker"
)

const (
	DedicatedDaemon = "dedicated-daemon"
	MarkerVolume    = "sbx-daemon-ownership"
	Schema          = "sbx.isolation/v1"
)

var SharedVolumes = []string{"sbx-home", "sbx-cache", "sbx-mise"}
var ownerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Policy struct {
	Boundary string `toml:"boundary"`
	Owner    string `toml:"owner"`
}

func (p Policy) Validate() error {
	if p.Boundary != "" && p.Boundary != DedicatedDaemon && p.Boundary != "shared-daemon" {
		return fmt.Errorf("isolation.boundary must be dedicated-daemon or shared-daemon")
	}
	if p.Owner != "" && !ownerPattern.MatchString(p.Owner) {
		return fmt.Errorf("isolation.owner must be a lowercase slug of at most 64 characters")
	}
	return nil
}

// LocalOwner is independent of SBX_HOME and the repository. Administrators can
// pin owner in global configuration for remote workers or host renames.
func LocalOwner() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return "local-" + hash(host+"\x00"+strconv.Itoa(os.Getuid())+"\x00"+home), nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:24]
}

type Rejection struct{ Reason string }

func (e *Rejection) Error() string            { return e.Reason }
func reject(format string, args ...any) error { return &Rejection{fmt.Sprintf(format, args...)} }
func IsRejection(err error) bool              { var r *Rejection; return errors.As(err, &r) }

func CheckBoundary(p Policy) error {
	if err := p.Validate(); err != nil {
		return reject("%s", err)
	}
	if p.Boundary == "shared-daemon" {
		return reject("shared-daemon deployment is unsupported: give each mutually untrusted user a dedicated Docker daemon (rootless daemon or isolated VM); resource labels do not isolate Docker API access")
	}
	return nil
}

type Status struct {
	Schema        string   `json:"schema_version"`
	Boundary      string   `json:"boundary"`
	Namespace     string   `json:"namespace"`
	Owner         string   `json:"owner"`
	RecordedOwner string   `json:"recorded_owner,omitempty"`
	Claimed       bool     `json:"claimed"`
	LegacyVolumes []string `json:"legacy_volumes,omitempty"`
	Allowed       bool     `json:"allowed"`
	Reason        string   `json:"reason,omitempty"`
}

func (s Status) Labels() map[string]string {
	return map[string]string{"sbx.isolation": Schema, "sbx.namespace": s.Namespace, "sbx.owner": s.Owner}
}

type Guard struct {
	Docker *docker.Client
	Policy Policy
	// Fixed in production. Package tests use isolated names on a real daemon.
	marker string
	shared []string
}

func New(client *docker.Client, policy Policy) *Guard {
	return &Guard{Docker: client, Policy: policy, marker: MarkerVolume, shared: append([]string(nil), SharedVolumes...)}
}

// Status is read-only, including on an empty daemon.
func (g *Guard) Status(ctx context.Context) (Status, error) {
	s := Status{Schema: Schema, Boundary: DedicatedDaemon, Owner: g.Policy.Owner}
	if g.Policy.Boundary != "" {
		s.Boundary = g.Policy.Boundary
	}
	if err := CheckBoundary(g.Policy); err != nil {
		s.Reason = err.Error()
		return s, err
	}
	if s.Owner == "" {
		var err error
		s.Owner, err = LocalOwner()
		if err != nil {
			return s, err
		}
	}
	client := g.Docker.WithContext(ctx)
	id, err := client.Run("info", "--format", "{{.ID}}")
	if err != nil {
		return s, err
	}
	if strings.TrimSpace(id) == "" {
		return s, fmt.Errorf("Docker daemon returned an empty identity")
	}
	s.Namespace = "daemon-" + hash(strings.TrimSpace(id))
	labels, exists, err := client.VolumeLabels(g.marker)
	if err != nil {
		return s, err
	}
	s.Claimed, s.RecordedOwner = exists, labels["sbx.owner"]
	if exists {
		for k, value := range s.Labels() {
			if labels[k] != value {
				s.Reason = "daemon ownership does not match this operator or daemon identity; select the correct Docker context/owner; ownership is never overwritten"
				return s, reject("%s", s.Reason)
			}
		}
	}
	for _, name := range g.shared {
		l, found, err := client.VolumeLabels(name)
		if err != nil {
			return s, err
		}
		if !found {
			continue
		}
		if l["sbx.isolation"] == "" && l["sbx.namespace"] == "" && l["sbx.owner"] == "" {
			s.LegacyVolumes = append(s.LegacyVolumes, name)
			continue
		}
		for k, value := range s.Labels() {
			if l[k] != value {
				s.Reason = "shared volume ownership mismatch: " + name + "; restore into the correct dedicated daemon; credentials were not changed"
				return s, reject("%s", s.Reason)
			}
		}
	}
	if !s.Claimed && len(s.LegacyVolumes) > 0 {
		s.Reason = "legacy shared volumes require explicit adoption; review `sbx isolation status`, then run `sbx isolation adopt` on this user's dedicated daemon"
		return s, reject("%s", s.Reason)
	}
	s.Allowed = true
	return s, nil
}

func (g *Guard) Check(ctx context.Context) error { _, err := g.Status(ctx); return err }

func (g *Guard) claim(ctx context.Context, s Status) (Status, error) {
	labels := s.Labels()
	labels["sbx.kind"] = "ownership"
	labels["sbx.retention"] = "manual"
	if _, err := g.Docker.WithContext(ctx).VolumeCreate(g.marker, labels); err != nil {
		return s, err
	}
	// A concurrent owner may have created the same volume: verify the actual
	// immutable labels instead of trusting our create arguments.
	return g.Status(ctx)
}

func (g *Guard) Ensure(ctx context.Context) (Status, error) {
	s, err := g.Status(ctx)
	if err != nil || s.Claimed {
		return s, err
	}
	return g.claim(ctx, s)
}

// Adopt records explicit ownership without reading, copying, relabeling or
// deleting any existing credential/cache volume.
func (g *Guard) Adopt(ctx context.Context) (Status, error) {
	s, err := g.Status(ctx)
	if err != nil && !(IsRejection(err) && !s.Claimed && len(s.LegacyVolumes) > 0 && strings.HasPrefix(s.Reason, "legacy shared volumes")) {
		return s, err
	}
	if s.Claimed {
		return s, nil
	}
	return g.claim(ctx, s)
}
