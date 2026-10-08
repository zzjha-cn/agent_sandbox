// Package docker 通过 docker CLI（而不是 SDK）操作容器、网络、volume 和镜像（ADR 0013）。
package docker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Client 执行 docker 命令。Verbose 为 true 时把每条命令打印到 Log。
type Client struct {
	Bin     string
	Verbose bool
	Log     io.Writer
}

func New(verbose bool) *Client {
	return &Client{Bin: "docker", Verbose: verbose, Log: os.Stderr}
}

// Error 带上完整命令行和 stderr，便于排查。
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("docker %s: %v: %s", shellJoin(e.Args), e.Err, strings.TrimSpace(e.Stderr))
}

func (e *Error) Unwrap() error { return e.Err }

func (c *Client) trace(args []string) {
	if c.Verbose && c.Log != nil {
		fmt.Fprintf(c.Log, "+ %s %s\n", c.Bin, shellJoin(args))
	}
}

// Run 执行一条 docker 命令，返回去掉首尾空白的 stdout。
func (c *Client) Run(args ...string) (string, error) {
	return c.RunInput(nil, args...)
}

// RunInput 同 Run，并把 stdin 传给命令。
func (c *Client) RunInput(stdin io.Reader, args ...string) (string, error) {
	c.trace(args)
	cmd := exec.Command(c.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

// RunBytes 执行命令并返回原始 stdout（不去空白，用于 tar 等二进制输出）。
func (c *Client) RunBytes(stdin io.Reader, args ...string) ([]byte, error) {
	c.trace(args)
	cmd := exec.Command(c.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		return out.Bytes(), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.Bytes(), nil
}

// Stream 执行命令并把 stdout/stderr 透传给用户（用于 build）。
func (c *Client) Stream(stdout, stderr io.Writer, args ...string) error {
	c.trace(args)
	cmd := exec.Command(c.Bin, args...)
	var errb bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &tailBuffer{buf: &errb, max: 4096})
	if err := cmd.Run(); err != nil {
		return &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return nil
}

// Interactive 把终端直接交给 docker（attach、shell）。
func (c *Client) Interactive(args ...string) error {
	c.trace(args)
	cmd := exec.Command(c.Bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return &Error{Args: args, Err: err}
	}
	return nil
}

// ---- 容器 ----

func (c *Client) RunContainer(spec RunSpec) (string, error) {
	return c.Run(spec.Args()...)
}

func (c *Client) Start(name string) error { _, err := c.Run("start", name); return err }

func (c *Client) Stop(name string) error { _, err := c.Run("stop", "-t", "10", name); return err }

func (c *Client) Rm(name string) error { _, err := c.Run("rm", "-f", name); return err }

// ExecOpts 是 docker exec 的选项。
type ExecOpts struct {
	User    string
	Workdir string
	Env     []string
	TTY     bool
	Stdin   io.Reader
}

func execArgs(name string, o ExecOpts, cmd []string) []string {
	args := []string{"exec"}
	if o.Stdin != nil {
		args = append(args, "-i")
	}
	if o.TTY {
		args = append(args, "-it")
	}
	if o.User != "" {
		args = append(args, "-u", o.User)
	}
	if o.Workdir != "" {
		args = append(args, "-w", o.Workdir)
	}
	for _, e := range o.Env {
		args = append(args, "-e", e)
	}
	args = append(args, name)
	return append(args, cmd...)
}

func (c *Client) Exec(name string, o ExecOpts, cmd ...string) (string, error) {
	return c.RunInput(o.Stdin, execArgs(name, o, cmd)...)
}

func (c *Client) ExecInteractive(name string, o ExecOpts, cmd ...string) error {
	o.TTY = true
	return c.Interactive(execArgs(name, o, cmd)...)
}

// Logs 返回容器最后 n 行日志（stdout 和 stderr 合并）。
func (c *Client) Logs(name string, n int) string {
	args := []string{"logs", "--tail", fmt.Sprint(n), name}
	c.trace(args)
	out, _ := exec.Command(c.Bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// State 是 docker inspect 里 M1 关心的部分。
type State struct {
	Status    string // created/running/paused/restarting/removing/exited/dead
	Running   bool
	ExitCode  int
	OOMKilled bool
}

// Inspect 返回容器状态；容器不存在时 exists=false 且 err=nil。
func (c *Client) Inspect(name string) (st State, exists bool, err error) {
	out, err := c.Run("container", "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		if isNotFound(err) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return State{}, true, fmt.Errorf("parse inspect: %w", err)
	}
	return st, true, nil
}

// ---- 网络 ----

func (c *Client) NetworkCreate(name string, internal bool, labels map[string]string) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, labelArgs(labels)...)
	_, err := c.Run(append(args, name)...)
	return err
}

func (c *Client) NetworkExists(name string) (bool, error) {
	_, err := c.Run("network", "inspect", "--format", "{{.Name}}", name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *Client) NetworkConnect(network, ctr, alias string) error {
	args := []string{"network", "connect"}
	if alias != "" {
		args = append(args, "--alias", alias)
	}
	_, err := c.Run(append(args, network, ctr)...)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func (c *Client) NetworkDisconnect(network, ctr string) error {
	_, err := c.Run("network", "disconnect", "-f", network, ctr)
	if err != nil && (isNotFound(err) || strings.Contains(err.Error(), "is not connected")) {
		return nil
	}
	return err
}

func (c *Client) NetworkRm(name string) error {
	_, err := c.Run("network", "rm", name)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

// ---- volume ----

// VolumeCreate 创建 volume，返回是否为新建。
func (c *Client) VolumeCreate(name string, labels map[string]string) (created bool, err error) {
	if _, err := c.Run("volume", "inspect", "--format", "{{.Name}}", name); err == nil {
		return false, nil
	} else if !isNotFound(err) {
		return false, err
	}
	args := append([]string{"volume", "create"}, labelArgs(labels)...)
	_, err = c.Run(append(args, name)...)
	return err == nil, err
}

func (c *Client) VolumeRm(name string) error {
	_, err := c.Run("volume", "rm", "-f", name)
	return err
}

// ---- 镜像 ----

func (c *Client) ImageExists(ref string) (bool, error) {
	_, err := c.Run("image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BuildSpec 描述一次 docker build。Dockerfile 通过 stdin 传入时 DockerfileContent 非空。
type BuildSpec struct {
	ContextDir string
	Dockerfile string // 相对 ContextDir 的路径
	Tag        string
	BuildArgs  map[string]string
	Labels     map[string]string
}

func (b BuildSpec) Args() []string {
	args := []string{"build", "-t", b.Tag}
	if b.Dockerfile != "" {
		args = append(args, "-f", b.Dockerfile)
	}
	for _, k := range sortedKeys(b.BuildArgs) {
		args = append(args, "--build-arg", k+"="+b.BuildArgs[k])
	}
	args = append(args, labelArgs(b.Labels)...)
	return append(args, b.ContextDir)
}

func (c *Client) Build(b BuildSpec, stdout, stderr io.Writer) error {
	return c.Stream(stdout, stderr, b.Args()...)
}

// ---- 查询 ----

// Item 是 docker ls --format '{{json .}}' 的一行，字段因对象类型而异。
type Item map[string]any

func (i Item) Str(k string) string { s, _ := i[k].(string); return s }

// ListByLabel 按 label 列出对象。kind 取 container / network / volume。
func (c *Client) ListByLabel(kind string, selectors ...string) ([]Item, error) {
	var args []string
	switch kind {
	case "container":
		args = []string{"ps", "-a"}
	case "network":
		args = []string{"network", "ls"}
	case "volume":
		args = []string{"volume", "ls"}
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	for _, s := range selectors {
		args = append(args, "--filter", "label="+s)
	}
	args = append(args, "--format", "{{json .}}")
	out, err := c.Run(args...)
	if err != nil {
		return nil, err
	}
	return parseItems(out)
}

// parseItems 解析 --format "{{json .}}" 的逐行输出。
func parseItems(out string) ([]Item, error) {
	var items []Item
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var it Item
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("parse docker ls output: %w", err)
		}
		items = append(items, it)
	}
	return items, nil
}

// HasEnv 报告容器的 Config.Env 里有没有这个变量名。
func (c *Client) HasEnv(name, key string) (bool, error) {
	out, err := c.Run("inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			return true, nil
		}
	}
	return false, nil
}

// Info 返回 docker info 的 json。Docker Desktop 下 MemTotal 是那台 VM 的内存，
// 不是宿主机的内存——sbx 的内存预算算的就是 VM。
func (c *Client) Info() (Item, error) {
	out, err := c.Run("info", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var it Item
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &it); err != nil {
		return nil, fmt.Errorf("parse docker info: %w", err)
	}
	return it, nil
}

// Running 列出当前正在运行的、带这些 label 的容器。
func (c *Client) Running(selectors ...string) ([]Item, error) {
	args := []string{"ps"}
	for _, s := range selectors {
		args = append(args, "--filter", "label="+s)
	}
	args = append(args, "--format", "{{json .}}")
	out, err := c.Run(args...)
	if err != nil {
		return nil, err
	}
	return parseItems(out)
}

// ---- helpers ----

func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such") || strings.Contains(s, "not found")
}

// IsNotFound 报告错误是否表示对象不存在。
func IsNotFound(err error) bool { return err != nil && isNotFound(err) }

func labelArgs(labels map[string]string) []string {
	var args []string
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}

func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"$&|;<>*?()[]{}\\`") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

type tailBuffer struct {
	buf *bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.buf.Len() > t.max {
		b := t.buf.Bytes()[t.buf.Len()-t.max:]
		nb := append([]byte(nil), b...)
		t.buf.Reset()
		t.buf.Write(nb)
	}
	return len(p), nil
}
