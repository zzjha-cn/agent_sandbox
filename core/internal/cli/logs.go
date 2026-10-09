package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sandx/internal/task"
)

// runLogLimit 是 run.log 的轮转阈值：超过就切成 run.log.1，只留一份旧的。
const runLogLimit = 10 << 20

// followTick 是 -f 的轮询间隔。
const followTick = 200 * time.Millisecond

// rotateRunLog 在 run.log 超过 limit 时轮转。在宿主机侧做：state 本来就是宿主机目录，
// 不用进容器，也不要求容器在跑。
func rotateRunLog(path string, limit int64) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= limit {
		return nil
	}
	return os.Rename(path, path+".1")
}

func (a *App) logsCmd() *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <task>",
		Short: "Show a headless run's output (sbx run -p); -f follows it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			if tail == 0 && follow {
				tail = 20
			}
			return a.showLogs(t, follow, tail)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output until the task finishes")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "only show the last n lines (default: everything, or 20 with -f)")
	return cmd
}

// showLogs 读宿主机上的 run.log——state 目录本来就是 bind mount，不用进容器，
// 所以容器停了照样看得到（headless 跑完容器就停了，这是常态）。
func (a *App) showLogs(t task.Task, follow bool, tail int) error {
	b, err := os.ReadFile(t.RunLogPath())
	if os.IsNotExist(err) {
		return fmt.Errorf("task %s has no run.log — it was never run with sbx run -p.\nInteractive output lives in tmux: sbx attach %s", t.Name, t.Name)
	}
	if err != nil {
		return err
	}
	if _, err := a.Out.Write(tailLines(b, tail)); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	return followFile(t.RunLogPath(), a.Out, int64(len(b)), func() bool { return a.taskDone(t) }, followTick)
}

// taskDone 报告这个 Task 的 headless 是不是已经结束（容器停了，或者退出码已经写下）。
func (a *App) taskDone(t task.Task) bool {
	if t.RunExit() != nil {
		return true
	}
	st, exists, err := a.Docker.Inspect(t.Container())
	return err == nil && (!exists || !st.Running)
}

// tailLines 返回 b 的最后 n 行；n <= 0 时返回全部。
func tailLines(b []byte, n int) []byte {
	if n <= 0 {
		return b
	}
	s := string(b)
	trimmed := strings.TrimSuffix(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return b
	}
	return []byte(strings.Join(lines[len(lines)-n:], "\n") + "\n")
}

// followFile 从 offset 起把 path 的新增内容写到 out，done() 为真后再读一轮收尾返回。
// 不依赖容器里的 tail：headless 跑完容器就停了，exec 进不去。
func followFile(path string, out io.Writer, offset int64, done func() bool, tick time.Duration) error {
	for {
		finished := done()
		n, err := copyFrom(path, out, offset)
		if err != nil {
			return err
		}
		offset += n
		if finished {
			return nil
		}
		time.Sleep(tick)
	}
}

func copyFrom(path string, out io.Writer, offset int64) (int64, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(out, f)
}
