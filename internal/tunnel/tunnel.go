// Package tunnel 管理 SSH 反向隧道（reverse port forwarding）。
//
// 本地主动向远程服务器发起 SSH 连接，用 -R 把本地端口「反向映射」到远程的某个端口，
// 使远程服务器通过 localhost:<RemotePort> 即可访问本地的 ApiCluster API。
// 典型场景：让一台内网/远程机器访问本机（Windows）上 127.0.0.1:3003 的聚合代理。
package tunnel

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// runCfg 是启动那一刻实际生效的配置快照。改了端口/主机但没重启隧道时，
	// 界面若继续显示新值，就是在说「新端口已经通了」——而 ssh 还挂着旧参数。
	runCfg config.TunnelSettings
	hasRun bool
	// logOff 本次启动时隧道日志的字节偏移：判定「这次到底成没成功」只看新写入的部分，
	// 免得上一次失败的旧日志把本次判断带偏。
	logOff int64
	// orphanList / orphanAt 缓存进程枚举结果：一次枚举要起 PowerShell（实测约 2 秒），
	// 而界面每 5 秒就轮询状态，不能每次都扫。
	orphanList []map[string]any
	orphanAt   time.Time
}

// orphanTTL 残留隧道枚举结果的缓存时长；启动/停止时会主动失效。
const orphanTTL = 20 * time.Second

// New 创建隧道管理器
func New() *Manager {
	return &Manager{status: "stopped"}
}

// LogPath 隧道日志文件路径
func LogPath() string {
	return filepath.Join(config.DataDir(), "tunnel.log")
}

// pidFilePath 记录本管理器上一次拉起的 ssh PID。
// 用它才能区分「ApiCluster 被强杀后遗留的自家隧道」和「用户自己在终端开的 ssh」：
// 前者可以安全清理，后者绝不能碰。
func pidFilePath() string {
	return filepath.Join(config.DataDir(), "tunnel.ssh.pid")
}

func writePidFile(pid int) {
	_ = os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0o600)
}

func readPidFile() int {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func removePidFile() { _ = os.Remove(pidFilePath()) }

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

	// 残留隧道只在「PID 与我们上次记录的一致」时清理（确定是自家孤儿）。
	// 其余情况绝不盲杀：用户自己在终端开的 ssh user@203.0.113.10 也是这个登录目标，
	// 按子串匹配一律 Stop-Process 会把他的交互会话一起杀掉。
	cleanupOwnOrphan(c)

	rotateLogIfNeeded()

	logF, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("无法写入隧道日志: %w", err)
	}
	var logOff int64
	if st, serr := logF.Stat(); serr == nil {
		logOff = st.Size()
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
	m.runCfg = c // 实际生效的配置快照，状态界面以此为准
	m.hasRun = true
	m.logOff = logOff
	m.mu.Unlock()
	writePidFile(cmd.Process.Pid)
	m.invalidateOrphanCache()

	go m.waitLoop(cmd)
	go m.confirmLoop(cmd)
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
		removePidFile()
		return
	}
	m.status = "crashed"
	if err != nil {
		m.lastErr = "隧道已断开: " + err.Error()
	} else {
		m.lastErr = "隧道已断开"
	}
	// ssh 的真实原因只写进日志（stderr 被重定向），退出码永远是 255；
	// 把「Permission denied (publickey)」「remote port forwarding failed」这类
	// 一行原因提到界面上，用户才知道下一步该改什么。
	if reason := m.failureReasonLocked(); reason != "" {
		m.lastErr = "隧道已断开：" + reason
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
	m.invalidateOrphanCache()
}

// Restart 重启隧道
func (m *Manager) Restart() error {
	m.Stop()
	m.invalidateOrphanCache()
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

// ===== 残留隧道识别（绝不盲杀） =====

type sshProc struct {
	pid int
	cmd string
}

// listSSHProcs 列出本机所有 ssh.exe 的 PID 与命令行。
// PowerShell 输出可能是 GBK/UTF8 混合（命令行里常有中文路径），先强制 UTF-8；
// 分隔符用 "|@|"（制表符在 PowerShell 格式串里容易踩转义坑）。
func listSSHProcs() []sshProc {
	ps := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8\n" +
		`Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" | ForEach-Object { "{0}|@|{1}" -f $_.ProcessId, $_.CommandLine }`
	c := exec.Command("powershell", "-NoProfile", "-Command", ps)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.Output()
	if err != nil {
		return nil
	}
	var res []sshProc
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		i := strings.Index(line, "|@|")
		if i <= 0 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(line[:i]))
		if pid == 0 {
			continue
		}
		res = append(res, sshProc{pid: pid, cmd: line[i+3:]})
	}
	return res
}

