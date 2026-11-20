// Package projectmemory owns reviewed, provider-independent project knowledge.
// It never reads provider transcripts or grants execution permissions.
package projectmemory

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"sandx/internal/fsutil"
)

const Schema = "sbx.project-memory/v1"
const MaxBytes = 16 << 10
const MaxEntries = 20
const maxStore = 8 << 20

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func ValidID(s string) bool { return validID.MatchString(s) }
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

type Input struct {
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	Content    string            `json:"content"`
	Paths      []string          `json:"paths,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Source     string            `json:"source"`
	Supersedes string            `json:"supersedes,omitempty"`
	MemoryType string            `json:"memory_type,omitempty"`
	Experience *Experience       `json:"experience,omitempty"`
	Requires   map[string]string `json:"requires,omitempty"`
}

func (v Input) Validate() error {
	if err := validateKnowledge(v); err != nil {
		return err
	}
	switch v.Kind {
	case "testing", "environment", "constraint", "troubleshooting", "workflow":
	default:
		return errors.New("unsupported memory kind")
	}
	if strings.TrimSpace(v.Title) == "" || strings.TrimSpace(v.Content) == "" || strings.TrimSpace(v.Source) == "" {
		return errors.New("title, content and explicit source are required")
	}
	b, _ := json.Marshal(v)
	if len(b) > MaxBytes/2 || !utf8.Valid(b) {
		return errors.New("memory entry exceeds 8 KiB")
	}
	if v.Supersedes != "" && !ValidID(v.Supersedes) {
		return errors.New("invalid supersedes ID")
	}
	if len(v.Paths) > 32 || len(v.Tags) > 32 {
		return errors.New("too many memory selectors")
	}
	for _, p := range v.Paths {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "..") {
			return errors.New("paths must be relative patterns without traversal")
		}
		if _, err := path.Match(p, "probe"); err != nil {
			return err
		}
	}
	return nil
}

// DecodeInput is deliberately separate from stored entries: imported JSON cannot
// provide approved state, review identity, revision or a prechosen entry ID.
func DecodeInput(r io.Reader) (Input, error) {
	var v Input
	b, e := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if e != nil {
		return v, e
	}
	if len(b) > MaxBytes || !utf8.Valid(b) {
		return v, errors.New("invalid or oversized memory input")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil {
		return v, e
	}
	if d.Decode(new(any)) != io.EOF {
		return v, errors.New("expected one memory object")
	}
	return v, v.Validate()
}

type Entry struct {
	Provenance     *Provenance     `json:"provenance,omitempty"`
	Import         *ImportOrigin   `json:"import_origin,omitempty"`
	ReviewIdentity *ReviewIdentity `json:"review_identity,omitempty"`
	ReviewDue      *time.Time      `json:"review_due,omitempty"`
	Input
	ID         string     `json:"id"`
	Revision   int        `json:"revision"`
	Status     string     `json:"status"`
	Reviewer   string     `json:"reviewer,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Digest     string     `json:"digest"`
}

func entryDigest(e Entry) string { e.Digest = ""; return Digest(e) }

type Project struct {
	FindingReviews []FindingReview `json:"finding_reviews,omitempty"`
	Schema         string          `json:"schema_version"`
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Sequence       uint64          `json:"sequence"`
	History        []Entry         `json:"history"`
	Feedback       []Feedback      `json:"feedback,omitempty"`
}

