package webui

import (
	"strings"
	"testing"

	"apicluster/internal/config"
)

// 「另存为」对话框没法自动化，这里至少钉住导出内容：站点分组、密码有无、备注与反引号处理。
func TestVaultMarkdown(t *testing.T) {
	got := vaultMarkdown([]config.VaultSite{
		{
			Name: "OpenAI", URL: "https://platform.openai.com", Note: "只有 Google 登录",
			Accounts: []config.VaultAccount{
				{Label: "Google 登录", User: "me@gmail.com"},
				{User: "me@outlook.com", Password: "a`b", Note: "公司号，月底到期"},
			},
		},
		{Name: "空站点"},
	})
	for _, want := range []string{
		"## OpenAI",
		"- 网址：https://platform.openai.com",
		"- 站点备注：只有 Google 登录",
		"- **Google 登录**：me@gmail.com ／ 无密码（验证码或第三方登录）",
		"- **账号 2**：me@outlook.com ／ 密码：a`b", // 含反引号的密码不再包 code
		"  - 备注：公司号，月底到期",
		"- （还没填账号）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("导出内容缺少 %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "`a`b`") {
		t.Errorf("含反引号的密码被错误地包进了 code：%s", got)
	}
}
