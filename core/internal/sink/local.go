// Package sink publishes retained artifact snapshots. Publishing never starts
// an agent and callers must durably record the delivery intent first.
package sink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"sandx/internal/artifact"
)

type Selection struct {
	Kind        string `json:"kind"`
	TargetRef   string `json:"target_ref"`
	Conflict    string `json:"conflict,omitempty"`
	DeliveryKey string `json:"delivery_key"`
}

type Grant struct {
	Kind         string `toml:"kind" json:"kind"`
	Path         string `toml:"path" json:"-"`
	Branch       string `toml:"branch" json:"branch,omitempty"`
	AllowReplace bool   `toml:"allow_replace" json:"allow_replace"`
}

type Receipt struct {
	Key            string `json:"key"`
	ArtifactDigest string `json:"artifact_digest"`
	TargetBinding  string `json:"target_binding"`
	State          string `json:"state"`
	ObjectRef      string `json:"object_ref,omitempty"`
}

type OutcomeError struct {
	Unknown bool
	Cause   error
}

func (e *OutcomeError) Error() string { return e.Cause.Error() }
func (e *OutcomeError) Unwrap() error { return e.Cause }

func Binding(sel Selection, g Grant) string {
	if normalized, err := normalize(g); err == nil {
		g = normalized
	}
	b, _ := json.Marshal(struct{ Kind, Ref, Path, Branch string }{sel.Kind, sel.TargetRef, filepath.Clean(g.Path), g.Branch})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func normalize(g Grant) (Grant, error) {
	if !filepath.IsAbs(g.Path) {
		return g, errors.New("sink path must be absolute")
	}
	if g.Kind == "git_local" {
		real, err := filepath.EvalSymlinks(g.Path)
		g.Path = real
		return g, err
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(g.Path))
	if err != nil {
		return g, err
	}
	g.Path = filepath.Join(real, filepath.Base(g.Path))
	return g, nil
}

func valid(sel Selection, g Grant) error {
	if sel.Kind != "local_directory" && sel.Kind != "git_local" {
		return errors.New("unsupported sink kind")
	}
	if sel.Kind != g.Kind || !filepath.IsAbs(g.Path) || sel.DeliveryKey == "" || filepath.Base(sel.DeliveryKey) != sel.DeliveryKey || sel.DeliveryKey == "." || sel.DeliveryKey == ".." {
		return errors.New("invalid delivery authorization or identity")
	}
	if sel.Conflict != "" && sel.Conflict != "reject" && sel.Conflict != "replace" {
		return errors.New("invalid conflict policy")
	}
	if sel.Conflict == "replace" && !g.AllowReplace {
		return errors.New("replacement is not authorized")
	}
	return nil
}

func withTargetLock(ctx context.Context, g Grant) (*os.File, func(), error) {
	parent := filepath.Dir(g.Path)
	if g.Kind == "git_local" {
		out, err := gitCommand(ctx, g.Path, nil, 4096, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, nil, err
		}
		parent = strings.TrimSpace(string(out))
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, nil, errors.New("delivery parent must already exist")
	}
	sum := sha256.Sum256([]byte(g.Path + "\x00" + g.Branch))
	name := filepath.Join(parent, ".sbx-delivery-"+hex.EncodeToString(sum[:8])+".lock")
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, errors.New("invalid delivery lock")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, nil, errors.New("delivery target is busy")
	}
	return f, func() { syscall.Flock(fd, syscall.LOCK_UN); f.Close() }, nil
}

