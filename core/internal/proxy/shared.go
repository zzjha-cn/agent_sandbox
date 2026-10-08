package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"sandx/internal/docker"
	"sandx/internal/fsutil"
)

const (
	SharedName = "sbx-proxy"
	EgressNet  = "sbx-egress"
	Image      = "ubuntu/squid:latest"
	confPath   = "/etc/sbx/squid.conf"
)

// Shared 管理全局唯一的 sbx-proxy。Dir 是宿主机上的 ~/.sbx/proxy，整个目录只读挂载到 /etc/sbx。
type Shared struct {
	Docker       *docker.Client
	Dir          string
	UpstreamHost string
	UpstreamPort string
	// Name/Egress 为空时用 sbx-proxy / sbx-egress（测试里换成独立的名字）
	Name   string
	Egress string
}

func (s Shared) name() string {
	if s.Name != "" {
		return s.Name
	}
	return SharedName
}

func (s Shared) egress() string {
	if s.Egress != "" {
		return s.Egress
	}
	return EgressNet
}

// Egress 是一个 Task 的出网代理。shared 下是全局共用的 sbx-proxy，dedicated 下是
// 这个 Task 独占的 sidecar（design §6.2）；run / stop / done / net 只认这组操作，
// 不关心背后是哪一种。
type Egress interface {
	Ensure() error
	AttachTask(TaskSpec) (string, error)
	DetachTask(taskID, network string) error
	StopIfIdle() (stopped bool, err error)
	AccessLog() ([]Entry, error)
}

var _, _ Egress = Shared{}, Dedicated{}

// TaskSpec 描述一个接入 proxy 的 Task。
type TaskSpec struct {
	TaskID   string // <ws>.<task>
	Network  string // Task 的 internal 网络
	CredFile string // state/<ws>/<task>/proxy.cred
	Allow    []string
	Block    []string // 策略拦截层；cloud_mcp=true 时为空
	Open     bool
}

