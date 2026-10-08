package agent

import "testing"

func TestParseStatus(t *testing.T) {
	const loggedIn = `{"loggedIn":true,"authMethod":"claude.ai","email":"a@b.c","orgName":"Acme","subscriptionType":"team"}`
	st, err := ParseStatus(loggedIn, nil)
	if err != nil || !st.LoggedIn {
		t.Fatalf("%v %+v", err, st)
	}
	if got, want := st.Describe(), "已登录：a@b.c · Acme · team · claude.ai"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// 没登录时 claude 的退出码非 0，但 JSON 照样打印：以 JSON 为准，不报错。
	st, err = ParseStatus("提示\n"+`{"loggedIn":false}`+"\n", errExit{})
	if err != nil || st.LoggedIn {
		t.Fatalf("%v %+v", err, st)
	}
	if st.Describe() != "未登录" {
		t.Fatal(st.Describe())
	}

	// 解析不出来时才把执行错误交出去。
	if _, err := ParseStatus("command not found", errExit{}); err == nil {
		t.Fatal("want exec error")
	}
	if _, err := ParseStatus("", nil); err == nil {
		t.Fatal("want parse error")
	}
}

type errExit struct{}

func (errExit) Error() string { return "exit status 1" }
