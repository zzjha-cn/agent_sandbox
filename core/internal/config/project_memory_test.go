package config

import "testing"

func TestProjectMemoryTrustedConfiguration(t *testing.T) {
	for _, body := range []string{"[memory]\nmode='native'", "[memory]\nproject='other'", "[memory]\npaths=['core/a.go']"} {
		if e := checkProject(body, "project"); e == nil {
			t.Fatal("repository chose memory policy", body)
		}
	}
	for _, mode := range []string{"native", "managed", "off"} {
		c := Default()
		c.Memory.Mode = mode
		if e := c.Validate(); e != nil {
			t.Fatal(e)
		}
	}
	c := Default()
	c.Memory.Mode = "invalid"
	if e := c.Validate(); e == nil {
		t.Fatal("invalid mode accepted")
	}
	ld, e := LoadLayers([]Layer{write(t, t.TempDir(), LayerGlobal, "[memory]\nmode='managed'\nproject='demo'\npaths=['core/a.go']", false)})
	if e != nil || ld.Config.Memory.Project != "demo" || ld.Sources["memory.mode"].Last() != LayerGlobal {
		t.Fatal(ld, e)
	}
}

func TestExtractionTrustedDefaultsLimitsAndLayers(t *testing.T) {
	if e := checkProject("[memory.extract]\nauto=true\nmodel='injected'", "repository"); e == nil {
		t.Fatal("repository enabled billed extraction")
	}
	c := Default()
	if c.Memory.Extract.Auto || c.Memory.Extract.TimeoutSeconds != 90 || c.Memory.Extract.MaxOutputTokens != 2048 {
		t.Fatal(c.Memory)
	}
	c.Memory.Extract.Auto = true
	if e := c.Validate(); e == nil {
		t.Fatal("automatic extraction without managed mode/model")
	}
	ld, e := LoadLayers([]Layer{write(t, t.TempDir(), LayerGlobal, "[memory]\nmode='managed'\nproject='demo'\n[memory.extract]\nauto=true\nmodel='test-model'\ntimeout_seconds=30\nmax_output_tokens=512\nmax_calls=1", false)})
	if e != nil || !ld.Config.Memory.Extract.Auto || ld.Config.Memory.Extract.MaxCalls != 1 || ld.Sources["memory.extract.auto"].Last() != LayerGlobal {
		t.Fatal(ld, e)
	}
	for _, v := range []int{-1, 0, 301} {
		c := Default()
		c.Memory.Extract.TimeoutSeconds = v
		if e := c.Validate(); e == nil {
			t.Fatal(v)
		}
	}
}

func TestMemoryRetrievalTrustedConfigurationAndMerge(t *testing.T) {
	for _, text := range []string{"[memory]\nquery='E401'", "[memory]\nwatch=['go.mod']", "[memory.facts]\nversion='1'"} {
		if checkProject(text, "project") == nil {
			t.Fatal("project chose observations", text)
		}
	}
	dir := t.TempDir()
	ld, err := LoadLayers([]Layer{write(t, dir, LayerGlobal, "[memory]\nmode='managed'\nquery='prep'\nwatch=['go.mod']\n[memory.facts]\nnode='20'\ngo='1.27'", false), write(t, dir, LayerWorkspace, "[memory]\nquery='GOMODCACHE'\n[memory.facts]\nnode='24'", false)})
	if err != nil || ld.Config.Memory.Query != "GOMODCACHE" || ld.Config.Memory.Facts["node"] != "24" || ld.Config.Memory.Facts["go"] != "1.27" || ld.Sources["memory.facts.node"].Last() != LayerWorkspace {
		t.Fatal(ld, err)
	}
	c := Default()
	c.Memory.Mode = "managed"
	c.Memory.Watch = []string{".env"}
	if c.Validate() == nil {
		t.Fatal("secret observation")
	}
	c.Memory.Watch = nil
	c.Memory.Facts = map[string]string{"file:go.mod": "forged"}
	if c.Validate() == nil {
		t.Fatal("asserted file hash")
	}
}