// lock 串行化对 proxy 目录的修改（多个 sbx 进程并发 run/done 时）。
func (s Shared) lock() (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (s Shared) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

// writeBase 写主配置、占位片段和空 passwd，返回主配置是否有变化。
func (s Shared) writeBase() (changed bool, err error) {
	conf := RenderMain(s.UpstreamHost, s.UpstreamPort)
	old, _ := os.ReadFile(s.path("squid.conf"))
	if string(old) != string(conf) {
		if err := fsutil.AtomicWrite(s.path("squid.conf"), conf, 0o644); err != nil {
			return false, err
		}
		changed = old != nil
	}
	// include 的通配符至少要匹配到一个文件
	if _, err := os.Stat(s.path("tasks", "00-empty.conf")); err != nil {
		if err := fsutil.AtomicWrite(s.path("tasks", "00-empty.conf"), []byte("# placeholder\n"), 0o644); err != nil {
			return false, err
		}
	}
	for _, d := range []string{"allow", "block"} {
		if err := os.MkdirAll(s.path(d), 0o755); err != nil {
			return false, err
		}
	}
	if _, err := os.Stat(s.path("passwd")); err != nil {
		if err := fsutil.AtomicWrite(s.path("passwd"), nil, 0o644); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// Ensure 确保 sbx-egress 网络和 sbx-proxy 容器存在并在运行。
func (s Shared) Ensure() error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	changed, err := s.writeBase()
	if err != nil {
		return err
	}
	if ok, err := s.Docker.NetworkExists(s.egress()); err != nil {
		return err
	} else if !ok {
		if err := s.Docker.NetworkCreate(s.egress(), false, map[string]string{"sbx.kind": "net"}); err != nil {
			return err
		}
	}
	st, exists, err := s.Docker.Inspect(s.name())
	if err != nil {
		return err
	}
	switch {
	case !exists:
		spec := docker.RunSpec{
			Name:       s.name(),
			Image:      Image,
			Labels:     map[string]string{"sbx.kind": "proxy", "sbx.proxy": "shared"},
			Network:    s.egress(),
			Mounts:     []docker.Mount{{Source: s.Dir, Target: "/etc/sbx", ReadOnly: true}},
			Resources:  docker.Resources{Memory: "128m"},
			LogMaxSize: "10m",
			// docker stop 超时被 SIGKILL 后 PID 文件会残留，下次 start 时 squid 拒绝启动
			Entrypoint: "sh",
			Cmd:        []string{"-c", "rm -f /run/squid.pid; exec squid -f " + confPath + " -NYC"},
		}
		if runtime.GOOS == "linux" {
			spec.AddHosts = []string{"host.docker.internal:host-gateway"}
		}
		if _, err := s.Docker.RunContainer(spec); err != nil {
			return err
		}
	case !st.Running:
		if err := s.Docker.Start(s.name()); err != nil {
			return err
		}
	case changed:
		if err := s.reconfigure(); err != nil {
			return err
		}
	}
	if err := s.waitReady(); err != nil {
		return err
	}
	return s.rotateIfLarge(logLimit)
}

// logLimit 是 access.log 的轮转阈值（logfile_rotate 1，最多占 2 倍）。
const logLimit = 20 << 20

// rotateIfLarge 在 access.log 超过 limit 字节时让 squid 轮转日志。每次 run/resume 都会经过这里。
func (s Shared) rotateIfLarge(limit int64) error {
	script := fmt.Sprintf(`f=/var/log/squid/access.log; [ "$(stat -c %%s "$f" 2>/dev/null || echo 0)" -gt %d ] || exit 0; squid -f %s -k rotate`, limit, confPath)
	if _, err := s.Docker.Exec(s.name(), docker.ExecOpts{}, "sh", "-c", script); err != nil {
		return fmt.Errorf("squid 日志轮转失败：%w", err)
	}
	return nil
}

func (s Shared) waitReady() error {
	var last error
	for i := 0; i < 40; i++ {
		st, _, err := s.Docker.Inspect(s.name())
		if err == nil && !st.Running {
			logs := s.Docker.Logs(s.name(), 20)
			return fmt.Errorf("%s 启动后退出了：\n%s", s.name(), logs)
		}
		if _, last = s.Docker.Exec(s.name(), docker.ExecOpts{}, "squid", "-f", confPath, "-k", "check"); last == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s 未就绪：%w", s.name(), last)
}

var confErr = regexp.MustCompile(`(?m)^.*(ERROR|FATAL).*$`)

// reconfigure 先 parse 检查配置，再热加载。输出里有 ERROR/FATAL 时报错。
func (s Shared) reconfigure() error {
	out, err := s.Docker.Exec(s.name(), docker.ExecOpts{}, "sh", "-c",
		"squid -f "+confPath+" -k parse 2>&1 && squid -f "+confPath+" -k reconfigure 2>&1")
	if err != nil {
		return fmt.Errorf("squid reconfigure 失败：%w\n%s", err, out)
	}
	if m := confErr.FindAllString(out, -1); len(m) > 0 {
		return fmt.Errorf("squid 配置有错误：\n%s", strings.Join(m, "\n"))
	}
	return nil
}

// loadToken 读取或生成 Task 的代理 token（32 字节随机数，hex）。
func loadToken(credFile string) (string, error) {
	b, err := os.ReadFile(credFile)
	if err == nil && len(strings.TrimSpace(string(b))) == 64 {
		return strings.TrimSpace(string(b)), nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	return tok, fsutil.AtomicWrite(credFile, []byte(tok+"\n"), 0o600)
}

// URL 返回 Task 使用的代理地址。
func URL(taskID, token string) string {
	return fmt.Sprintf("http://%s:%s@proxy:3128", taskID, token)
}

func joinLines(l []string) []byte {
	if len(l) == 0 {
		return nil
	}
	return []byte(strings.Join(l, "\n") + "\n")
}

// AttachTask 写入 Task 的白名单、拦截表、片段和凭据，接入 Task 网络并热加载。返回代理地址。
func (s Shared) AttachTask(t TaskSpec) (string, error) {
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	tok, err := loadToken(t.CredFile)
	if err != nil {
		return "", err
	}
	if err := fsutil.AtomicWrite(s.path("allow", t.TaskID+".txt"), joinLines(t.Allow), 0o644); err != nil {
		return "", err
	}
	block := len(t.Block) > 0
	if block {
		if err := fsutil.AtomicWrite(s.path("block", t.TaskID+".txt"), joinLines(t.Block), 0o644); err != nil {
			return "", err
		}
	} else {
		os.Remove(s.path("block", t.TaskID+".txt"))
	}
	if err := fsutil.AtomicWrite(s.path("tasks", t.TaskID+".conf"), RenderTask(t.TaskID, block, t.Open), 0o644); err != nil {
		return "", err
	}
	old, _ := os.ReadFile(s.path("passwd"))
	pw := upsertPasswd(string(old), t.TaskID, apr1(tok, randomSalt()))
	if err := fsutil.AtomicWrite(s.path("passwd"), []byte(pw), 0o644); err != nil {
		return "", err
	}
	if err := s.Docker.NetworkConnect(t.Network, s.name(), "proxy"); err != nil {
		return "", err
	}
	if err := s.reconfigure(); err != nil {
		return "", err
	}
	return URL(t.TaskID, tok), nil
}

// DetachTask 删除 Task 的片段和凭据，热加载，并从 Task 网络断开。
func (s Shared) DetachTask(taskID, network string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	for _, p := range []string{s.path("tasks", taskID+".conf"), s.path("allow", taskID+".txt"), s.path("block", taskID+".txt")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if old, err := os.ReadFile(s.path("passwd")); err == nil {
		if err := fsutil.AtomicWrite(s.path("passwd"), []byte(upsertPasswd(string(old), taskID, "")), 0o644); err != nil {
			return err
		}
	}
	st, exists, err := s.Docker.Inspect(s.name())
	if err != nil {
		return err
	}
	if exists && st.Running {
		if err := s.reconfigure(); err != nil {
			return err
		}
	}
	if exists && network != "" {
		return s.Docker.NetworkDisconnect(network, s.name())
	}
	return nil
}

// StopIfIdle 在没有运行中的 shared Task 时停掉 sbx-proxy（不删除）。
func (s Shared) StopIfIdle() (stopped bool, err error) {
	items, err := s.Docker.ListByLabel("container", "sbx.role=agent", "sbx.proxy=shared")
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if it.Str("State") == "running" {
			return false, nil
		}
	}
	st, exists, err := s.Docker.Inspect(s.name())
	if err != nil || !exists || !st.Running {
		return false, err
	}
	return true, s.Docker.Stop(s.name())
}