func syncDir(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Publish resumes only a matching staged transaction, under a target-wide lock.
// Ambiguous post-commit errors remain unknown until Query verifies the target.
func Publish(ctx context.Context, source string, m artifact.Manifest, artifactDigest string, sel Selection, g Grant, limits artifact.Limits) (Receipt, error) {
	r := Receipt{Key: sel.DeliveryKey, ArtifactDigest: artifactDigest, TargetBinding: Binding(sel, g), State: "failed"}
	if err := valid(sel, g); err != nil {
		return r, err
	}
	var err error
	g, err = normalize(g)
	if err != nil {
		return r, err
	}
	ledger, unlock, err := withTargetLock(ctx, g)
	if err != nil {
		return r, err
	}
	defer unlock()
	if g.Kind == "git_local" {
		if query, queryErr := queryGit(ctx, m, r, g); queryErr == nil && query.State == "delivered" {
			return query, nil
		}
		if err := bindDelivery(ledger, r); err != nil {
			return r, err
		}
		return publishGit(ctx, source, m, r, sel, g, limits)
	}
	if g.Kind != "local_directory" {
		return r, errors.New("unsupported sink kind")
	}
	if query, err := queryDirectory(ctx, m, r, g.Path, limits); err == nil && query.State == "delivered" {
		return query, nil
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := bindDelivery(ledger, r); err != nil {
		return r, err
	}
	target := g.Path
	stage := target + ".sbx-stage-" + sel.DeliveryKey
	backup := target + ".sbx-backup-" + sel.DeliveryKey
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return r, errors.New("target is not a directory")
		}
		if sel.Conflict != "replace" {
			return r, errors.New("delivery target already exists")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
		copied, err := artifact.Copy(ctx, source, stage, []string{"."}, nil, limits)
		if err != nil {
			return r, err
		}
		if copied.Digest != m.Digest {
			return r, errors.New("artifact changed during delivery")
		}
		if err := writeReceipt(stage, r); err != nil {
			return r, err
		}
	} else if err != nil {
		return r, err
	} else {
		if query, err := queryDirectory(ctx, m, r, stage, limits); err != nil || query.State != "delivered" {
			return r, errors.New("staging directory requires manual reconciliation")
		}
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if _, err := os.Lstat(target); err == nil {
		if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			return r, errors.New("replacement backup already exists; reconcile before publishing")
		}
		if err := os.Rename(target, backup); err != nil {
			return r, err
		}
		if err := syncDir(filepath.Dir(target)); err != nil {
			return r, &OutcomeError{Unknown: true, Cause: err}
		}
	}
	if err := os.Rename(stage, target); err != nil {
		return r, &OutcomeError{Unknown: true, Cause: err}
	}
	if err := syncDir(filepath.Dir(target)); err != nil {
		return r, &OutcomeError{Unknown: true, Cause: err}
	}
	query, err := queryDirectory(ctx, m, r, target, limits)
	if err != nil {
		return r, &OutcomeError{Unknown: true, Cause: err}
	}
	return query, nil
}

func writeReceipt(dir string, r Receipt) error {
	meta := filepath.Join(dir, ".sbx")
	if err := os.Mkdir(meta, 0700); err != nil {
		return err
	}
	r.State = "delivered"
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(meta, "receipt.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	return errors.Join(syncDir(meta), syncDir(dir))
}

func queryDirectory(ctx context.Context, m artifact.Manifest, expected Receipt, dir string, limits artifact.Limits) (Receipt, error) {
	r := expected
	r.State = "unknown"
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		r.State = "absent"
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if !info.IsDir() {
		return r, errors.New("target is not a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return r, err
	}
	defer root.Close()
	meta, err := root.Lstat(".sbx")
	if err != nil || !meta.IsDir() {
		return r, errors.New("target receipt is missing or invalid")
	}
	file, err := root.OpenFile(".sbx/receipt.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return r, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 8192 {
		return r, errors.New("invalid target receipt")
	}
	var receipt Receipt
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return r, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return r, errors.New("invalid trailing target receipt data")
	}
	if receipt.Key != r.Key || receipt.TargetBinding != r.TargetBinding || receipt.ArtifactDigest != r.ArtifactDigest || receipt.State != "delivered" {
		return r, errors.New("target belongs to a different delivery")
	}
	actual, err := artifact.Scan(ctx, dir, []string{"."}, nil, limits)
	if err != nil {
		return r, err
	}
	if actual.Digest != m.Digest {
		return r, errors.New("target content differs from delivery receipt")
	}
	r.State = "delivered"
	r.ObjectRef = m.Digest
	return r, nil
}

func Query(ctx context.Context, m artifact.Manifest, artifactDigest string, sel Selection, g Grant, limits artifact.Limits) (Receipt, error) {
	r := Receipt{Key: sel.DeliveryKey, ArtifactDigest: artifactDigest, TargetBinding: Binding(sel, g), State: "unknown"}
	if err := valid(sel, g); err != nil {
		return r, err
	}
	var err error
	g, err = normalize(g)
	if err != nil {
		return r, err
	}
	switch g.Kind {
	case "local_directory":
		return queryDirectory(ctx, m, r, g.Path, limits)
	case "git_local":
		return queryGit(ctx, m, r, g)
	default:
		return r, fmt.Errorf("unsupported sink kind")
	}
}
