package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"sandx/internal/fsutil"
)

// allowLine 匹配 [network] 段里的 allow = [...] 这一行的开头。
var allowLine = regexp.MustCompile(`^(\s*)allow\s*=\s*\[`)

// AddAllow 把域名写进 config.toml 的 [network].allow，返回真正新增的那些。
// 直接改文本而不是重新序列化整个结构，这样文件里的注释和排版都能保住。
func AddAllow(path string, hosts []string) (added []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	text := string(data)

	cur, err := currentAllow(text)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, h := range cur {
		have[h] = true
	}
	merged := append([]string(nil), cur...)
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || have[h] {
			continue
		}
		have[h] = true
		merged = append(merged, h)
		added = append(added, h)
	}
	if len(added) == 0 {
		return nil, nil
	}
	sort.Strings(merged)

	out, err := writeAllow(text, merged)
	if err != nil {
		return nil, err
	}
	if err := fsutil.AtomicWrite(path, []byte(out), 0o644); err != nil {
		return nil, err
	}
	return added, nil
}

// currentAllow 读出现有的 network.allow。
func currentAllow(text string) ([]string, error) {
	var c struct {
		Network struct {
			Allow []string `toml:"allow"`
		} `toml:"network"`
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if _, err := toml.Decode(text, &c); err != nil {
		return nil, fmt.Errorf("config.toml 解析失败，先手动修好再试：%w", err)
	}
	return c.Network.Allow, nil
}

// writeAllow 把 allow 数组替换（或新增）成一行。
func writeAllow(text string, hosts []string) (string, error) {
	line := "allow = [" + quoteJoin(hosts) + "]"
	lines := strings.Split(text, "\n")

	start, end := sectionRange(lines, "network")
	if start < 0 {
		// 没有 [network] 段：补在文件末尾
		var b strings.Builder
		b.WriteString(strings.TrimRight(text, "\n"))
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[network]\n" + line + "\n")
		return b.String(), nil
	}
	for i := start; i < end; i++ {
		m := allowLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		last, err := arrayEnd(lines, i)
		if err != nil {
			return "", err
		}
		rest := append([]string{m[1] + line}, lines[last+1:]...)
		return strings.Join(append(lines[:i:i], rest...), "\n"), nil
	}
	// 有 [network] 但没有 allow：插在段首
	rest := append([]string{line}, lines[start+1:]...)
	return strings.Join(append(lines[:start+1:start+1], rest...), "\n"), nil
}

// sectionRange 返回 [name] 段的头行下标和结束位置（下一个段头或文件末尾）。
func sectionRange(lines []string, name string) (start, end int) {
	start = -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "[") {
			continue
		}
		if start >= 0 {
			return start, i
		}
		if t == "["+name+"]" {
			start = i
		}
	}
	if start < 0 {
		return -1, -1
	}
	return start, len(lines)
}

// arrayEnd 从 start 行开始找数组的收尾 ]，支持写成多行的数组。
func arrayEnd(lines []string, start int) (int, error) {
	depth := 0
	for i := start; i < len(lines); i++ {
		for _, r := range lines[i] {
			switch r {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return i, nil
				}
			}
		}
	}
	return 0, errors.New("config.toml 里的 allow 数组没有收尾的 ]")
}

func quoteJoin(hosts []string) string {
	q := make([]string, len(hosts))
	for i, h := range hosts {
		q[i] = strconv.Quote(h)
	}
	return strings.Join(q, ", ")
}