// matchesTunnel 判断一条 ssh 命令行是否就是「按 c 参数建立的反向隧道」。
// 要求同时命中 -N、完全相同的 -R 映射、以及 user@host（有私钥时再要求同一路径），
// 这样用户在终端里手敲的 `ssh user@203.0.113.10`（没有 -N/-R）不会被误判为我们的人。
func matchesTunnel(cmdline string, c config.TunnelSettings) bool {
	low := strings.ToLower(cmdline)
	remote, local := effectivePorts(c)
	need := []string{"-n ", fmt.Sprintf("-r %d:127.0.0.1:%d", remote, local), strings.ToLower(fmt.Sprintf("%s@%s", c.User, c.Host))}
	for _, s := range need {
		if !strings.Contains(low, s) {
			return false
		}
	}
	if c.PemPath != "" && !strings.Contains(low, strings.ToLower(c.PemPath)) {
		return false
	}
	return true
}

func effectivePorts(c config.TunnelSettings) (int, int) {
	remote, local := c.RemotePort, c.LocalPort
	if remote <= 0 {
		remote = 3003
	}
	if local <= 0 {
		local = 3003
	}
	return remote, local
}

// cleanupOwnOrphan 只清理「PID 与我们上次记录的一致、且命令行确实是我们这条隧道」的残留。
// ApiCluster 被强杀时 ssh 子进程不会被级联结束，会占着远程端口转发监听，
// 新隧道因此反复 "remote port forwarding failed"。
func cleanupOwnOrphan(c config.TunnelSettings) {
	own := readPidFile()
	if own == 0 {
		return
	}
	removePidFile()
	for _, p := range listSSHProcs() {
		if p.pid != own || !matchesTunnel(p.cmd, c) {
			continue // PID 已被别的程序复用，或那不是我们的隧道：不动
		}
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(p.pid)).Run()
		time.Sleep(800 * time.Millisecond)
		return
	}
}

// Orphans 返回所有「看起来就是这条隧道、但不是当前管理器启动」的 ssh 进程，
// 供界面提示「端口可能已被上一个残留会话占着」。ours 表示 PID 与记录文件一致。
func (m *Manager) Orphans() []map[string]any {
	m.mu.Lock()
	running := m.cmd != nil
	mypid := m.pid
	cached := m.orphanList != nil && time.Since(m.orphanAt) < orphanTTL
	var list []map[string]any
	if cached {
		list = m.orphanList
		m.mu.Unlock()
		return excludeSelf(list, running, mypid)
	}
	m.mu.Unlock()

	c := config.Get().Settings.Tunnel
	own := readPidFile()
	for _, p := range listSSHProcs() {
		if !matchesTunnel(p.cmd, c) {
			continue // 命令行不同（比如用户在终端里手开的会话）一律不算我们的残留
		}
		list = append(list, map[string]any{"pid": p.pid, "command": p.cmd, "ours": p.pid == own})
	}
	m.mu.Lock()
	m.orphanList, m.orphanAt = list, time.Now()
	m.mu.Unlock()
	return excludeSelf(list, running, mypid)
}

// excludeSelf 过滤掉当前管理器自己启动的那一条
func excludeSelf(list []map[string]any, running bool, mypid int) []map[string]any {
	if !running || mypid == 0 {
		return list
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if pid, _ := x["pid"].(int); pid != mypid {
			out = append(out, x)
		}
	}
	return out
}

// invalidateOrphanCache 进程状态一变，缓存就必须作废（否则界面会拿着上一轮的残留名单）
func (m *Manager) invalidateOrphanCache() {
	m.mu.Lock()
	m.orphanList, m.orphanAt = nil, time.Time{}
	m.mu.Unlock()
}

// KillOrphan 结束指定 PID 的残留隧道：必须是命令行匹配我们这条隧道的进程，
// 且不是当前管理器自己启动的那个。
func (m *Manager) KillOrphan(pid int) error {
	c := config.Get().Settings.Tunnel
	m.mu.Lock()
	mypid := m.pid
	m.mu.Unlock()
	if pid <= 0 || pid == mypid {
		return fmt.Errorf("不该结束这个进程")
	}
	for _, p := range listSSHProcs() {
		if p.pid != pid {
			continue
		}
		if !matchesTunnel(p.cmd, c) {
			return fmt.Errorf("PID %d 不是本程序配置的隧道进程，不会替你结束它", pid)
		}
		if err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
			return fmt.Errorf("结束残留隧道失败: %w", err)
		}
		time.Sleep(600 * time.Millisecond)
		return nil
	}
	return nil // 已经没了
}

