package toolgate

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sandx/internal/external"
	"sync"
	"syscall"
)

const maxJournalBytes = 16 << 20

type line struct {
	Sequence int    `json:"sequence"`
	Audit    Audit  `json:"audit"`
	Digest   string `json:"digest"`
}
type Journal struct {
	mu       sync.Mutex
	file     *os.File
	sequence int
	size     int64
	failed   bool
}

func NewJournal(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = parent.Sync()
		parent.Close()
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Journal{file: f}, nil
}
func (j *Journal) Append(a Audit) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failed {
		return errors.New("tool audit journal is unavailable")
	}
	entry := line{Sequence: j.sequence + 1, Audit: a, Digest: external.Digest(a)}
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if j.size+int64(len(b)) > maxJournalBytes {
		j.failed = true
		return errors.New("tool audit journal limit exceeded")
	}
	n, err := j.file.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		j.failed = true
		return err
	}
	j.sequence++
	j.size += int64(n)
	return nil
}
func (j *Journal) Close() error { j.mu.Lock(); defer j.mu.Unlock(); return j.file.Close() }
func ReadJournal(path string) ([]Audit, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJournalBytes {
		return nil, errors.New("invalid tool journal")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, maxJournalBytes+1))
	scanner.Buffer(make([]byte, 4096), 8192)
	var audits []Audit
	for scanner.Scan() {
		var entry line
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.Sequence != len(audits)+1 || entry.Digest != external.Digest(entry.Audit) {
			return nil, errors.New("invalid tool journal entry")
		}
		audits = append(audits, entry.Audit)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// A crash during the final append must remain visible, not silently complete.
	if info.Size() > 0 {
		b := []byte{0}
		if _, err := f.ReadAt(b, info.Size()-1); err != nil || b[0] != '\n' {
			return nil, errors.New("incomplete tool journal")
		}
	}
	return audits, nil
}
