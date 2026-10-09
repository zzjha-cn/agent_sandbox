package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTailLines(t *testing.T) {
	b := []byte("a\nb\nc\n")
	for _, c := range []struct {
		n    int
		want string
	}{{0, "a\nb\nc\n"}, {-1, "a\nb\nc\n"}, {2, "b\nc\n"}, {10, "a\nb\nc\n"}, {1, "c\n"}} {
		if got := string(tailLines(b, c.n)); got != c.want {
			t.Errorf("tailLines(n=%d) = %q，期望 %q", c.n, got, c.want)
		}
	}
	if got := string(tailLines([]byte("no trailing newline"), 1)); got != "no trailing newline" {
		t.Errorf("%q", got)
	}
}

// -f 要在任务结束后把最后写进去的内容也读完再返回，不能漏尾巴。
func TestFollowFileReadsFinalWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := false
	go func() {
		time.Sleep(30 * time.Millisecond)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("two\n")
		f.Close()
		done = true
	}()
	// offset 从 4 开始：第一行当作"已经打印过"的部分
	if err := followFile(p, &out, 4, func() bool { return done }, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out.String() != "two\n" {
		t.Fatalf("跟随到的内容：%q", out.String())
	}
}

func TestRotateRunLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.log")
	os.WriteFile(p, []byte("0123456789"), 0o644)
	if err := rotateRunLog(p, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Fatal("没超过阈值不该轮转")
	}
	if err := rotateRunLog(p, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("轮转后 run.log 应该让位给新的")
	}
	if b, _ := os.ReadFile(p + ".1"); string(b) != "0123456789" {
		t.Fatalf("旧日志没保住：%q", b)
	}
	// 文件不存在时是 no-op，不报错
	if err := rotateRunLog(filepath.Join(t.TempDir(), "nope.log"), 1); err != nil {
		t.Fatal(err)
	}
}
