package task

import (
	"testing"
	"time"

	"sandx/internal/docker"
	"sandx/internal/workspace"
)

func TestDerive(t *testing.T) {
	run := docker.State{Status: "running", Running: true}
	code := func(n int) *int { return &n }
	cases := []struct {
		st      docker.State
		exists  bool
		s       *AgentStatus
		runExit *int
		want    string
	}{
		{docker.State{}, false, nil, nil, "absent"},
		{docker.State{Status: "exited", ExitCode: 0}, true, nil, nil, "stopped"},
		{docker.State{Status: "exited", ExitCode: 143}, true, nil, nil, "stopped"},
		{docker.State{Status: "created"}, true, nil, nil, "stopped"},
		{docker.State{Status: "exited", ExitCode: 2}, true, nil, nil, "exited(2)"},
		{docker.State{Status: "exited", ExitCode: 137, OOMKilled: true}, true, nil, nil, "exited(oom)"},
		{run, true, nil, nil, "starting"},
		{run, true, &AgentStatus{State: "idle"}, nil, "idle"},
		{run, true, &AgentStatus{State: "running"}, nil, "running"},
		{run, true, &AgentStatus{State: "exited"}, nil, "exited(agent)"},
		{run, true, &AgentStatus{State: "weird"}, nil, "starting"},
		// headless 跑完是容器自己 kill 1 停的，容器退出码只是 SIGTERM，
		// 要显示的是 claude 的退出码（M3-7）
		{docker.State{Status: "exited", ExitCode: 137}, true, &AgentStatus{State: "exited"}, code(0), "exited(0)"},
		{docker.State{Status: "exited", ExitCode: 137}, true, nil, code(2), "exited(2)"},
		// 但 OOM 优先：那是容器被杀，不是 claude 正常结束
		{docker.State{Status: "exited", ExitCode: 137, OOMKilled: true}, true, nil, code(0), "exited(oom)"},
		// 还在跑的时候，上一轮留下的退出码不该影响状态
		{run, true, &AgentStatus{State: "running"}, code(0), "running"},
	}
	for i, c := range cases {
		if got := Derive(c.st, c.exists, c.s, c.runExit); got != c.want {
			t.Errorf("case %d: got %s want %s", i, got, c.want)
		}
	}
}

func TestNamingAndMeta(t *testing.T) {
	ws := workspace.Workspace{Root: "/r/demo", GitDir: "/r/demo/.git", ID: "demo-abc123"}
	home := t.TempDir()
	tk, err := New(home, ws, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Container() != "sbx-demo-abc123-t1" || tk.Network() != "sbx-demo-abc123-t1-net" ||
		tk.ID() != "demo-abc123.t1" || tk.Branch() != "sbx/t1" || tk.DepVolume(0) != "sbx-demo-abc123-t1-dep-0" {
		t.Fatal(tk)
	}
	if tk.Worktree() != home+"/worktrees/demo-abc123/t1" {
		t.Fatal(tk.Worktree())
	}
	m, _ := New(home, ws, "main")
	if m.Worktree() != "/r/demo" || m.Branch() != "" {
		t.Fatal(m.Worktree())
	}
	if _, ok, _ := tk.ReadMeta(); ok {
		t.Fatal("meta should not exist")
	}
	want := Meta{Task: "t1", Base: "abc", CreatedAt: time.Unix(1, 0).UTC()}
	if err := tk.WriteMeta(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := tk.ReadMeta()
	if err != nil || !ok || got.Base != "abc" || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatal(got, ok, err)
	}
	if _, err := New(home, ws, "Bad"); err == nil {
		t.Fatal("expected invalid name")
	}
}
