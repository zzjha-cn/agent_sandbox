package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// M4-5：sbx 的 volume、镜像和构建缓存都没有配额，而 sbx done 只回收 Task 自己的
// 依赖 volume——sbx-cache / sbx-home / sbx-mise 是只涨不消的。Docker 的 local volume
// 驱动没有通用 quota，所以这里**只报告不自动删**：列出占用和可回收的部分，
// 回收由人来决定。自动删的风险太具体了：sbx-home 删掉就要重新登录。

// diskReclaimWarn 是「可回收的部分」超过多少就提醒。
const diskReclaimWarn = 5 << 30

// dfEntry 是 docker system df -v 里的一行（volume 或 image）。
type dfEntry struct {
	Name       string // volume
	Repository string // image
	Tag        string
	Labels     string
	Links      string // volume：挂着它的容器数
	Containers string // image：用着它的容器数
	Size       string
	UniqueSize string
}

type dfOutput struct {
	Images     []dfEntry
	Volumes    []dfEntry
	BuildCache []dfEntry
}

// diskUsage 是算出来的 sbx 占用。
type diskUsage struct {
	Images, Volumes, Cache int64
	// Reclaimable 是没有任何容器挂着的那部分——它们是能删的候选，
	// 但不一定该删：sbx-cache 没有 Task 在跑时也是 0 links。
	ReclaimImages, ReclaimVolumes int64
	IdleVolumes                   []string // 没容器挂着的 sbx volume 名，供人决定
}

func (d diskUsage) Total() int64 { return d.Images + d.Volumes + d.Cache }

// isSBX 判断一条 volume 记录是不是 sbx 的。名字前缀和 label 都认：
// 名字是给人看的约定，label 才是真正的归属标记，少一个都会漏。
func (e dfEntry) isSBXVolume() bool {
	return strings.HasPrefix(e.Name, "sbx-") || strings.Contains(e.Labels, "sbx.")
}

// parseDF 把 docker system df -v 的 JSON 汇总成 sbx 的占用。
// 纯函数，解析不出来的尺寸当 0——这是个报告，不该因为一行格式意外就整项失败。
func parseDF(data []byte) (diskUsage, error) {
	var out dfOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return diskUsage{}, err
	}
	var d diskUsage
	for _, e := range out.Volumes {
		if !e.isSBXVolume() {
			continue
		}
		n := parseSize(e.Size)
		d.Volumes += n
		if e.Links == "0" || e.Links == "" || e.Links == "N/A" {
			d.ReclaimVolumes += n
			d.IdleVolumes = append(d.IdleVolumes, e.Name)
		}
	}
	for _, e := range out.Images {
		if !strings.HasPrefix(e.Repository, "sbx/") {
			continue
		}
		// UniqueSize 才是这个镜像自己占的：Size 把共享的基础层也算进来了，
		// 几个 profile 加起来会得到一个远大于实际的数。
		n := parseSize(e.UniqueSize)
		d.Images += n
		if e.Containers == "0" {
			d.ReclaimImages += n
		}
	}
	// 构建缓存分不出是谁的（BuildKit 不带 label），整体计入：
	// 这台机器上的构建缓存基本都是 sbx 自己攒的。
	for _, e := range out.BuildCache {
		d.Cache += parseSize(e.Size)
	}
	return d, nil
}

var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1},
}

// parseSize 解析 docker 打印的尺寸（"1.34GB"、"4.453kB"、"0B"）。
// docker 用的是十进制单位，这里照着来，不要换成 1<<30。
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	for _, u := range sizeUnits {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
		if err != nil {
			return 0
		}
		return int64(v * u.mult)
	}
	return 0
}

// diskDetail 是 doctor 里那一行的正文。
func (d diskUsage) detail() string {
	return fmt.Sprintf("images %s + volumes %s + build cache %s = %s",
		humanBytes(d.Images), humanBytes(d.Volumes), humanBytes(d.Cache), humanBytes(d.Total()))
}
