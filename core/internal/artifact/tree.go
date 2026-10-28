// Package artifact copies bounded, explicitly selected trees into private
// snapshots. It rejects selected symlinks and special files, and confines reads
// to the opened source root even if another process renames directories.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type Limits struct {
	Bytes     int64 `json:"bytes" toml:"bytes"`
	Files     int   `json:"files" toml:"files"`
	FileBytes int64 `json:"file_bytes" toml:"file_bytes"`
}

func DefaultLimits() Limits { return Limits{64 << 20, 10000, 16 << 20} }

func (l Limits) Validate() error {
	if l.Bytes <= 0 || l.Files <= 0 || l.FileBytes <= 0 || l.FileBytes > l.Bytes {
		return errors.New("invalid tree limits")
	}
	return nil
}

type File struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable,omitempty"`
}

type Manifest struct {
	Files  []File `json:"files"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

func Filter(m Manifest, exclude []string) Manifest {
	filtered := Manifest{Files: []File{}}
	for _, f := range m.Files {
		if !Reserved(f.Path) && !Selected(exclude, f.Path) {
			filtered.Files = append(filtered.Files, f)
			filtered.Bytes += f.Size
		}
	}
	sort.Slice(filtered.Files, func(i, j int) bool { return filtered.Files[i].Path < filtered.Files[j].Path })
	b, _ := json.Marshal(filtered.Files)
	sum := sha256.Sum256(b)
	filtered.Digest = hex.EncodeToString(sum[:])
	return filtered
}

// ValidateSelection accepts relative exact paths, directory prefixes, and glob
// components. ** matches zero or more components; no absolute or parent paths.
func ValidateSelection(patterns []string) error {
	if len(patterns) == 0 {
		return errors.New("an explicit path selection is required")
	}
	for _, p := range patterns {
		if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("invalid relative selection %q", p)
		}
		for _, part := range strings.Split(p, "/") {
			if part != "**" {
				if _, err := path.Match(part, "x"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func matches(pattern, name string) bool {
	if pattern == "." || pattern == name || strings.HasPrefix(name, pattern+"/") {
		return true
	}
	var match func([]string, []string) bool
	match = func(p, n []string) bool {
		if len(p) == 0 {
			return len(n) == 0
		}
		if p[0] == "**" {
			return match(p[1:], n) || len(n) > 0 && match(p, n[1:])
		}
		if len(n) == 0 {
			return false
		}
		ok, _ := path.Match(p[0], n[0])
		return ok && match(p[1:], n[1:])
	}
	return match(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func Selected(patterns []string, name string) bool {
	for _, p := range patterns {
		if matches(p, name) {
			return true
		}
	}
	return false
}

// Reserved paths cannot become task inputs or published artifacts. This is a
// fixed baseline; trusted policy can add exclusions, never remove this set.
func Reserved(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".git" || part == ".sbx" || part == ".ssh" || part == ".aws" || part == ".claude" || part == ".codex" || part == ".env" || strings.HasPrefix(part, ".env.") || part == "auth.json" || part == "credentials.json" {
			return true
		}
	}
	return false
}

// Scan hashes the selected tree through a directory descriptor. Nonblocking,
// no-follow opens prevent a raced FIFO or symlink from hanging/escaping reads.
func Scan(ctx context.Context, dir string, include, exclude []string, limits Limits) (Manifest, error) {
	return scan(ctx, dir, "", include, exclude, limits)
}

func scan(ctx context.Context, dir, dest string, include, exclude []string, limits Limits) (Manifest, error) {
	m := Manifest{Files: []File{}}
	if err := ValidateSelection(include); err != nil {
		return m, err
	}
	if len(exclude) > 0 {
		if err := ValidateSelection(exclude); err != nil {
			return m, err
		}
	}
	if err := limits.Validate(); err != nil {
		return m, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer r.Close()
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if Reserved(name) || Selected(exclude, name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !Selected(include, name) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink selected: %s", name)
		}
		f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		before, err := f.Stat()
		if err != nil {
			return err
		}
		if !before.Mode().IsRegular() {
			return fmt.Errorf("special file selected: %s", name)
		}
		if before.Size() > limits.FileBytes || len(m.Files) >= limits.Files || before.Size() > limits.Bytes-m.Bytes {
			return errors.New("tree exceeds limits")
		}
		h := sha256.New()
		var out *os.File
		var writer io.Writer = h
		if dest != "" {
			target := filepath.Join(dest, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if before.Mode()&0111 != 0 {
				mode = 0700
			}
			out, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			defer out.Close()
			writer = io.MultiWriter(h, out)
		}
		n, err := copyContext(ctx, writer, io.LimitReader(f, limits.FileBytes+1))
		if err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		current, err := r.Lstat(name)
		if err != nil {
			return err
		}
		if n != before.Size() || !os.SameFile(before, after) || !os.SameFile(after, current) || before.ModTime() != after.ModTime() || after.Size() != n {
			return fmt.Errorf("input changed while reading: %s", name)
		}
		if out != nil {
			if err := out.Sync(); err != nil {
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
		m.Bytes += n
		m.Files = append(m.Files, File{name, n, hex.EncodeToString(h.Sum(nil)), before.Mode()&0111 != 0})
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	b, _ := json.Marshal(m.Files)
	sum := sha256.Sum256(b)
	m.Digest = hex.EncodeToString(sum[:])
	return m, nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			written, e := w.Write(buf[:n])
			total += int64(written)
			if e != nil {
				return total, e
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Copy publishes a complete private copy only after two matching inventories.
// The caller must quiesce writers for collection; a double scan detects normal
// concurrent changes but is not a transaction against a malicious host writer.
func Copy(ctx context.Context, source, dest string, include, exclude []string, limits Limits) (Manifest, error) {
	if err := SeparatePaths(source, dest); err != nil {
		return Manifest{}, err
	}
	if _, err := os.Lstat(dest); err == nil {
		return Manifest{}, errors.New("destination exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".snapshot-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	m, err := scan(ctx, source, tmp, include, exclude, limits)
	if err != nil {
		return Manifest{}, err
	}
	check, err := Scan(ctx, source, include, exclude, limits)
	if err != nil {
		return Manifest{}, err
	}
	if check.Digest != m.Digest {
		return Manifest{}, errors.New("input changed during snapshot")
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := syncTree(tmp); err != nil {
		return Manifest{}, err
	}
	// Application callers hold the task/target lock. Recheck before committing
	// the unique snapshot name; host administrators are outside this boundary.
	if _, err := os.Lstat(dest); err == nil {
		return Manifest{}, errors.New("destination appeared during snapshot")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return Manifest{}, err
	}
	if err := syncDir(filepath.Dir(dest)); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func syncTree(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, name)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

// SeparatePaths rejects a snapshot destination within its input (and the
// reverse), resolving existing ancestors before creating any directory.
func SeparatePaths(source, dest string) error {
	src, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	src, err = filepath.Abs(src)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	ancestor := absolute
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return errors.New("no existing destination ancestor")
		}
		ancestor = next
	}
	real, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return err
	}
	suffix, err := filepath.Rel(ancestor, absolute)
	if err != nil {
		return err
	}
	target := filepath.Join(real, suffix)
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if inside(src, target) || inside(target, src) {
		return errors.New("snapshot source and destination overlap")
	}
	return nil
}
