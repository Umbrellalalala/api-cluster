// Package tunnel 管理 SSH 反向隧道（reverse port forwarding）。
//
// 本地主动向远程服务器发起 SSH 连接，用 -R 把本地端口「反向映射」到远程的某个端口，
// 使远程服务器通过 localhost:<RemotePort> 即可访问本地的 ApiCluster API。
// 典型场景：让一台内网/远程机器访问本机（Windows）上 127.0.0.1:3003 的聚合代理。
package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"apicluster/internal/config"
)

const (
	// CREATE_NO_WINDOW：不给子进程分配控制台窗口
	createNoWindow = 0x08000000
	// 异常退出后的最大自动重启次数（达到后停止，避免死循环刷屏）
	maxRestarts = 10
)

// Manager SSH 反向隧道进程管理器（并发安全）。
// 与 sidecar.Manager 类似，但子进程是 Windows 自带 OpenSSH 客户端 ssh.exe。
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	logFile   *os.File
	status    string // stopped / connecting / running / crashed
	pid       int
	startedAt time.Time
	lastErr   string
	restarts  int
	stopping  bool
}

// New 创建隧道管理器
func New() *Manager {
	return &Manager{status: "stopped"}
}

// LogPath 隧道日志文件路径
func LogPath() string {
	return filepath.Join(config.DataDir(), "tunnel.log")
}

// ===== 命令构造 =====

// buildArgs 根据隧道设置构造 ssh 命令参数。
// 命令形如：
//
//	ssh -i <pem> -o ServerAliveInterval=60 -o ServerAliveCountMax=3 \
//	    -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new \
//	    -N -R <remotePort>:127.0.0.1:<localPort> <user>@<host>
func buildArgs(c config.TunnelSettings) []string {
	args := []string{}
	if c.PemPath != "" {
		args = append(args, "-i", c.PemPath)
	}
	// 保活 + 连接超时 + 转发失败即退出 + 自动接受新主机指纹（避免首次连接卡在交互式确认）。
	// ConnectTimeout：主机不可达时 15 秒内快速失败退出（而非默认的长 TCP 超时卡在 connecting），
	// 这样「进程存活」≈「隧道已建立」，状态判断更可靠，用户也能及时看到失败而非干等。
	if c.ExtraArgs != "" {
		args = append(args, strings.Fields(c.ExtraArgs)...)
	} else {
		args = append(args,
			"-o", "ServerAliveInterval=60",
			"-o", "ServerAliveCountMax=3",
			"-o", "ConnectTimeout=15",
			"-o", "ExitOnForwardFailure=yes",
			"-o", "StrictHostKeyChecking=accept-new",
		)
	}
	args = append(args, "-N")
	remotePort := c.RemotePort
	if remotePort <= 0 {
		remotePort = 3003
	}
	localPort := c.LocalPort
	if localPort <= 0 {
		localPort = 3003
	}
	args = append(args, "-R", fmt.Sprintf("%d:127.0.0.1:%d", remotePort, localPort))
	args = append(args, fmt.Sprintf("%s@%s", c.User, c.Host))
	return args
}

// Validate 校验隧道配置是否完整可启动
func Validate(c config.TunnelSettings) error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("请填写远程主机 IP 或域名")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("请填写远程登录用户名")
	}
	if c.PemPath != "" {
		if st, err := os.Stat(c.PemPath); err != nil || st.IsDir() {
			return fmt.Errorf("私钥文件不存在或不可读：%s", c.PemPath)
		}
	}
	return nil
}

// ===== 进程生命周期 =====

// StartIfEnabled 若设置为启用则自动建立隧道（失败只记录，不阻塞 ApiCluster）
func (m *Manager) StartIfEnabled() {
	if !config.Get().Settings.Tunnel.Enabled {
		return
	}
	if err := m.Start(); err != nil {
		m.setErr(err.Error())
	}
}

// Start 启动 SSH 反向隧道进程
func (m *Manager) Start() error {
	c := config.Get().Settings.Tunnel
	if err := Validate(c); err != nil {
		m.setErr(err.Error())
		return err
	}

	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil {
		m.mu.Unlock()
		return nil // 已在运行
	}
	m.stopping = false
	m.mu.Unlock()

	// 清理残留的孤儿 ssh 进程：ApiCluster 被强杀时子进程 ssh 不会被连带结束，
	// 残留会话仍占着远程端口的转发监听，导致新隧道 "remote port forwarding failed"。
	cleanupOrphan(c)

	logF, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("无法写入隧道日志: %w", err)
	}

	args := buildArgs(c)
	cmd := exec.Command("ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Stdout = logF
	cmd.Stderr = logF

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("启动 ssh 失败（请确认系统已安装 OpenSSH 客户端）: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.logFile = logF
	m.status = "connecting"
	m.pid = cmd.Process.Pid
	m.startedAt = time.Now()
	m.lastErr = ""
	m.mu.Unlock()

	go m.waitLoop(cmd)
	return nil
}

// waitLoop 等待隧道进程退出；非主动停止时按退避策略自动重连
func (m *Manager) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()

	m.mu.Lock()
	if m.cmd != cmd {
		m.mu.Unlock()
		return // 已被新实例替换
	}
	stopping := m.stopping
	m.cmd = nil
	m.pid = 0
	if m.logFile != nil {
		_ = m.logFile.Close()
		m.logFile = nil
	}
	if stopping {
		m.status = "stopped"
		m.restarts = 0
		m.mu.Unlock()
		return
	}
	m.status = "crashed"
	if err != nil {
		m.lastErr = "隧道已断开: " + err.Error()
	} else {
		m.lastErr = "隧道已断开"
	}
	shouldRestart := m.restarts < maxRestarts
	if shouldRestart {
		m.restarts++
	}
	attempt := m.restarts
	// 默认自动重连；仅当用户显式关闭时（DisableAutoReconnect=true）才不重连
	disableReconnect := config.Get().Settings.Tunnel.DisableAutoReconnect
	m.mu.Unlock()

	if !shouldRestart {
		m.setErr("隧道反复断开，已停止自动重连（达到上限，请检查网络或配置）")
		return
	}
	if disableReconnect {
		return
	}
	// 退避后重连
	go func() {
		time.Sleep(time.Duration(3*attempt) * time.Second)
		m.mu.Lock()
		stillDown := m.cmd == nil && !m.stopping
		m.mu.Unlock()
		if stillDown {
			_ = m.Start()
		}
	}()
}

