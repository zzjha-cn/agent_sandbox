package cli

import (
	"bytes"
	"strings"
	"testing"
)

// tabwriter 按字节算宽度，中文列会错位；writeTable 按显示宽度对齐。
func TestWriteTableAlignsCJK(t *testing.T) {
	var b bytes.Buffer
	err := writeTable(&b, []string{"HOST", "KIND"}, [][]string{
		{"example.com", "不在白名单"},
		{"a.io", "策略拦截/遥测"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", b.String())
	}
	// 每一行的第二列都应该从同一个显示列开始
	var want int
	for i, l := range lines {
		col := widthCJK(l[:strings.LastIndex(l, strings.Fields(l)[len(strings.Fields(l))-1])])
		if i == 0 {
			want = col
		} else if col != want {
			t.Errorf("第 %d 行第二列从 %d 开始，期望 %d：\n%s", i, col, want, b.String())
		}
	}
	// 最后一列不补尾空格
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			t.Errorf("行尾有多余空格：%q", l)
		}
	}
}

func TestWidthCJK(t *testing.T) {
	for _, c := range []struct {
		s string
		w int
	}{{"abc", 3}, {"不在白名单", 10}, {"a中b", 4}, {"", 0}} {
		if got := widthCJK(c.s); got != c.w {
			t.Errorf("widthCJK(%q) = %d，期望 %d", c.s, got, c.w)
		}
	}
}
