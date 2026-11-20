package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sandx/internal/fsutil"
	"syscall"
	"time"
)

// FileRecallLedger is host-owned and kept outside the agent's mounted workspace.
// Atomic replace + parent fsync establishes reservation durability before use.
type FileRecallLedger struct {
	Path            string
	Binding         string
	RequireExisting bool
}

func (l FileRecallLedger) Update(ctx context.Context, fn func(*RecallHistory) error) error {
	// Remember the first pre-existing ancestor so new ledger directories are
	// durable too, not only the replacement file's immediate parent.
	ancestor := filepath.Dir(l.Path)
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if !info.IsDir() {
				return errors.New("invalid recall directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		ancestor = filepath.Dir(ancestor)
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	h := RecallHistory{Schema: "sbx.memory-recalls/v1", Records: []RecallRecord{}}
	b, err := ReadFile(l.Path, 4<<20)
	if err == nil {
		if err = json.Unmarshal(b, &h); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) || l.RequireExisting {
		return err
	}
	if len(b) == 0 {
		h.Binding = l.Binding
	}
	if h.Binding != l.Binding {
		return errors.New("recall ledger binding changed")
	}
	if h.Schema != "sbx.memory-recalls/v1" || len(h.Records) > RecallMaxCalls {
		return errors.New("invalid recall ledger")
	}
	if err = fn(&h); err != nil {
		return err
	}
	b, err = json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errors.New("recall ledger exceeds limit")
	}
	if err = fsutil.AtomicWrite(l.Path, b, 0600); err != nil {
		return err
	}
	for dir := filepath.Dir(l.Path); ; dir = filepath.Dir(dir) {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return err
		}
		if dir == ancestor {
			break
		}
	}
	return nil
}

// ReadRecallHistory reads an atomic persisted view without creating files or
// requiring project credentials. A missing file means no calls were admitted.
func ReadRecallHistory(path string) (RecallHistory, error) {
	h := RecallHistory{Schema: "sbx.memory-recalls/v1", Records: []RecallRecord{}}
	b, err := ReadFile(path, 4<<20)
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return h, err
	}
	if err = json.Unmarshal(b, &h); err != nil {
		return h, err
	}
	if h.Schema != "sbx.memory-recalls/v1" || len(h.Records) > RecallMaxCalls {
		return h, errors.New("invalid recall ledger")
	}
	for _, r := range h.Records {
		if r.Digest != recallDigest(r) {
			return h, errors.New("invalid recall record digest")
		}
	}
	return h, nil
}

// Initialize is called only when the host starts a new conversation.
func (l FileRecallLedger) Initialize(ctx context.Context) error {
	l.RequireExisting = false
	return l.Update(ctx, func(h *RecallHistory) error {
		if len(h.Records) != 0 {
			return errors.New("recall session already used")
		}
		return nil
	})
}
