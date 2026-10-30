package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"sandx/internal/external"
	"sandx/internal/task"
	"sandx/internal/workspace"
)

const RecordSchema = "sbx.execution-record/v2"
const MaxRecordBytes = 32 << 20

var (
	ErrRecordIncompatible = errors.New("v1 execution records are unsupported; preserve the old home and reconcile pending operations with the previous binary before using a new SBX_HOME")
	ErrRecordBusy         = errors.New("execution record is locked")
	ErrRecordInvalid      = errors.New("execution record is missing, invalid or incompatible")
	ErrRecoveryRequired   = errors.New("execution requires reconciliation before another run")
)

type AttemptRecord struct {
	Result          Result         `json:"result"`
	StartedAt       time.Time      `json:"started_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Closed          bool           `json:"closed"`
	ErrorKind       ErrorKind      `json:"error_kind,omitempty"`
	SecondaryErrors []ErrorKind    `json:"secondary_errors,omitempty"`
	Pipeline        *PipelineFacts `json:"pipeline,omitempty"`
}

// OperationRecord stores safe references, never a tool payload or credentials.
// G1 guards unresolved intents; concrete operation writers arrive with sinks/tools.
type OperationRecord struct {
	External   *external.Record `json:"external,omitempty"`
	Key        string           `json:"key"`
	AttemptID  string           `json:"attempt_id"`
	Kind       string           `json:"kind"`
	State      string           `json:"state"`
	ObjectRef  string           `json:"object_ref,omitempty"`
	TargetRef  string           `json:"target_ref,omitempty"`
	ReceiptRef string           `json:"receipt_ref,omitempty"`
}

type TaskRecord struct {
	SchemaVersion string            `json:"schema_version"`
	TaskKey       string            `json:"task_key"`
	Sequence      uint64            `json:"sequence"`
	Attempts      []AttemptRecord   `json:"attempts"`
	Operations    []OperationRecord `json:"operations,omitempty"`
}

// RecordStore is consumed by lifecycle orchestration and read-only status.
// Save requires the caller to hold Lock for the whole read/modify/write cycle.
type RecordStore interface {
	Lock() (func(), error)
	Load() (*TaskRecord, error)
	Save(context.Context, *TaskRecord) error
}

type FileStore struct{ dir, key string }

func NewFileStore(t task.Task) (*FileStore, error) {
	if t.ExecutionDir != "" || t.WS.ID == "tasks" {
		expected, err := task.NewExecution(t.Home, t.Name)
		if err != nil {
			return nil, err
		}
		if t.WS.ID != "tasks" || t.Worktree() != expected.Worktree() || t.WS.Root != expected.WS.Root || t.WS.GitDir != "" {
			return nil, errors.New("execution workspace identity does not match its task")
		}
	}
	if t.Home == "" || t.WS.ID == "" {
		return nil, fmt.Errorf("execution record requires a home and repository identity")
	}
	for _, name := range []string{t.WS.ID, t.Name} {
		if err := workspace.ValidateTaskName(name); err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(t.Home)
	if err != nil {
		return nil, err
	}
	return &FileStore{dir: filepath.Join(abs, "executions", t.WS.ID, t.Name), key: t.ID()}, nil
}

// checkPath rejects symlink/non-directory control paths without creating any.
func (s *FileStore) checkPath() error {
	for p := s.dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && !info.IsDir() {
			return ErrRecordInvalid
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// System ancestors such as /var may legitimately be symlinks on macOS.
		if p == filepath.Dir(filepath.Dir(filepath.Dir(s.dir))) {
			break
		}
	}
	return nil
}

func privateOpen(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = ErrRecordInvalid
		}
		return nil, err
	}
	return f, nil
}

func (s *FileStore) Lock() (func(), error) {
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	// Create each controlled directory privately, including intermediate ones.
	for _, dir := range []string{filepath.Dir(filepath.Dir(s.dir)), filepath.Dir(s.dir), s.dir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := syncDirectory(filepath.Dir(dir)); err != nil {
			return nil, err
		}
	}
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	f, err := privateOpen(filepath.Join(s.dir, "record.lock"), syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrRecordBusy
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

type diskRecord struct {
	Record json.RawMessage `json:"record"`
	SHA256 string          `json:"sha256"`
}

func strictDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrRecordInvalid
	}
	return nil
}

func readRegular(path string) ([]byte, error) {
	f, err := privateOpen(path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxRecordBytes {
		return nil, ErrRecordInvalid
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRecordBytes+1))
	if len(b) > MaxRecordBytes {
		return nil, ErrRecordInvalid
	}
	return b, err
}

func (s *FileStore) Load() (*TaskRecord, error) {
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	b, err := readRegular(filepath.Join(s.dir, "record.json"))
	if errors.Is(err, os.ErrNotExist) {
		_, markerErr := os.Lstat(filepath.Join(s.dir, "initialized"))
		if errors.Is(markerErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, ErrRecordInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read record", ErrRecordInvalid)
	}
	marker, err := readRegular(filepath.Join(s.dir, "initialized"))
	if err == nil && string(marker) == "sbx.execution-record/v1\n" {
		return nil, errors.Join(ErrRecordInvalid, ErrRecordIncompatible)
	}
	if err != nil || string(marker) != RecordSchema+"\n" {
		return nil, ErrRecordInvalid
	}
	var disk diskRecord
	if err := strictDecode(b, &disk); err != nil {
		return nil, ErrRecordInvalid
	}
	digest := sha256.Sum256(disk.Record)
	if hex.EncodeToString(digest[:]) != disk.SHA256 {
		return nil, ErrRecordInvalid
	}
	var r TaskRecord
	if err := strictDecode(disk.Record, &r); err != nil {
		return nil, ErrRecordInvalid
	}
	if err := s.validate(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *FileStore) validate(r *TaskRecord) error {
	if r.SchemaVersion != RecordSchema || r.TaskKey != s.key || r.Sequence == 0 || len(r.Attempts) == 0 {
		return ErrRecordInvalid
	}
	seen := map[string]bool{}
	for _, a := range r.Attempts {
		if a.Result.AttemptID == "" || seen[a.Result.AttemptID] || a.Result.TaskID == "" || a.StartedAt.IsZero() || a.UpdatedAt.Before(a.StartedAt) || len(a.Result.Stages) != len(initialStages()) {
			return ErrRecordInvalid
		}
		seen[a.Result.AttemptID] = true
		for i, stage := range a.Result.Stages {
			if stage.Phase != initialStages()[i].Phase {
				return ErrRecordInvalid
			}
			switch stage.State {
			case ProgressPending, ProgressRunning, ProgressSucceeded, ProgressFailed, ProgressSkipped, ProgressHandedOff, ProgressUnknown:
			default:
				return ErrRecordInvalid
			}
		}
	}
	for _, op := range r.Operations {
		if op.External != nil {
			e := op.External
			if op.Kind != "tool" || e.Validate() != nil || e.OperationID != op.Key || e.RequestDigest != op.ObjectRef || e.GrantBinding != op.TargetRef || e.OperationID != op.ReceiptRef {
				return ErrRecordInvalid
			}
			state := "unknown"
			switch e.State {
			case "intent":
				state = "intent"
			case "completed":
				state = "completed"
			case "rejected", "not_found":
				state = "failed"
			}
			if op.State != state {
				return ErrRecordInvalid
			}
		}
		if op.Key == "" || !seen[op.AttemptID] || (op.Kind != "tool" && op.Kind != "delivery") {
			return ErrRecordInvalid
		}
		switch op.State {
		case "intent", "completed", "failed", "unknown":
		default:
			return ErrRecordInvalid
		}
	}
	return nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *FileStore) Save(ctx context.Context, r *TaskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.Load()
	if err != nil {
		return err
	}
	if (current == nil && r.Sequence != 0) || (current != nil && current.Sequence != r.Sequence) {
		return ErrRecordInvalid
	}
	next := *r
	next.Sequence++
	if err := s.validate(&next); err != nil {
		return err
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	b, err := json.Marshal(diskRecord{Record: payload, SHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		return err
	}
	if len(b)+1 > MaxRecordBytes {
		return fmt.Errorf("execution record exceeds 32 MiB; preserve history and use a new task")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if current == nil {
		f, err := privateOpen(filepath.Join(s.dir, "initialized"), syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(RecordSchema + "\n")
		err = errors.Join(writeErr, f.Sync(), f.Close(), syncDirectory(s.dir))
		if err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(s.dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(b, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(s.dir, "record.json")); err != nil {
		return err
	}
	if err := syncDirectory(s.dir); err != nil {
		return err
	}
	r.Sequence = next.Sequence
	return nil
}
