package cli

import (
	"fmt"
	"io"
	"strings"
)

// padCJK 把 s 右侧补空格到 n 个显示宽度：CJK 字符占两格，ASCII 占一格。
// tabwriter 按字节算宽度，中英混排的列会错位，需要对齐的地方用这个。
func padCJK(s string, n int) string {
	if w := widthCJK(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func widthCJK(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x20000 && r <= 0x3fffd)) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// writeTable 按显示宽度对齐输出表格，列间隔两个空格。
// 取代 tabwriter：后者按字节算宽度，PATH 里有中文目录名、KIND 列本身是中文时都会错位。
// 最后一列不补尾空格。
func writeTable(w io.Writer, header []string, rows [][]string) error {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = widthCJK(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && widthCJK(c) > widths[i] {
				widths[i] = widthCJK(c)
			}
		}
	}
	for _, r := range append([][]string{header}, rows...) {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
			} else {
				b.WriteString(padCJK(c, widths[i]) + "  ")
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(b.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}
