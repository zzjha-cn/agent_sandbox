// Package assets 打包 sbx 运行需要的静态文件：Dockerfile、hooks 脚本、squid 模板、白名单预设。
package assets

import "embed"

//go:embed profiles agent-layer allowlist proxy
var FS embed.FS

// Read 读取一个内嵌文件，不存在时 panic（内嵌文件缺失属于构建错误）。
func Read(path string) []byte {
	b, err := FS.ReadFile(path)
	if err != nil {
		panic("assets: " + err.Error())
	}
	return b
}
