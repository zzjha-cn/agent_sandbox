// Package proxy 管理 shared 模式的 egress proxy（squid）：配置渲染、凭据、Task 接入与摘除（ADR 0005、0014、0015）。
package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"sandx/assets"
)

// ParseList 解析白名单文本：一行一个域名，忽略空行和 # 注释。
func ParseList(text string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.ToLower(strings.TrimSpace(line)); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// BuiltinList 读取内嵌的白名单预设（builtin / web-go / policy-block）。
func BuiltinList(name string) []string {
	b, err := assets.FS.ReadFile("allowlist/" + name + ".txt")
	if err != nil {
		return nil
	}
	return ParseList(string(b))
}

// covered 报告 d 是否已被通配项 w（以 . 开头）覆盖。
func covered(d, w string) bool {
	if d == w {
		return false
	}
	bare := strings.TrimPrefix(d, ".")
	return bare == w[1:] || strings.HasSuffix(bare, w)
}

// RenderAllow 合并各层白名单：取并集、去重（.x.com 已包含 x.com 和 a.x.com）、排序，
// 并移除策略拦截层里的精确条目（通配项覆盖的部分由 deny 规则兜底）。
func RenderAllow(block []string, layers ...[]string) []string {
	set := map[string]bool{}
	for _, l := range layers {
		for _, d := range l {
			d = strings.ToLower(strings.TrimSpace(d))
			if d != "" {
				set[d] = true
			}
		}
	}
	for _, b := range block {
		delete(set, strings.ToLower(b))
		delete(set, "."+strings.TrimPrefix(strings.ToLower(b), "."))
	}
	var wild []string
	for d := range set {
		if strings.HasPrefix(d, ".") {
			wild = append(wild, d)
		}
	}
	var out []string
	for d := range set {
		keep := true
		for _, w := range wild {
			if covered(d, w) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// ACLName 把 task-id 转成合法的 squid ACL 名后缀。
func ACLName(taskID string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(taskID)
}

var (
	mainTmpl = template.Must(template.New("squid").Parse(string(assets.Read("proxy/squid.conf.tmpl"))))
	taskTmpl = template.Must(template.New("task").Parse(string(assets.Read("proxy/task.conf.tmpl"))))
)

// RenderMain 渲染主配置。upstreamHost 为空时不串联上游。
func RenderMain(upstreamHost, upstreamPort string) []byte {
	var b bytes.Buffer
	if err := mainTmpl.Execute(&b, map[string]string{"UpstreamHost": upstreamHost, "UpstreamPort": upstreamPort}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// RenderTask 渲染一个 Task 的 ACL 片段。block 为 true 时引用 block/<task-id>.txt。
func RenderTask(taskID string, block, open bool) []byte {
	var b bytes.Buffer
	err := taskTmpl.Execute(&b, map[string]any{"TaskID": taskID, "ACL": ACLName(taskID), "Block": block, "Open": open})
	if err != nil {
		panic(err)
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// upsertPasswd 在 htpasswd 文本里新增或替换 user 的条目；hash 为空表示删除。
func upsertPasswd(text, user, hash string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, user+":") {
			continue
		}
		out = append(out, line)
	}
	if hash != "" {
		out = append(out, fmt.Sprintf("%s:%s", user, hash))
	}
	sort.Strings(out)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}
