// Package sidecar 管理内置的 CLIProxyAPI 边车进程。
//
// CLIProxyAPI 负责把 Claude Code / Codex / Antigravity / Grok / Kimi 等订阅账号
// 通过 OAuth 登录后包装成 OpenAI / Claude / Gemini / Codex 兼容的 API。
// ApiCluster 只负责：拉起进程、守护重启、代理它的管理接口、提供图形界面。
package sidecar

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
	defaultPort = 8317
	// CREATE_NO_WINDOW：不给子进程分配控制台窗口
	createNoWindow = 0x08000000
	// 健康检查总超时
	readyTimeout = 45 * time.Second
	// 异常退出后的最大自动重启次数
	maxRestarts = 5
	// 稳定运行超过这个时长后再崩，重新给满自动重启配额
	stableUptime = 60 * time.Second
)

// Manager 边车进程管理器（并发安全）
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	logFile   *os.File
	status    string // stopped / starting / running / crashed
	ready     bool
	pid       int
	startedAt time.Time
	lastErr   string
	restarts  int
	stopping  bool
	// authFailed 最近一次管理接口调用是否返回 401（密钥与 config.yaml 里的哈希不是同一把）
	authFailed bool

	client *http.Client
}

// New 创建边车管理器
func New() *Manager {
	return &Manager{
		status: "stopped",
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// ===== 路径与配置 =====

// Dir 边车数据目录（<数据目录>/cliproxy）
func Dir() string {
	return filepath.Join(config.DataDir(), "cliproxy")
}

// defaultExeCandidates 返回 CLIProxyAPI.exe 的候选路径（按优先级）
func defaultExeCandidates() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		out = append(out,
			filepath.Join(base, "CLIProxyAPI.exe"),
			filepath.Join(base, "sidecar", "CLIProxyAPI.exe"),
		)
	}
	out = append(out, filepath.Join(Dir(), "CLIProxyAPI.exe"))
	return out
}

