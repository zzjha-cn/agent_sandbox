package taskmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"sandx/internal/projectmemory"
)

type Limits struct {
	TimeoutSeconds  int `json:"timeout_seconds" toml:"timeout_seconds"`
	MaxOutputTokens int `json:"max_output_tokens" toml:"max_output_tokens"`
	MaxCalls        int `json:"max_calls" toml:"max_calls"`
}

func (l Limits) Validate() error {
	if l.TimeoutSeconds < 1 || l.TimeoutSeconds > 300 || l.MaxOutputTokens < 256 || l.MaxOutputTokens > 8192 || l.MaxCalls < 1 || l.MaxCalls > 3 {
		return errors.New("extraction limits: timeout 1-300s, output 256-8192 tokens, calls 1-3")
	}
	return nil
}

type Candidate struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Paths    []string `json:"paths,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Evidence []string `json:"evidence"`
}

// Model output has no execution facts, approval, ID, source or supersedes field.
type Output struct {
	Explanation string      `json:"explanation"`
	Suggestions []string    `json:"suggestions"`
	Candidates  []Candidate `json:"candidates"`
}
type Job struct {
	Schema         string    `json:"schema_version"`
	ID             string    `json:"id"`
	Project        string    `json:"project_id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Extractor      string    `json:"extractor_version"`
	Material       Material  `json:"material"`
	MaterialDigest string    `json:"material_digest"`
	Limits         Limits    `json:"limits"`
	State          string    `json:"state"`
	Calls          int       `json:"calls"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Output         *Output   `json:"output,omitempty"`
	OutputDigest   string    `json:"output_digest,omitempty"`
	Candidates     []string  `json:"candidate_ids"`
	Error          string    `json:"error,omitempty"` // fixed safe code, never raw HTTP errors/body
}

func (j Job) identity() string {
	return strings.TrimPrefix(projectmemory.Digest(struct {
		Project, Provider, Model, Extractor, Material string
		Limits                                        Limits
	}{j.Project, j.Provider, j.Model, j.Extractor, j.MaterialDigest, j.Limits}), "sha256:")
}

type Jobs struct{ Home string }

func (s Jobs) path(id string) (string, error) {
	if !projectmemory.ValidID(id) {
		return "", errors.New("invalid extraction job ID")
	}
	return filepath.Join(s.Home, "memory-extractions", id, "job.json"), nil
}
func (s Jobs) lock(id string) (func(), error) {
	name, e := s.path(id)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(name), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(filepath.Dir(name), "job.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("extraction job is busy")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func (s Jobs) save(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	name, e := s.path(j.ID)
	if e != nil {
		return e
	}
	return save(name, j)
}
func (s Jobs) Load(id string) (Job, error) {
	var j Job
	name, e := s.path(id)
	if e != nil {
		return j, e
	}
	if e = read(name, &j); e != nil {
		return j, e
	}
	if j.Schema != JobSchema || j.ID != id || j.identity() != id || j.MaterialDigest != projectmemory.Digest(j.Material) || j.Material.Validate() != nil || j.Limits.Validate() != nil || j.Calls < 0 || j.Calls > j.Limits.MaxCalls {
		return j, errors.New("invalid extraction job binding")
	}
	switch j.State {
	case "pending", "running", "succeeded", "failed", "interrupted":
	default:
		return j, errors.New("invalid extraction job state")
	}
	if j.Output != nil && (projectmemory.Digest(j.Output) != j.OutputDigest || validateOutput(*j.Output, j.Material) != nil) {
		return j, errors.New("invalid extraction output binding")
	}
	if j.State == "succeeded" && j.Output == nil {
		return j, errors.New("missing extraction result")
	}
	return j, nil
}
func (s Jobs) Create(project, provider, model string, m Material, l Limits) (Job, error) {
	j := Job{Schema: JobSchema, Project: project, Provider: provider, Model: model, Extractor: ExtractorVersion, Material: m, MaterialDigest: projectmemory.Digest(m), Limits: l, State: "pending", Candidates: []string{}, CreatedAt: time.Now().UTC()}
	if provider != "codex" && provider != "claude" {
		return j, errors.New("unsupported extraction provider")
	}
	if strings.TrimSpace(model) == "" || len(model) > 200 {
		return j, errors.New("memory.extract.model is required")
	}
	if e := l.Validate(); e != nil {
		return j, e
	}
	if e := m.Validate(); e != nil {
		return j, e
	}
	if _, e := (projectmemory.Store{Home: s.Home}).Load(project); e != nil {
		return j, e
	}
	j.ID = j.identity()
	u, e := s.lock(j.ID)
	if e != nil {
		return j, e
	}
	defer u()
	if old, e := s.Load(j.ID); e == nil {
		return old, nil
	} else if !os.IsNotExist(e) {
		return j, e
	}
	e = s.save(&j)
	return j, e
}

// Recover never issues a request: the lock proves no extraction worker still owns
// this job. A running record may have incurred a charge even without a result.
func (s Jobs) Recover(id string) (Job, error) {
	u, e := s.lock(id)
	if e != nil {
		return Job{}, e
	}
	defer u()
	j, e := s.Load(id)
	if e != nil {
		return j, e
	}
	if j.State == "running" {
		j.State = "interrupted"
		j.Error = "worker_interrupted"
		e = s.save(&j)
	}
	return j, e
}

type Model interface {
	Generate(context.Context, Job) ([]byte, error)
}

// Run holds a per-job process lock throughout the call. retry is explicit; the
// original business execution is never reachable from this package.
func (s Jobs) Run(ctx context.Context, id string, model Model, retry bool) (Job, error) {
	u, e := s.lock(id)
	if e != nil {
		return Job{}, e
	}
	defer u()
	j, e := s.Load(id)
	if e != nil {
		return j, e
	}
	if j.State == "succeeded" {
		return j, nil
	}
	if j.State == "running" {
		j.State = "interrupted"
		j.Error = "worker_interrupted"
		if e = s.save(&j); e != nil {
			return j, e
		}
	}
	if j.State != "pending" && !retry {
		return j, errors.New("extraction requires explicit retry; the previous model call may have been billed")
	}
	fail := func(code string) (Job, error) {
		j.State = "failed"
		j.Error = code
		e := s.save(&j)
		return j, errors.Join(errors.New(code), e)
	}
	if j.Output == nil {
		if j.Calls >= j.Limits.MaxCalls {
			return fail("call_budget_exhausted")
		}
		if model == nil {
			return fail("model_unavailable")
		}
		j.State = "running"
		j.Error = ""
		j.Calls++
		if e = s.save(&j); e != nil {
			return j, e
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(j.Limits.TimeoutSeconds)*time.Second)
		b, err := model.Generate(callCtx, j)
		cancel()
		if err != nil {
			return fail("model_call_failed")
		}
		if len(b) > 32<<10 {
			return fail("model_output_too_large")
		}
		var out Output
		if e = decode(b, &out); e != nil {
			return fail("invalid_model_output")
		}
		if e = validateOutput(out, j.Material); e != nil {
			return fail("unsafe_or_unbound_output")
		}
		j.Output = &out
		j.OutputDigest = projectmemory.Digest(out)
		// Save validated result before publishing; recovery can finish without a call.
		if e = s.save(&j); e != nil {
			return j, e
		}
	}
	inputs := []projectmemory.Input{}
	refs := []projectmemory.Provenance{}
	for _, c := range j.Output.Candidates {
		inputs = append(inputs, projectmemory.Input{Kind: c.Kind, Title: c.Title, Content: c.Content, Paths: c.Paths, Tags: c.Tags, Source: "extraction:" + j.ID})
		refs = append(refs, projectmemory.Provenance{Job: j.ID, Task: j.Material.Handoff.TaskKey, Attempt: j.Material.Handoff.Attempt, Extractor: j.Extractor, MaterialDigest: j.MaterialDigest, OutputDigest: j.OutputDigest, Evidence: c.Evidence})
	}
	j.Candidates, e = (projectmemory.Store{Home: s.Home}).PublishExtraction(j.Project, inputs, refs)
	if e != nil {
		return fail("candidate_publication_failed")
	}
	j.State = "succeeded"
	j.Error = ""
	e = s.save(&j)
	return j, e
}
func validateOutput(o Output, m Material) error {
	if o.Candidates == nil || o.Suggestions == nil || strings.TrimSpace(o.Explanation) == "" {
		return errors.New("extraction output requires explanation and candidates/suggestions arrays")
	}
	if len(o.Candidates) > 5 || len(o.Suggestions) > 10 || len(o.Explanation) > 4096 {
		return errors.New("extraction result exceeds limits")
	}
	allowed := map[string]bool{"handoff:" + m.Handoff.Digest: true}
	if projectmemory.Digest(m.Summary) != projectmemory.Digest(Summary{}) {
		allowed["summary:"+projectmemory.Digest(m.Summary)] = true
	}
	for _, c := range o.Candidates {
		v := projectmemory.Input{Kind: c.Kind, Title: c.Title, Content: c.Content, Paths: c.Paths, Tags: c.Tags, Source: "extraction"}
		if e := v.Validate(); e != nil {
			return e
		}
		if len(c.Evidence) == 0 || len(c.Evidence) > 8 {
			return errors.New("candidate requires evidence references")
		}
		for _, r := range c.Evidence {
			if !allowed[r] {
				return fmt.Errorf("unknown evidence reference")
			}
		}
	}
	b, _ := json.Marshal(o)
	return checkSafe(b)
}

var secretPattern = regexp.MustCompile(`(?i)(-----BEGIN[ A-Z]*PRIVATE KEY-----|\bBearer\s+[a-z0-9._~+/-]{8,}|\bsk-[a-z0-9_-]{12,}|\bAKIA[A-Z0-9]{16}\b|\beyJ[a-z0-9_-]{10,}\.[a-z0-9_-]+\.[a-z0-9_-]+|(?:api[_-]?key|password|access[_-]?token|secret)\s*[=:]\s*["']?[a-z0-9_./+\-]{8,})`)

func checkSafe(b []byte) error {
	if secretPattern.Match(b) {
		return errors.New("possible credential in memory material/output; provide a scrubbed safe summary")
	}
	return nil
}
