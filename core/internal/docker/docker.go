// Package docker 通过 docker CLI（而不是 SDK）操作容器、网络、volume 和镜像（ADR 0013）。
package docker

import (
	"bufio"
	"bytes"
	"context"
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
	ctx     context.Context
	// ResourceLabels are supplied by the validated daemon ownership scope.
	// They apply to persistent resources, never credentials or environment.
	ResourceLabels map[string]string
}

func (c *Client) resourceLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+len(c.ResourceLabels))
	for k, v := range labels {
		out[k] = v
	}
	for k, v := range c.ResourceLabels {
		out[k] = v
	}
	return out
}

func New(verbose bool) *Client {
	return &Client{Bin: "docker", Verbose: verbose, Log: os.Stderr}
}

// WithContext returns a shallow client copy whose commands use ctx. This lets
// one command scope cancellation without mutating the shared CLI client.
func (c *Client) WithContext(ctx context.Context) *Client {
	clone := *c
	clone.ctx = ctx
	return &clone
}

// Context exposes the command deadline to adapters that also take local locks.
func (c *Client) Context() context.Context { return c.commandContext() }

func (c *Client) commandContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
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

func (c *Client) RunContext(ctx context.Context, args ...string) (string, error) {
	return c.RunInputContext(ctx, nil, args...)
}

// RunInput 同 Run，并把 stdin 传给命令。
func (c *Client) RunInput(stdin io.Reader, args ...string) (string, error) {
	return c.RunInputContext(c.commandContext(), stdin, args...)
}

func (c *Client) RunInputContext(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	c.trace(args)
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return strings.TrimSpace(out.String()), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

// RunBytes 执行命令并返回原始 stdout（不去空白，用于 tar 等二进制输出）。
func (c *Client) RunBytes(stdin io.Reader, args ...string) ([]byte, error) {
	return c.RunBytesContext(c.commandContext(), stdin, args...)
}

func (c *Client) RunBytesContext(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	c.trace(args)
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return out.Bytes(), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.Bytes(), nil
}

// Stream 执行命令并把 stdout/stderr 透传给用户（用于 build）。
func (c *Client) Stream(stdout, stderr io.Writer, args ...string) error {
	return c.StreamEnv(nil, stdout, stderr, args...)
}

// StreamEnv 和 Stream 一样，另外把 extraEnv 追加到继承来的环境上（后者覆盖前者）。
func (c *Client) StreamEnv(extraEnv []string, stdout, stderr io.Writer, args ...string) error {
	c.trace(args)
	ctx := c.commandContext()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var errb bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &tailBuffer{buf: &errb, max: 4096})
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return nil
}

// Interactive 把终端直接交给 docker（attach、shell）。
func (c *Client) Interactive(args ...string) error {
	c.trace(args)
	ctx := c.commandContext()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return &Error{Args: args, Err: err}
	}
	return nil
}

// ---- 容器 ----

func (c *Client) RunContainer(spec RunSpec) (string, error) {
	spec.Labels = c.resourceLabels(spec.Labels)
	return c.Run(spec.Args()...)
}

func (c *Client) CreateContainer(spec RunSpec) (string, error) {
	spec.Labels = c.resourceLabels(spec.Labels)
	return c.Run(spec.CreateArgs()...)
}

func (c *Client) RunContainerContext(ctx context.Context, spec RunSpec) (string, error) {
	spec.Labels = c.resourceLabels(spec.Labels)
	return c.RunContext(ctx, spec.Args()...)
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
	out, _ := exec.CommandContext(c.commandContext(), c.Bin, args...).CombinedOutput()
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

// ContainerIP 返回容器在某张网络上的 IP。forwarder 要把这个地址给 Task 容器当
// --add-host，所以必须等容器起来之后再问。
func (c *Client) ContainerIP(name, network string) (string, error) {
	out, err := c.Run("inspect", "-f", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", name)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(out)
	if ip == "" {
		return "", fmt.Errorf("%s has no address on network %s", name, network)
	}
	return ip, nil
}

// ExtraHosts 返回容器创建时 --add-host 给的那些条目（host:ip）。
func (c *Client) ExtraHosts(name string) ([]string, error) {
	out, err := c.Run("inspect", "-f", "{{range .HostConfig.ExtraHosts}}{{println .}}{{end}}", name)
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			hosts = append(hosts, l)
		}
	}
	return hosts, nil
}

// ---- 网络 ----

