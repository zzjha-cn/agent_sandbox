package proxy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"sandx/internal/docker"
	"sandx/internal/fsutil"
)

// Dedicated 是一个 Task 独占的 squid sidecar（design §6.2、M2-7）。
// 和 shared 的区别只有三处：实例是一个 Task 一个、不做代理认证（能连上它的
// 只有这个 Task 的 internal 网络）、整份配置写在 Task 自己的 state 目录里。
// 生命周期跟着 Task 走：run 创建，stop 停，done 删。
type Dedicated struct {
	Docker *docker.Client
	Dir    string // <state>/<ws>/<task>/proxy，整个目录只读挂到 /etc/sbx
	Name   string // sbx-<ws>-<task>-proxy
	Agent  string // agent 容器名，StopIfIdle 用
	TaskID string // <ws>.<task>，只用于给 access.log 的条目补上归属
	Labels map[string]string

	UpstreamHost string
	UpstreamPort string
	Egress       string // 为空时用 sbx-egress（测试里换成独立的名字）
}

func (d Dedicated) egress() string {
	if d.Egress != "" {
		return d.Egress
	}
	return EgressNet
}

func (d Dedicated) path(name string) string { return filepath.Join(d.Dir, name) }

// Ensure 只保证 sbx-egress 网络在。实例本身要等 AttachTask 拿到 TaskSpec 才能建
// （配置是渲染出来的，建容器时就要挂进去）。
func (d Dedicated) Ensure() error {
	if ok, err := d.Docker.NetworkExists(d.egress()); err != nil || ok {
		return err
	}
	return d.Docker.NetworkCreate(d.egress(), false, map[string]string{"sbx.kind": "net"})
}

// write 落盘整份配置，返回配置是否有变化（没变就不用 reconfigure）。
func (d Dedicated) write(t TaskSpec) (changed bool, err error) {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return false, err
	}
	block := len(t.Block) > 0
	files := map[string][]byte{
		"allow.txt":  joinLines(t.Allow),
		"block.txt":  joinLines(t.Block),
		"squid.conf": RenderDedicated(t.TaskID, block, t.Open, d.UpstreamHost, d.UpstreamPort),
	}
	for name, want := range files {
		old, _ := os.ReadFile(d.path(name))
		if string(old) == string(want) {
			continue
		}
		if err := fsutil.AtomicWrite(d.path(name), want, 0o644); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// AttachTask 写配置、建（或启动）sidecar 并接入两个网络，返回代理地址。
// 已经在跑且配置变了就热加载——和 shared 一样不需要重启 Task。
func (d Dedicated) AttachTask(t TaskSpec) (string, error) {
	changed, err := d.write(t)
	if err != nil {
		return "", err
	}
	st, exists, err := d.Docker.Inspect(d.Name)
	if err != nil {
		return "", err
	}
	switch {
	case !exists:
		labels := map[string]string{"sbx.kind": "proxy", "sbx.proxy": "dedicated"}
		for k, v := range d.Labels {
			labels[k] = v
		}
		spec := docker.RunSpec{
			Name:       d.Name,
			Image:      Image,
			Labels:     labels,
			Network:    t.Network,
			Aliases:    []string{"proxy"},
			Mounts:     []docker.Mount{{Source: d.Dir, Target: "/etc/sbx", ReadOnly: true}},
			Resources:  docker.Resources{Memory: "128m"},
			LogMaxSize: "10m",
			// 和 shared 一样：被 SIGKILL 过的话 PID 文件会残留，start 时 squid 拒绝启动
			Entrypoint: "sh",
			Cmd:        []string{"-c", "rm -f /run/squid.pid; exec squid -f " + confPath + " -NYC"},
		}
		if runtime.GOOS == "linux" {
			spec.AddHosts = []string{"host.docker.internal:host-gateway"}
		}
		if _, err := d.Docker.RunContainer(spec); err != nil {
			return "", err
		}
		// Task 的网络是 internal 的，出网要靠 sbx-egress
		if err := d.Docker.NetworkConnect(d.egress(), d.Name, ""); err != nil {
			return "", err
		}
	case !st.Running:
		if err := d.Docker.Start(d.Name); err != nil {
			return "", err
		}
	case changed:
		if err := d.reconfigure(); err != nil {
			return "", err
		}
	}
	if err := d.waitReady(); err != nil {
		return "", err
	}
	return "http://proxy:3128", d.rotateIfLarge(logLimit)
}

// DetachTask 删掉 sidecar 和它的配置目录。network 参数只为和 shared 对齐：
// 容器一删，网络连接跟着没了。
func (d Dedicated) DetachTask(taskID, network string) error {
	if err := d.Docker.Rm(d.Name); err != nil && !docker.IsNotFound(err) {
		return err
	}
	if err := os.RemoveAll(d.Dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// StopIfIdle 在这个 Task 的 agent 容器没在跑时停掉 sidecar（不删除）。
func (d Dedicated) StopIfIdle() (bool, error) {
	if d.Agent != "" {
		st, exists, err := d.Docker.Inspect(d.Agent)
		if err != nil {
			return false, err
		}
		if exists && st.Running {
			return false, nil
		}
	}
	st, exists, err := d.Docker.Inspect(d.Name)
	if err != nil || !exists || !st.Running {
		return false, err
	}
	return true, d.Docker.Stop(d.Name)
}

// AccessLog 读出这个实例的日志。没有代理认证，日志里的 %un 恒为 "-"，
// 这里按实例归属补上 TaskID，上层的筛选和分类就不用分两套。
func (d Dedicated) AccessLog() ([]Entry, error) {
	out, err := d.Docker.Exec(d.Name, docker.ExecOpts{},
		"sh", "-c", "cat "+logPath+".0 2>/dev/null; cat "+logPath+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	entries := ParseAccessLog(strings.NewReader(out))
	for i := range entries {
		if entries[i].TaskID == "" || entries[i].TaskID == "-" {
			entries[i].TaskID = d.TaskID
		}
	}
	return entries, nil
}

func (d Dedicated) reconfigure() error {
	out, err := d.Docker.Exec(d.Name, docker.ExecOpts{}, "sh", "-c",
		"squid -f "+confPath+" -k parse 2>&1 && squid -f "+confPath+" -k reconfigure 2>&1")
	if err != nil {
		return fmt.Errorf("squid reconfigure 失败：%w\n%s", err, out)
	}
	if m := confErr.FindAllString(out, -1); len(m) > 0 {
		return fmt.Errorf("squid 配置有错误：\n%s", strings.Join(m, "\n"))
	}
	return nil
}

func (d Dedicated) waitReady() error {
	var last error
	for i := 0; i < 40; i++ {
		st, _, err := d.Docker.Inspect(d.Name)
		if err == nil && !st.Running {
			return fmt.Errorf("%s 启动后退出了：\n%s", d.Name, d.Docker.Logs(d.Name, 20))
		}
		if _, last = d.Docker.Exec(d.Name, docker.ExecOpts{}, "squid", "-f", confPath, "-k", "check"); last == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("%s 未就绪：%w", d.Name, last)
}

func (d Dedicated) rotateIfLarge(limit int64) error {
	script := fmt.Sprintf(`f=%s; [ "$(stat -c %%s "$f" 2>/dev/null || echo 0)" -gt %d ] || exit 0; squid -f %s -k rotate`, logPath, limit, confPath)
	if _, err := d.Docker.Exec(d.Name, docker.ExecOpts{}, "sh", "-c", script); err != nil {
		return fmt.Errorf("squid 日志轮转失败：%w", err)
	}
	return nil
}
