package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock serializes task mutations across CLI processes. Keep lock files: unlinking
// an advisory lock can create two independently locked inodes for the same task.
func (d *RuntimeDriver) Lock(req Request) (func(), error) {
	dir := filepath.Join(req.Task.Home, "locks", req.Task.WS.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, req.Task.Name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("task %s has another execution in progress: %w", req.Task.Name, err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
