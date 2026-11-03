package projectmemory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const BundleSchema = "sbx.memory-bundle/v1"
const MaxBundle = 256 << 10

// ReviewIdentity is a local OS identity bound by an administrator-owned policy.
// It is not a signature or an identity asserted by a sharing bundle.
type ReviewIdentity struct {
	Subject      string `json:"subject"`
	Organization string `json:"organization"`
	PolicyDigest string `json:"policy_digest"`
}
type ImportOrigin struct {
	BundleDigest  string `json:"bundle_digest"`
	SourceProject string `json:"source_project"`
	EntryDigest   string `json:"entry_digest"`
}
type SharedEntry struct {
	Input  Input  `json:"input"`
	Digest string `json:"digest"`
}

// Bundles deliberately omit local IDs, audit identities, extraction task keys,
// raw source paths, histories and approval authority. Explicitly selected title
// and content are exported verbatim and must be checked by the operator.
type Bundle struct {
	Schema  string        `json:"schema_version"`
	Project string        `json:"source_project"`
	Entries []SharedEntry `json:"entries"`
	Digest  string        `json:"digest"`
}

func bundleDigest(b Bundle) string        { b.Digest = ""; return Digest(b) }
func expired(e Entry, now time.Time) bool { return e.ReviewDue != nil && !now.Before(*e.ReviewDue) }

// Export requires explicit IDs: it never silently exports a whole project.
func (s Store) Export(project string, ids []string) (Bundle, error) {
	b := Bundle{Schema: BundleSchema, Project: project, Entries: []SharedEntry{}}
	if len(ids) == 0 || len(ids) > MaxEntries {
		return b, errors.New("select 1-20 approved entry IDs for export")
	}
	p, e := s.Load(project)
	if e != nil {
		return b, e
	}
	seen := map[string]bool{}
	contents := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return b, errors.New("duplicate export ID")
		}
		seen[id] = true
		found := false
		for _, v := range p.Latest() {
			if v.ID == id {
				if v.Status != "approved" || expired(v, time.Now()) {
					return b, errors.New("export requires current approved, unexpired entries")
				}
				// Source may contain local task IDs or paths; the recipient gets a bounded
				// portable label and only deliberately selected experience text/selectors.
				in := v.Input
				in.Source = "shared project experience"
				in.Supersedes = ""
				if e := in.Validate(); e != nil {
					return b, e
				}
				if !contents[Digest(in)] {
					b.Entries = append(b.Entries, SharedEntry{Input: in, Digest: Digest(in)})
					contents[Digest(in)] = true
				}
				found = true
			}
		}
		if !found {
			return b, errors.New("export entry not found")
		}
	}
	sort.Slice(b.Entries, func(i, j int) bool { return b.Entries[i].Digest < b.Entries[j].Digest })
	b.Digest = bundleDigest(b)
	raw, _ := json.Marshal(b)
	if len(raw) > MaxBundle {
		return b, errors.New("bundle too large")
	}
	return b, nil
}

// checkJSON rejects duplicate keys before typed decoding. Input depth is bounded
// even if a malicious peer supplies a deeply nested unknown field.
func checkJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting too deep")
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	if t == nil {
		return errors.New("null fields are not supported in sharing bundles")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := k.(string)
			if !ok || keys[key] {
				return errors.New("duplicate JSON key")
			}
			keys[key] = true
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, e = d.Token()
	return e
}
func DecodeBundle(raw []byte) (Bundle, error) {
	var b Bundle
	if len(raw) > MaxBundle || !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return b, errors.New("invalid or oversized bundle")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if e := checkJSON(d, 0); e != nil {
		return b, e
	}
	if _, e := d.Token(); e != io.EOF {
		return b, errors.New("trailing bundle data")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&b); e != nil {
		return b, e
	}
	return b, b.Validate()
}
func (b Bundle) Validate() error {
	if b.Schema != BundleSchema || !ValidID(b.Project) || b.Digest != bundleDigest(b) || b.Entries == nil || len(b.Entries) == 0 || len(b.Entries) > MaxEntries {
		return errors.New("invalid bundle schema, identity, digest or size")
	}
	seen := map[string]bool{}
	for _, v := range b.Entries {
		if e := v.Input.Validate(); e != nil {
			return e
		}
		if v.Input.Supersedes != "" || v.Digest != Digest(v.Input) || seen[v.Digest] {
			return errors.New("invalid shared entry digest or duplicate")
		}
		seen[v.Digest] = true
	}
	raw, _ := json.Marshal(b)
	if len(raw) > MaxBundle {
		return errors.New("bundle too large")
	}
	return nil
}
func (s Store) ImportBundle(project string, b Bundle) ([]Entry, error) {
	if e := b.Validate(); e != nil {
		return nil, e
	}
	unlock, e := s.lock()
	if e != nil {
		return nil, e
	}
	defer unlock()
	p, e := s.Load(project)
	if e != nil {
		return nil, e
	}
	entries := []Entry{}
	changed := false
	for _, v := range b.Entries {
		// Replay identity excludes mutable local review state. Import never overwrites
		// an approved/rejected/archived copy of the same source content.
		origin := ImportOrigin{BundleDigest: b.Digest, SourceProject: b.Project, EntryDigest: v.Digest}
		id := strings.TrimPrefix(Digest([]string{"import", b.Project, v.Digest}), "sha256:")
		found := false
		for _, old := range p.Latest() {
			if old.ID == id {
				entries = append(entries, old)
				found = true
				break
			}
		}
		if found {
			continue
		}
		in := v.Input
		in.Source = "import:" + b.Digest
		if e := in.Validate(); e != nil {
			return nil, e
		}
		n := Entry{Input: in, ID: id, Revision: 1, Status: "candidate", CreatedAt: time.Now().UTC(), Import: &origin}
		n.Digest = entryDigest(n)
		p.History = append(p.History, n)
		changed = true
		entries = append(entries, n)
	}
	if changed {
		p.Sequence++
		f, _ := s.projectPath(project)
		if e = write(f, p); e != nil {
			return nil, e
		}
	}
	return entries, nil
}

