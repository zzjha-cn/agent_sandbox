package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// 项目层的三条红线（M2-2、M3-10、design §9.1）：这一层是跟着仓库走的，clone 下来就会生效，
// 所以它不该决定凭据从哪来、不该指向宿主机上的具体路径，也不该决定沙箱里执行什么命令。
var (
	secretKey = regexp.MustCompile(`(?i)(key|token|secret|password|credential)`)
	absPath   = regexp.MustCompile(`^(/|~/|[A-Za-z]:[\\/])`)
	// execKey 是会让命令在容器里跑起来的字段（on_idle、on_exit……）。
	// 用模式而不是固定名单：以后加 on_start、notify_cmd 也自动被挡住。
	// 信任确认虽然会把 .sbx/ 的改动摊开给人看，但不该指望每个人都读懂一行 shell。
	execKey = regexp.MustCompile(`(?i)^(on_[a-z0-9_]+|.*_(cmd|command|script|hook))$`)
)

// checkProject 在项目层配置里找密钥类字段和绝对路径，发现就直接报错。
// 走的是通用的 map 解码，不是 Config 结构体：未知字段在这里也要被检查，
// 否则"写个 sbx 还不认识的 api_key"就能绕过去。
func checkProject(data, path string) error {
	var m map[string]any
	if _, err := toml.Decode(data, &m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var bad []string
	walkTOML(m, nil, func(key string, v any) {
		last := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			last = key[i+1:]
		}
		if secretKey.MatchString(last) {
			bad = append(bad, fmt.Sprintf("%s (credential-like fields can only be set in the %s or %s layer)", key, LayerGlobal, LayerWorkspace))
			return
		}
		if execKey.MatchString(last) {
			bad = append(bad, fmt.Sprintf("%s (commands that run inside the container can only be set in the %s or %s layer)", key, LayerGlobal, LayerWorkspace))
			return
		}
		if s, ok := v.(string); ok && absPath.MatchString(s) {
			bad = append(bad, fmt.Sprintf("%s = %q (absolute paths can only be set in the %s or %s layer)", key, s, LayerGlobal, LayerWorkspace))
		}
	})
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("%s: the project layer contains settings that are not allowed:\n  - %s", path, strings.Join(bad, "\n  - "))
}

// walkTOML 遍历解码出来的 TOML，对每个标量（含数组里的每一项）调用 fn。
func walkTOML(v any, path []string, fn func(key string, v any)) {
	key := strings.Join(path, ".")
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkTOML(t[k], append(path, k), fn)
		}
	case []any:
		for _, e := range t {
			walkTOML(e, path, fn)
		}
	default:
		if key != "" {
			fn(key, v)
		}
	}
}
