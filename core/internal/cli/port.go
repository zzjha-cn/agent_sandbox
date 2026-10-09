package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"sandx/internal/docker"
	"sandx/internal/task"
)

// socatImage 是转发容器用的镜像。它只做一件事：把宿主机上的端口转给 Task 容器。
const socatImage = "alpine/socat:latest"

func (a *App) portCmd() *cobra.Command {
	var rm bool
	cmd := &cobra.Command{
		Use:   "port <task> <port>",
		Short: "Expose a port from the task on 127.0.0.1 (--rm takes it back)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.taskArg(args)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if rm {
					return a.removePorts(t, 0)
				}
				return a.listPorts(t)
			}
			port, err := strconv.Atoi(args[1])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("the port must be a number between 1 and 65535: %q", args[1])
			}
			if rm {
				return a.removePorts(t, port)
			}
			return a.addPort(t, port)
		},
	}
	cmd.Flags().BoolVar(&rm, "rm", false, "remove the forwarder for this port (omit the port to remove all of them)")
	return cmd
}

// portContainer 是某个 Task 某个端口的转发容器名。
func portContainer(t task.Task, port int) string {
	return fmt.Sprintf("%s-port-%d", t.Container(), port)
}

// addPort 起一个 socat 转发容器（R7 的决定）：它接在 Task 的 internal 网络上，
// 把宿主机 127.0.0.1 的一个随机空闲端口转给容器里的服务。
//
// 另一个方案是重建 agent 容器加 -p，但那会杀掉正在跑的会话——无人值守场景下
// 不可接受。转发容器可以随时来去，Task 本身完全不受影响。
func (a *App) addPort(t task.Task, port int) error {
	if err := a.requireRunning(t); err != nil {
		return err
	}
	name := portContainer(t, port)
	if st, exists, err := a.Docker.Inspect(name); err != nil {
		return err
	} else if exists {
		if st.Running {
			addr, err := a.portAddr(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "%s is already mapped: %s\n", t.Name, addr)
			return nil
		}
		if err := a.Docker.Rm(name); err != nil && !docker.IsNotFound(err) {
			return err
		}
	}
	labels := t.Labels()
	labels["sbx.kind"] = "port"
	spec := docker.RunSpec{
		Name:   name,
		Image:  socatImage,
		Labels: labels,
		// 先接默认 bridge：Task 的网络是 --internal 的，接在上面 -p 不起作用
		// （docker 不会给只连 internal 网络的容器做端口映射）。
		// 建完再 network connect 到 Task 网络去找那个服务。
		Network: "bridge",
		// 只绑 127.0.0.1：端口默认不对外暴露（design §11）
		Publish: []string{"127.0.0.1::" + strconv.Itoa(port)},
		Cmd: []string{fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
			fmt.Sprintf("TCP:%s:%d", t.Container(), port)},
		Resources:  docker.Resources{Memory: "32m"},
		LogMaxSize: "10m",
	}
	if _, err := a.Docker.RunContainer(spec); err != nil {
		return err
	}
	if err := a.Docker.NetworkConnect(t.Network(), name, ""); err != nil {
		a.Docker.Rm(name)
		return err
	}
	addr, err := a.portAddr(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "task %s: port %d -> %s\n", t.Name, port, addr)
	fmt.Fprintf(a.Out, "take it back with: sbx port %s %d --rm\n", t.Name, port)
	return nil
}

// portAddr 读出 docker 分配到的宿主机端口。
func (a *App) portAddr(name string) (string, error) {
	out, err := a.Docker.Run("port", name)
	if err != nil {
		return "", err
	}
	// 形如 "3000/tcp -> 127.0.0.1:54321"
	for _, line := range strings.Split(out, "\n") {
		if _, addr, ok := strings.Cut(line, "-> "); ok {
			return "http://" + strings.TrimSpace(addr) + "/", nil
		}
	}
	return "", fmt.Errorf("cannot read the port mapping of %s: %s", name, out)
}

func (a *App) listPorts(t task.Task) error {
	items, err := a.Docker.ListByLabel("container", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind=port")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(a.Out, "task %s has no port mappings. Map one with: sbx port %s <port>\n", t.Name, t.Name)
		return nil
	}
	var rows [][]string
	for _, it := range items {
		name := it.Str("Names")
		addr, err := a.portAddr(name)
		if err != nil {
			addr = "(stopped)"
		}
		rows = append(rows, []string{strings.TrimPrefix(name, t.Container()+"-port-"), addr, it.Str("State")})
	}
	return writeTable(a.Out, []string{"PORT", "URL", "STATE"}, rows)
}

func (a *App) removePorts(t task.Task, port int) error {
	var names []string
	if port > 0 {
		names = []string{portContainer(t, port)}
	} else {
		items, err := a.Docker.ListByLabel("container", "sbx.ws="+t.WS.ID, "sbx.task="+t.Name, "sbx.kind=port")
		if err != nil {
			return err
		}
		for _, it := range items {
			names = append(names, it.Str("Names"))
		}
	}
	if len(names) == 0 {
		if port > 0 {
			fmt.Fprintf(a.Out, "task %s: port %d is not mapped\n", t.Name, port)
		}
		return nil
	}
	for _, n := range names {
		_, exists, err := a.Docker.Inspect(n)
		if err != nil {
			return err
		}
		if !exists {
			if port > 0 {
				fmt.Fprintf(a.Out, "task %s: port %d is not mapped\n", t.Name, port)
			}
			continue
		}
		if err := a.Docker.Rm(n); err != nil && !docker.IsNotFound(err) {
			return err
		}
		fmt.Fprintf(a.Out, "took back %s\n", n)
	}
	return nil
}
