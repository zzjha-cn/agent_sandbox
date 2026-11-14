package projectmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"syscall"

	"sandx/internal/artifact"
)

func ValidateWatch(paths []string) error {
	if len(paths) > 16 {
		return errors.New("at most 16 memory watch files are allowed")
	}
	for _, name := range paths {
		if name == "" || len(name) > 240 || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") || artifact.Reserved(name) && name != ".sbx/sandbox.toml" && name != ".sbx/prep.Dockerfile" {
			return errors.New("memory watch requires non-secret relative files within the input root")
		}
	}
	return nil
}

// ObserveFiles reads only explicit host-selected paths, confined by os.Root.
// Missing files produce unknown conditions; only digests leave this function.
func ObserveFiles(root string, paths []string, facts map[string]string) (map[string]string, error) {
	if err := ValidateFacts(facts); err != nil {
		return nil, err
	}
	if err := ValidateWatch(paths); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range facts {
		if strings.HasPrefix(k, "file:") {
			return nil, errors.New("file facts must be observed using watch, not asserted")
		}
		out[k] = v
	}
	if len(paths) == 0 {
		return out, nil
	}
	r, err := os.OpenRoot(root)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, errors.New("cannot open memory observation root")
	}
	defer r.Close()
	for _, name := range paths {
		// Reject symlink components. os.Root additionally confines rename races.
		prefix := ""
		for _, part := range strings.Split(name, "/") {
			prefix = path.Join(prefix, part)
			st, e := r.Lstat(prefix)
			if os.IsNotExist(e) {
				break
			}
			if e != nil || st.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("memory watch symlinks or inaccessible paths are forbidden")
			}
		}
		expected, e := r.Lstat(name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, errors.New("cannot inspect memory watch file")
		}
		f, e := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, errors.New("cannot read memory watch file")
		}
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || !os.SameFile(expected, st) || st.Size() > 1<<20 {
			f.Close()
			return nil, errors.New("memory watch requires stable regular files of at most 1 MiB")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, (1<<20)+1))
		after, statErr := f.Stat()
		f.Close()
		if e != nil || n > 1<<20 || statErr != nil || n != st.Size() || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
			return nil, errors.New("memory watch file changed or exceeded budget")
		}
		out["file:"+name] = "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	if err = ValidateFacts(out); err != nil {
		return nil, err
	}
	return out, nil
}
