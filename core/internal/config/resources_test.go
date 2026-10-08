package config

import "testing"

func TestMemoryBytes(t *testing.T) {
	const g = int64(1) << 30
	cases := map[string]int64{
		"3g":    3 * g,
		"3G":    3 * g,
		"512m":  512 << 20,
		"1024k": 1 << 20,
		"2048":  2048,
		"1.5g":  g + g/2,
		"":      0,
		"abc":   0,
		"0g":    0,
	}
	for in, want := range cases {
		if got := (Resources{Memory: in}).MemoryBytes(); got != want {
			t.Errorf("%q：想要 %d，得到 %d", in, want, got)
		}
	}
}