func (c *Client) NetworkCreate(name string, internal bool, labels map[string]string) error {
	labels = c.resourceLabels(labels)
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

// NetworkConnected inspects only network names, never environment or credentials.
func (c *Client) NetworkConnected(network, container string) (bool, error) {
	out, err := c.Run("container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return false, err
	}
	var networks map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &networks); err != nil {
		return false, err
	}
	_, connected := networks[network]
	return connected, nil
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

// VolumeLabels reads only non-secret ownership metadata, never volume contents.
func (c *Client) VolumeLabels(name string) (map[string]string, bool, error) {
	out, err := c.Run("volume", "inspect", "--format", "{{json .Labels}}", name)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return nil, true, fmt.Errorf("invalid volume labels: %w", err)
	}
	return labels, true, nil
}

// VolumeCreate 创建 volume，返回是否为新建。
func (c *Client) VolumeCreate(name string, labels map[string]string) (created bool, err error) {
	labels = c.resourceLabels(labels)
	if len(c.ResourceLabels) > 0 {
		existing, found, err := c.VolumeLabels(name)
		if err != nil {
			return false, err
		}
		if found {
			return false, c.checkVolumeScope(name, existing)
		}
	} else {
		if _, err := c.Run("volume", "inspect", "--format", "{{.Name}}", name); err == nil {
			return false, nil
		} else if !isNotFound(err) {
			return false, err
		}
	}
	args := append([]string{"volume", "create"}, labelArgs(labels)...)
	_, err = c.Run(append(args, name)...)
	if err == nil && len(c.ResourceLabels) > 0 {
		var actual map[string]string
		var found bool
		actual, found, err = c.VolumeLabels(name)
		if err == nil && !found {
			err = fmt.Errorf("volume disappeared after creation: %s", name)
		}
		if err == nil {
			err = c.checkVolumeScope(name, actual)
		}
	}
	return err == nil, err
}

func (c *Client) checkVolumeScope(name string, labels map[string]string) error {
	labeled := false
	for k := range c.ResourceLabels {
		labeled = labeled || labels[k] != ""
	}
	if !labeled {
		return nil
	}
	for k, expected := range c.ResourceLabels {
		// Legacy unlabeled volumes are retained under an explicitly adopted
		// daemon. A partially labeled or foreign scope is never adopted.
		if labels[k] != expected {
			return fmt.Errorf("volume ownership mismatch: %s", name)
		}
	}
	return nil
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
	// Secrets 是 BuildKit 的 --secret id=<id>,src=<宿主机路径>。
	// 它只在那一条 RUN 的挂载期间可见，不落进任何镜像层——prep 的凭据靠它（M4-7）。
	Secrets map[string]string
	// NoProxy 为 true 时从两条路上都把代理甩掉。Docker Desktop 会把它设置里的代理
	// 注入 docker build，不甩掉的话构建会原样踩一次"全走代理、到不了内网"
	// （boundary.md §2.8）。两条路都要堵：
	//
	//	~/.docker/config.json 的 proxies —— CLI 据此自动加 --build-arg，
	//	                                     显式传空的同名 build-arg 才盖得住
	//	进程环境里的 HTTP_PROXY 等     —— 影响 CLI 自己的出站连接
	NoProxy bool
}

func (b BuildSpec) Args() []string {
	args := []string{"build", "-t", b.Tag}
	if b.Dockerfile != "" {
		args = append(args, "-f", b.Dockerfile)
	}
	for _, k := range sortedKeys(b.BuildArgs) {
		args = append(args, "--build-arg", k+"="+b.BuildArgs[k])
	}
	if b.NoProxy {
		for _, k := range proxyVars {
			args = append(args, "--build-arg", k+"=")
		}
	}
	for _, id := range sortedKeys(b.Secrets) {
		args = append(args, "--secret", "id="+id+",src="+b.Secrets[id])
	}
	args = append(args, labelArgs(b.Labels)...)
	return append(args, b.ContextDir)
}

func (c *Client) Build(b BuildSpec, stdout, stderr io.Writer) error {
	var env []string
	if len(b.Secrets) > 0 {
		env = append(env, "DOCKER_BUILDKIT=1")
	}
	if b.NoProxy {
		env = append(env, clearProxyEnv()...)
	}
	return c.StreamEnv(env, stdout, stderr, b.Args()...)
}

// proxyVars 是 Docker Desktop 可能注入、而 prep 构建必须甩掉的那几个。
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

// clearProxyEnv 返回把代理变量置空的 KEY= 列表。置空而不是删除：
// exec.Cmd 的 Env 是整份替换，用后出现的同名项覆盖前面的即可，不必重建环境。
func clearProxyEnv() []string {
	out := make([]string, 0, len(proxyVars))
	for _, k := range proxyVars {
		out = append(out, k+"=")
	}
	return out
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
