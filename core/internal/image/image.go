// Package image 构建派生镜像：Profile + Agent 层 → sbx/<profile>:<hash>（ADR 0007）。
package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sandx/assets"
	"sandx/internal/docker"
)

// Inputs 决定派生镜像的 hash。
// ProfileFile 和 BaseImage 二选一：前者是要构建的 Dockerfile（内置 Profile 或
// <repo>/.sbx/Dockerfile），后者是直接拿来用的现成镜像（配置里的 image，M3-2）。
type Inputs struct {
	Profile       string
	ProfileFile   []byte // Profile 的 Dockerfile
	BaseImage     string // 直接用这个镜像当底，不构建 Profile 层
	AgentFile     []byte // Agent 层 Dockerfile
	Entrypoint    []byte
	ClaudeVersion string
	UID, GID      int
}

// CustomInputs 用一份自备的 Dockerfile 当 Profile（<repo>/.sbx/Dockerfile，M3-2）。
// name 只用于镜像 tag，内容变了 hash 就变，所以不会和内置 Profile 撞车。
func CustomInputs(name string, dockerfile []byte, claudeVersion string, uid, gid int) Inputs {
	return Inputs{
		Profile:       name,
		ProfileFile:   dockerfile,
		AgentFile:     assets.Read("agent-layer/Dockerfile"),
		Entrypoint:    assets.Read("agent-layer/entrypoint.sh"),
		ClaudeVersion: claudeVersion,
		UID:           uid,
		GID:           gid,
	}
}

// ImageInputs 直接拿一个现成镜像当底（配置里的 image，M3-2）。
func ImageInputs(ref, claudeVersion string, uid, gid int) Inputs {
	return Inputs{
		Profile:       "custom",
		BaseImage:     ref,
		AgentFile:     assets.Read("agent-layer/Dockerfile"),
		Entrypoint:    assets.Read("agent-layer/entrypoint.sh"),
		ClaudeVersion: claudeVersion,
		UID:           uid,
		GID:           gid,
	}
}

// BuiltinInputs 返回内置 Profile 的输入。
func BuiltinInputs(profile, claudeVersion string, uid, gid int) (Inputs, error) {
	pf, err := assets.FS.ReadFile("profiles/" + profile + "/Dockerfile")
	if err != nil {
		return Inputs{}, fmt.Errorf("unknown built-in profile %q", profile)
	}
	return Inputs{
		Profile:       profile,
		ProfileFile:   pf,
		AgentFile:     assets.Read("agent-layer/Dockerfile"),
		Entrypoint:    assets.Read("agent-layer/entrypoint.sh"),
		ClaudeVersion: claudeVersion,
		UID:           uid,
		GID:           gid,
	}, nil
}

