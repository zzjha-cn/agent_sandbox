// Package trust 对仓库里的 .sbx/ 目录做信任确认（design §9.3）。
//
// .sbx/ 是跟着仓库走的：git pull 下来的配置会影响沙箱怎么跑（白名单、
// 将来还有自定义 Dockerfile）。所以它的内容一变，sbx run 就先停下来让你看一眼
// 改了什么，确认过（sbx trust）才继续。
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"sandx/internal/fsutil"
)

// Dir 是仓库里被纳入信任范围的目录名。
const Dir = ".sbx"

// maxInline 以内的文本文件会把内容存进快照，这样下次变更时能直接显示 diff，
// 不用去翻 git 历史。超过的只存哈希。
const maxInline = 256 << 10

// File 是快照里的一个文件。Text 为空且 Omitted 非空时表示内容没存。
type File struct {
	Hash    string `json:"hash"`              // 内容的 sha256（符号链接存的是链接目标的哈希）
	Size    int64  `json:"size"`              //
	Link    bool   `json:"link,omitempty"`    // 符号链接：Text 是链接目标，不是文件内容
	Text    string `json:"text,omitempty"`    //
	Omitted string `json:"omitted,omitempty"` // "too-large" / "binary"
}

// Snapshot 是某一时刻 .sbx/ 的全部内容。
type Snapshot struct {
	Hash      string          `json:"hash"`
	TrustedAt time.Time       `json:"trusted_at,omitempty"`
	Files     map[string]File `json:"files"`
}

// Empty 表示仓库里没有 .sbx/，或者里面一个文件都没有。这种仓库不需要信任确认。
func (s Snapshot) Empty() bool { return len(s.Files) == 0 }

// Paths 返回快照里的文件路径，已排序。
func (s Snapshot) Paths() []string {
	out := make([]string, 0, len(s.Files))
	for p := range s.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Scan 读取 <root>/.sbx/ 下的所有文件，算出快照。目录不存在时返回空快照。
//
// 符号链接按链接本身记录，不跟随：否则把 .sbx/Dockerfile 指向别处就能在
// 哈希不变的情况下换掉实际内容。
func Scan(root string) (Snapshot, error) {
	base := filepath.Join(root, Dir)
	s := Snapshot{Files: map[string]File{}}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == base {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := readOne(p, d)
		if err != nil {
			return err
		}
		s.Files[rel] = f
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("failed to read %s: %w", base, err)
	}
	s.Hash = hash(s.Files)
	return s, nil
}

func readOne(p string, d fs.DirEntry) (File, error) {
	if d.Type()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			return File{}, err
		}
		return File{Hash: sum([]byte(target)), Size: int64(len(target)), Link: true, Text: target}, nil
	}
	if !d.Type().IsRegular() {
		return File{}, fmt.Errorf("%s is neither a regular file nor a symlink; move it out of %s/", p, Dir)
	}
	info, err := d.Info()
	if err != nil {
		return File{}, err
	}
	f := File{Size: info.Size()}
	if info.Size() > maxInline {
		h, err := hashFile(p)
		if err != nil {
			return File{}, err
		}
		f.Hash, f.Omitted = h, "too-large"
		return f, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return File{}, err
	}
	f.Hash, f.Size = sum(b), int64(len(b))
	if utf8.Valid(b) && !strings.ContainsRune(string(b), 0) {
		f.Text = string(b)
	} else {
		f.Omitted = "binary"
	}
	return f, nil
}

// hash 把所有文件按路径排序串成一个哈希。内容用各自的哈希参与，
// 大文件因此不必整个读进内存。
func hash(files map[string]File) string {
	h := sha256.New()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := files[p]
		kind := "f"
		if f.Link {
			kind = "l"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00", p, kind, f.Size, f.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Path 是某个 Workspace 的信任记录。
func Path(home, wsID string) string {
	return filepath.Join(home, "trust", wsID+".json")
}

// Load 读取信任记录。文件不存在时返回 ok=false。
func Load(path string) (Snapshot, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, false, fmt.Errorf("failed to parse the trust record %s (delete it and run sbx trust again): %w", path, err)
	}
	if s.Files == nil {
		s.Files = map[string]File{}
	}
	return s, true, nil
}

// Save 写入信任记录。
func Save(path string, s Snapshot) error {
	s.TrustedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, append(b, '\n'), 0o644)
}

// Kind 是一处变更的类型。
type Kind string

const (
	Added    Kind = "added"
	Removed  Kind = "removed"
	Modified Kind = "modified"
)

// Change 是一处变更。Old/New 为 nil 表示内容没存进快照（二进制或太大），
// Note 说明原因；这时只能告诉你"变了"，给不出 diff。
type Change struct {
	Kind Kind
	Path string
	Old  []byte
	New  []byte
	Note string
}

// Diff 比较已信任的快照和当前快照。
func Diff(old, cur Snapshot) []Change {
	seen := map[string]bool{}
	var out []Change
	for _, p := range cur.Paths() {
		seen[p] = true
		o, had := old.Files[p]
		n := cur.Files[p]
		switch {
		case !had:
			out = append(out, change(Added, p, File{}, n))
		case o.Hash != n.Hash || o.Link != n.Link:
			out = append(out, change(Modified, p, o, n))
		}
	}
	for _, p := range old.Paths() {
		if !seen[p] {
			out = append(out, change(Removed, p, old.Files[p], File{}))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func change(k Kind, p string, o, n File) Change {
	c := Change{Kind: k, Path: p, Old: body(o), New: body(n)}
	switch {
	case n.Omitted == "too-large" || o.Omitted == "too-large":
		c.Note = "file is too large; its content is not kept in the snapshot"
	case n.Omitted == "binary" || o.Omitted == "binary":
		c.Note = "binary file; its content is not kept in the snapshot"
	case n.Link || o.Link:
		c.Note = "symlink"
	}
	return c
}

func body(f File) []byte {
	if f.Hash == "" || f.Omitted != "" {
		return nil
	}
	if f.Link {
		return []byte("-> " + f.Text + "\n")
	}
	return []byte(f.Text)
}
