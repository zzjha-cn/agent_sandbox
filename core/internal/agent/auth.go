package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Status 是 claude auth status 输出里 sbx 用得到的字段（默认就是 JSON）。
type Status struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

// ParseStatus 解析 claude auth status 的输出。没登录时 claude 的退出码非 0，
// 但 JSON 照样打印，所以能解析出来就以 JSON 为准，execErr 只在解析不出来时用。
func ParseStatus(out string, execErr error) (Status, error) {
	var st Status
	if j := jsonObject(out); j != "" {
		if err := json.Unmarshal([]byte(j), &st); err == nil {
			return st, nil
		}
	}
	if execErr != nil {
		return st, execErr
	}
	return st, fmt.Errorf("cannot parse the output of claude auth status: %s", out)
}

// jsonObject 截出输出里的 JSON 对象，容忍前后多出来的提示行。
func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

// Describe 把登录态渲染成一行，给 sbx login 用。
func (s Status) Describe() string {
	if !s.LoggedIn {
		return "not logged in"
	}
	var parts []string
	for _, p := range []string{s.Email, s.OrgName, s.SubscriptionType, s.AuthMethod} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "logged in"
	}
	return "logged in: " + strings.Join(parts, " · ")
}