func (p Project) Latest() []Entry {
	m := map[string]Entry{}
	for _, e := range p.History {
		m[e.ID] = e
	}
	out := make([]Entry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type Store struct {
	Home string
	// Restrictions are supplied by the trusted host policy, never imported data.
	AllowedProjects []string
	Actor           *ReviewIdentity
}

func (s Store) root() string { return filepath.Join(s.Home, "project-memory") }
func (s Store) projectPath(id string) (string, error) {
	if s.AllowedProjects != nil {
		allowed := false
		for _, project := range s.AllowedProjects {
			if project == id {
				allowed = true
			}
		}
		if !allowed {
			return "", errors.New("project exceeds organization memory grant")
		}
	}
	if !ValidID(id) {
		return "", errors.New("invalid project ID")
	}
	return filepath.Join(s.root(), "projects", id+".json"), nil
}

// ReadFile refuses symlinks, non-regular files and oversized data. All storage is
// operator-owned, outside the container's writable state mount.
func ReadFile(name string, max int64) ([]byte, error) {
	f, e := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("invalid memory file type or size")
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if int64(len(b)) > max {
		return nil, errors.New("memory file too large")
	}
	return b, e
}
func decodeFile(name string, v any) error {
	b, e := ReadFile(name, maxStore)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing memory data")
	}
	return nil
}
func write(name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if len(b) > maxStore {
		return errors.New("memory store exceeds 8 MiB")
	}
	return fsutil.AtomicWrite(name, append(b, '\n'), 0600)
}
func (s Store) lock() (func(), error) {
	if e := os.MkdirAll(s.root(), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(s.root(), "store.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func (s Store) Load(id string) (Project, error) {
	var p Project
	name, e := s.projectPath(id)
	if e != nil {
		return p, e
	}
	if e = decodeFile(name, &p); e != nil {
		return p, e
	}
	if p.Schema != Schema || p.ID != id {
		return p, errors.New("invalid project identity/schema")
	}
	revs := map[string]int{}
	for _, v := range p.History {
		if !ValidID(v.ID) || v.Revision != revs[v.ID]+1 || v.Digest != entryDigest(v) {
			return p, errors.New("invalid memory history/digest")
		}
		if e = v.Input.Validate(); e != nil {
			return p, e
		}
		switch v.Status {
		case "candidate", "approved", "rejected", "archived", "superseded":
		default:
			return p, errors.New("invalid memory state")
		}
		revs[v.ID] = v.Revision
	}
	if e = validateFindingReviews(p); e != nil {
		return p, e
	}
	return p, validateFeedbackHistory(p)
}
func (s Store) Create(name string) (Project, error) {
	if _, e := s.projectPath(name); e != nil {
		return Project{}, e
	}
	if !ValidID(name) {
		return Project{}, errors.New("project name must be 1-80 letters, digits, underscores or hyphens")
	}
	u, e := s.lock()
	if e != nil {
		return Project{}, e
	}
	defer u()
	p := Project{Schema: Schema, ID: name, Name: name, Sequence: 1, History: []Entry{}}
	f, _ := s.projectPath(name)
	if _, e = os.Lstat(f); !os.IsNotExist(e) {
		return p, errors.New("project already exists or cannot be inspected")
	}
	return p, write(f, p)
}
func canonical(repo string) (string, error) {
	a, e := filepath.Abs(repo)
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(a)
}
func (s Store) Bind(id, repo string) error {
	u, e := s.lock()
	if e != nil {
		return e
	}
	defer u()
	if _, e = s.Load(id); e != nil {
		return e
	}
	repo, e = canonical(repo)
	if e != nil {
		return e
	}
	m := map[string]string{}
	f := filepath.Join(s.root(), "bindings.json")
	if e = decodeFile(f, &m); e != nil && !os.IsNotExist(e) {
		return e
	}
	if old := m[repo]; old != "" && old != id {
		return errors.New("repository already bound to another project")
	}
	m[repo] = id
	return write(f, m)
}
func (s Store) Resolve(id, repo string) (string, error) {
	if id != "" {
		_, e := s.Load(id)
		return id, e
	}
	repo, e := canonical(repo)
	if e != nil {
		return "", e
	}
	m := map[string]string{}
	if e = decodeFile(filepath.Join(s.root(), "bindings.json"), &m); e != nil {
		return "", e
	}
	id = m[repo]
	if id == "" {
		return "", errors.New("no project binding; use sbx memory project bind or trusted memory.project")
	}
	_, e = s.Load(id)
	return id, e
}
func (s Store) Add(id string, v Input) (Entry, error) {
	return s.Management().SubmitCandidate(context.Background(), id, v, 0)
}

// Review uses a mandatory digest comparison; approving a stale candidate fails.
func (s Store) Review(project, id, digest, status, reviewer, reason string) (Entry, error) {
	return s.ReviewAt(project, id, digest, status, reviewer, reason, 0, nil)
}

func (s Store) ReviewAt(project, id, digest, status, reviewer, reason string, sequence uint64, due *time.Time) (Entry, error) {
	return s.Management().Review(context.Background(), project, id, digest, status, reviewer, reason, sequence, due)
}

type Selection struct {
	Paths []string          `json:"paths,omitempty"`
	Tags  []string          `json:"tags,omitempty"`
	Query string            `json:"query,omitempty"`
	Facts map[string]string `json:"facts,omitempty"`
	Limit int               `json:"limit,omitempty"`
	Bytes int               `json:"max_bytes,omitempty"`
}

func (q Selection) Validate() error {
	if err := validateQuery(q); err != nil {
		return err
	}
	if len(q.Paths) > 32 || len(q.Tags) > 32 {
		return errors.New("too many memory selectors")
	}
	for _, p := range q.Paths {
		if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			return errors.New("memory selectors require bounded relative paths")
		}
	}
	for _, tag := range q.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 128 {
			return errors.New("invalid memory tag")
		}
	}
	return nil
}

func applies(e Entry, q Selection) bool {
	if len(e.Paths) > 0 {
		match := false
		for _, pattern := range e.Paths {
			for _, p := range q.Paths {
				ok, _ := path.Match(pattern, p)
				prefix := strings.TrimSuffix(pattern, "/**")
				if ok || strings.HasSuffix(pattern, "/**") && (p == prefix || strings.HasPrefix(p, prefix+"/")) {
					match = true
				}
			}
		}
		if !match {
			return false
		}
	}
	if len(q.Tags) > 0 {
		match := false
		for _, t := range q.Tags {
			for _, et := range e.Tags {
				if t == et {
					match = true
				}
			}
		}
		if !match {
			return false
		}
	}
	return true
}

type Snapshot struct {
	Schema         string         `json:"schema_version"`
	Project        string         `json:"project_id"`
	Entries        []Entry        `json:"entries"`
	Selection      Selection      `json:"selection"`
	Context        string         `json:"context"`
	Digest         string         `json:"digest"`
	Selector       string         `json:"selector_version,omitempty"`
	Decisions      []Decision     `json:"decisions,omitempty"`
	DecisionCounts map[string]int `json:"decision_counts,omitempty"`
}

func snapshotDigest(v Snapshot) string { v.Digest = ""; return Digest(v) }
func (s Store) Select(id string, q Selection) (Snapshot, error) {
	return s.Queries().Search(context.Background(), id, q)
}
func (s Store) Check(v Snapshot) error {
	return s.Queries().ValidateSnapshot(context.Background(), v)
}
func SaveSnapshot(name string, v Snapshot) error {
	if v.Digest != snapshotDigest(v) {
		return errors.New("invalid snapshot digest")
	}
	if _, e := os.Lstat(name); e == nil {
		return errors.New("snapshot already exists")
	} else if !os.IsNotExist(e) {
		return e
	}
	return write(name, v)
}
func LoadSnapshot(name string) (Snapshot, error) {
	var v Snapshot
	e := decodeFile(name, &v)
	if e == nil && (v.Schema != "sbx.memory-context/v1" || v.Digest != snapshotDigest(v)) {
		e = errors.New("invalid snapshot digest")
	}
	return v, e
}

func (s Store) Pin(v Snapshot) error {
	u, e := s.lock()
	if e != nil {
		return e
	}
	defer u()
	if e := s.Check(v); e != nil {
		return e
	}
	name, e := s.snapshotPath(v.Digest)
	if e != nil {
		return e
	}
	if old, e := LoadSnapshot(name); e == nil {
		if old.Digest != v.Digest {
			return errors.New("snapshot collision")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	return SaveSnapshot(name, v)
}
func (s Store) snapshotPath(digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", errors.New("invalid snapshot digest")
	}
	if _, e := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); e != nil {
		return "", e
	}
	return filepath.Join(s.root(), "snapshots", strings.TrimPrefix(digest, "sha256:")+".json"), nil
}
func (s Store) Pinned(project, digest string) (Snapshot, error) {
	name, e := s.snapshotPath(digest)
	if e != nil {
		return Snapshot{}, e
	}
	v, e := LoadSnapshot(name)
	if e != nil {
		return v, e
	}
	if v.Project != project || v.Digest != digest {
		return v, errors.New("snapshot project/digest mismatch")
	}
	return v, s.Check(v)
}
