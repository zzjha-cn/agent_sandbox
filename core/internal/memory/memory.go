// Package memory 在宿主机和沙箱（sbx-home）之间同步 Claude 的项目自动记忆（ADR 0016）。
//
// 两边各有一份 ~/.claude/projects/<key>/memory/。sbx run 时把宿主机的记忆导入沙箱，
// sbx memory pull 把沙箱新记的导回宿主机。用 ~/.sbx/memory/<key>.json 记录上次两边一致时
// 每个文件的 hash，做三方比较：只有一边改过就取改过的一边，两边都改过算冲突。
// MEMORY.md 是索引，冲突时按行取并集。
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sandx/internal/fsutil"
)

// Index 是记忆目录里的索引文件，冲突时按行合并。
const Index = "MEMORY.md"

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// Key 是 Claude 用来区分项目的目录名：路径里的非字母数字字符都换成 -。
// worktree 里的 .git 指回主仓库，所以同一个仓库的所有 Task 共用主仓库的 key。
func Key(repoRoot string) string { return nonAlnum.ReplaceAllString(repoRoot, "-") }

// SandboxDir 是沙箱里的记忆目录。
func SandboxDir(key string) string { return "/home/agent/.claude/projects/" + key + "/memory" }

// HostDir 是宿主机上的记忆目录。
func HostDir(hostClaude, key string) string {
	return filepath.Join(hostClaude, "projects", key, "memory")
}

// Files 是一个记忆目录的内容：文件名 → 内容（只含顶层普通文件）。
type Files map[string][]byte

// Base 记录上次两边一致时每个文件的 hash。
type Base map[string]string

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Action 是同步计划里的一项。
type Action struct {
	File string
	Kind string // add | update | merge | keep-dst（目标改过，保留）| conflict | same | skip-deleted
	Data []byte // add/update/merge 时要写入目标的内容
}

// Plan 计算把 src 同步到 dst 的动作，并返回同步后的 base。只写 dst，不删除任何文件。
func Plan(src, dst Files, base Base) ([]Action, Base) {
	next := Base{}
	for k, v := range base {
		next[k] = v
	}
	names := make([]string, 0, len(src))
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	var acts []Action
	for _, n := range names {
		s := src[n]
		hs, hb := hash(s), base[n]
		d, inDst := dst[n]
		switch {
		case !inDst && hb != "" && hb == hs:
			// 目标那边删掉了，源这边自上次同步后没改过：尊重删除
			acts = append(acts, Action{File: n, Kind: "skip-deleted"})
			delete(next, n)
		case !inDst:
			acts = append(acts, Action{File: n, Kind: "add", Data: s})
			next[n] = hs
		case hash(d) == hs:
			acts = append(acts, Action{File: n, Kind: "same"})
			next[n] = hs
		case hash(d) == hb:
			// 只有源改过
			acts = append(acts, Action{File: n, Kind: "update", Data: s})
			next[n] = hs
		case hs == hb:
			// 只有目标改过
			acts = append(acts, Action{File: n, Kind: "keep-dst"})
		case n == Index:
			m := unionLines(d, s)
			acts = append(acts, Action{File: n, Kind: "merge", Data: m})
			// base 记成源的 hash：反向同步时源（未改）等于 base，会拿到合并结果
			next[n] = hs
		default:
			acts = append(acts, Action{File: n, Kind: "conflict"})
		}
	}
	return acts, next
}

// unionLines 以 a 为基础，把 b 里 a 没有的非空行按顺序追加到末尾。
func unionLines(a, b []byte) []byte {
	have := map[string]bool{}
	for _, l := range strings.Split(string(a), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	out := strings.TrimRight(string(a), "\n")
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !have[t] {
			out += "\n" + l
			have[t] = true
		}
	}
	return []byte(out + "\n")
}

// Changes 只保留需要写入的动作。
func Changes(acts []Action) Files {
	f := Files{}
	for _, a := range acts {
		if a.Data != nil {
			f[a.File] = a.Data
		}
	}
	return f
}

// ReadDir 读取宿主机上的记忆目录；不存在时返回空。
func ReadDir(dir string) (Files, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Files{}, nil
	}
	if err != nil {
		return nil, err
	}
	f := Files{}
	for _, e := range ents {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		f[e.Name()] = b
	}
	return f, nil
}

// WriteDir 原子写入宿主机记忆目录。
func WriteDir(dir string, files Files) error {
	for n, b := range files {
		if err := fsutil.AtomicWrite(filepath.Join(dir, n), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// LoadBase 读取 ~/.sbx/memory/<key>.json。
func LoadBase(path string) (Base, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Base{}, nil
	}
	if err != nil {
		return nil, err
	}
	base := Base{}
	return base, json.Unmarshal(b, &base)
}

func SaveBase(path string, base Base) error {
	b, _ := json.MarshalIndent(base, "", "  ")
	return fsutil.AtomicWrite(path, append(b, '\n'), 0o644)
}
