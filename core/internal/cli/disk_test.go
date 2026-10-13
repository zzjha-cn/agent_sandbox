package cli

import "testing"

func TestParseSize(t *testing.T) {
	// docker 用十进制单位打印，别按 1<<30 算
	for in, want := range map[string]int64{
		"1.34GB": 1_340_000_000, "4.453kB": 4453, "0B": 0, "N/A": 0, "": 0, "712MB": 712_000_000,
	} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d want %d", in, got, want)
		}
	}
}

func TestParseDF(t *testing.T) {
	const out = `{
	  "Images": [
	    {"Repository":"sbx/web-go","Tag":"abc","Size":"1.34GB","UniqueSize":"398.4MB","Containers":"2"},
	    {"Repository":"sbx/py-rust","Tag":"def","Size":"1.2GB","UniqueSize":"200MB","Containers":"0"},
	    {"Repository":"node","Tag":"20","Size":"900MB","UniqueSize":"900MB","Containers":"0"}
	  ],
	  "Volumes": [
	    {"Name":"sbx-cache","Labels":"sbx.kind=shared","Size":"1.1GB","Links":"1"},
	    {"Name":"sbx-demo-abc-t1-dep-0","Labels":"sbx.kind=dep","Size":"300MB","Links":"0"},
	    {"Name":"6e30e6ab","Labels":"com.docker.volume.anonymous=","Size":"5GB","Links":"1"}
	  ],
	  "BuildCache": [{"Size":"4.453kB"},{"Size":"1MB"}]
	}`
	d, err := parseDF([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	// 镜像按 UniqueSize 算：Size 把共享基础层重复计入，几个 profile 加起来会虚高
	if d.Images != 398_400_000+200_000_000 {
		t.Errorf("images = %d", d.Images)
	}
	// 匿名 volume 不是 sbx 的，不算
	if d.Volumes != 1_100_000_000+300_000_000 {
		t.Errorf("volumes = %d", d.Volumes)
	}
	if d.Cache != 4453+1_000_000 {
		t.Errorf("cache = %d", d.Cache)
	}
	if d.ReclaimImages != 200_000_000 || d.ReclaimVolumes != 300_000_000 {
		t.Errorf("reclaim = %d / %d", d.ReclaimImages, d.ReclaimVolumes)
	}
	if len(d.IdleVolumes) != 1 || d.IdleVolumes[0] != "sbx-demo-abc-t1-dep-0" {
		t.Errorf("idle = %v", d.IdleVolumes)
	}
}