type Finding struct {
	Kind    string   `json:"kind"`
	Entries []string `json:"entries"`
}
type Maintenance struct {
	Schema           string         `json:"schema_version"`
	Project          string         `json:"project_id"`
	Sequence         uint64         `json:"sequence"`
	HistoryRevisions int            `json:"history_revisions"`
	StatusCounts     map[string]int `json:"status_counts"`
	StoreBytes       int            `json:"store_bytes"`
	StoreLimitBytes  int            `json:"store_limit_bytes"`
	Findings         []Finding      `json:"findings"`
}

func normalized(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// Inspect derives all results from checked history. There is no separate index
// to corrupt or repair; malformed authority records are never silently rebuilt.
func (s Store) Inspect(project string) (Maintenance, error) {
	out := Maintenance{Schema: "sbx.memory-maintenance/v1", Project: project, StoreLimitBytes: maxStore, StatusCounts: map[string]int{}, Findings: []Finding{}}
	unlock, e := s.lock()
	if e != nil {
		return out, e
	}
	defer unlock()
	p, e := s.Load(project)
	if e != nil {
		return out, e
	}
	f, _ := s.projectPath(project)
	raw, e := ReadFile(filepath.Clean(f), maxStore)
	if e != nil {
		return out, e
	}
	out.Sequence = p.Sequence
	out.HistoryRevisions = len(p.History)
	out.StoreBytes = len(raw)
	active := []Entry{}
	for _, v := range p.Latest() {
		out.StatusCounts[v.Status]++
		if expired(v, time.Now()) && v.Status == "approved" {
			out.Findings = append(out.Findings, Finding{Kind: "review_due", Entries: []string{v.ID}})
		}
		if v.Status == "candidate" || v.Status == "approved" {
			active = append(active, v)
		}
	}
	// Group in linear time. These are lexical hints, never semantic conflict judgments.
	titles := map[string][]Entry{}
	contents := map[string][]Entry{}
	for _, v := range active {
		paths := append([]string{}, v.Paths...)
		tags := append([]string{}, v.Tags...)
		sort.Strings(paths)
		sort.Strings(tags)
		scope := v.Kind + "\x00" + Digest([]any{paths, tags})
		titles[scope+"\x00"+normalized(v.Title)] = append(titles[scope+"\x00"+normalized(v.Title)], v)
		contents[scope+"\x00"+normalized(v.Content)] = append(contents[scope+"\x00"+normalized(v.Content)], v)
	}
	for _, group := range contents {
		if len(group) < 2 {
			continue
		}
		ids := []string{}
		for _, v := range group {
			ids = append(ids, v.ID)
		}
		out.Findings = append(out.Findings, Finding{Kind: "duplicate", Entries: ids})
	}
	for _, group := range titles {
		if len(group) < 2 {
			continue
		}
		ids := []string{}
		distinct := map[string]bool{}
		for _, v := range group {
			ids = append(ids, v.ID)
			distinct[normalized(v.Content)] = true
		}
		if len(distinct) > 1 {
			out.Findings = append(out.Findings, Finding{Kind: "possible_conflict", Entries: ids})
		}
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		return out.Findings[i].Kind+strings.Join(out.Findings[i].Entries, ",") < out.Findings[j].Kind+strings.Join(out.Findings[j].Entries, ",")
	})
	return out, nil
}
