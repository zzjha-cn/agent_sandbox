package memory

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"path"
	"strings"

	"sandx/internal/docker"
)

// Sandbox 读写 sbx-home 里的记忆目录。Prefix 是执行命令的 docker 参数前缀：
//   - 在运行中的 Task 容器里：exec -i -u agent <容器>
//   - 没有运行中的容器时：run --rm -i --network none -v sbx-home:/home/agent <镜像>
type Sandbox struct {
	Docker *docker.Client
	Prefix []string
}

func ExecIn(c *docker.Client, container string) Sandbox {
	return Sandbox{Docker: c, Prefix: []string{"exec", "-i", "-u", "agent", container}}
}

func OneShot(c *docker.Client, image string) Sandbox {
	return Sandbox{Docker: c, Prefix: []string{"run", "--rm", "-i", "--network", "none",
		"--mount", "type=volume,source=sbx-home,target=/home/agent", image}}
}

func (s Sandbox) args(cmd ...string) []string {
	return append(append([]string{}, s.Prefix...), cmd...)
}

// Read 读取沙箱里的记忆目录（顶层普通文件）。
func (s Sandbox) Read(dir string) (Files, error) {
	out, err := s.Docker.RunBytes(nil, s.args("sh", "-c",
		`[ -d "$1" ] || exit 0; cd "$1" && find . -maxdepth 1 -type f ! -name '.*' -print0 | xargs -0 -r tar cf -`, "sh", dir)...)
	if err != nil {
		return nil, err
	}
	f := Files{}
	if len(out) == 0 {
		return f, nil
	}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		f[path.Base(strings.TrimPrefix(h.Name, "./"))] = b
	}
	return f, nil
}

// Write 把文件写进沙箱里的记忆目录（以 agent 身份，覆盖同名文件）。
func (s Sandbox) Write(dir string, files Files) error {
	if len(files) == 0 {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for n, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		tw.Write(b)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	_, err := s.Docker.RunBytes(&buf, s.args("sh", "-c", `mkdir -p "$1" && tar xf - -C "$1" --no-same-owner`, "sh", dir)...)
	return err
}
