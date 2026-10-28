package sink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"sandx/internal/artifact"
)

type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, errors.New("Git output exceeds limit")
	}
	b.remaining -= len(p)
	return b.Buffer.Write(p)
}

func gitCommand(ctx context.Context, repo string, input []byte, limit int, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Stdin = bytes.NewReader(input)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=sbx", "GIT_AUTHOR_EMAIL=sbx@localhost", "GIT_COMMITTER_NAME=sbx", "GIT_COMMITTER_EMAIL=sbx@localhost")
	out := &boundedBuffer{remaining: limit}
	cmd.Stdout = out
	cmd.Stderr = &boundedBuffer{remaining: 8192}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("local Git operation failed: %w", err)
	}
	return out.Bytes(), nil
}

func validateBranch(ctx context.Context, g Grant) error {
	if !strings.HasPrefix(g.Branch, "refs/heads/") {
		return errors.New("Git sink requires an explicit local refs/heads branch")
	}
	_, err := gitCommand(ctx, g.Path, nil, 4096, "check-ref-format", g.Branch)
	return err
}

func queryGit(ctx context.Context, m artifact.Manifest, r Receipt, g Grant) (Receipt, error) {
	r.State = "unknown"
	if err := validateBranch(ctx, g); err != nil {
		return r, err
	}
	ref, err := gitCommand(ctx, g.Path, nil, 4096, "rev-parse", "--verify", "--quiet", g.Branch)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return r, err
		}
		r.State = "absent"
		return r, nil
	}
	commit := strings.TrimSpace(string(ref))
	body, err := gitCommand(ctx, g.Path, nil, 16384, "show", "-s", "--format=%B", commit)
	if err != nil {
		return r, err
	}
	var receipt Receipt
	if err := json.Unmarshal(bytes.TrimSpace(body), &receipt); err != nil {
		return r, errors.New("branch belongs to a different local publication")
	}
	if receipt.Key != r.Key || receipt.ArtifactDigest != r.ArtifactDigest || receipt.TargetBinding != r.TargetBinding || receipt.State != "delivered" {
		return r, errors.New("branch belongs to a different delivery")
	}
	tree, err := gitCommand(ctx, g.Path, nil, 4<<20, "ls-tree", "-rz", commit)
	if err != nil {
		return r, err
	}
	entries := map[string]string{}
	for _, line := range bytes.Split(tree, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(line), "\t")
		parts := strings.Fields(header)
		if !ok || len(parts) != 3 || parts[1] != "blob" {
			return r, errors.New("invalid published Git tree")
		}
		entries[name] = parts[0] + " " + parts[2]
	}
	if len(entries) != len(m.Files) {
		return r, errors.New("published Git tree differs from artifact")
	}
	for _, f := range m.Files {
		entry, ok := entries[f.Path]
		if !ok {
			return r, errors.New("published file missing")
		}
		mode, oid, _ := strings.Cut(entry, " ")
		expectedMode := "100644"
		if f.Executable {
			expectedMode = "100755"
		}
		if mode != expectedMode {
			return r, errors.New("published mode differs")
		}
		blob, err := gitCommand(ctx, g.Path, nil, int(f.Size)+1, "cat-file", "blob", oid)
		if err != nil {
			return r, err
		}
		sum := sha256.Sum256(blob)
		if int64(len(blob)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
			return r, errors.New("published content differs")
		}
	}
	r.State = "delivered"
	r.ObjectRef = commit
	return r, nil
}

type gitTree struct {
	files map[string]string
	dirs  map[string]*gitTree
}

func newTree() *gitTree { return &gitTree{files: map[string]string{}, dirs: map[string]*gitTree{}} }

func publishGit(ctx context.Context, source string, m artifact.Manifest, r Receipt, sel Selection, g Grant, limits artifact.Limits) (Receipt, error) {
	if err := validateBranch(ctx, g); err != nil {
		return r, err
	}
	if queried, err := queryGit(ctx, m, r, g); err == nil && queried.State == "delivered" {
		return queried, nil
	}
	oldBytes, oldErr := gitCommand(ctx, g.Path, nil, 4096, "rev-parse", "--verify", "--quiet", g.Branch)
	old := strings.TrimSpace(string(oldBytes))
	if oldErr == nil && sel.Conflict != "replace" {
		return r, errors.New("local branch already exists")
	}
	actual, err := artifact.Scan(ctx, source, []string{"."}, nil, limits)
	if err != nil {
		return r, err
	}
	if actual.Digest != m.Digest {
		return r, errors.New("artifact changed")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return r, err
	}
	defer root.Close()
	tree := newTree()
	for _, f := range m.Files {
		b, err := root.ReadFile(f.Path)
		if err != nil {
			return r, err
		}
		sum := sha256.Sum256(b)
		if int64(len(b)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
			return r, errors.New("artifact changed while reading")
		}
		oid, err := gitCommand(ctx, g.Path, b, 4096, "hash-object", "-w", "--stdin")
		if err != nil {
			return r, err
		}
		node := tree
		parts := strings.Split(f.Path, "/")
		for _, dir := range parts[:len(parts)-1] {
			if node.dirs[dir] == nil {
				node.dirs[dir] = newTree()
			}
			node = node.dirs[dir]
		}
		mode := "100644"
		if f.Executable {
			mode = "100755"
		}
		node.files[parts[len(parts)-1]] = mode + " blob " + strings.TrimSpace(string(oid))
	}
	var writeTree func(*gitTree) (string, error)
	writeTree = func(node *gitTree) (string, error) {
		entries := map[string]string{}
		for name, entry := range node.files {
			entries[name] = entry
		}
		for name, dir := range node.dirs {
			oid, err := writeTree(dir)
			if err != nil {
				return "", err
			}
			entries[name] = "040000 tree " + oid
		}
		names := []string{}
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		var input bytes.Buffer
		for _, name := range names {
			fmt.Fprintf(&input, "%s\t%s%c", entries[name], name, 0)
		}
		out, err := gitCommand(ctx, g.Path, input.Bytes(), 4096, "mktree", "-z")
		return strings.TrimSpace(string(out)), err
	}
	treeID, err := writeTree(tree)
	if err != nil {
		return r, err
	}
	r.State = "delivered"
	message, _ := json.Marshal(r)
	args := []string{"commit-tree", treeID}
	if oldErr == nil {
		args = append(args, "-p", old)
	}
	commit, err := gitCommand(ctx, g.Path, append(message, '\n'), 4096, args...)
	if err != nil {
		r.State = "failed"
		return r, err
	}
	commitID := strings.TrimSpace(string(commit))
	// An empty old value requires an absent ref; an existing ref is replaced
	// only via compare-and-swap, never by a blind update or merge.
	if _, err := gitCommand(ctx, g.Path, nil, 4096, "update-ref", g.Branch, commitID, old); err != nil {
		r.State = "unknown"
		return r, &OutcomeError{Unknown: true, Cause: err}
	}
	queried, err := queryGit(ctx, m, r, g)
	if err != nil {
		r.State = "unknown"
		return r, &OutcomeError{Unknown: true, Cause: err}
	}
	return queried, nil
}
