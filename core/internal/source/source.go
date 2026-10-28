// Package source prepares isolated input copies and owns the Git-specific
// preparation adapter. Paths here are already resolved by trusted policy.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"sandx/internal/artifact"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

type Selection struct {
	Kind        string   `json:"kind"`
	Ref         string   `json:"ref,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Include     []string `json:"include,omitempty"`
	WorkingTree bool     `json:"working_tree,omitempty"`
	Untracked   bool     `json:"untracked,omitempty"`
}

type Grant struct {
	Kind             string   `toml:"kind"`
	Path             string   `toml:"path"`
	Include          []string `toml:"include"`
	Exclude          []string `toml:"exclude"`
	AllowWorkingTree bool     `toml:"allow_working_tree"`
	AllowUntracked   bool     `toml:"allow_untracked"`
}

type Prepared struct {
	Selection Selection         `json:"selection"`
	Revision  string            `json:"revision,omitempty"`
	Baseline  artifact.Manifest `json:"baseline"`
	Binding   string            `json:"binding,omitempty"`
}

func GrantBinding(grant Grant) (string, error) {
	if !filepath.IsAbs(grant.Path) {
		return "", errors.New("source path must be absolute")
	}
	real, err := filepath.EvalSymlinks(grant.Path)
	if err != nil {
		return "", err
	}
	grant.Path = real
	b, _ := json.Marshal(grant)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Prepare makes a new copy; it never reuses or deletes a preexisting workspace.
func Prepare(ctx context.Context, sel Selection, grant Grant, dest string, limits artifact.Limits) (Prepared, error) {
	p := Prepared{Selection: sel}
	if err := limits.Validate(); err != nil {
		return p, err
	}
	if sel.Kind == "" || sel.Kind == "empty" {
		if sel.Ref != "" || sel.Revision != "" || sel.WorkingTree || sel.Untracked || len(sel.Include) > 0 {
			return p, errors.New("empty source cannot select input")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return p, err
		}
		if err := os.Mkdir(dest, 0700); err != nil {
			return p, err
		}
		m, err := artifact.Scan(ctx, dest, []string{"."}, nil, limits)
		if err != nil {
			os.Remove(dest)
			return p, err
		}
		p.Baseline = m
		return p, nil
	}
	if sel.Ref == "" || sel.Kind != grant.Kind || !filepath.IsAbs(grant.Path) {
		return p, errors.New("source is not authorized")
	}
	if real, err := filepath.EvalSymlinks(grant.Path); err == nil {
		grant.Path = real
	} else {
		return p, err
	}
	binding, err := GrantBinding(grant)
	if err != nil {
		return p, err
	}
	p.Binding = binding
	if err := artifact.SeparatePaths(grant.Path, dest); err != nil {
		return p, err
	}
	if sel.Kind == "files" {
		info, err := os.Lstat(grant.Path)
		if err != nil {
			return p, err
		}
		if info.Mode().IsRegular() {
			name := filepath.Base(grant.Path)
			if len(grant.Include) > 0 && !artifact.Selected(grant.Include, name) {
				return p, errors.New("registered file is outside the source selection")
			}
			grant.Path = filepath.Dir(grant.Path)
			grant.Include = []string{strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]").Replace(name)}
		} else if !info.IsDir() {
			return p, errors.New("file source is a link or special file")
		}
	}
	include := sel.Include
	if len(include) == 0 {
		include = grant.Include
	}
	if err := artifact.ValidateSelection(include); err != nil {
		return p, err
	}
	// The registered selection is the authorization ceiling. Copying its full
	// tree first prevents a task glob from expanding access beyond that ceiling.
	if err := artifact.ValidateSelection(grant.Include); err != nil {
		return p, err
	}
	if sel.WorkingTree && !grant.AllowWorkingTree || sel.Untracked && !grant.AllowUntracked {
		return p, errors.New("working input not authorized")
	}
	if sel.Untracked && !sel.WorkingTree {
		return p, errors.New("untracked selection requires explicit working_tree")
	}
	if sel.Kind == "files" && (sel.Revision != "" || sel.WorkingTree || sel.Untracked) {
		return p, errors.New("files source cannot select Git options")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return p, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".source-")
	if err != nil {
		return p, err
	}
	defer os.RemoveAll(tmp)
	input := grant.Path
	switch sel.Kind {
	case "files":
	case "git":
		ref := sel.Revision
		if ref == "" {
			ref = "HEAD"
		}
		if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\n\r") {
			return p, errors.New("invalid revision")
		}
		sha, err := gitText(ctx, grant.Path, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err != nil {
			return p, err
		}
		p.Revision = sha
		input = filepath.Join(tmp, "revision")
		if err := os.Mkdir(input, 0700); err != nil {
			return p, err
		}
		if err := exportRevision(ctx, grant.Path, sha, input, grant, limits); err != nil {
			return p, err
		}
		if sel.WorkingTree {
			head, err := gitText(ctx, grant.Path, "rev-parse", "HEAD")
			if err != nil {
				return p, err
			}
			if head != sha {
				return p, errors.New("working_tree requires the selected revision to equal HEAD")
			}
			if err := overlayWorking(ctx, grant.Path, input, sel.Untracked, grant, limits); err != nil {
				return p, err
			}
		}
	default:
		return p, errors.New("unsupported source kind")
	}
	ceiling := filepath.Join(tmp, "authorized")
	if _, err := artifact.Copy(ctx, input, ceiling, grant.Include, grant.Exclude, limits); err != nil {
		return p, err
	}
	m, err := artifact.Copy(ctx, ceiling, dest, include, grant.Exclude, limits)
	if err != nil {
		return p, err
	}
	if sel.Kind == "git" && sel.WorkingTree {
		head, err := gitText(ctx, grant.Path, "rev-parse", "HEAD")
		if err != nil || head != p.Revision {
			os.RemoveAll(dest)
			return p, errors.New("Git HEAD changed during working input preparation")
		}
	}
	p.Baseline = m
	return p, nil
}

type boundedOutput struct {
	bytes.Buffer
	left int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.left {
		return 0, errors.New("Git inventory exceeds limit")
	}
	b.left -= len(p)
	return b.Buffer.Write(p)
}
func gitText(ctx context.Context, repo string, args ...string) (string, error) {
	b, err := gitBytes(ctx, repo, 4096, args...)
	return strings.TrimSpace(string(b)), err
}

func gitBytes(ctx context.Context, repo string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out := &boundedOutput{left: limit}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return out.Bytes(), nil
}

func exportRevision(ctx context.Context, repo, sha, dest string, g Grant, limits artifact.Limits) error {
	inventory, err := gitBytes(ctx, repo, 16<<20, "ls-tree", "-rlz", sha)
	if err != nil {
		return err
	}
	var total int64
	count := 0
	for _, entry := range bytes.Split(inventory, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(entry), "\t")
		if !ok {
			return errors.New("invalid Git tree")
		}
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			return errors.New("unsafe Git path")
		}
		if artifact.Reserved(name) || artifact.Selected(g.Exclude, name) || !artifact.Selected(g.Include, name) {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 4 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return errors.New("selected Git links and submodules require a separate supported source")
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || size > limits.FileBytes || size > limits.Bytes-total || count >= limits.Files {
			return errors.New("Git input exceeds limits")
		}
		data, err := gitBytes(ctx, repo, int(size)+1, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		if int64(len(data)) != size {
			return errors.New("Git object size changed")
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if fields[0] == "100755" {
			mode = 0700
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return err
		}
		total += size
		count++
	}
	return nil
}

func overlayWorking(ctx context.Context, repo, dest string, untracked bool, g Grant, limits artifact.Limits) error {
	trackedBytes, err := gitBytes(ctx, repo, 16<<20, "ls-files", "-z", "--cached")
	tracked := string(trackedBytes)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	var trackedSelection []string
	if untracked {
		extra, err := gitBytes(ctx, repo, 16<<20, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return err
		}
		tracked += string(extra)
	}
	for _, name := range strings.Split(tracked, "\x00") {
		if name != "" {
			set[name] = true
			if artifact.Selected(g.Include, name) && !artifact.Reserved(name) && !artifact.Selected(g.Exclude, name) {
				literal := strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]").Replace(name)
				trackedSelection = append(trackedSelection, literal)
			}
		}
	}
	// Snapshot the working tree before using its bytes; never copy directly into
	// a published workspace while a source might still be changing.
	copyDir := dest + "-working"
	defer os.RemoveAll(copyDir)
	include := trackedSelection
	var m artifact.Manifest
	if len(include) == 0 {
		err = os.Mkdir(copyDir, 0700)
	} else {
		m, err = artifact.Copy(ctx, repo, copyDir, include, g.Exclude, limits)
	}
	if err != nil {
		return err
	}
	// Remove deleted tracked files from the revision export.
	for name := range set {
		if artifact.Reserved(name) || !artifact.Selected(g.Include, name) || artifact.Selected(g.Exclude, name) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(copyDir, filepath.FromSlash(name))); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(filepath.Join(dest, filepath.FromSlash(name))); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, entry := range m.Files {
		if !untracked && !set[entry.Path] {
			continue
		}
		from := filepath.Join(copyDir, filepath.FromSlash(entry.Path))
		to := filepath.Join(dest, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			return err
		}
		if err := os.Remove(to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

// CheckResume re-resolves the authorized input into a disposable copy and
// rejects revision/content changes. The retained execution workspace is never
// overwritten by this comparison.
func CheckResume(ctx context.Context, old Prepared, g Grant, scratch string, limits artifact.Limits) error {
	next, err := Prepare(ctx, old.Selection, g, scratch, limits)
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if next.Revision != old.Revision || next.Baseline.Digest != old.Baseline.Digest || next.Binding != old.Binding {
		return errors.New("source changed; create a new task with an explicit input selection")
	}
	return nil
}

// PrepareLegacy preserves the original main/worktree and branch semantics.
func PrepareLegacy(ctx context.Context, t task.Task, base string, logf func(string, ...any)) (string, error) {
	if t.IsMain() {
		if base != "" {
			logf("the main task uses the repo root directly; ignoring --base")
		}
		return t.WS.HeadRefContext(ctx)
	}
	ref := base
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := workspace.GitContext(ctx, t.WS.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("--base %q is not a valid commit: %w", ref, err)
	}
	created, note, err := t.WS.EnsureWorktreeContext(ctx, t.Worktree(), t.Branch(), sha)
	if err != nil {
		return "", err
	}
	if note != "" {
		logf("%s", note)
	}
	if created {
		logf("worktree: %s (branch %s)", t.Worktree(), t.Branch())
	}
	if note != "" || !created {
		if mb, err := workspace.GitContext(ctx, t.WS.Root, "merge-base", sha, t.Branch()); err == nil {
			sha = mb
		}
	}
	return sha, nil
}
