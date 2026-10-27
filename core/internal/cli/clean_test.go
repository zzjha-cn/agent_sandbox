package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sandx/internal/task"
)

// docker images 的输出是按创建时间倒序的，planImages 直接依赖这个顺序：
// 每个 repository 的第一行就是最新的那个。
func TestPlanImagesKeepsNewestPerRepository(t *testing.T) {
	refs := []string{
		"sbx/web-go:new",
		"sbx/web-go:old",
		"sbx/web-go:older",
		"sbx/prep-shop:new",
		"sbx/prep-shop:old",
		"sbx/prep-api:only",
	}
	got := planImages(refs, nil)
	want := []string{"sbx/web-go:old", "sbx/web-go:older", "sbx/prep-shop:old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remove = %v, want %v", got, want)
	}
}

// 还被 Task 钉着的镜像不能删——哪怕它已经不是最新的。
func TestPlanImagesKeepsWhatTasksStillUse(t *testing.T) {
	refs := []string{"sbx/prep-shop:new", "sbx/prep-shop:old", "sbx/prep-shop:ancient"}
	got := planImages(refs, map[string]bool{"sbx/prep-shop:old": true})
	if len(got) != 1 || got[0] != "sbx/prep-shop:ancient" {
		t.Fatalf("remove = %v, want only the ancient one", got)
	}
}

func TestPlanImagesNothingToRemove(t *testing.T) {
	if got := planImages([]string{"sbx/web-go:a"}, nil); len(got) != 0 {
		t.Fatalf("remove = %v, want none", got)
	}
}

// imagesInUse 要扫**所有** Workspace 的 state，不只当前这个：
// sbx-cache 和镜像都是全局的，只看眼前的仓库会删掉别人还在用的种子。
func TestImagesInUseSpansAllWorkspaces(t *testing.T) {
	home := t.TempDir()
	write := func(ws, name string, m task.Meta) {
		dir := filepath.Join(home, "state", ws, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shop-a1", "fix-login", task.Meta{PrepTag: "sbx/prep-shop:aaa", Image: "sbx/web-go:111"})
	write("api-b2", "main", task.Meta{PrepTag: "sbx/prep-api:bbb"})
	// 没有 meta.json 的 Task 目录不该让整条命令失败
	if err := os.MkdirAll(filepath.Join(home, "state", "api-b2", "half-built"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &App{Home: home}
	inUse, err := a.imagesInUse()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"sbx/prep-shop:aaa", "sbx/web-go:111", "sbx/prep-api:bbb"} {
		if !inUse[ref] {
			t.Fatalf("%s should be in use: %v", ref, inUse)
		}
	}
	if len(inUse) != 3 {
		t.Fatalf("in use = %v", inUse)
	}
}

func TestImagesInUseWithoutStateDir(t *testing.T) {
	a := &App{Home: t.TempDir()}
	inUse, err := a.imagesInUse()
	if err != nil || len(inUse) != 0 {
		t.Fatalf("inUse = %v, err = %v", inUse, err)
	}
}

// 这条是会静默坏掉的那个：标记留着而内容没了，sbx 会认为已经播过种，
// 私有依赖再也不会重播，并且一声不吭。所以 rm 标记必须排在所有 rm -rf 前面。
func TestClearScriptRemovesSeedMarkersFirst(t *testing.T) {
	s := clearScript([]string{"go-mod", "npm"}, true, "501", "20")
	marker := strings.Index(s, "/v/.sbx-prep/*")
	content := strings.Index(s, `rm -rf "/v/go-mod"`)
	if marker < 0 || content < 0 {
		t.Fatalf("script missing a step: %s", s)
	}
	if marker > content {
		t.Fatalf("markers must be removed before contents: %s", s)
	}
	if !strings.HasPrefix(s, "set -e; ") {
		t.Fatalf("script must abort on the first failure: %s", s)
	}
}

// 默认档不碰播种标记：它只清派生缓存，私有依赖一根毛都不该动。
func TestClearScriptShallowLeavesSeedMarkers(t *testing.T) {
	s := clearScript(derivedCacheDirs, false, "501", "20")
	if strings.Contains(s, ".sbx-prep") {
		t.Fatalf("shallow clean must not touch seed markers: %s", s)
	}
	if !strings.Contains(s, `chown 501:20 "/v/go-build"`) {
		t.Fatalf("recreated dirs must go back to the host uid/gid: %s", s)
	}
}

// 两档的分界就是这个列表：派生的不要网络，包缓存要重下。错放一个就改变了命令的语义。
func TestCacheTiersDoNotOverlap(t *testing.T) {
	derived := map[string]bool{}
	for _, d := range derivedCacheDirs {
		derived[d] = true
	}
	for _, d := range packageCacheDirs {
		if derived[d] {
			t.Fatalf("%s cannot be both derived and a package cache", d)
		}
	}
	if !derived["go-build"] || len(derivedCacheDirs) != 1 {
		t.Fatalf("derived = %v; go-build is the only purely derived dir today", derivedCacheDirs)
	}
}

func TestParseDU(t *testing.T) {
	got := parseDU("1024\t/v/go-mod\n512\t/v/npm\ndu: /v/pip: No such file\n")
	if got["go-mod"] != 1024*1024 || got["npm"] != 512*1024 {
		t.Fatalf("sizes = %v", got)
	}
	if _, ok := got["pip"]; ok {
		t.Fatalf("unmeasurable dirs should be absent: %v", got)
	}
}

func TestParseSeeds(t *testing.T) {
	got := parseSeeds("shop-e76272 sbx/prep-shop-e76272:a1b2c3\nalgo-bd734a sbx/prep-algo-bd734a:7bcac2\n\n")
	if len(got) != 2 || got["shop-e76272"] != "sbx/prep-shop-e76272:a1b2c3" {
		t.Fatalf("seeds = %v", got)
	}
}

// doctor 的"可回收"名单里不该出现三个共享 volume：没有 Task 在跑时它们必然是
// 0 links，每次都上榜，而它们恰恰是最不该顺手删的。
func TestNotSharedDropsTheSharedVolumes(t *testing.T) {
	got := notShared([]string{"sbx-cache", "sbx-home", "sbx-mise", "sbx-daemon-ownership", "sbx-shop-e76272-fix-login-deps-0"})
	if len(got) != 1 || got[0] != "sbx-shop-e76272-fix-login-deps-0" {
		t.Fatalf("idle = %v", got)
	}
}
