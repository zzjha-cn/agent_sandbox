// Package fsutil 提供文件写入工具。
//
// 凡是会被容器读取的文件都必须通过 AtomicWrite 写入：单文件 bind mount
// 在宿主机原地改写时，容器会读到截断的内容（M0-3）。
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite 在目标同目录下写临时文件、fsync，再 rename 覆盖目标。
// 父目录不存在时自动创建（0755）。
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func(e error) error {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("atomic write %s: %w", path, e)
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := f.Chmod(perm); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("atomic write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("atomic write %s: %w", path, err)
	}
	return nil
}
