package cli

import (
	"errors"
	"fmt"

	"sandx/internal/proxy"
	"sandx/internal/task"
)

// egressMode 决定一个 Task 用哪种代理。已经建过的 Task 以 meta 里记的为准：
// 容器和网络是按那个模式建起来的，中途改配置换不掉它，要换得 done 之后重建。
// 没建过的才看合并后的配置。
func (a *App) egressMode(t task.Task) string {
	if meta, ok, _ := t.ReadMeta(); ok && meta.Proxy != "" {
		return meta.Proxy
	}
	return a.Cfg.Network.Proxy
}

// egress 返回这个 Task 该用的代理（design §6.2）。
func (a *App) egress(t task.Task) proxy.Egress {
	return a.egressOf(t, a.egressMode(t))
}

// egressOf 按指定模式取代理。新建 Task 时用这个，直接给生效配置里的模式——
// 这时候 meta 要么没有，要么是上一轮留下的，都不该左右这次的选择。
func (a *App) egressOf(t task.Task, mode string) proxy.Egress {
	if mode == "dedicated" {
		return a.dedicated(t)
	}
	return a.proxy()
}

func (a *App) dedicated(t task.Task) proxy.Dedicated {
	host, port, _ := a.Cfg.Upstream()
	return proxy.Dedicated{
		Docker: a.Docker, Dir: t.ProxyDir(), Name: t.Proxy(), Agent: t.Container(),
		TaskID: a.taskID(t), Labels: t.Labels(),
		UpstreamHost: host, UpstreamPort: port,
	}
}

// taskID 是代理上这个 Task 的身份（shared 下是代理用户名）。建 Task 时记进了 meta，
// 以它为准，这样改了命名规则也不会对不上。
func (a *App) taskID(t task.Task) string {
	if meta, ok, _ := t.ReadMeta(); ok && meta.TaskID != "" {
		return meta.TaskID
	}
	return t.ID()
}

// proxyName 是打印给用户看的代理名字。
func (a *App) proxyName(t task.Task) string {
	if a.egressMode(t) == "dedicated" {
		return t.Proxy()
	}
	return proxy.SharedName
}

// accessLogs 收集 sbx net denied 要分析的日志。指定了 Task 就只读它自己的代理；
// 否则把当前 Workspace 用得上的代理都读一遍（shared 一份，每个 dedicated Task 各一份），
// 读不到的跳过——Task 停了它的 sidecar 也就停了，这不算错。
func (a *App) accessLogs(t *task.Task) ([]proxy.Entry, error) {
	if t != nil {
		es, err := a.egress(*t).AccessLog()
		if err != nil {
			return nil, fmt.Errorf("读不到 %s 的日志（代理没在运行？）：%w", a.proxyName(*t), err)
		}
		return es, nil
	}
	var out []proxy.Entry
	read := false
	if es, err := a.proxy().AccessLog(); err == nil {
		out, read = append(out, es...), true
	}
	names, err := a.taskNames()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		tk, err := a.task(n)
		if err != nil || a.egressMode(tk) != "dedicated" {
			continue
		}
		if es, err := a.dedicated(tk).AccessLog(); err == nil {
			out, read = append(out, es...), true
		}
	}
	if !read {
		return nil, errors.New("读不到任何代理的日志（代理没在运行？）")
	}
	return out, nil
}
