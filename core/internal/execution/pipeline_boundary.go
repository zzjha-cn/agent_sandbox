package execution

import (
	"errors"
	"os"
	"path/filepath"
	"sandx/internal/source"
	"strings"
)

func plannedPath(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	ancestor := abs
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return "", errors.New("cannot resolve resource path")
		}
		ancestor = next
	}
	real, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	suffix, err := filepath.Rel(ancestor, abs)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, suffix), nil
}

func overlaps(a, b string) bool {
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(a, b) || inside(b, a)
}

// Registry authorization cannot turn the task's own journals, credentials or
// private artifact store into an input or a replaceable delivery directory.
func pipelineBoundary(req Request) error {
	var resources []string
	p := req.Pipeline
	if p.Spec.Source != nil {
		resources = append(resources, p.Policy.Sources[p.Spec.Source.Ref].Path)
	}
	if p.Spec.Sink != nil {
		resources = append(resources, p.Policy.Sinks[p.Spec.Sink.TargetRef].Path)
	}
	var protected []string
	for _, name := range []string{"executions", "tool-calls", "artifacts", "state", "locks", "workspaces", "worktrees", "proxy", "verifications", "config.toml", "execution-policy.toml"} {
		protected = append(protected, filepath.Join(req.Task.Home, name))
	}
	for _, provider := range req.Config.Agents {
		name := provider.APIKeyFile
		if name == "" {
			continue
		}
		if name == "~" || strings.HasPrefix(name, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			name = filepath.Join(home, strings.TrimPrefix(name, "~"))
		}
		protected = append(protected, name)
	}
	for _, g := range p.Policy.Tools {
		if g.CredentialFile != "" {
			protected = append(protected, g.CredentialFile)
			credential, err := plannedPath(g.CredentialFile)
			if err != nil {
				return err
			}
			for _, mounted := range []string{filepath.Join(req.Task.Home, "workspaces"), filepath.Join(req.Task.Home, "worktrees"), filepath.Join(req.Task.Home, "state"), filepath.Join(req.Task.Home, "proxy")} {
				mount, err := plannedPath(mounted)
				if err != nil {
					return err
				}
				if overlaps(credential, mount) {
					return errors.New("connector credential overlaps an agent-accessible mount")
				}
			}
		}
	}
	for _, resource := range resources {
		resolved, err := plannedPath(resource)
		if err != nil {
			return err
		}
		for _, private := range protected {
			private, err = plannedPath(private)
			if err != nil {
				return err
			}
			if overlaps(resolved, private) {
				return errors.New("registered source or sink overlaps private sbx state or credentials")
			}
		}
	}
	return nil
}

func (s *Service) authorizePrepared(req Request) error {
	p := req.Pipeline
	if err := p.refresh(); err != nil {
		return err
	}
	if err := pipelineBoundary(req); err != nil {
		return err
	}
	if s.facts == nil || s.facts.Prepared == nil {
		return errors.New("input preparation is not recorded")
	}
	if p.Spec.Source != nil {
		binding, err := source.GrantBinding(p.Policy.Sources[p.Spec.Source.Ref])
		if err != nil || binding != s.facts.Prepared.Binding {
			return errors.New("source authorization changed during preparation")
		}
	}
	m := s.facts.Prepared.Baseline
	limits := p.Policy.InputLimits
	if m.Bytes > limits.Bytes || len(m.Files) > limits.Files {
		return errors.New("prepared input exceeds current policy")
	}
	for _, f := range m.Files {
		if f.Size > limits.FileBytes {
			return errors.New("prepared file exceeds current policy")
		}
	}
	return nil
}
