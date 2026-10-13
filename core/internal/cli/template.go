package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"sandx/assets"
)

// 模板的注释语言。只影响 sbx 写出来的模板文件里的注释——程序自己的输出一律英文。
// 模板不一样：它是给人逐行读的，也是 agent 照着填的 ground truth，读不顺就等于没写。
const (
	langEN = "en"
	langZH = "zh"
)

// 两份随仓库分发的配置模板。路径里的 %s 是语言码。
const (
	projectTemplate = "templates/sandbox.%s.toml.tmpl"
	forwardTemplate = "templates/forward.%s.toml.tmpl"
)

// langFlag 给所有写模板的命令挂同一个 --lang。
func langFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "lang", langEN, "comment language of the template: en | zh")
}

// templateAsset 取出某个模板的某个语言版本。名字里的 %s 换成语言码。
func templateAsset(pattern, lang string) ([]byte, error) {
	switch lang {
	case langEN, langZH:
	default:
		return nil, fmt.Errorf("unknown --lang %q: it is either %s or %s", lang, langEN, langZH)
	}
	return assets.Read(fmt.Sprintf(pattern, lang)), nil
}

// writeTemplate 把模板写到 path。已存在且没有 --force 就报错而不是覆盖：
// 模板文件一旦写过就多半被人改过了，覆盖是会丢东西的操作。
func writeTemplate(path, pattern, lang string, force bool) error {
	b, err := templateAsset(pattern, lang)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