// ProbeRemote 登录远程执行一次「本地端口是否真的通」的检查：
// 远程机器上 curl 自己 127.0.0.1:<remote_port>/healthz。
// 这是唯一能证明反向隧道真的可用的办法——本机 ssh 进程活着只能说明连接没断。
func (m *Manager) ProbeRemote() map[string]any {
	c := config.Get().Settings.Tunnel
	remote, _ := effectivePorts(c)
	if c.Host == "" || c.User == "" {
		return map[string]any{"ok": false, "error": "先填好远程主机与用户名"}
	}
	args := []string{}
	if c.PemPath != "" {
		args = append(args, "-i", c.PemPath)
	}
	args = append(args,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-l", c.User, c.Host,
		fmt.Sprintf(`if timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/%d" 2>/dev/null; then printf OPEN; curl -s -m 4 -o /dev/null -w %%{http_code} http://127.0.0.1:%d/healthz 2>/dev/null || true; else printf CLOSED; fi`, remote, remote),
	)
	cmd := exec.Command("ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	done := make(chan struct{})
	var out, errb bytes.Buffer
	go func() {
		cmd.Stdout = &out
		cmd.Stderr = &errb
		_ = cmd.Run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return map[string]any{"ok": false, "error": "远程探测超时（25 秒）"}
	}
	text := strings.TrimSpace(out.String())
	res := map[string]any{"ok": false, "remote_port": remote, "result": text}
	switch {
	case strings.HasPrefix(text, "OPEN"):
		res["ok"] = true
		res["http"] = strings.TrimSpace(text[4:])
	case strings.Contains(text, "CLOSED"):
		res["error"] = fmt.Sprintf("远程 127.0.0.1:%d 没人监听：反向隧道没建立成功（多半是被上一个残留会话占着端口）", remote)
	default:
		msg := strings.TrimSpace(errb.String())
		if msg != "" {
			res["error"] = "探测失败：" + firstLine(msg)
		} else {
			res["error"] = "探测没有输出"
		}
	}
	return res
}

func firstLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if i := strings.Index(s, "\n"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// ===== 就绪确认与日志 =====

// confirmLoop 判定隧道真的建立：进程活过 3 秒，且日志里没出现致命错误。
// 只按「进程存活」判断会把「认证卡住」「网络半开」也报成已建立。
func (m *Manager) confirmLoop(cmd *exec.Cmd) {
	time.Sleep(3 * time.Second)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != cmd {
		return // 已被替换或停止
	}
	if reason := m.failureReasonLocked(); reason != "" {
		m.lastErr = reason
		return // 保持 connecting；若 ssh 因此退出，waitLoop 会接手
	}
	m.status = "running"
}

// fatalSigns 是 ssh 会在 stderr 打的致命特征串（小写匹配）。
var fatalSigns = []string{
	"permission denied",
	"remote port forwarding failed",
	"connection timed out",
	"connection refused",
	"connection reset by peer",
	"network is unreachable",
	"no route to host",
	"could not resolve hostname",
	"host key verification failed",
	"no matching host key",
	"bad configuration option",
	"too many authentication failures",
	"not accessible",
	"ssh: connect to host",
}

// failureReasonLocked 从本次启动之后写入的日志里找最后一条致命错误。
// 用启动时记录的字节偏移过滤，避免上一次失败的旧日志污染本次判断。
// 调用方必须已持有 m.mu（此处不再加锁：sync.Mutex 不可重入，
// confirmLoop / waitLoop 都是在持锁区间里调用它，再加锁会自死锁）。
func (m *Manager) failureReasonLocked() string {
	off := m.logOff
	data, err := os.ReadFile(LogPath())
	if err != nil || int64(len(data)) <= off {
		return ""
	}
	tail := string(data[off:])
	found := ""
	for _, line := range strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n") {
		low := strings.ToLower(line)
		for _, s := range fatalSigns {
			if strings.Contains(low, s) {
				found = firstLine(line)
				break
			}
		}
	}
	if len(found) > 200 {
		found = found[:200]
	}
	return found
}

// rotateLogIfNeeded 隧道日志无人重连时每 3 秒就可能刷一行，超过 2MB 时截断保留末尾。
func rotateLogIfNeeded() {
	st, err := os.Stat(LogPath())
	if err != nil || st.Size() < 2<<20 {
		return
	}
	data, err := os.ReadFile(LogPath())
	if err != nil {
		return
	}
	_ = os.WriteFile(LogPath(), data[len(data)-(200<<10):], 0o600)
}

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

	// 运行中一律以「启动那一刻实际生效的配置」为准：用户改了端口/主机但没重启隧道时，
	// 继续显示新值等于谎报「新端口已经通了」，而 ssh 还挂着旧参数。
	shown, drift := c, false
	if running && m.hasRun {
		shown = m.runCfg
		drift = shown != c
	}
	// 就绪与否由 confirmLoop 判定（进程活过 3 秒 + 日志无致命错误），
	// 不再用「存活满 2 秒」这种猜法。
	ready := running && m.status == "running"

	return map[string]any{
		"enabled":                c.Enabled,
		"status":                 m.status,
		"running":                running,
		"ready":                  ready,
		"needs_restart":          drift,
		"pid":                    m.pid,
		"uptime_seconds":         uptime,
		"restarts":               m.restarts,
		"last_error":             m.lastErr,
		"pem_path":               shown.PemPath,
		"host":                   shown.Host,
		"user":                   shown.User,
		"remote_port":            shown.RemotePort,
		"local_port":             shown.LocalPort,
		"disable_auto_reconnect": c.DisableAutoReconnect,
		"extra_args":             c.ExtraArgs,
		"log_path":               LogPath(),
		"remote_url":             fmt.Sprintf("http://localhost:%d", shown.RemotePort),
		"command":                strings.Join(append([]string{"ssh"}, buildArgs(shown)...), " "),
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
