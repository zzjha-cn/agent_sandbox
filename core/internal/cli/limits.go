package cli

import (
	"fmt"
	"sort"
	"strings"

	"sandx/internal/docker"
	"sandx/internal/task"
)

// vmBudget 是允许 max_running × memory 占到 Docker VM 内存的比例，
// 剩下的留给 squid（128m）、镜像层缓存和 VM 自己（design §11、R10）。
//
// R10 原本写的是 0.85，但那样 design §11 推荐的「10GB VM + 3 × 3g」自己就会报警
// （9g > 8.5g），照文档配置的人每次 sbx run 都看到它——一个每次都响的警告等于没有警告。
// 2026-10-09 决定放宽到 0.95：推荐配置安静，真正配过头的（比如 4 × 3g）仍然会响。
const vmBudget = 0.95

// runningAgents 列出当前正在运行的 agent 容器，返回 <ws>.<task> 形式的名字。
//
// 这里**不按 Workspace 过滤**：max_running 要管的是 Docker VM 的内存，
// 而那是所有仓库共用的。限额取自当前 Workspace 的配置，计数是全局的。
func (a *App) runningAgents() ([]string, error) {
	items, err := a.Docker.Running("sbx.role=agent")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range items {
		out = append(out, agentID(it))
	}
	sort.Strings(out)
	return out, nil
}

func agentID(it docker.Item) string {
	ws, name := "", ""
	for _, kv := range strings.Split(it.Str("Labels"), ",") {
		if v, ok := strings.CutPrefix(kv, "sbx.ws="); ok {
			ws = v
		}
		if v, ok := strings.CutPrefix(kv, "sbx.task="); ok {
			name = v
		}
	}
	if ws == "" || name == "" {
		return it.Str("Names")
	}
	return ws + "." + name
}

// checkConcurrency 在即将启动一个容器时拦住超额的那一次。
//
// design §10.1 把这一步画在"Task 不存在"的分支里，但启动一个已停止的 Task
// 同样会多占一份内存，所以这里对两种情况都查。t 自己已经在跑时直接放行
// （sbx run 一个运行中的 Task 只是 attach，不新增占用）。
func (a *App) checkConcurrency(t task.Task) error {
	running, err := a.runningAgents()
	if err != nil {
		// 数不出来不该挡住正常使用；真起不来的话后面的 docker 调用会报错
		a.logf("警告: 统计运行中的 Task 失败，跳过并发检查：%v", err)
		return nil
	}
	return concurrencyError(running, t.ID(), a.Cfg.MaxRunning, a.Cfg.Resources.Memory)
}

// concurrencyError 是 checkConcurrency 的判断部分，不碰 docker。
func concurrencyError(running []string, self string, max int, mem string) error {
	for _, id := range running {
		if id == self {
			return nil
		}
	}
	if len(running) < max {
		return nil
	}
	return fmt.Errorf("已经有 %d 个 Task 在跑，达到 max_running = %d：%s\n"+
		"先 sbx stop 掉一个，或者在 ~/.sbx/config.toml 里调大 max_running（注意内存：每个 Task 上限 %s）",
		len(running), max, strings.Join(running, "、"), mem)
}

// warnMemoryBudget 在 max_running × memory 超出 Docker VM 内存的 85% 时提醒一次（R10）。
// 只是提醒：--memory 是上限不是预留，用不满是常态，不该因此拒绝执行。
func (a *App) warnMemoryBudget() {
	per := a.Cfg.Resources.MemoryBytes()
	if per <= 0 {
		return
	}
	info, err := a.Docker.Info()
	if err != nil {
		return
	}
	total, ok := info["MemTotal"].(float64)
	if !ok || total <= 0 {
		return
	}
	if msg := budgetWarning(a.Cfg.MaxRunning, a.Cfg.Resources.Memory, per, int64(total)); msg != "" {
		a.logf("警告: %s", msg)
	}
}

// budgetWarning 是 warnMemoryBudget 的判断部分，不碰 docker。没超预算时返回空串。
func budgetWarning(maxRunning int, mem string, per, total int64) string {
	if per <= 0 || total <= 0 {
		return ""
	}
	want := int64(maxRunning) * per
	if float64(want) <= float64(total)*vmBudget {
		return ""
	}
	return fmt.Sprintf("max_running(%d) × resources.memory(%s) = %s，超过 Docker VM 内存 %s 的 %.0f%%。"+
		"并发跑满时可能触发 VM 级 OOM；调小 max_running 或 memory，或者把 Docker 的内存调大。",
		maxRunning, mem, humanBytes(want), humanBytes(total), vmBudget*100)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fg", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fm", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d", n)
	}
}