// Stop 主动中断隧道（手动停止）。
// 立即 kill ssh 进程，并置 stopping 标志：让 waitLoop 与「退避等待中的重连 goroutine」
// 都放弃自动重连。无论当前是 connecting / running / 退避重连中，调用后都会回到 stopped。
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.stopping = true
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		// 可能正处于「崩溃后的退避重连等待中」：置位 stopping 后，重连 goroutine
		// 醒来会检查 !stopping 而放弃重连。这里直接清理状态。
		m.mu.Lock()
		m.status = "stopped"
		m.pid = 0
		m.restarts = 0
		m.lastErr = ""
		m.mu.Unlock()
		return
	}
	_ = cmd.Process.Kill()
	// 等 waitLoop 收尾（最多 5 秒）
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		done := m.cmd == nil
		m.mu.Unlock()
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.mu.Lock()
	m.status = "stopped"
	m.pid = 0
	m.restarts = 0
	m.lastErr = ""
	m.mu.Unlock()
}

// Restart 重启隧道
func (m *Manager) Restart() error {
	m.Stop()
	m.mu.Lock()
	m.restarts = 0
	m.stopping = false
	m.mu.Unlock()
	time.Sleep(400 * time.Millisecond)
	return m.Start()
}

func (m *Manager) setErr(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	if m.status == "stopped" || m.status == "crashed" {
		m.status = "crashed"
	}
	m.mu.Unlock()
}

// cleanupOrphan 结束指向同一 user@host 的残留 ssh 进程（孤儿清理）。
// ApiCluster 被强杀/崩溃时，子进程 ssh.exe 不会被自动结束；这些残留会话在远程
// 仍持有端口转发的监听，新隧道会因 "remote port forwarding failed" 反复失败。
// 用 CIM 查询进程命令行精确匹配（包含登录目标即认定是我们的隧道），避免误伤无关 ssh。
func cleanupOrphan(c config.TunnelSettings) {
	target := fmt.Sprintf("%s@%s", c.User, c.Host)
	// 防御性转义单引号（user/host 一般不含，但不可假设）
	esc := strings.ReplaceAll(target, "'", "''")
	ps := fmt.Sprintf(
		`Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" | Where-Object { $_.CommandLine -like '*%s*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`,
		esc,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
	// 给远程 sshd 一点时间随会话关闭释放端口转发监听
	time.Sleep(800 * time.Millisecond)
}

// ===== 状态查询 =====

// Status 返回隧道状态快照（供管理界面使用）
func (m *Manager) Status() map[string]any {
	c := config.Get().Settings.Tunnel

	m.mu.Lock()
	defer m.mu.Unlock()

	var uptime int64
	if m.pid != 0 && !m.startedAt.IsZero() {
		uptime = int64(time.Since(m.startedAt).Seconds())
	}
	running := m.cmd != nil

	// ssh 进程存活超过 2 秒视为隧道已建立（ExitOnForwardFailure=yes 会让
	// 转发失败/连接失败时立即退出，因此存活即代表反向隧道已就绪）。
	ready := false
	if running && uptime >= 2 && m.status == "connecting" {
		m.status = "running"
	}
	if running && m.status == "running" {
		ready = true
	}

	return map[string]any{
		"enabled":        c.Enabled,
		"status":         m.status,
		"running":        running,
		"ready":          ready,
		"pid":            m.pid,
		"uptime_seconds": uptime,
		"restarts":       m.restarts,
		"last_error":     m.lastErr,
		"pem_path":       c.PemPath,
		"host":           c.Host,
		"user":           c.User,
		"remote_port":    c.RemotePort,
		"local_port":     c.LocalPort,
		"disable_auto_reconnect": c.DisableAutoReconnect,
		"extra_args":     c.ExtraArgs,
		"log_path":       LogPath(),
		"remote_url":     fmt.Sprintf("http://localhost:%d", c.RemotePort),
		"command":        strings.Join(append([]string{"ssh"}, buildArgs(c)...), " "),
	}
}

// LogTail 读取隧道日志末尾若干行
func LogTail(maxLines int) string {
	data, err := os.ReadFile(LogPath())
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
