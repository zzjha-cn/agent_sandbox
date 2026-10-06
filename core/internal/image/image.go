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

	"sandx/assets"
	"sandx/internal/docker"
)

// Inputs 决定派生镜像的 hash。
type Inputs struct {
	Profile       string
	ProfileFile   []byte // Profile 的 Dockerfile
	AgentFile     []byte // Agent 层 Dockerfile
	Entrypoint    []byte
	ClaudeVersion string
	UID, GID      int
}

// BuiltinInputs 返回内置 Profile 的输入。
func BuiltinInputs(profile, claudeVersion string, uid, gid int) (Inputs, error) {
	pf, err := assets.FS.ReadFile("profiles/" + profile + "/Dockerfile")
	if err != nil {
		return Inputs{}, fmt.Errorf("未知的内置 profile %q", profile)
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

// ProfileTag 是 Profile 层镜像的 tag。
func (in Inputs) ProfileTag() string {
	return "sbx/profile-" + in.Profile + ":" + hash12(in.ProfileFile)
}

// Tag 是派生镜像的 tag。
func (in Inputs) Tag() string {
	h := hash12(in.ProfileFile, in.AgentFile, in.Entrypoint, []byte(in.ClaudeVersion),
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
	ptag := in.ProfileTag()
	if ok, err := b.Docker.ImageExists(ptag); err != nil {
		return "", err
	} else if !ok {
		fmt.Fprintf(b.Out, "构建 Profile 镜像 %s …\n", ptag)
		if err := b.build(ptag, map[string][]byte{"Dockerfile": in.ProfileFile}, nil); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(b.Out, "构建 Agent 层 %s …\n", tag)
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
		fmt.Fprintf(b.Out, "构建失败，重试一次：%v\n", err)
		err = b.Docker.Build(spec, b.Out, b.Out)
	}
	return err
}
