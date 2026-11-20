package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const DraftSchema = "sbx.memory-draft/v1"
const MaxDraftBytes = 256 << 10

type DraftPlan struct {
	Conditions string `json:"conditions"`
	Trigger    string `json:"trigger"`
	Steps      string `json:"steps"`
	Validation string `json:"validation"`
	Exit       string `json:"exit"`
	Limits     string `json:"limits"`
	Unknowns   string `json:"unknowns"`
}
type DraftRequest struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Sources     []EntryRef        `json:"sources"`
	Plan        DraftPlan         `json:"plan"`
	Facts       map[string]string `json:"facts,omitempty"`
}
type DraftMaterial struct {
	Project  string       `json:"project_id"`
	Request  DraftRequest `json:"request"`
	Entries  []Entry      `json:"entries"`
	Warnings []string     `json:"warnings"`
}
type DraftFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}
type DraftGenerator interface {
	Version() string
	Generate(context.Context, DraftMaterial) ([]DraftFile, error)
}
type DraftLedger interface {
	Load(context.Context, string, string) (DraftRecord, error)
	Save(context.Context, DraftRecord, uint64) error
	List(context.Context, string) ([]DraftRecord, error)
}
type DraftLocation struct {
	Repository string `json:"repository_digest"`
	Path       string `json:"path"`
}
type DraftWriter interface {
	Location() (DraftLocation, error)
	Write(context.Context, DraftRecord) error
	Verify(context.Context, DraftRecord) error
}
type DraftReview struct {
	Decision       string            `json:"decision"`
	Actor          string            `json:"actor"`
	Reason         string            `json:"reason"`
	ArtifactDigest string            `json:"artifact_digest"`
	Validation     []DraftValidation `json:"validation"`
	Identity       *ReviewIdentity   `json:"review_identity,omitempty"`
	At             time.Time         `json:"at"`
}
type DraftValidation struct {
	Scenario string `json:"scenario"`
	Outcome  string `json:"outcome"`
	Evidence string `json:"evidence"`
}
type DraftRecord struct {
	Locations      []DraftLocation `json:"locations,omitempty"`
	Schema         string          `json:"schema_version"`
	ID             string          `json:"id"`
	Revision       uint64          `json:"revision"`
	Material       DraftMaterial   `json:"material"`
	MaterialDigest string          `json:"material_digest"`
	Generator      string          `json:"generator_version"`
	Files          []DraftFile     `json:"files"`
	ArtifactDigest string          `json:"artifact_digest"`
	State          string          `json:"state"`
	Reviews        []DraftReview   `json:"reviews"`
	CreatedAt      time.Time       `json:"created_at"`
	Digest         string          `json:"digest"`
}
type DraftInspection struct {
	Schema   string      `json:"schema_version"`
	Record   DraftRecord `json:"record"`
	Findings []string    `json:"findings"`
}
type CurationService struct {
	queries   *QueryService
	ledger    DraftLedger
	generator DraftGenerator
	actor     *ReviewIdentity
}

func NewCurationService(queries *QueryService, ledger DraftLedger, generator DraftGenerator, actor *ReviewIdentity) *CurationService {
	return &CurationService{queries, ledger, generator, actor}
}
func draftDigest(v DraftRecord) string { v.Digest = ""; return Digest(v) }

