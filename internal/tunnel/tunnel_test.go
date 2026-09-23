package tunnel

import (
	"strings"
	"testing"

	"apicluster/internal/config"
)

func TestBuildArgs(t *testing.T) {
	c := config.TunnelSettings{
		PemPath:    `C:\Users\you\.ssh\id_rsa`,
		Host:       "203.0.113.10",
		User:       "ubuntu",
		RemotePort: 3003,
		LocalPort:  3003,
	}
	args := buildArgs(c)
	s := strings.Join(args, " ")

	if !strings.Contains(s, "-i "+c.PemPath) {
		t.Fatalf("缺少 -i 私钥参数: %s", s)
	}
	if !strings.Contains(s, "-R 3003:127.0.0.1:3003") {
		t.Fatalf("反向隧道参数错误: %s", s)
	}
	if !strings.Contains(s, "ubuntu@203.0.113.10") {
		t.Fatalf("登录目标错误: %s", s)
	}
	if !strings.Contains(s, "-N") {
		t.Fatalf("缺少 -N（只建隧道不登录）: %s", s)
	}
	if !strings.Contains(s, "ServerAliveInterval=60") {
		t.Fatalf("缺少保活参数: %s", s)
	}
	if !strings.Contains(s, "ExitOnForwardFailure=yes") {
		t.Fatalf("缺少转发失败即退出参数: %s", s)
	}
}

func TestBuildArgsDefaults(t *testing.T) {
	c := config.TunnelSettings{Host: "1.2.3.4", User: "root"}
	args := buildArgs(c)
	s := strings.Join(args, " ")
	// 端口缺省 3003
	if !strings.Contains(s, "-R 3003:127.0.0.1:3003") {
		t.Fatalf("端口缺省应为 3003: %s", s)
	}
	// 无 pem 时不带 -i
	if strings.Contains(s, "-i ") {
		t.Fatalf("无 pem 时不应带 -i: %s", s)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(config.TunnelSettings{}); err == nil {
		t.Fatal("缺少 host 应报错")
	}
	if err := Validate(config.TunnelSettings{Host: "1.2.3.4"}); err == nil {
		t.Fatal("缺少 user 应报错")
	}
	if err := Validate(config.TunnelSettings{Host: "1.2.3.4", User: "root"}); err != nil {
		t.Fatalf("host+user 应合法: %v", err)
	}
	if err := Validate(config.TunnelSettings{Host: "1.2.3.4", User: "root", PemPath: "Z:\\不存在.pem"}); err == nil {
		t.Fatal("不存在的 pem 应报错")
	}
}