func hash12(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		// 带长度前缀，避免拼接歧义
		fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ProfileTag 是 Profile 层镜像的 tag。用现成镜像时就是那个镜像本身。
func (in Inputs) ProfileTag() string {
	if in.BaseImage != "" {
		return in.BaseImage
	}
	return "sbx/profile-" + in.Profile + ":" + hash12(in.ProfileFile)
}

// Tag 是派生镜像的 tag。
func (in Inputs) Tag() string {
	h := hash12(in.ProfileFile, []byte(in.BaseImage), in.AgentFile, in.Entrypoint, []byte(in.ClaudeVersion),
		[]byte(strconv.Itoa(in.UID)), []byte(strconv.Itoa(in.GID)))
	return "sbx/" + in.Profile + ":" + h
}

// Builder 负责确保镜像存在。
type Builder struct {
	Docker   *docker.Client
	Upstream string // http://host:port，构建时的代理；空表示直连
	Out      io.Writer
}

// Ensure 确保派生镜像存在，返回 tag。已存在时立即返回。
func (b Builder) Ensure(in Inputs) (string, error) {
	tag := in.Tag()
	if ok, err := b.Docker.ImageExists(tag); err != nil || ok {
		return tag, err
	}
	ptag, err := b.EnsureProfile(in)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(b.Out, "building the agent layer %s ...\n", tag)
	files := map[string][]byte{"Dockerfile": in.AgentFile, "entrypoint.sh": in.Entrypoint}
	args := map[string]string{
		"BASE":           ptag,
		"UID":            strconv.Itoa(in.UID),
		"GID":            strconv.Itoa(in.GID),
		"CLAUDE_VERSION": in.ClaudeVersion,
	}
	if err := b.build(tag, files, args); err != nil {
		return "", err
	}
	return tag, nil
}

// EnsureProfile 确保 Profile 层镜像存在，返回 tag。
func (b Builder) EnsureProfile(in Inputs) (string, error) {
	ptag := in.ProfileTag()
	if in.BaseImage != "" {
		// 现成镜像：本地没有就拉一次，不构建
		if ok, err := b.Docker.ImageExists(ptag); err != nil {
			return "", err
		} else if !ok {
			fmt.Fprintf(b.Out, "pulling image %s ...\n", ptag)
			if _, err := b.Docker.Run("pull", ptag); err != nil {
				return "", fmt.Errorf("cannot pull image %s: %w", ptag, err)
			}
		}
		return ptag, b.checkBase(ptag)
	}
	if ok, err := b.Docker.ImageExists(ptag); err != nil || ok {
		if err == nil {
			return ptag, b.checkBase(ptag)
		}
		return ptag, err
	}
	fmt.Fprintf(b.Out, "building the profile image %s ...\n", ptag)
	if err := b.build(ptag, map[string][]byte{"Dockerfile": in.ProfileFile}, nil); err != nil {
		return "", err
	}
	return ptag, b.checkBase(ptag)
}

// checkBase 确认底层镜像是 Debian 或 Ubuntu 系（ADR 0007）。
// Agent 层要用 apt-get 装东西、用 useradd 建用户，换成 Alpine 之类会在构建中途
// 以难懂的方式失败，不如在这里说清楚。
// Node 不在这里查：claude 是 npm 包，Node 因此属于 Agent 层，底层没有它会自己装。
func (b Builder) checkBase(ref string) error {
	// 末尾 exit 0：镜像里没有某个命令会让整条命令非 0 退出，那会被当成
	// "镜像跑不起来" 而放行，正好漏掉要查的情况
	out, err := b.Docker.Run("run", "--rm", "--entrypoint", "sh", ref, "-c",
		"cat /etc/os-release 2>/dev/null; exit 0")
	if err != nil {
		// 跑不起来的镜像后面构建时一样会失败，这里不拦
		return nil
	}
	low := strings.ToLower(out)
	if strings.Contains(low, "debian") || strings.Contains(low, "ubuntu") {
		return nil
	}
	name := "(cannot read /etc/os-release)"
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			name = strings.Trim(v, `"`)
		}
	}
	return fmt.Errorf("%s is not a Debian/Ubuntu-based image: %s. The sbx agent layer needs apt-get and useradd (ADR 0007)", ref, name)
}

// build 在临时目录里准备构建上下文，失败时重试 1 次（M0-4：偶发 TLS EOF）。
func (b Builder) build(tag string, files map[string][]byte, args map[string]string) error {
	dir, err := os.MkdirTemp("", "sbx-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	all := map[string]string{}
	for k, v := range args {
		all[k] = v
	}
	if b.Upstream != "" {
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			all[k] = b.Upstream
		}
	}
	spec := docker.BuildSpec{ContextDir: dir, Dockerfile: filepath.Join(dir, "Dockerfile"), Tag: tag,
		BuildArgs: all, Labels: map[string]string{"sbx.kind": "image"}}
	err = b.Docker.Build(spec, b.Out, b.Out)
	if err != nil {
		fmt.Fprintf(b.Out, "build failed, retrying once: %v\n", err)
		err = b.Docker.Build(spec, b.Out, b.Out)
	}
	return err
}
