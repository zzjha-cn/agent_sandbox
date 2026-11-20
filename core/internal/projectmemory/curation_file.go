package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type StoreDraftLedger struct{ Store Store }

func (l StoreDraftLedger) file(project, id string) (string, error) {
	if _, err := l.Store.projectPath(project); err != nil {
		return "", err
	}
	if !ValidID(id) {
		return "", errors.New("invalid draft ID")
	}
	return filepath.Join(l.Store.root(), "drafts", project, id+".json"), nil
}
func (l StoreDraftLedger) Load(ctx context.Context, project, id string) (DraftRecord, error) {
	var v DraftRecord
	if _, err := l.Store.Load(project); err != nil {
		return v, err
	}
	name, err := l.file(project, id)
	if err != nil {
		return v, err
	}
	if err = ctx.Err(); err != nil {
		return v, err
	}
	if err = decodeFile(name, &v); err != nil {
		return v, err
	}
	if v.ID != id || v.Material.Project != project {
		return v, errors.New("draft project/ID mismatch")
	}
	return v, v.Validate()
}
func (l StoreDraftLedger) Save(ctx context.Context, v DraftRecord, expected uint64) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.Revision != expected+1 {
		return errors.New("invalid draft revision")
	}
	unlock, err := l.Store.lockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = l.Store.Load(v.Material.Project); err != nil {
		return err
	}
	name, err := l.file(v.Material.Project, v.ID)
	if err != nil {
		return err
	}
	old, err := l.Load(ctx, v.Material.Project, v.ID)
	if expected == 0 {
		if !os.IsNotExist(err) {
			return errors.New("draft exists or cannot be inspected")
		}
	} else {
		if err != nil {
			return err
		}
		if old.Revision != expected || old.MaterialDigest != v.MaterialDigest || old.ArtifactDigest != v.ArtifactDigest || old.Generator != v.Generator {
			return errors.New("draft changed; inspect current revision")
		}
		if len(v.Reviews) < len(old.Reviews) {
			return errors.New("draft review history cannot shrink")
		}
		for i := range old.Reviews {
			if Digest(old.Reviews[i]) != Digest(v.Reviews[i]) {
				return errors.New("draft reviews are append-only")
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	if err = write(name, v); err != nil {
		return err
	}
	for _, dir := range []string{filepath.Dir(name), filepath.Dir(filepath.Dir(name)), l.Store.root()} {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (l StoreDraftLedger) List(ctx context.Context, project string) ([]DraftRecord, error) {
	if _, err := l.Store.Load(project); err != nil {
		return nil, err
	}
	name, err := l.file(project, "probe")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(name))
	if os.IsNotExist(err) {
		return []DraftRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 128 {
		return nil, errors.New("too many draft records; narrow or archive host records")
	}
	out := []DraftRecord{}
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		v, err := l.Load(ctx, project, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(v)
		total += len(b)
		if total > 4<<20 {
			return nil, errors.New("draft relation listing exceeds 4 MiB")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s Store) Curation() *CurationService {
	return NewCurationService(s.Queries(), StoreDraftLedger{s}, ReferenceDraftGenerator{}, s.Actor)
}

// FileDraftWriter only creates an explicitly named repository draft directory.
// The directory must not be an active instructions/skills destination.
type FileDraftWriter struct {
	Repo   string
	Output string
}

func (w FileDraftWriter) open() (*os.Root, error) {
	if w.Output == "" || w.Output == "." || path.Clean(w.Output) != w.Output || path.IsAbs(w.Output) || strings.Contains(w.Output, "..") || strings.Contains(w.Output, "\\") {
		return nil, errors.New("draft output must be a confined relative directory")
	}
	for _, part := range strings.Split(w.Output, "/") {
		switch strings.ToLower(part) {
		case ".git", ".agents", ".codex", "sbx_skills", "skills", "agents.md", "skill.md":
			return nil, errors.New("choose a draft location outside active instructions/skills")
		}
	}
	r, err := os.OpenRoot(w.Repo)
	if err != nil {
		return nil, err
	}
	prefix := ""
	for _, part := range strings.Split(w.Output, "/") {
		prefix = path.Join(prefix, part)
		info, e := r.Lstat(prefix)
		if e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			r.Close()
			return nil, errors.New("draft output cannot follow symlinks or files")
		}
		if e != nil && !os.IsNotExist(e) {
			r.Close()
			return nil, e
		}
	}
	return r, nil
}
func draftMarker(v DraftRecord) []byte {
	b, _ := json.Marshal([]string{v.ID, v.MaterialDigest, v.ArtifactDigest})
	return b
}
func writeNew(r *os.Root, name string, b []byte) error {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}
func readDraftFile(r *os.Root, name string) ([]byte, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 256<<10 {
		return nil, errors.New("invalid draft file size/type")
	}
	b, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != st.Size() || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return nil, errors.New("draft file changed while reading")
	}
	return b, nil
}
func (w FileDraftWriter) Write(ctx context.Context, v DraftRecord) error {
	if err := v.Validate(); err != nil {
		return err
	}
	r, err := w.open()
	if err != nil {
		return err
	}
	defer r.Close()
	parent := path.Dir(w.Output)
	if err = r.MkdirAll(parent, 0700); err != nil {
		return err
	}
	err = r.Mkdir(w.Output, 0700)
	created := err == nil
	if err != nil && !os.IsExist(err) {
		return err
	}
	marker := path.Join(w.Output, ".sbx-draft.json")
	if created {
		if err = writeNew(r, marker, draftMarker(v)); err != nil {
			return err
		}
	} else {
		b, err := readDraftFile(r, marker)
		if err != nil || string(b) != string(draftMarker(v)) {
			return errors.New("output exists and is not this exact draft")
		}
	}
	for _, file := range v.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		name := path.Join(w.Output, file.Name)
		if b, e := readDraftFile(r, name); e == nil {
			if string(b) != file.Content {
				return errors.New("draft file changed; refusing overwrite")
			}
			continue
		} else if !os.IsNotExist(e) {
			return e
		}
		if err = writeNew(r, name, []byte(file.Content)); err != nil {
			return err
		}
	}
	return w.Verify(ctx, v)
}
func (w FileDraftWriter) Verify(ctx context.Context, v DraftRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := w.open()
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := readDraftFile(r, path.Join(w.Output, ".sbx-draft.json"))
	if err != nil || string(b) != string(draftMarker(v)) {
		return errors.New("draft marker missing or changed")
	}
	for _, file := range v.Files {
		b, err := readDraftFile(r, path.Join(w.Output, file.Name))
		if err != nil || string(b) != file.Content {
			return errors.New("draft file missing or changed")
		}
	}
	dir, err := r.Open(w.Output)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(16)
	dir.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) != len(v.Files)+1 {
		return errors.New("unexpected draft directory contents")
	}
	allowed := map[string]bool{".sbx-draft.json": true}
	for _, f := range v.Files {
		allowed[f.Name] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.IsDir() {
			return errors.New("unreviewed extra draft artifact")
		}
	}

	return nil
}

func (w FileDraftWriter) Location() (DraftLocation, error) {
	r, err := w.open()
	if err != nil {
		return DraftLocation{}, err
	}
	defer r.Close()
	root, err := filepath.Abs(w.Repo)
	if err != nil {
		return DraftLocation{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return DraftLocation{}, err
	}
	return DraftLocation{Repository: Digest(root), Path: w.Output}, nil
}
