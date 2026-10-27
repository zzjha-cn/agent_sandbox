package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
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

// safetyKeys 是安全开关：项目层既不能关、也不能开，只能在全局层或工作区层设置（M4-2）。
// 它和上面三条红线是两个概念——那三条按键名的形状来猜，这一条是一份显式登记的清单，
// 按完整的点号 key 精确匹配。新增一项保护机制时，开关要同时登记到这里。
var safetyKeys = map[string]bool{
	"git.protect": true,
	// on_drift = "build" 会让 sbx run 在依赖漂移时自动构建——也就是自动连一次内网。
	// 跟着仓库走的配置不该有这个决定权（boundary.md §2.6）。
	"prep.on_drift": true,
}

// projectDenyPrefixes 是整棵子树都不许出现在项目层的配置，按点号前缀匹配。
// prep.secret 装的是宿主机上的凭据文件路径；写成相对路径时 absPath 拦不住它，
// 所以这里按位置挡，不按形状挡。
var projectDenyPrefixes = []string{
	"isolation.",
	"prep.secret.",
	// forward 开的是真实的洞：它决定你这台机器往哪儿连。跟着仓库走的配置
	// 不该有这个决定权，所以整棵子树只能写在全局层或工作区层（boundary.md §3.3.2）。
	// 和 [[services]] 正好相反——后者只有 image/env/port，没有地址。
	"forward.",
}

// projectExempt 是明确豁免「形状红线」的 key。名字长得像凭据，但内容不是：
// prep.secrets 只有 BuildKit secret 的 id，值存在别处，正是为了能进仓库共享。
var projectExempt = map[string]bool{"prep.secrets": true}

// projectExemptPrefixes 是按前缀豁免形状红线的子树。
// services.env 是伴生容器**自己**的环境变量：POSTGRES_PASSWORD = "dev" 是在给一个
// 用完即弃的空库设密码，不是从宿主机取凭据，值也到不了 Task 之外。不豁免的话
// boundary.md §3.2 的示例写法当场被自己的红线拦下。
var projectExemptPrefixes = []string{"services.env."}

// checkProject 在项目层配置里找密钥类字段和绝对路径，发现就直接报错。
// 走的是通用的 map 解码，不是 Config 结构体：未知字段在这里也要被检查，
// 否则"写个 sbx 还不认识的 api_key"就能绕过去。
func checkProject(data, path string) error {
	return checkRedLines(data, path, "the project layer")
}

// checkRedLines 是红线检查的本体。who 只影响报错措辞——同一套规则也用在 prep 的
// 声明上，那时说"项目层"会让人去翻一个根本不存在的文件。
func checkRedLines(data, path, who string) error {
	var m map[string]any
	if _, err := toml.Decode(data, &m); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var bad []string
	seen := map[string]bool{}
	walkTOML(m, nil, func(key string, v any) {
		last := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			last = key[i+1:]
		}
		for _, p := range projectDenyPrefixes {
			if strings.HasPrefix(key, p) {
				// 整棵子树被挡时只说一次：一条 [[forward]] 有三四个键，
				// 逐键报错会把"这一整节不能写在这里"这句话淹掉。
				msg := fmt.Sprintf("%s* (this whole section can only be set in the %s or %s layer)", p, LayerGlobal, LayerWorkspace)
				if !seen[msg] {
					seen[msg] = true
					bad = append(bad, msg)
				}
				return
			}
		}
		if projectExempt[key] {
			return
		}
		for _, p := range projectExemptPrefixes {
			if strings.HasPrefix(key, p) {
				return
			}
		}
		if safetyKeys[key] {
			bad = append(bad, fmt.Sprintf("%s (this is a safety switch; it can only be set in the %s or %s layer)", key, LayerGlobal, LayerWorkspace))
			return
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
	return fmt.Errorf("%s: %s contains settings that are not allowed:\n  - %s", path, who, strings.Join(bad, "\n  - "))
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
	case []map[string]any:
		// 数组表（[[services]]、[[forward]]）解出来是这个类型，不是 []any。
		// 漏掉这一条的话整棵数组表会原样掉进 default 分支，里面的键一条红线都过不到。
		for _, e := range t {
			walkTOML(e, path, fn)
		}
	default:
		if key != "" {
			fn(key, v)
		}
	}
}

// ManifestPath 是 prep 镜像里那份声明的位置，报错时要指得出来。
// 和 prep.ManifestPath 同值；放在这里是因为 config 不能反过来 import prep。
const ManifestPath = "/sbx/prep.json"

// CheckPrepManifest 用项目层的红线检查 prep 镜像里的 /sbx/prep.json（M4-15）。
// 它和 .sbx/sandbox.toml 一样跟着仓库走、一样受信任确认覆盖，不该有更高权限，
// 所以这里就是把它的字段摊成 TOML 再走同一个 checkProject。
//
// ca 不走这一关：那几个路径是**镜像里**的位置，absPath 那条红线防的是指向宿主机，
// 对镜像内的路径没有意义。ca 的校验在 prep 侧单独做（必须是绝对路径、不含 ..）。
func CheckPrepManifest(allow []string, env map[string]string) error {
	var b strings.Builder
	if len(allow) > 0 {
		q := make([]string, len(allow))
		for i, a := range allow {
			q[i] = strconv.Quote(a)
		}
		fmt.Fprintf(&b, "allow = [%s]\n", strings.Join(q, ", "))
	}
	if len(env) > 0 {
		b.WriteString("[env]\n")
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%q = %q\n", k, env[k])
		}
	}
	return checkRedLines(b.String(), ManifestPath, "this declaration (it is merged with project-layer privileges)")
}
