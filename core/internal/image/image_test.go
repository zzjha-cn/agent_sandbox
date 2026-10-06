package image

import "testing"

func TestHashStability(t *testing.T) {
	base, err := BuiltinInputs("web-go", "latest", 501, 20)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := BuiltinInputs("web-go", "latest", 501, 20)
	if base.Tag() != again.Tag() || base.ProfileTag() != again.ProfileTag() {
		t.Fatal("tag not stable")
	}
	mut := []func(*Inputs){
		func(i *Inputs) { i.ProfileFile = append([]byte{}, append(i.ProfileFile, '#')...) },
		func(i *Inputs) { i.AgentFile = append([]byte{}, append(i.AgentFile, '#')...) },
		func(i *Inputs) { i.Entrypoint = []byte("x") },
		func(i *Inputs) { i.ClaudeVersion = "2.0.0" },
		func(i *Inputs) { i.UID = 1000 },
		func(i *Inputs) { i.GID = 1000 },
	}
	for n, m := range mut {
		in, _ := BuiltinInputs("web-go", "latest", 501, 20)
		m(&in)
		if in.Tag() == base.Tag() {
			t.Errorf("mutation %d did not change tag", n)
		}
	}
	if len(base.Tag()) != len("sbx/web-go:")+12 {
		t.Fatal(base.Tag())
	}
	if _, err := BuiltinInputs("nope", "latest", 1, 1); err == nil {
		t.Fatal("expected unknown profile error")
	}
}
