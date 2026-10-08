package cli

import (
	"strings"
	"testing"

	"sandx/internal/docker"
)

func TestConcurrencyLimit(t *testing.T) {
	three := []string{"a-111111.one", "a-111111.two", "b-222222.main"}
	cases := []struct {
		name    string
		running []string
		self    string
		max     int
		blocked bool
	}{
		{"没跑满", three[:2], "a-111111.new", 3, false},
		{"正好跑满", three, "a-111111.new", 3, true},
		// 重启一个已经在跑的 Task 只是 attach，不该被自己挡住
		{"自己已经在跑", three, "a-111111.one", 3, false},
		// 计数是跨 Workspace 的：Docker VM 的内存是共用的
		{"别的仓库占满了", three, "c-333333.main", 3, true},
		{"调大限额", three, "a-111111.new", 5, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := concurrencyError(c.running, c.self, c.max, "3g")
			if (err != nil) != c.blocked {
				t.Fatalf("blocked=%v，得到 err=%v", c.blocked, err)
			}
			if err != nil && !strings.Contains(err.Error(), "sbx stop") {
				t.Errorf("错误信息要告诉用户怎么办，得到：%v", err)
			}
		})
	}
}

func TestBudgetWarning(t *testing.T) {
	const g = int64(1) << 30
	cases := []struct {
		name       string
		max        int
		per, total int64
		warn       bool
	}{
		// 阈值 0.95 的理由就是这一条：design §11 推荐的配置必须安静，
		// 否则每次 sbx run 都报警，等于没有警告（2026-10-09 决定）。
		{"design 推荐的 3×3g / 10g VM", 3, 3 * g, 10 * g, false},
		// 实际的 Docker Desktop VM 会比标称小一点，这也要安静
		{"3×3g / 9.7g VM", 3, 3 * g, 9*g + g*7/10, false},
		{"VM 只有 8g", 3, 3 * g, 8 * g, true},
		{"调大了 memory", 3, 4 * g, 10 * g, true},
		{"调大了 max_running", 4, 3 * g, 10 * g, true},
		{"算不出每个 Task 的内存", 3, 0, 10 * g, false},
		{"读不到 VM 内存", 3, 3 * g, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := budgetWarning(c.max, "3g", c.per, c.total)
			if (got != "") != c.warn {
				t.Fatalf("warn=%v，得到 %q", c.warn, got)
			}
		})
	}
}

func TestAgentIDFromLabels(t *testing.T) {
	it := docker.Item{"Labels": "sbx.role=agent,sbx.ws=shop-e76272,sbx.task=fix-login", "Names": "sbx-shop-e76272-fix-login"}
	if got := agentID(it); got != "shop-e76272.fix-login" {
		t.Errorf("想要 shop-e76272.fix-login，得到 %q", got)
	}
	// label 缺失时退回容器名，总要给得出个标识
	bare := docker.Item{"Labels": "sbx.role=agent", "Names": "sbx-weird"}
	if got := agentID(bare); got != "sbx-weird" {
		t.Errorf("想要 sbx-weird，得到 %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		3 << 30:   "3.0g",
		512 << 20: "512m",
		900:       "900",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("%d：想要 %s，得到 %s", in, want, got)
		}
	}
}