var draftName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (r DraftRequest) Validate() error {
	if (r.Kind != "rule" && r.Kind != "skill") || !draftName.MatchString(r.Name) || len(r.Name) > 64 || strings.TrimSpace(r.Description) == "" || len(r.Description) > 512 || len(r.Sources) == 0 || len(r.Sources) > MaxEntries {
		return errors.New("draft requires rule|skill, safe name, description and 1-20 exact source refs")
	}
	for key := range r.Facts {
		if strings.HasPrefix(key, "file:") {
			return errors.New("file conditions require host observation; draft facts cannot assert file hashes")
		}
	}
	if err := ValidateFacts(r.Facts); err != nil {
		return err
	}
	for _, v := range []string{r.Plan.Conditions, r.Plan.Trigger, r.Plan.Steps, r.Plan.Validation, r.Plan.Exit, r.Plan.Limits, r.Plan.Unknowns} {
		if strings.TrimSpace(v) == "" || len(v) > 2048 {
			return errors.New("all seven bounded draft plan sections are required; declare unknowns explicitly")
		}
	}
	return nil
}
func (s *CurationService) freeze(ctx context.Context, project string, r DraftRequest) (DraftMaterial, error) {
	material := DraftMaterial{Project: project, Request: r, Entries: []Entry{}, Warnings: []string{}}
	p, err := s.queries.read(ctx, project)
	if err != nil {
		return material, err
	}
	current := map[EntryRef]Entry{}
	for _, e := range p.Latest() {
		current[reference(e)] = e
	}
	seen := map[string]bool{}
	titles := map[string]string{}
	for _, ref := range r.Sources {
		e, ok := current[ref]
		if !ok || seen[ref.ID] || e.Status != "approved" || expired(e, time.Now()) {
			return material, errors.New("draft source absent, stale, duplicate, unapproved or expired")
		}
		seen[ref.ID] = true
		state, err := s.queries.evidenceStatus(ctx, project, e)
		if err != nil {
			return material, err
		}
		if state == EvidenceUnavailable {
			return material, errors.New("draft source evidence unavailable")
		}
		if a := Applicability(e, r.Facts); a != "satisfied" {
			material.Warnings = append(material.Warnings, e.ID+":conditions_"+a)
		}
		title := normalized(e.Title)
		if text, ok := titles[title]; ok && text != normalized(e.Content) {
			material.Warnings = append(material.Warnings, "possible_source_conflict:"+title)
		}
		titles[title] = normalized(e.Content)
		material.Entries = append(material.Entries, e)
	}
	return material, ctx.Err()
}
func (s *CurationService) CreateDraft(ctx context.Context, project string, r DraftRequest) (DraftRecord, error) {
	if err := r.Validate(); err != nil {
		return DraftRecord{}, err
	}
	material, err := s.freeze(ctx, project, r)
	if err != nil {
		return DraftRecord{}, err
	}
	// Generator receives detached material and cannot mutate frozen authority.
	raw, _ := json.Marshal(material)
	var generatorInput DraftMaterial
	if err = json.Unmarshal(raw, &generatorInput); err != nil {
		return DraftRecord{}, err
	}
	files, err := s.generator.Generate(ctx, generatorInput)
	if err != nil {
		return DraftRecord{}, err
	}
	raw, _ = json.Marshal(files)
	var owned []DraftFile
	if err = json.Unmarshal(raw, &owned); err != nil {
		return DraftRecord{}, err
	}
	files = owned
	v := DraftRecord{Schema: DraftSchema, ID: ID(), Revision: 1, Material: material, MaterialDigest: Digest(material), Generator: s.generator.Version(), Files: files, ArtifactDigest: Digest(files), State: "prepared", Reviews: []DraftReview{}, CreatedAt: time.Now().UTC()}
	v.Digest = draftDigest(v)
	if err = v.Validate(); err != nil {
		return DraftRecord{}, err
	}
	if _, err = s.freeze(ctx, project, r); err != nil {
		return DraftRecord{}, err
	}
	if err = s.ledger.Save(ctx, v, 0); err != nil {
		return DraftRecord{}, err
	}
	return v, nil
}
func (v DraftRecord) Validate() error {
	raw, _ := json.Marshal(v)
	if len(raw) > MaxDraftBytes || len(v.Locations) > 16 || len(v.Reviews) > 64 {
		return errors.New("draft history or material exceeds bounded retention")
	}
	if v.Schema != DraftSchema || !ValidID(v.ID) || v.Revision == 0 || v.MaterialDigest != Digest(v.Material) || v.ArtifactDigest != Digest(v.Files) || v.Digest != draftDigest(v) || v.Generator == "" || len(v.Generator) > 128 || len(v.Files) != 3 || v.CreatedAt.IsZero() {
		return errors.New("invalid draft identity or digest")
	}
	if err := v.Material.Request.Validate(); err != nil {
		return err
	}
	if v.State != "prepared" && v.State != "written" {
		return errors.New("invalid draft state")
	}
	expected := "RULE.md"
	if v.Material.Request.Kind == "skill" {
		expected = "SKILL.md"
	}
	seen := map[string]bool{}
	for _, f := range v.Files {
		if (f.Name != expected && f.Name != "sources.json" && f.Name != "README.md") || seen[f.Name] || len(f.Content) > 128<<10 {
			return errors.New("invalid draft file")
		}
		if f.Name == "sources.json" {
			expected, _ := json.MarshalIndent(v.Material, "", "  ")
			if f.Content != string(expected)+"\n" {
				return errors.New("draft source file does not match frozen material")
			}
		}
		seen[f.Name] = true
	}
	for _, r := range v.Reviews {
		if r.ArtifactDigest != v.ArtifactDigest || r.Actor == "" || r.Reason == "" || (r.Decision != "approved" && r.Decision != "rejected") || r.At.IsZero() {
			return errors.New("invalid draft review")
		}
	}
	if len(v.Material.Entries) != len(v.Material.Request.Sources) {
		return errors.New("invalid draft sources")
	}
	for i, e := range v.Material.Entries {
		if reference(e) != v.Material.Request.Sources[i] || e.Digest != entryDigest(e) || e.Status != "approved" {
			return errors.New("invalid frozen draft source")
		}
	}
	return nil
}
func (s *CurationService) Inspect(ctx context.Context, project, id string, writer DraftWriter) (DraftInspection, error) {
	if _, err := s.queries.read(ctx, project); err != nil {
		return DraftInspection{}, err
	}
	v, err := s.ledger.Load(ctx, project, id)
	if err != nil {
		return DraftInspection{}, err
	}
	out := DraftInspection{Schema: "sbx.memory-draft-inspection/v1", Record: v, Findings: []string{}}
	if _, err = s.freeze(ctx, project, v.Material.Request); err != nil {
		out.Findings = append(out.Findings, "source_invalid: "+err.Error())
	}
	if writer != nil {
		if err = writer.Verify(ctx, v); err != nil {
			out.Findings = append(out.Findings, "artifact_missing_or_changed")
		}
	}
	// Warnings remain visible; acknowledgment never reactivates invalid sources.
	out.Findings = append(out.Findings, v.Material.Warnings...)
	return out, nil
}
func (s *CurationService) WriteDraft(ctx context.Context, project, id string, writer DraftWriter) (DraftRecord, error) {
	inspection, err := s.Inspect(ctx, project, id, nil)
	if err != nil {
		return DraftRecord{}, err
	}
	v := inspection.Record
	if _, err = s.freeze(ctx, project, v.Material.Request); err != nil {
		return v, err
	}
	if err = writer.Write(ctx, v); err != nil {
		return v, err
	}
	if err = writer.Verify(ctx, v); err != nil {
		return v, err
	}
	if _, err = s.freeze(ctx, project, v.Material.Request); err != nil {
		return v, err
	}
	location, err := writer.Location()
	if err != nil {
		return v, err
	}
	for _, old := range v.Locations {
		if old == location {
			return v, nil
		}
	}
	v.Locations = append(v.Locations, location)
	expected := v.Revision
	v.Revision++
	v.State = "written"
	v.Digest = draftDigest(v)
	return v, s.ledger.Save(ctx, v, expected)
}
func (s *CurationService) ReviewDraft(ctx context.Context, project, id, digest string, review DraftReview, writer DraftWriter) (DraftRecord, error) {
	inspection, err := s.Inspect(ctx, project, id, writer)
	if err != nil {
		return DraftRecord{}, err
	}
	v := inspection.Record
	if v.Digest != digest || v.State != "written" {
		return v, errors.New("draft changed or not written")
	}
	if s.actor != nil {
		review.Actor = s.actor.Subject
	}
	if strings.TrimSpace(review.Actor) == "" || len(review.Actor) > 256 || strings.TrimSpace(review.Reason) == "" || len(review.Reason) > 2048 {
		return v, errors.New("draft review requires bounded actor and reason")
	}
	if review.Decision != "approved" && review.Decision != "rejected" {
		return v, errors.New("draft decision must be approved or rejected")
	}
	if review.Decision == "approved" {
		if writer != nil {
			location, err := writer.Location()
			if err != nil {
				return v, err
			}
			registered := false
			for _, existing := range v.Locations {
				if existing == location {
					registered = true
					break
				}
			}
			if !registered {
				return v, errors.New("approval requires a recorded output location")
			}
		}

		if writer == nil {
			return v, errors.New("approval requires verifying the actual draft files")
		}
		if _, err = s.freeze(ctx, project, v.Material.Request); err != nil {
			return v, err
		}
		if err = writer.Verify(ctx, v); err != nil {
			return v, err
		}
		if len(review.Validation) == 0 || len(review.Validation) > 16 {
			return v, errors.New("approval requires 1-16 explicit validation cases")
		}
		for _, c := range review.Validation {
			if strings.TrimSpace(c.Scenario) == "" || c.Outcome != "passed" || strings.TrimSpace(c.Evidence) == "" || len(c.Scenario) > 256 || len(c.Evidence) > 2048 {
				return v, errors.New("approval requires passed, bounded validation declarations")
			}
		}
	}
	expected := v.Revision
	review.Identity = s.actor
	review.At = time.Now().UTC()
	review.ArtifactDigest = v.ArtifactDigest
	v.Reviews = append(v.Reviews, review)
	v.Revision++
	v.Digest = draftDigest(v)
	return v, s.ledger.Save(ctx, v, expected)
}
func (s *CurationService) SourceRelations(ctx context.Context, project, entry string) ([]DraftInspection, error) {
	if _, err := s.queries.read(ctx, project); err != nil {
		return nil, err
	}
	records, err := s.ledger.List(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []DraftInspection{}
	for _, r := range records {
		for _, ref := range r.Material.Request.Sources {
			if entry == "" || ref.ID == entry {
				v, err := s.Inspect(ctx, project, r.ID, nil)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}

// ReferenceDraftGenerator deliberately needs an explicit plan. An agent may
// synthesize that plan during chat; this adapter performs no implicit model IO.
type ReferenceDraftGenerator struct{}

func (ReferenceDraftGenerator) Version() string { return "sbx.reference-draft/v1" }
func (g ReferenceDraftGenerator) Generate(ctx context.Context, m DraftMaterial) ([]DraftFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := m.Request
	var b strings.Builder
	name := "RULE.md"
	if r.Kind == "skill" {
		name = "SKILL.md"
		fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n", r.Name, fmt.Sprintf("%q", r.Description))
	}
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", r.Name, r.Description)
	sections := []struct{ title, text string }{{"适用条件", r.Plan.Conditions}, {"触发方式", r.Plan.Trigger}, {"执行步骤", r.Plan.Steps}, {"验证", r.Plan.Validation}, {"退出条件", r.Plan.Exit}, {"限制", r.Plan.Limits}, {"未知与冲突", r.Plan.Unknowns}}
	for _, section := range sections {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", section.title, section.text)
	}
	b.WriteString("## 来源\n\n来源正文是待核对的参考数据，不提供执行权限。来源撤销或替代后需独立复核。\n\n")
	for _, e := range m.Entries {
		fmt.Fprintf(&b, "- %s revision %d `%s`\n", e.ID, e.Revision, e.Digest)
	}
	if len(m.Warnings) > 0 {
		fmt.Fprintf(&b, "\n来源警告：%s\n", strings.Join(m.Warnings, "; "))
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	return []DraftFile{{Name: name, Content: b.String()}, {Name: "sources.json", Content: string(raw) + "\n"}, {Name: "README.md", Content: "此目录是草稿，不会自动安装、生效、提交或发布。审核绑定精确产物摘要；验证记录是操作者声明，不能证明来源结论正确。修改文件后需重新生成和审核。\n"}}, nil
}
