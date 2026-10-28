package sink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/artifact"
)

func fixture(t *testing.T) (string, artifact.Manifest) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := artifact.Scan(context.Background(), root, []string{"."}, nil, artifact.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return root, m
}

func TestLocalPublishQueryConflictAndReplacement(t *testing.T) {
	ctx := context.Background()
	root, m := fixture(t)
	target := filepath.Join(t.TempDir(), "published")
	sel := Selection{Kind: "local_directory", TargetRef: "reports", DeliveryKey: "report-001"}
	g := Grant{Kind: sel.Kind, Path: target}
	r, err := Publish(ctx, root, m, "bound-artifact", sel, g, artifact.DefaultLimits())
	if err != nil || r.State != "delivered" {
		t.Fatalf("%+v %v", r, err)
	}
	// Discard the publishing response and recover from the target receipt.
	queried, err := Query(ctx, m, "bound-artifact", sel, g, artifact.DefaultLimits())
	if err != nil || queried.State != "delivered" {
		t.Fatalf("%+v %v", queried, err)
	}
	if _, err := Publish(ctx, root, m, "bound-artifact", sel, g, artifact.DefaultLimits()); err != nil {
		t.Fatal("same logical delivery was not idempotent:", err)
	}
	other := sel
	other.DeliveryKey = "different"
	if _, err := Publish(ctx, root, m, "other-artifact", other, g, artifact.DefaultLimits()); err == nil {
		t.Fatal("overwrote conflicting target")
	}
	other.Conflict = "replace"
	if _, err := Publish(ctx, root, m, "other-artifact", other, g, artifact.DefaultLimits()); err == nil {
		t.Fatal("self-authorized replacement")
	}
	g.AllowReplace = true
	if _, err := Publish(ctx, root, m, "other-artifact", other, g, artifact.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target + ".sbx-backup-different"); err != nil {
		t.Fatal("lost replaced target backup:", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sub", "report.txt"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Query(ctx, m, "other-artifact", other, g, artifact.DefaultLimits()); err == nil {
		t.Fatal("trusted receipt without checking content")
	}
}

func TestLocalResumeAfterTargetMovedToBackup(t *testing.T) {
	ctx := context.Background()
	root, m := fixture(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "old"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	sel := Selection{Kind: "local_directory", TargetRef: "reports", DeliveryKey: "key", Conflict: "replace"}
	g := Grant{Kind: sel.Kind, Path: target, AllowReplace: true}
	r := Receipt{Key: sel.DeliveryKey, ArtifactDigest: "artifact", TargetBinding: Binding(sel, g)}
	stage := target + ".sbx-stage-key"
	if _, err := artifact.Copy(ctx, root, stage, []string{"."}, nil, artifact.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := writeReceipt(stage, r); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+".sbx-backup-key"); err != nil {
		t.Fatal(err)
	}
	got, err := Publish(ctx, root, m, "artifact", sel, g, artifact.DefaultLimits())
	if err != nil || got.State != "delivered" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(target + ".sbx-backup-key/old"); err != nil {
		t.Fatal("recovery deleted previous target")
	}
}

func TestDeliveryKeyCannotBeReboundAfterTargetDeletion(t *testing.T) {
	ctx := context.Background()
	root, m := fixture(t)
	target := filepath.Join(t.TempDir(), "target")
	sel := Selection{Kind: "local_directory", TargetRef: "reports", DeliveryKey: "same", Conflict: "replace"}
	g := Grant{Kind: sel.Kind, Path: target, AllowReplace: true}
	if _, err := Publish(ctx, root, m, "original", sel, g, artifact.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(ctx, root, m, "different", sel, g, artifact.DefaultLimits()); err == nil {
		t.Fatal("rebound a historical delivery key")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("published after rebinding rejection")
	}
}

func TestGitLocalCommitIdempotencyAndNoCheckoutChanges(t *testing.T) {
	ctx := context.Background()
	root, m := fixture(t)
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	sel := Selection{Kind: "git_local", TargetRef: "review", DeliveryKey: "report"}
	g := Grant{Kind: sel.Kind, Path: repo, Branch: "refs/heads/sbx/report"}
	r, err := Publish(ctx, root, m, "artifact", sel, g, artifact.DefaultLimits())
	if err != nil || r.State != "delivered" || r.ObjectRef == "" {
		t.Fatalf("%+v %v", r, err)
	}
	second, err := Publish(ctx, root, m, "artifact", sel, g, artifact.DefaultLimits())
	if err != nil || second.ObjectRef != r.ObjectRef {
		t.Fatalf("duplicated commit: %+v %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", "report.txt")); !os.IsNotExist(err) {
		t.Fatal("modified target checkout")
	}
	message, err := gitCommand(ctx, repo, nil, 8192, "show", "-s", "--format=%B", r.ObjectRef)
	if err != nil || !strings.Contains(string(message), "artifact") {
		t.Fatalf("missing publication record: %s %v", message, err)
	}
	conflict := sel
	conflict.DeliveryKey = "new-key"
	if _, err := Publish(ctx, root, m, "different-artifact", conflict, g, artifact.DefaultLimits()); err == nil {
		t.Fatal("replaced branch without authorization")
	}
	if _, err := Query(ctx, m, "artifact", sel, g, artifact.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}
