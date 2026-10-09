package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// apiKey 是从配置解析出来的 API key（M2-13、design §7.1）。Env 为空表示没配。
type apiKey struct {
	Env    string // 注入容器的环境变量名，如 ANTHROPIC_API_KEY
	Value  string
	Source string // 这个值从哪来的，只用于打印，不含 key 本身
}

// apiKey 读取当前 Agent 的 api_key_env / api_key_file。没配时返回零值。
//
// 配了但取不到值要报错，不能静默退回订阅登录：那样会在"以为用的是 API key"
// 的情况下把请求记到订阅账号上。
func (a *App) apiKey(name string) (apiKey, error) {
	if name == "" {
		name = a.Cfg.DefaultAgent
	}
	ag, ok := a.Cfg.Agents[name]
	if !ok {
		return apiKey{}, nil
	}
	cli, ok := authCLIs[name]
	if !ok || cli.keyEnv == "" {
		if ag.APIKeyEnv != "" || ag.APIKeyFile != "" {
			return apiKey{}, fmt.Errorf("agents.%s configures an API key, but sbx does not know which environment variable %s uses", name, name)
		}
		return apiKey{}, nil
	}
	switch {
	case ag.APIKeyEnv != "":
		v := strings.TrimSpace(os.Getenv(ag.APIKeyEnv))
		if v == "" {
			return apiKey{}, fmt.Errorf("agents.%s.api_key_env = %q, but that environment variable is empty on the host", name, ag.APIKeyEnv)
		}
		return apiKey{Env: cli.keyEnv, Value: v, Source: "environment variable " + ag.APIKeyEnv}, nil
	case ag.APIKeyFile != "":
		path := expandHome(ag.APIKeyFile)
		b, err := os.ReadFile(path)
		if err != nil {
			return apiKey{}, fmt.Errorf("cannot read agents.%s.api_key_file: %w", name, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return apiKey{}, fmt.Errorf("agents.%s.api_key_file = %q is an empty file", name, path)
		}
		return apiKey{Env: cli.keyEnv, Value: v, Source: "file " + path}, nil
	}
	return apiKey{}, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
