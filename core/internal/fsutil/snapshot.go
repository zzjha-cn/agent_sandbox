package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SnapshotPath captures a reserved state path in memory and returns a restore
// function. Existing regular files are restored atomically; symlinks are refused.
func SnapshotPath(path string) (func() error, error) {
	type entry struct {
		name string
		mode os.FileMode
		data []byte
	}
	var entries []entry
	_, err := os.Lstat(path)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	if !missing {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			stat, e := d.Info()
			if e != nil {
				return e
			}
			if !stat.IsDir() && !stat.Mode().IsRegular() {
				return fmt.Errorf("cannot snapshot non-regular state: %s", p)
			}
			rel, e := filepath.Rel(path, p)
			if e != nil {
				return e
			}
			item := entry{name: rel, mode: stat.Mode()}
			if stat.Mode().IsRegular() {
				item.data, e = os.ReadFile(p)
				if e != nil {
					return e
				}
			}
			entries = append(entries, item)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return func() error {
		// Restore an existing file via rename, so a failed write never first
		// deletes the only old copy. Remove newly generated files within a
		// reserved directory, then restore its previous contents individually.
		if missing {
			return os.RemoveAll(path)
		}
		if current, e := os.Lstat(path); e == nil && current.IsDir() != entries[0].mode.IsDir() {
			if e := os.RemoveAll(path); e != nil {
				return e
			}
		} else if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return e
		}
		if entries[0].mode.IsDir() {
			previous := map[string]os.FileMode{}
			for _, item := range entries {
				previous[item.name] = item.mode
			}
			if e := filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
				if errors.Is(e, fs.ErrNotExist) {
					return nil
				}
				if e != nil {
					return e
				}
				rel, e := filepath.Rel(path, p)
				if e != nil {
					return e
				}
				if mode, existed := previous[rel]; existed && d.Type()&os.ModeSymlink == 0 && mode.IsDir() == d.IsDir() {
					return nil
				}
				if e := os.RemoveAll(p); e != nil {
					return e
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}); e != nil {
				return e
			}
		}
		var failures []error
		for _, item := range entries {
			p := filepath.Join(path, item.name)
			var e error
			if item.mode.IsDir() {
				e = os.MkdirAll(p, item.mode.Perm())
				if e == nil {
					e = os.Chmod(p, item.mode.Perm())
				}
			} else {
				e = AtomicWrite(p, item.data, item.mode.Perm())
			}
			if e != nil {
				failures = append(failures, e)
			}
		}
		return errors.Join(failures...)
	}, nil
}
