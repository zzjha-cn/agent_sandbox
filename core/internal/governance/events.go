package governance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sandx/internal/execution"
)

const TeamEventSchema = "sbx.team-event/v1"

// TeamEvent is a deliberate allowlist projection. No Result, raw task name,
// prompt, error text, endpoint, path, tool payload or credential can enter it.
type TeamEvent struct {
	SchemaVersion string              `json:"schema_version"`
	ID            string              `json:"id"`
	Organization  string              `json:"organization"`
	Subject       string              `json:"subject"`
	PolicyDigest  string              `json:"policy_digest"`
	TaskKey       string              `json:"task_key"`
	AttemptKey    string              `json:"attempt_key"`
	Time          time.Time           `json:"time"`
	Type          execution.EventType `json:"type"`
	Phase         string              `json:"phase,omitempty"`
	Outcome       string              `json:"outcome,omitempty"`
}
type Queue struct {
	Home     string
	Snapshot Snapshot
	Scope    string
}

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Subject() string      { host, _ := os.Hostname(); return hash(strconv.Itoa(os.Getuid()) + "@" + host) }

var eventID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (q Queue) dir() string { return filepath.Join(q.Home, "team-events") }
func privateDir(name string) error {
	if err := os.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	i, e := os.Lstat(name)
	if e != nil {
		return e
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || i.Mode().Perm()&0077 != 0 {
		return errors.New("team event directory must be private and operator-owned")
	}
	return nil
}
func (q Queue) Emit(_ context.Context, e execution.Event) error {
	if !q.Snapshot.Managed {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	record := TeamEvent{SchemaVersion: TeamEventSchema, ID: hex.EncodeToString(id[:]), Organization: q.Snapshot.Policy.Organization, Subject: Subject(), PolicyDigest: q.Snapshot.Digest, TaskKey: hash(q.Home + "\x00" + q.Scope + "\x00" + e.TaskID), AttemptKey: hash(q.Home + "\x00" + q.Scope + "\x00" + e.AttemptID), Time: e.Time, Type: e.Type}
	switch execution.Phase(e.Phase) {
	case execution.AdmissionPhase, execution.PreparePhase, execution.ExecutePhase, execution.CollectPhase, execution.EvaluatePhase, execution.DeliverPhase, execution.FinalizePhase:
		record.Phase = e.Phase
	}
	if e.Result != nil {
		record.Outcome = string(e.Result.ExecutionState)
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(q.Home, 0700); err != nil {
		return err
	}
	if err := privateDir(q.dir()); err != nil {
		return err
	}
	entries, err := os.ReadDir(q.dir())
	if err != nil {
		return err
	}
	if len(entries) >= 10000 {
		return errors.New("team event outbox full; flush before running more tasks")
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(q.dir(), ".pending-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, filepath.Join(q.dir(), record.ID+".json")); err != nil {
		return err
	}
	return syncDir(q.dir())
}
func syncDir(name string) error {
	f, e := os.Open(name)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (e TeamEvent) Validate() error {
	if e.SchemaVersion != TeamEventSchema || !eventID.MatchString(e.ID) || !digest.MatchString(e.Subject) || !digest.MatchString(e.PolicyDigest) || !digest.MatchString(e.TaskKey) || !digest.MatchString(e.AttemptKey) || e.Time.IsZero() || e.Organization == "" || len(e.Organization) > 80 {
		return errors.New("invalid team event identity")
	}
	switch e.Type {
	case execution.EventAdmitted, execution.EventPhaseStarted, execution.EventPhaseFinished, execution.EventAgentStarted, execution.EventAgentExited, execution.EventCompleted, execution.EventFailed:
	default:
		return errors.New("invalid team event type")
	}
	switch e.Phase {
	case "", "admission", "prepare", "execute", "collect", "evaluate", "deliver", "finalize":
	default:
		return errors.New("invalid team event phase")
	}
	switch execution.State(e.Outcome) {
	case "", execution.Ready, execution.Running, execution.Finished, execution.AgentFailed, execution.TimedOut, execution.Cancelled, execution.DeliveryFailed, execution.Rejected, execution.EnvironmentErr, execution.AgentStartErr, execution.InternalErr, execution.VerificationErr, execution.ReviewPending, execution.ArtifactDeliveryErr, execution.OutcomeUnknown:
	default:
		return errors.New("invalid team event outcome")
	}
	return nil
}
func readPrivate(name string, limit int64) ([]byte, error) {
	f, e := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil {
		return nil, e
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > limit {
		return nil, errors.New("team event file or credential must be private, bounded and operator-owned")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("team event file too large")
	}
	return b, e
}

type FlushResult struct {
	Sent    int `json:"sent"`
	Pending int `json:"pending"`
}

// Flush never follows redirects and never reads proxy credentials from the
// environment. A lost response retains the exact event ID for at-least-once retry.
func (q Queue) Flush(ctx context.Context, authorize func() error) (FlushResult, error) {
	out := FlushResult{}
	if !q.Snapshot.Managed || q.Snapshot.Policy.Events.Endpoint == "" {
		return out, errors.New("no organization collector configured")
	}
	if err := endpoint(q.Snapshot.Policy.Events.Endpoint); err != nil {
		return out, err
	}
	if _, err := os.Lstat(q.dir()); errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err := privateDir(q.dir()); err != nil {
		return out, err
	}
	lock, err := os.OpenFile(filepath.Join(q.dir(), ".flush.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return out, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return out, errors.New("event export already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entries, err := os.ReadDir(q.dir())
	if err != nil {
		return out, err
	}
	files := []string{}
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".json") {
			files = append(files, f.Name())
		}
	}
	out.Pending = len(files)
	if out.Pending == 0 {
		return out, nil
	}
	token, err := readPrivate(filepath.Join(q.Home, "team-events.token"), 4096)
	if err != nil {
		return out, errors.New("cannot read private team-events.token")
	}
	token = bytes.TrimSpace(token)
	if len(token) == 0 || bytes.ContainsAny(token, "\r\n") {
		return out, errors.New("invalid team event credential")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, name := range files {
		if authorize == nil {
			return out, errors.New("event export requires current organization authorization")
		}
		if err := authorize(); err != nil {
			return out, err
		}
		if !eventID.MatchString(strings.TrimSuffix(name, ".json")) {
			return out, errors.New("invalid event filename")
		}
		b, err := readPrivate(filepath.Join(q.dir(), name), 16<<10)
		if err != nil {
			return out, err
		}
		var e TeamEvent
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err = d.Decode(&e); err != nil {
			return out, errors.New("invalid queued event")
		}
		if d.Decode(new(any)) != io.EOF {
			return out, errors.New("trailing queued event data")
		}
		if err = e.Validate(); err != nil {
			return out, err
		}
		if name != e.ID+".json" || e.Organization != q.Snapshot.Policy.Organization || e.Subject != Subject() {
			return out, errors.New("event belongs to another organization or operator")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.Snapshot.Policy.Events.Endpoint, bytes.NewReader(b))
		if err != nil {
			return out, err
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", e.ID)
		resp, err := client.Do(req)
		if err != nil {
			return out, errors.New("team event delivery failed; queue retained")
		}
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return out, errors.New("collector did not acknowledge event; queue retained")
		}
		if err = os.Remove(filepath.Join(q.dir(), name)); err != nil {
			return out, err
		}
		if err = syncDir(q.dir()); err != nil {
			return out, err
		}
		out.Sent++
		out.Pending--
	}
	return out, nil
}

// Fanout durably queues before writing the public stream. Queue failures are
// returned to the execution service and cannot silently count as delivery.
type Fanout struct {
	Queue  execution.EventSink
	Public execution.EventSink
}

func (f Fanout) Emit(ctx context.Context, e execution.Event) error {
	err := f.Queue.Emit(ctx, e)
	if err != nil {
		err = &execution.RequiredEventError{Cause: err}
	}
	return errors.Join(err, f.Public.Emit(ctx, e))
}