// ResolveExe 解析可用的 CLIProxyAPI.exe 路径
func ResolveExe(explicit string) (string, bool) {
	if explicit != "" {
		if st, err := os.Stat(explicit); err == nil && !st.IsDir() {
			return explicit, true
		}
	}
	for _, p := range defaultExeCandidates() {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Ensure 补齐边车设置：生成 API Key / 管理密钥 / 目录 / config.yaml。
// 返回是否发生了变更（需要落盘）。
func (m *Manager) Ensure() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked()
}

func (m *Manager) ensureLocked() (bool, error) {
	cs := config.Get().Settings.CliProxy
	changed := false

	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return false, err
	}
	if cs.Port == 0 {
		cs.Port = defaultPort
		changed = true
	}
	if cs.APIKey == "" {
		cs.APIKey = "sk-apicluster-" + randomHex(16)
		changed = true
	}
	if cs.MgmtKey == "" {
		cs.MgmtKey = randomHex(24)
		changed = true
	}
	if cs.ConfigPath == "" {
		cs.ConfigPath = filepath.Join(Dir(), "config.yaml")
		changed = true
	}
	if cs.AuthDir == "" {
		cs.AuthDir = filepath.Join(Dir(), "auths")
		changed = true
	}
	if err := os.MkdirAll(cs.AuthDir, 0o755); err != nil {
		return changed, err
	}

	if changed {
		s := config.Get().Settings
		s.CliProxy = cs
		config.SetSettings(s)
	}

	// config.yaml 不存在或与当前设置不一致时重写。
	// 注意：边车 CLIProxyAPI 会把明文 secret-key 用 bcrypt 哈希后回写 config.yaml。
	// 因此这里若发现现有 secret-key 已是 bcrypt 哈希，就复用原值，避免「明文 ↔ bcrypt」
	// 反复重写触发边车无限 reload（这也是管理面板偶发 401 的根源）。
	secretKey := cs.MgmtKey
	cur, readErr := os.ReadFile(cs.ConfigPath)
	if readErr == nil {
		if existing := extractSecretKey(string(cur)); looksLikeBcrypt(existing) {
			secretKey = existing
		}
	}
	want := renderConfig(cs, secretKey)
	if readErr != nil || string(cur) != want {
		if err := os.WriteFile(cs.ConfigPath, []byte(want), 0o600); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// extractSecretKey 从 config.yaml 文本里提取 remote-management.secret-key 的值（去掉引号）
func extractSecretKey(yamlText string) string {
	for _, line := range strings.Split(yamlText, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "secret-key:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, "secret-key:"))
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}

// looksLikeBcrypt 判断值是否为 bcrypt 哈希（$2a$ / $2b$ / $2y$ 前缀）
func looksLikeBcrypt(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}

// renderConfig 生成 CLIProxyAPI 的 config.yaml
func renderConfig(cs config.CliProxySettings, secretKey string) string {
	yamlPath := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	var b strings.Builder
	b.WriteString("# 本文件由 ApiCluster 自动生成，每次启动/改设置都会被覆盖，请勿手工修改。\n")
	b.WriteString("# 要改端口、exe 路径或密钥，请在 ApiCluster 的「订阅账号」页操作。\n")
	b.WriteString("host: \"127.0.0.1\"\n")
	fmt.Fprintf(&b, "port: %d\n", cs.Port)
	fmt.Fprintf(&b, "auth-dir: \"%s\"\n", yamlPath(cs.AuthDir))
	b.WriteString("api-keys:\n")
	fmt.Fprintf(&b, "  - \"%s\"\n", cs.APIKey)
	b.WriteString("remote-management:\n")
	b.WriteString("  allow-remote: false\n")
	fmt.Fprintf(&b, "  secret-key: \"%s\"\n", secretKey)
	b.WriteString("  # 启用内置 CPAMC 管理面板（首次需联网从 GitHub 下载 management.html）\n")
	b.WriteString("  disable-control-panel: false\n")
	b.WriteString("usage-statistics-enabled: true\n")
	b.WriteString("debug: false\n")
	b.WriteString("logging-to-file: false\n")
	b.WriteString("routing:\n")
	b.WriteString("  strategy: \"round-robin\"\n")
	return b.String()
}

// LogPath 边车日志文件路径
func LogPath() string { return filepath.Join(Dir(), "cliproxy.log") }

// ===== 进程生命周期 =====

// StartIfEnabled 若设置为启用则启动边车（失败只记录，不阻塞 ApiCluster）
func (m *Manager) StartIfEnabled() {
	changed, err := m.Ensure()
	if err != nil {
		m.setErr("准备边车配置失败: " + err.Error())
		return
	}
	if changed {
		_ = config.Save()
	}
	if !config.Get().Settings.CliProxy.Enabled {
		return
	}
	if err := m.Start(); err != nil {
		m.setErr(err.Error())
	}
}

// Start 启动边车进程
func (m *Manager) Start() error {
	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil && m.status != "stopped" && m.status != "crashed" {
		m.mu.Unlock()
		return nil // 已在运行
	}
	m.stopping = false
	m.mu.Unlock()

	if _, err := m.Ensure(); err != nil {
		return err
	}
	cs := config.Get().Settings.CliProxy

	exe, ok := ResolveExe(cs.ExePath)
	if !ok {
		return fmt.Errorf("未找到 CLIProxyAPI.exe，请把它放到 ApiCluster.exe 同目录，或在「订阅账号」页指定路径")
	}

	// 端口占用检测（若为上一轮残留的边车实例则先清理）
	if err := m.cleanupOrphan(cs.Port); err != nil {
		return err
	}

	logF, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("无法写入边车日志: %w", err)
	}

	cmd := exec.Command(exe, "-config", cs.ConfigPath)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Stdout = logF
	cmd.Stderr = logF

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("启动 CLIProxyAPI 失败: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.logFile = logF
	m.status = "starting"
	m.ready = false
	m.pid = cmd.Process.Pid
	m.startedAt = time.Now()
	m.lastErr = ""
	m.authFailed = false
	m.mu.Unlock()

	go m.waitLoop(cmd)
	go m.healthLoop()
	return nil
}

// portOwner 返回监听该端口的进程 PID 与进程名（无人监听返回 0,""）。
// 用 PowerShell 查一次，只在端口被占这类少见分支里调用。
func portOwner(port int) (int, string) {
	ps := fmt.Sprintf(
		"$c = Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1;"+
			" if ($c) { $p = Get-Process -Id $c.OwningProcess -ErrorAction SilentlyContinue; if ($p) { Write-Output (\"{0}|{1}\" -f $p.Id, $p.ProcessName) } }",
		port)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return 0, ""
	}
	line := strings.TrimSpace(string(out))
	i := strings.LastIndex(line, "|")
	if i <= 0 {
		return 0, ""
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(line[:i]))
	return pid, strings.TrimSpace(line[i+1:])
}

// probeWithMgmtKey 用当前管理密钥访问边车：200 表示这就是我们自己那台实例。
func probeWithMgmtKey(port int) bool {
	cs := config.Get().Settings.CliProxy
	if cs.MgmtKey == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v0/management/config", port), nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Management-Key", cs.MgmtKey)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// portListening 端口上是否有人监听（先花 150ms 探一下，避免每 5 秒去起 PowerShell）
func portListening(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Orphan 报告「端口上有一台边车在跑、但不归本管理器管」这种状态。
// ApiCluster 被强杀时子进程不会被级联结束；界面只按 m.cmd 判断的话，
// 会显示「已停止」而端口实际还在被用。
func (m *Manager) Orphan() map[string]any {
	cs := config.Get().Settings.CliProxy
	port := cs.Port
	if port == 0 {
		port = defaultPort
	}
	out := map[string]any{"port": port, "port_busy": false, "ours": false, "pid": 0, "name": ""}
	if m.isRunning() {
		out["port_busy"] = true
		out["ours"] = true
		return out
	}
	if !portListening(port) {
		return out
	}
	pid, name := portOwner(port)
	if pid == 0 {
		out["port_busy"] = true // 有人监听但查不到是谁（权限或时序）
		return out
	}
	out["port_busy"], out["pid"], out["name"] = true, pid, name
	out["ours"] = probeWithMgmtKey(port) // 密钥能通 = 上一轮残留的自家实例
	return out
}

// KillOrphan 结束占用端口的那台残留边车。只动「用我们的管理密钥能鉴权通过」
// 的那一个 PID：按镜像名 taskkill /IM 会连带杀掉用户自己另外开的 CLIProxyAPI。
func (m *Manager) KillOrphan() error {
	port := config.Get().Settings.CliProxy.Port
	if port == 0 {
		port = defaultPort
	}
	pid, name := portOwner(port)
	if pid == 0 {
		return nil // 端口本来就空着
	}
	if name != "CLIProxyAPI" || !probeWithMgmtKey(port) {
		return fmt.Errorf("端口 %d 被 PID %d（%s）占用，它不是 ApiCluster 启动的边车，不会替你结束它；请改端口或自行处理该进程", port, pid, name)
	}
	if err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		return fmt.Errorf("结束残留边车（PID %d）失败: %w", pid, err)
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

// isRunning 是否有由本管理器启动的进程在跑
func (m *Manager) isRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// cleanupOrphan 启动前检查端口占用：占用者若是能用我们的管理密钥鉴权通过的
// CLIProxyAPI，就认定是上一轮残留的自家实例，只结束那一个 PID。
func (m *Manager) cleanupOrphan(port int) error {
	if !portListening(port) {
		return nil // 端口空闲（绝大多数情况，省一次 PowerShell）
	}
	pid, name := portOwner(port)
	if pid == 0 {
		return nil // 监听者查不到（权限/时序）：交给后面的端口绑定去报错
	}
	if name != "CLIProxyAPI" || !probeWithMgmtKey(port) {
		return fmt.Errorf("边车端口 %d 已被其他程序占用（PID %d，%s），请关闭该程序或换端口", port, pid, name)
	}
	if err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		return fmt.Errorf("结束残留边车（PID %d）失败: %w", pid, err)
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

// waitLoop 等待进程退出；非主动停止时按退避策略自动重启
func (m *Manager) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()

	m.mu.Lock()
	if m.cmd != cmd {
		m.mu.Unlock()
		return // 已被新实例替换
	}
	stopping := m.stopping
	m.cmd = nil
	m.ready = false
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
		m.lastErr = "边车异常退出: " + err.Error()
	} else {
		m.lastErr = "边车已退出"
	}
	// 稳态跑过一阵子之后再崩，算新一轮故障，重新给满重启配额；
	// 秒崩秒起的抖动只消耗同一份配额 —— 否则就绪时清零会让「自动重启」变成无限重启。
	if !m.startedAt.IsZero() && time.Since(m.startedAt) >= stableUptime {
		m.restarts = 0
	}
	// 「随 ApiCluster 启动」关着时不做崩溃自愈：用户手动点过一次「启动」，
	// 不等于同意这个进程在他关掉以后被无限拉起来。
	enabled := config.Get().Settings.CliProxy.Enabled
	shouldRestart := enabled && m.restarts < maxRestarts
	switch {
	case shouldRestart:
		m.restarts++
	case enabled:
		m.lastErr += "（已达自动重启上限，请查看日志）"
	default:
		m.lastErr += "（未开启「随 ApiCluster 启动」，崩溃后不再自动重启）"
	}
	attempt := m.restarts
	m.mu.Unlock()

	if !shouldRestart {
		return
	}
	// 退避后重启
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

// healthLoop 轮询 /healthz 直到就绪。
// 端口每轮都从当前配置读：用户在「订阅账号」页改了端口还没重启边车时，
// 探测旧端口会把 ready 报成 true（实际端口上没人监听），是种说谎的就绪状态。
func (m *Manager) healthLoop() {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		alive := m.cmd != nil
		m.mu.Unlock()
		if !alive {
			return
		}
		port := config.Get().Settings.CliProxy.Port
		if port == 0 {
			port = defaultPort
		}
		resp, err := m.client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				m.mu.Lock()
				m.ready = true
				m.status = "running"
				m.lastErr = "" // 已经好了就别再把上一次的崩溃提示常驻在页面上
				m.mu.Unlock()
				return
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	m.mu.Lock()
	if m.cmd != nil {
		m.lastErr = "边车启动超时，请查看日志"
	}
	m.mu.Unlock()
}

// Stop 停止边车进程
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.stopping = true
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		m.mu.Lock()
		m.status = "stopped"
		m.ready = false
		m.pid = 0
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
	m.ready = false
	m.pid = 0
	m.mu.Unlock()
}

// Restart 重启边车
func (m *Manager) Restart() error {
	m.Stop()
	m.mu.Lock()
	m.restarts = 0
	m.stopping = false
	m.mu.Unlock()
	// 等端口释放
	time.Sleep(600 * time.Millisecond)
	return m.Start()
}

func (m *Manager) setErr(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	if m.status == "stopped" {
		m.status = "crashed"
	}
	m.mu.Unlock()
}

// ===== 状态查询 =====

// Status 返回边车状态快照（供管理界面使用）
func (m *Manager) Status() map[string]any {
	cs := config.Get().Settings.CliProxy
	exe, exeOK := ResolveExe(cs.ExePath)

	m.mu.Lock()
	defer m.mu.Unlock()

	var uptime int64
	if m.pid != 0 && !m.startedAt.IsZero() {
		uptime = int64(time.Since(m.startedAt).Seconds())
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cs.Port)
	return map[string]any{
		"enabled":          cs.Enabled,
		"status":           m.status,
		"running":          m.cmd != nil,
		"ready":            m.ready,
		"pid":              m.pid,
		"port":             cs.Port,
		"uptime_seconds":   uptime,
		"restarts":         m.restarts,
		"last_error":       m.lastErr,
		"exe_path":         exe,
		"exe_path_setting": cs.ExePath,
		"exe_found":        exeOK,
		"config_path":      cs.ConfigPath,
		"auth_dir":         cs.AuthDir,
		"log_path":         LogPath(),
		"api_key":          cs.APIKey,
		"mgmt_key":         cs.MgmtKey,
		"auth_failed":      m.authFailed,
		"base_url":         base + "/v1",
		"endpoints": map[string]string{
			"OpenAI": base + "/v1",
			"Claude": base + "/v1/messages",
			"Gemini": base + "/v1beta",
			"Codex":  base + "/backend-api/codex",
			"健康检查":   base + "/healthz",
		},
	}
}

// ===== 管理接口代理 =====

// Mgmt 调用边车的管理接口（自动带上管理密钥）
func (m *Manager) Mgmt(method, sub, rawQuery string, body []byte) (int, []byte, error) {
	cs := config.Get().Settings.CliProxy
	if !strings.HasPrefix(sub, "/") || strings.Contains(sub, "..") {
		return 0, nil, fmt.Errorf("非法管理接口路径")
	}
	m.mu.Lock()
	running := m.cmd != nil
	m.mu.Unlock()
	if !running {
		return 0, nil, fmt.Errorf("订阅账号服务未运行，请先点击「启动」")
	}

	target := fmt.Sprintf("http://127.0.0.1:%d/v0/management%s", cs.Port, sub)
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Management-Key", cs.MgmtKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("管理接口请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	m.mu.Lock()
	if resp.StatusCode == http.StatusUnauthorized {
		m.authFailed = true
	} else if resp.StatusCode < 400 {
		m.authFailed = false
	}
	m.mu.Unlock()
	return resp.StatusCode, data, nil
}

// RepairMgmtKey 修复管理面板 401（invalid management key）。
//
// 边车加载配置时会把明文 secret-key 用 bcrypt 哈希并回写 config.yaml；
// 而 ensureLocked 为了不触发边车无限 reload，会一直复用那份哈希。
// 一旦 keys.json 里的明文与那把哈希不是同一把（配置从 .bak 回滚、手改过
// config.yaml、旧目录迁移等），面板就永远鉴权失败，且无法自愈。
// 这里绕过复用逻辑，把明文重新写回去（原文件备份为 config.yaml.bak），
// 边车下次启动时会重新哈希，两边恢复一致。regenerate 为 true 时先换一把新密钥。
func (m *Manager) RepairMgmtKey(regenerate bool) (string, error) {
	if regenerate {
		s := config.Get().Settings
		s.CliProxy.MgmtKey = randomHex(24)
		config.SetSettings(s)
		if err := config.Save(); err != nil {
			return "", err
		}
	}
	if _, err := m.Ensure(); err != nil { // 补齐密钥 / 目录 / 配置路径
		return "", err
	}
	cs := config.Get().Settings.CliProxy
	if cs.ConfigPath == "" || cs.MgmtKey == "" {
		return "", fmt.Errorf("边车密钥或配置路径为空，无法修复")
	}
	if old, err := os.ReadFile(cs.ConfigPath); err == nil && len(old) > 0 {
		_ = os.WriteFile(cs.ConfigPath+".bak", old, 0o600)
	}
	if err := os.WriteFile(cs.ConfigPath, []byte(renderConfig(cs, cs.MgmtKey)), 0o600); err != nil {
		return "", fmt.Errorf("写入 config.yaml 失败: %w", err)
	}
	m.mu.Lock()
	m.authFailed = false
	m.mu.Unlock()
	return cs.MgmtKey, nil
}

// VerifyMgmtKey 用当前密钥探测管理接口：200 为 true，401 立即 false，
// 边车还没起来时继续等到 timeout。
func (m *Manager) VerifyMgmtKey(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		code, _, err := m.Mgmt(http.MethodGet, "/config", "", nil)
		if err == nil {
			if code == http.StatusOK {
				return true
			}
			if code == http.StatusUnauthorized {
				return false
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// LogTail 读取边车日志末尾若干行
func (m *Manager) LogTail(maxLines int) string {
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

// LoginURL 请求某个 provider 的 OAuth 登录地址。
// is_webui=1 让边车启动本地回调转发，浏览器授权后能回到边车完成换取 token。
func (m *Manager) LoginURL(provider string) (string, string, error) {
	sub, ok := map[string]string{
		"claude":      "/anthropic-auth-url",
		"codex":       "/codex-auth-url",
		"antigravity": "/antigravity-auth-url",
		"kimi":        "/kimi-auth-url",
		"xai":         "/xai-auth-url",
	}[provider]
	if !ok {
		return "", "", fmt.Errorf("不支持的账号类型: %s", provider)
	}
	code, data, err := m.Mgmt(http.MethodGet, sub, "is_webui=1", nil)
	if err != nil {
		return "", "", err
	}
	if code != http.StatusOK {
		return "", "", fmt.Errorf("边车返回 HTTP %d: %s", code, truncate(string(data), 200))
	}
	var out struct {
		URL   string `json:"url"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", fmt.Errorf("解析登录地址失败: %w", err)
	}
	if out.Error != "" {
		return "", "", fmt.Errorf("%s", out.Error)
	}
	if out.URL == "" {
		return "", "", fmt.Errorf("边车未返回登录地址")
	}
	return out.URL, out.State, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// OAuthProviders 支持的账号类型（供界面展示）
func OAuthProviders() []map[string]string {
	return []map[string]string{
		{"id": "claude", "name": "Claude Code", "hint": "Anthropic 订阅账号"},
		{"id": "codex", "name": "Codex", "hint": "OpenAI ChatGPT 订阅账号"},
		{"id": "antigravity", "name": "Antigravity", "hint": "Google Gemini 账号"},
		{"id": "kimi", "name": "Kimi", "hint": "Moonshot 账号"},
		{"id": "xai", "name": "Grok (xAI)", "hint": "xAI 账号"},
	}
}

// EndpointHelp 端点说明（供界面展示）
func EndpointHelp(port int) []map[string]string {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	return []map[string]string{
		{"name": "OpenAI 兼容", "path": base + "/v1", "note": "chat/completions、responses、images、videos"},
		{"name": "Claude 兼容", "path": base + "/v1/messages", "note": "Anthropic Messages API"},
		{"name": "Gemini 兼容", "path": base + "/v1beta", "note": "generateContent / streamGenerateContent"},
		{"name": "Codex 兼容", "path": base + "/backend-api/codex", "note": "Codex CLI chatgpt_base_url"},
	}
}
