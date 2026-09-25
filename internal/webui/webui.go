// Package webui 提供管理界面（内嵌静态资源 + REST API）。
// UI 在 WebView2 桌面窗口中渲染，也可用浏览器直接访问。
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"apicluster/internal/autostart"
	"apicluster/internal/balance"
	"apicluster/internal/catalog"
	"apicluster/internal/config"
	"apicluster/internal/proxy"
	"apicluster/internal/sidecar"
	"apicluster/internal/tunnel"
)

//go:embed static
var staticFS embed.FS

// Handler 管理界面处理器
type Handler struct {
	proxyPort int
	cli       *sidecar.Manager
	tun       *tunnel.Manager
	// Restart 由 main 注入：安排重启自身（停边车/停隧道后重新拉起新进程）。
	// 为 nil 时「重启程序」不可用（例如只跑 HTTP 服务的场景）。
	Restart func()
}

// New 创建 Handler
func New(proxyPort int, cli *sidecar.Manager, tun *tunnel.Manager) *Handler {
	return &Handler{proxyPort: proxyPort, cli: cli, tun: tun}
}

// ServeHTTP 路由
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/config" && r.Method == http.MethodGet:
		h.handleGetConfig(w, r)
	case path == "/api/key" && r.Method == http.MethodPost:
		h.handleSetKey(w, r)
	case path == "/api/key" && r.Method == http.MethodDelete:
		h.handleDeleteKey(w, r)
	case path == "/api/custom" && r.Method == http.MethodPost:
		h.handleUpsertCustom(w, r)
	case path == "/api/custom" && r.Method == http.MethodDelete:
		h.handleDeleteCustom(w, r)
	case path == "/api/settings" && r.Method == http.MethodPost:
		h.handleSettings(w, r)
	case path == "/api/schemes" && r.Method == http.MethodPost:
		h.handleSchemes(w, r)
	case path == "/api/vault" && r.Method == http.MethodPost:
		h.handleVault(w, r)
	case path == "/api/marks" && r.Method == http.MethodPost:
		h.handleMarks(w, r)
	case path == "/api/vault/export" && r.Method == http.MethodPost:
		h.handleVaultExport(w, r)
	case path == "/api/test" && r.Method == http.MethodPost:
		h.handleTest(w, r)
	case path == "/api/balance" && r.Method == http.MethodGet:
		h.handleBalance(w, r)
	case path == "/api/open" && r.Method == http.MethodGet:
		h.handleOpen(w, r)
	case path == "/api/theme" && r.Method == http.MethodGet:
		h.handleTheme(w, r)
	case path == "/api/restart" && r.Method == http.MethodPost:
		h.handleRestart(w, r)
	// ===== 内置 CLIProxyAPI 边车（订阅账号） =====
	case path == "/api/cliproxy/status" && r.Method == http.MethodGet:
		h.handleCliStatus(w, r)
	case path == "/api/cliproxy/action" && r.Method == http.MethodPost:
		h.handleCliAction(w, r)
	case path == "/api/cliproxy/settings" && r.Method == http.MethodPost:
		h.handleCliSettings(w, r)
	case path == "/api/cliproxy/fix-mgmt-key" && r.Method == http.MethodPost:
		h.handleCliFixKey(w, r)
	case path == "/api/cliproxy/browse-exe" && r.Method == http.MethodPost:
		h.handleCliBrowseExe(w, r)
	case path == "/api/cliproxy/kill-orphan" && r.Method == http.MethodPost:
		h.handleCliKillOrphan(w, r)
	case path == "/api/cliproxy/mgmt":
		h.handleCliMgmt(w, r)
	case path == "/api/cliproxy/login" && r.Method == http.MethodGet:
		h.handleCliLogin(w, r)
	case path == "/api/cliproxy/log" && r.Method == http.MethodGet:
		h.handleCliLog(w, r)
	case path == "/api/cliproxy/open-terminal" && r.Method == http.MethodPost:
		h.handleCliOpenTerminal(w, r)
	case path == "/api/cliproxy/open-panel" && r.Method == http.MethodPost:
		h.handleCliOpenPanel(w, r)
	// ===== SSH 反向隧道 =====
	case path == "/api/tunnel/status" && r.Method == http.MethodGet:
		h.handleTunnelStatus(w, r)
	case path == "/api/tunnel/settings" && r.Method == http.MethodPost:
		h.handleTunnelSettings(w, r)
	case path == "/api/tunnel/action" && r.Method == http.MethodPost:
		h.handleTunnelAction(w, r)
	case path == "/api/tunnel/log" && r.Method == http.MethodGet:
		h.handleTunnelLog(w, r)
	case path == "/api/tunnel/probe" && r.Method == http.MethodPost:
		h.handleTunnelProbe(w, r)
	case path == "/api/tunnel/kill-orphan" && r.Method == http.MethodPost:
		h.handleTunnelKillOrphan(w, r)
	case path == "/api/tunnel/browse-pem" && r.Method == http.MethodPost:
		h.handleTunnelBrowsePem(w, r)
	default:
		h.serveStatic(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": catalog.Providers,
		"keys":      cfg.Keys,
		"custom":    cfg.Custom,
		"settings":  cfg.Settings,
		"schemes":   cfg.Schemes,
		"vault":     cfg.Vault,
		"marks":     cfg.Marks,
		"autostart": autostart.IsEnabled(),
		// 进程实际监听的端口（settings.proxy_port 是「配置值」，两者不等就说明没重启）
		"running_port": h.proxyPort,
	})
}

func (h *Handler) handleSetKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID string              `json:"provider_id"`
		APIKey     string              `json:"api_key"`   // 向后兼容：单 key
		APIKeys    []string            `json:"api_keys"`  // 新格式：多 key 列表（顺序即优先级，首位 = 当前使用）
		KeyNames   []string            `json:"key_names"` // 与 api_keys 一一对应的自定义名称
		KeyNos     []int               `json:"key_nos"`   // 与 api_keys 一一对应的稳定序号
		KeyIndex   int                 `json:"key_index"`
		Type       string              `json:"type"`
		BaseURL    string              `json:"base_url"`
		Path       string              `json:"path"`
		AuthHeader string              `json:"auth_header"`
		AuthPrefix string              `json:"auth_prefix"`
		Models      []string                 `json:"models"`
		ModelCaps   map[string][]string      `json:"model_caps"`   // 覆盖模型的能力标签（模型ID -> text/code/...）
		ModelLimits map[string]config.ModelLimit `json:"model_limits"` // 单个模型的并发/每分钟请求数限制
		Quota       int64                    `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg := config.Get()
	existing := cfg.Keys[body.ProviderID]
	// 内置厂商的 Key 列表按「整体替换」处理：界面每次发来的是卡片上的完整状态，
	// 空列表就意味着用户把输入框清空了、要删掉 Key。
	// 旧写法在列表为空时两个分支都不进，于是旧 Key 原样留着，
	// 而名称/编号已被清空 —— 表现是「Key 没删掉，名字先没了」。
	if len(body.APIKeys) > 0 {
		existing.APIKeys = body.APIKeys
		existing.APIKey = "" // 清空旧单 key
	} else if body.APIKey != "" {
		existing.APIKey = body.APIKey
		existing.APIKeys = []string{body.APIKey}
	} else {
		existing.APIKeys = nil
		existing.APIKey = ""
	}
	existing.KeyNames = body.KeyNames
	existing.KeyNos = body.KeyNos
	existing.KeyIndex = body.KeyIndex
	// 规范化：补齐名称、把当前使用的 key 移到列表首位
	existing.NormalizeKeys()
	existing.Type = body.Type
	existing.BaseURL = body.BaseURL
	existing.Path = body.Path
	existing.AuthHeader = body.AuthHeader
	existing.AuthPrefix = body.AuthPrefix
	existing.Models = body.Models
	existing.ModelCaps = normalizeModelCaps(body.ModelCaps, body.Models)
	existing.ModelLimits = normalizeModelLimits(body.ModelLimits, body.Models)
	existing.Quota = body.Quota
	config.SetKey(body.ProviderID, existing)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// normalizeModelCaps 清洗覆盖模型的能力标签：只保留 models 里存在的 ID、
// 去掉空标签项；结果为空时返回 nil（缺省 = 按目录默认能力）。
func normalizeModelCaps(caps map[string][]string, models []string) map[string][]string {
	if len(caps) == 0 || len(models) == 0 {
		return nil
	}
	valid := make(map[string]bool, len(models))
	for _, m := range models {
		valid[m] = true
	}
	out := make(map[string][]string, len(caps))
	for id, list := range caps {
		if !valid[id] {
			continue
		}
		cl := make([]string, 0, len(list))
		for _, c := range list {
			c = strings.TrimSpace(c)
			if c != "" {
				cl = append(cl, c)
			}
		}
		if len(cl) > 0 {
			out[id] = cl
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeModelLimits 清洗模型限流配置：只保留 models 里存在且确有约束（并发/每分钟 > 0）的项
func normalizeModelLimits(limits map[string]config.ModelLimit, models []string) map[string]config.ModelLimit {
	if len(limits) == 0 || len(models) == 0 {
		return nil
	}
	valid := make(map[string]bool, len(models))
	for _, m := range models {
		valid[m] = true
	}
	out := make(map[string]config.ModelLimit, len(limits))
	for id, l := range limits {
		if !valid[id] {
			continue
		}
		if l.Concurrency <= 0 && l.RPM <= 0 {
			continue
		}
		out[id] = l
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *Handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("provider_id")
	config.DeleteKey(id)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) handleUpsertCustom(w http.ResponseWriter, r *http.Request) {
	var p config.CustomProvider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if p.ID == "" {
		p.ID = "custom-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	// 规范化 key
	p.NormalizeKeys()
	config.UpsertCustom(p)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}

func (h *Handler) handleDeleteCustom(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	config.DeleteCustom(id)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSettings 保存全局设置。
// 必须在现有 Settings 上逐字段覆盖：前端只发它自己那几项，
// 若把请求体整个反序列化再写入，cliproxy（含管理密钥）与 tunnel 配置会被清零，
// 表现就是「拨一下开机自启，订阅账号边车/远程隧道全回到出厂」+ 管理面板 401。
func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s := config.Get().Settings
	assign := func(key string, dst any) error {
		raw, ok := body[key]
		if !ok {
			return nil // 没发过来的字段保持原值
		}
		return json.Unmarshal(raw, dst)
	}
	fields := []struct {
		key string
		dst any
	}{
		{"autostart", &s.AutoStart},
		{"proxy_port", &s.ProxyPort},
		{"auto_open_browser", &s.AutoOpenBrowser},
		{"auto_route_enabled", &s.AutoRouteEnabled},
		{"auto_models", &s.AutoModels},
		{"auto_provider_order", &s.AutoProviderOrder},
	}
	for _, f := range fields {
		if err := assign(f.key, f.dst); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": f.key + ": " + err.Error()})
			return
		}
	}
	if s.ProxyPort == 0 {
		s.ProxyPort = 3003
	}
	config.SetSettings(s)
	var autoErr string
	if s.AutoStart {
		if err := autostart.Enable(); err != nil {
			autoErr = err.Error()
			// 开启失败就把开关拨回已保存的值，别让界面显示「已开」而注册表里没写
			s.AutoStart = false
			config.SetSettings(s)
		}
	} else if err := autostart.Disable(); err != nil {
		autoErr = err.Error()
	}
	_ = config.Save()
	resp := map[string]any{"ok": true, "autostart": autostart.IsEnabled(),
		// 真实在监听的端口：界面用它判断「配置已改但还没重启」
		"running_port": h.proxyPort}
	if autoErr != "" {
		resp["error"] = autoErr
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSchemes 整体保存自动路由方案（一个别名 → 一组有序模型）。
// 名字与内置别名（inurl / inurl-code / …）冲突的方案会被剔掉并回给前端提示，
// 而不是整批拒绝 —— 后者会让用户改一个名字就丢掉其他方案的编辑。
func (h *Handler) handleSchemes(w http.ResponseWriter, r *http.Request) {
	var schemes []config.RouteScheme
	if err := json.NewDecoder(r.Body).Decode(&schemes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	kept := make([]config.RouteScheme, 0, len(schemes))
	rejected := make([]string, 0, 2)
	for _, sc := range schemes {
		if proxy.IsReservedAlias(sc.Name) {
			rejected = append(rejected, sc.Name)
			continue
		}
		kept = append(kept, sc)
	}
	config.SetSchemes(kept)
	_ = config.Save()
	resp := map[string]any{"ok": true}
	if len(rejected) > 0 {
		resp["rejected"] = rejected
		resp["error"] = "方案名不能与内置别名相同：" + strings.Join(rejected, "、") + "（这些名字已由自动路由占用）"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMarks 整体保存密钥库的收藏 / 分组标记（按厂商 ID 关联）。
// 全量替换：前端发来的是界面上完整的标记集合；空的（未收藏且未分组）不落盘，
// 免得 keys.json 里堆一堆没意义的条目。
func (h *Handler) handleMarks(w http.ResponseWriter, r *http.Request) {
	var in map[string]config.LibraryMark
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	out := make(map[string]config.LibraryMark, len(in))
	for id, m := range in {
		id = strings.TrimSpace(id)
		m.Group = strings.TrimSpace(m.Group)
		if r := []rune(m.Group); len(r) > 16 {
			m.Group = string(r[:16]) // 按字（不是字节）截断，避免把中文分组名切出乱码
		}
		if id == "" || (!m.Favorite && m.Group == "") {
			continue
		}
		out[id] = m
	}
	config.SetMarks(out)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "marks": out})
}

// maxVaultLogoBytes 内联 logo（data URL）的体积上限。前端上传时会先用 canvas
// 压缩到 128px，正常只有几 KB；这里兜住「直接拖入原图」的情况。
const maxVaultLogoBytes = 2 << 20

// handleVault 整体保存账号库（网站登录账号 / 密码）。
// 与自动路由方案一致采用「全量替换」：前端任何改动都提交完整列表。
func (h *Handler) handleVault(w http.ResponseWriter, r *http.Request) {
	var sites []config.VaultSite
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&sites); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cleaned, err := normalizeVault(sites)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	config.SetVault(cleaned)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vault": cleaned})
}

// normalizeVault 清洗账号库：填 ID、去空白、丢弃空行空站点、校验 logo 形式。
func normalizeVault(sites []config.VaultSite) ([]config.VaultSite, error) {
	out := make([]config.VaultSite, 0, len(sites))
	seen := make(map[string]bool, len(sites))
	for _, s := range sites {
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		s.Note = strings.TrimSpace(s.Note)
		s.Logo = strings.TrimSpace(s.Logo)
		accounts := make([]config.VaultAccount, 0, len(s.Accounts))
		for _, a := range s.Accounts {
			a.Label = strings.TrimSpace(a.Label)
			a.User = strings.TrimSpace(a.User)
			a.Note = strings.TrimSpace(a.Note)
			// Password 不 trim：密码可能以空格开头/结尾
			if a.Label == "" && a.User == "" && a.Password == "" && a.Note == "" {
				continue
			}
			accounts = append(accounts, a)
		}
		s.Accounts = accounts
		if s.Name == "" && s.URL == "" && len(accounts) == 0 {
			continue // 点了「新建」却什么都没填
		}
		if s.Logo != "" {
			remote := strings.HasPrefix(s.Logo, "http://") || strings.HasPrefix(s.Logo, "https://")
			if !remote && !strings.HasPrefix(s.Logo, "data:image/") {
				return nil, fmt.Errorf("logo 仅支持 http(s) 图标地址或图片 data URL")
			}
			if len(s.Logo) > maxVaultLogoBytes {
				return nil, fmt.Errorf("logo 图片过大，请选择更小的图片（上限 2MB）")
			}
		}
		if s.ID == "" || seen[s.ID] {
			s.ID = fmt.Sprintf("site-%d-%d", time.Now().UnixNano(), len(out))
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out, nil
}

// handleVaultExport 把账号库导出成一份 Markdown 文件：弹系统「另存为」对话框选位置后写入。
// 内容取自服务端当前配置，不让前端把密码再上传一遍。
func (h *Handler) handleVaultExport(w http.ResponseWriter, r *http.Request) {
	sites := config.Get().Vault
	if len(sites) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "账号库还是空的，没有可导出的内容"})
		return
	}
	content := vaultMarkdown(sites)
	name := "ApiCluster-账号库-" + time.Now().Format("20060102-1504") + ".md"
	path, err := saveFileDialog("导出账号库", name, content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// saveFileDialog 弹系统「另存为」对话框，用户确认后把 content 写到所选路径。
// 与「远程隧道 → 浏览私钥」同一套做法（PowerShell + Windows Forms），进程隐藏窗口。
func saveFileDialog(title, fileName, content string) (string, error) {
	ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.Title = %s
$d.Filter = 'Markdown (*.md)|*.md|文本文件 (*.txt)|*.txt|所有文件 (*.*)|*.*'
$d.DefaultExt = 'md'
$d.FileName = %s
$d.InitialDirectory = [Environment]::GetFolderPath('Desktop')
if (-not (Test-Path $d.InitialDirectory)) { $d.InitialDirectory = [Environment]::GetFolderPath('UserProfile') }
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Write($d.FileName) }`,
		psQuote(title), psQuote(fileName))
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("打开保存对话框失败: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", nil // 用户取消
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	return path, nil
}

// psQuote 把字符串安全地包成 PowerShell 单引号字面量（内部单引号翻倍）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// vaultMarkdown 账号库 → 可读可粘贴的 Markdown（含明文密码，风险提示写在文件开头）。
func vaultMarkdown(sites []config.VaultSite) string {
	accTotal := 0
	for _, s := range sites {
		accTotal += len(s.Accounts)
	}
	var b strings.Builder
	b.WriteString("# ApiCluster 账号库\n\n")
	fmt.Fprintf(&b, "导出时间：%s　站点 %d 家 / 账号 %d 个\n\n", time.Now().Format("2006-01-02 15:04"), len(sites), accTotal)
	b.WriteString("> ⚠️ 内含明文密码，别发到聊天群、网盘或公开仓库。\n\n")
	for _, s := range sites {
		name := s.Name
		if name == "" {
			name = "（未命名站点）"
		}
		fmt.Fprintf(&b, "## %s\n\n", name)
		if s.URL != "" {
			fmt.Fprintf(&b, "- 网址：%s\n", s.URL)
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "- 站点备注：%s\n", s.Note)
		}
		if len(s.Accounts) == 0 {
			fmt.Fprintf(&b, "- （还没填账号）\n")
		}
		for i, a := range s.Accounts {
			label := a.Label
			if label == "" {
				label = fmt.Sprintf("账号 %d", i+1)
			}
			user := a.User
			if user == "" {
				user = "（没填账号）"
			}
			line := fmt.Sprintf("- **%s**：%s", label, user)
			if a.Password != "" {
				line += " ／ 密码：" + mdCode(a.Password)
			} else {
				line += " ／ 无密码（验证码或第三方登录）"
			}
			b.WriteString(line + "\n")
			if a.Note != "" {
				fmt.Fprintf(&b, "  - 备注：%s\n", a.Note)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// mdCode 用反引号包住密码；密码里本身有反引号时改用明文，避免把 Markdown 打乱。
func mdCode(s string) string {
	if strings.ContainsAny(s, "`\n") {
		return s
	}
	return "`" + s + "`"
}

// handleBalance 查询某厂商的账户余额/额度（多 key 时返回所有 key 的余额）
func (h *Handler) handleBalance(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("provider_id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 provider_id"})
		return
	}
	cfg := config.Get()
	pc, ok := cfg.Keys[id]
	if !ok || !pc.HasAnyKey() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该厂商未配置 API Key"})
		return
	}
	keys := pc.AllKeys()
	results := make([]map[string]any, 0, len(keys))
	for i, key := range keys {
		res := balance.Query(id, key)
		name, no := "", i+1
		if i < len(pc.KeyNames) {
			name = pc.KeyNames[i]
		}
		if i < len(pc.KeyNos) && pc.KeyNos[i] > 0 {
			no = pc.KeyNos[i]
		}
		results = append(results, map[string]any{
			"index":   i,
			"current": i == pc.KeyIndex,
			"name":    name, // 与卡片行标签一致，避免「Key 2」贴到别的 Key 上
			"no":      no,
			"ok":      res.OK,
			"text":    res.Text,
			"error":   res.Error,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": results, "count": len(keys)})
}

// handleTest 模型测试：转发到本地代理（复用完整路由逻辑）。
// 支持三类测试，由请求体 kind 决定（缺省 = 文本对话）：
//   - text        → POST /v1/chat/completions（对话）
//   - image_gen   → POST /v1/images/generations（文生图）
//   - video_gen   → POST /v1/videos/generations（文生视频，异步）
func (h *Handler) handleTest(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	kind, _ := req["kind"].(string)
	delete(req, "kind")
	if kind == "" {
		kind = "text"
	}
	// key_index：指定用第几个 Key 测试（管理界面「按 Key 测试」）。
	// 它不属于 OpenAI 参数，必须剥离后再转发，否则会被透传给上游厂商。
	forceKey := -1
	if v, ok := req["key_index"]; ok {
		if f, ok := v.(float64); ok && f >= 0 {
			forceKey = int(f)
		}
		delete(req, "key_index")
	}

	endpoint := "/v1/chat/completions"
	switch kind {
	case "image_gen":
		endpoint = "/v1/images/generations"
		if s, _ := req["prompt"].(string); s == "" {
			req["prompt"] = "一只可爱的橘猫坐在窗台上"
		}
	case "video_gen":
		endpoint = "/v1/videos/generations"
		if s, _ := req["prompt"].(string); s == "" {
			req["prompt"] = "一只猫在草地上奔跑"
		}
	default:
		req["stream"] = false
		// 兜底限制最大输出 tokens，避免测试时无谓消耗额度
		if _, ok := req["max_tokens"]; !ok {
			req["max_tokens"] = 64
		}
	}
	data, _ := json.Marshal(req)

	client := &http.Client{Timeout: 300 * time.Second}
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", h.proxyPort, endpoint), strings.NewReader(string(data)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if forceKey >= 0 {
		httpReq.Header.Set("X-ApiCluster-Key-Index", strconv.Itoa(forceKey))
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "本地代理未运行: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	// 透传路由信息（自动路由时显示实际命中的厂商/模型）
	if p := resp.Header.Get("X-Routed-Provider"); p != "" {
		w.Header().Set("X-Routed-Provider", p)
		w.Header().Set("X-Routed-Model", resp.Header.Get("X-Routed-Model"))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleOpen 用系统默认浏览器打开外部链接（WebView2 中点击「前往获取 Key」等）
func (h *Handler) handleOpen(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的 URL"})
		return
	}
	go openExternal(url)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRestart 让界面也能触发「重启程序」：设置页改端口后必须重启才生效，
// 以前只有一句文案，用户得自己去托盘找。真正的重启由 main 注入的钩子完成
// （走和托盘「重启」一样的路径：安排新实例 → 停边车/隧道 → 落盘 → 退出）。
func (h *Handler) handleRestart(w http.ResponseWriter, r *http.Request) {
	if h.Restart == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "当前运行方式不支持从界面重启，请手动退出后重新打开"})
		return
	}
	go func() {
		time.Sleep(400 * time.Millisecond) // 先把响应发出去再走关闭流程
		h.Restart()
	}()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTheme 返回 LifeSystem 写入的主题同步文件内容（dark/light），
// 供前端轮询、在 ApiCluster 被内嵌时跟随 LifeSystem 的主题切换。
func (h *Handler) handleTheme(w http.ResponseWriter, r *http.Request) {
	theme := ""
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".life_system_theme")); err == nil {
			t := strings.TrimSpace(string(data))
			if t == "dark" || t == "light" {
				theme = t
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"theme": theme})
}

// ===== 内置 CLIProxyAPI 边车（订阅账号） =====

func (h *Handler) cliGuard(w http.ResponseWriter) bool {
	if h.cli == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "订阅账号服务未初始化"})
		return false
	}
	return true
}

// handleCliStatus 边车状态 + 支持的账号类型 + 对外端点说明
func (h *Handler) handleCliStatus(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	st := h.cli.Status()
	port, _ := st["port"].(int)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    st,
		"orphan":    h.cli.Orphan(), // 端口上是否有不归本程序管理的残留边车（强杀后会遇到）
		"providers": sidecar.OAuthProviders(),
		"endpoints": sidecar.EndpointHelp(port),
	})
}

// handleCliKillOrphan 结束占用边车端口的「上一轮自家残留实例」（只按 PID 精确结束）
func (h *Handler) handleCliKillOrphan(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	if err := h.cli.KillOrphan(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": h.cli.Status(), "orphan": h.cli.Orphan()})
}

// handleCliAction 启动 / 停止 / 重启边车
func (h *Handler) handleCliAction(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	switch body.Action {
	case "start":
		err = h.cli.Start()
	case "stop":
		h.cli.Stop()
	case "restart":
		err = h.cli.Restart()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知操作: " + body.Action})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "status": h.cli.Status()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": h.cli.Status()})
}

// handleCliSettings 保存边车设置（启用 / 端口 / exe 路径）
func (h *Handler) handleCliSettings(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	var body struct {
		Enabled *bool   `json:"enabled"`
		Port    *int    `json:"port"`
		ExePath *string `json:"exe_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s := config.Get().Settings
	wasEnabled := s.CliProxy.Enabled
	if body.Enabled != nil {
		s.CliProxy.Enabled = *body.Enabled
	}
	if body.Port != nil && *body.Port > 0 && *body.Port < 65536 {
		s.CliProxy.Port = *body.Port
	}
	if body.ExePath != nil {
		s.CliProxy.ExePath = strings.TrimSpace(*body.ExePath)
	}
	config.SetSettings(s)
	_ = config.Save()
	// 端口/路径变化后重写边车 config.yaml
	_, _ = h.cli.Ensure()
	// 把「随 ApiCluster 启动」从开改成关，同时停掉当前正在跑的边车：
	// 这个开关在订阅账号页就在启停按钮旁边，用户理解的就是「关＝别在跑」。
	stoppedNow := false
	if wasEnabled && !s.CliProxy.Enabled {
		if r, ok := h.cli.Status()["running"].(bool); ok && r {
			h.cli.Stop()
			stoppedNow = true // 只在真的停了的时候才这么报
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stopped": stoppedNow, "status": h.cli.Status()})
}

// handleCliFixKey 一键修复管理面板 401：把明文管理密钥写回边车 config.yaml
// （边车启动时会自己重新哈希），必要时重启边车，然后实测一次鉴权。
func (h *Handler) handleCliFixKey(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	var body struct {
		Regenerate bool `json:"regenerate"` // true = 换一把新密钥，而不是沿用现在的
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	key, err := h.cli.RepairMgmtKey(body.Regenerate)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 只在边车本来就在跑时重启它；没跑就别替用户拉起来
	wasRunning := false
	if st, ok := h.cli.Status()["running"].(bool); ok {
		wasRunning = st
	}
	verified := false
	note := "配置已重写；边车当前没有运行，下次启动后生效"
	if wasRunning {
		if err := h.cli.Restart(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "配置已修复，但重启边车失败：" + err.Error()})
			return
		}
		if verified = h.cli.VerifyMgmtKey(25 * time.Second); verified {
			note = "已修复并通过鉴权实测"
		} else {
			note = "配置已重写、边车也已重启，但鉴权仍没通过；请展开下方运行日志看 CLIProxyAPI 自己的报错"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"mgmt_key": key,
		"verified": verified,
		"note":     note,
	})
}

// handleCliBrowseExe 弹出系统文件对话框让用户挑 CLIProxyAPI.exe，
// 免得在输入框里手敲一长串路径。起始目录用自动查找命中的那个目录。
func (h *Handler) handleCliBrowseExe(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if exe, ok := sidecar.ResolveExe(config.Get().Settings.CliProxy.ExePath); ok {
		dir = filepath.Dir(exe)
	}
	ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = %s
$d.Filter = '可执行文件 (*.exe)|*.exe|所有文件 (*.*)|*.*'
$d.InitialDirectory = %s
if (-not (Test-Path $d.InitialDirectory)) { $d.InitialDirectory = [Environment]::GetFolderPath('UserProfile') }
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Write($d.FileName) }`,
		psQuote("选择 CLIProxyAPI.exe"), psQuote(dir))
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "打开文件选择框失败: " + err.Error()})
		return
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// handleCliMgmt 通用管理接口代理：path 为边车管理接口路径（可带 ?query）
func (h *Handler) handleCliMgmt(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	full := strings.TrimSpace(r.URL.Query().Get("path"))
	if full == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 path 参数"})
		return
	}
	sub, query := full, ""
	if i := strings.Index(full, "?"); i >= 0 {
		sub, query = full[:i], full[i+1:]
	}
	var body []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
		body, _ = io.ReadAll(r.Body)
	}
	code, data, err := h.cli.Mgmt(r.Method, sub, query, body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

// handleCliLogin 请求 OAuth 登录地址，并在系统浏览器中打开授权页
func (h *Handler) handleCliLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	authURL, state, err := h.cli.LoginURL(provider)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	go openExternal(authURL)
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL, "state": state})
}

// handleCliLog 返回边车日志尾部
func (h *Handler) handleCliLog(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 2000 {
			n = x
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, h.cli.LogTail(n))
}

// handleCliOpenTerminal 在服务端打开 CLIProxyAPI 自带的 TUI 管理界面
func (h *Handler) handleCliOpenTerminal(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	cs := config.Get().Settings.CliProxy
	exe, ok := sidecar.ResolveExe(cs.ExePath)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "未找到 CLIProxyAPI.exe，请把它放到 ApiCluster.exe 同目录，或在「订阅账号」页指定路径",
		})
		return
	}
	// 通过 PowerShell Start-Process 启动 TUI，确保在用户桌面上弹出独立窗口
	// 传入 -password 管理密钥，TUI 会预填密码，用户直接按回车即可登录
	exePath := exe
	configPath := cs.ConfigPath
	password := cs.MgmtKey
	// 转义单引号
	exePath = strings.ReplaceAll(exePath, "'", "''")
	configPath = strings.ReplaceAll(configPath, "'", "''")
	password = strings.ReplaceAll(password, "'", "''")
	psCmd := fmt.Sprintf(
		"Start-Process -FilePath '%s' -ArgumentList '-tui','-config','%s','-password','%s' -WorkingDirectory '%s' -WindowStyle Normal",
		exePath, configPath, password, strings.ReplaceAll(filepath.Dir(exe), "'", "''"),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "打开 TUI 失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCliOpenPanel 准备 CLIProxyAPI 内置的 CPAMC 管理面板并返回其地址，
// 由前端在应用内 tab（iframe）中加载，不再打开系统浏览器。
// 只有开着「随 ApiCluster 启动」时才自动拉起边车；关着的情况下不替用户拧开关，
// 而是回 need_start，让界面给出一个明确的「启动并加载面板」按钮
// （否则用户关了这个开关，却还会在翻页签时被悄悄启动）。
func (h *Handler) handleCliOpenPanel(w http.ResponseWriter, r *http.Request) {
	if !h.cliGuard(w) {
		return
	}
	st := h.cli.Status()
	running, _ := st["running"].(bool)
	cs := config.Get().Settings.CliProxy
	if !running {
		if !cs.Enabled {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "need_start": true})
			return
		}
		if err := h.cli.Start(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "自动启动订阅账号服务失败：" + err.Error()})
			return
		}
		st = h.cli.Status()
	}
	panelURL := fmt.Sprintf("http://127.0.0.1:%d/management.html", cs.Port)
	// 等待面板就绪（首次启动可能需联网下载面板资源），最多 20 秒
	client := &http.Client{Timeout: 3 * time.Second}
	var resp *http.Response
	var err error
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err = client.Get(panelURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "无法连接管理面板：" + err.Error()})
			} else {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"error": "控制面板尚未就绪：首次启动需联网下载面板资源，请稍等片刻后重试（可在「订阅账号」页查看运行日志确认下载进度）",
				})
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	resp.Body.Close()
	// 返回面板地址与管理密钥：前端在内嵌 tab（iframe）中加载面板，并把密钥复制到剪贴板
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "mgmt_key": cs.MgmtKey, "panel_url": panelURL, "pid": st["pid"],
	})
}

// ===== SSH 反向隧道 =====

// handleTunnelStatus 返回隧道状态 + 当前设置 + 日志路径
func (h *Handler) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	if h.tun == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "隧道服务未初始化"})
		return
	}
	st := h.tun.Status()
	// 本地端口缺省 = 代理端口
	if v, _ := st["local_port"].(int); v <= 0 {
		st["local_port"] = h.proxyPort
	}
	if v, _ := st["remote_port"].(int); v <= 0 {
		st["remote_port"] = h.proxyPort
	}
	// 残留隧道：界面必须区分「我起的」和「上一轮遗留的」，否则用户看到已停止
	// 而远程端口其实还被旧会话占着
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "orphans": h.tun.Orphans()})
}

// handleTunnelProbe 真的去远程那一侧看一眼端口通不通：
// 「ssh 进程活着」不等于「反向隧道可用」，这个按钮给出确定答案。
func (h *Handler) handleTunnelProbe(w http.ResponseWriter, r *http.Request) {
	if h.tun == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "隧道服务未初始化"})
		return
	}
	writeJSON(w, http.StatusOK, h.tun.ProbeRemote())
}

// handleTunnelKillOrphan 结束指定的残留隧道进程（后端会再校验一次命令行）
func (h *Handler) handleTunnelKillOrphan(w http.ResponseWriter, r *http.Request) {
	if h.tun == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "隧道服务未初始化"})
		return
	}
	var body struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.tun.KillOrphan(body.PID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": h.tun.Status(), "orphans": h.tun.Orphans()})
}

// handleTunnelSettings 保存隧道设置（pem / host / user / 端口 / 随启动 / 自动重连）
func (h *Handler) handleTunnelSettings(w http.ResponseWriter, r *http.Request) {
	if h.tun == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "隧道服务未初始化"})
		return
	}
	var body struct {
		Enabled               *bool   `json:"enabled"`
		PemPath               *string `json:"pem_path"`
		Host                  *string `json:"host"`
		User                  *string `json:"user"`
		RemotePort            *int    `json:"remote_port"`
		LocalPort             *int    `json:"local_port"`
		DisableAutoReconnect  *bool   `json:"disable_auto_reconnect"`
		ExtraArgs             *string `json:"extra_args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s := config.Get().Settings
	t := s.Tunnel
	if body.Enabled != nil {
		t.Enabled = *body.Enabled
	}
	if body.PemPath != nil {
		t.PemPath = strings.TrimSpace(*body.PemPath)
	}
	if body.Host != nil {
		t.Host = strings.TrimSpace(*body.Host)
	}
	if body.User != nil {
		t.User = strings.TrimSpace(*body.User)
	}
	if body.RemotePort != nil {
		t.RemotePort = *body.RemotePort
	}
	if body.LocalPort != nil {
		t.LocalPort = *body.LocalPort
	}
	if body.DisableAutoReconnect != nil {
		t.DisableAutoReconnect = *body.DisableAutoReconnect
	}
	if body.ExtraArgs != nil {
		t.ExtraArgs = strings.TrimSpace(*body.ExtraArgs)
	}
	s.Tunnel = t
	config.SetSettings(s)
	_ = config.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": h.tun.Status()})
}

// handleTunnelAction 启动 / 停止 / 重启隧道
func (h *Handler) handleTunnelAction(w http.ResponseWriter, r *http.Request) {
	if h.tun == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "隧道服务未初始化"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var err error
	switch body.Action {
	case "start":
		err = h.tun.Start()
	case "stop":
		h.tun.Stop()
	case "restart":
		err = h.tun.Restart()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知操作: " + body.Action})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "status": h.tun.Status()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": h.tun.Status()})
}

// handleTunnelLog 返回隧道日志尾部
func (h *Handler) handleTunnelLog(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 2000 {
			n = x
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, tunnel.LogTail(n))
}

// handleTunnelBrowsePem 弹出系统文件选择对话框让用户选 .pem 私钥文件，
// 返回选中路径（前端填入表单，无需手动敲路径）。用 PowerShell 的 OpenFileDialog 实现，
// 进程隐藏窗口，不打扰用户。
func (h *Handler) handleTunnelBrowsePem(w http.ResponseWriter, r *http.Request) {
	ps := `Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = '选择 SSH 私钥文件'
$d.Filter = 'SSH 私钥 (*.pem;*.key;id_*)|*.pem;*.key;id_*|所有文件 (*.*)|*.*'
$d.InitialDirectory = [Environment]::GetFolderPath('UserProfile') + '\.ssh'
if (-not (Test-Path $d.InitialDirectory)) { $d.InitialDirectory = [Environment]::GetFolderPath('UserProfile') }
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Write($d.FileName) }`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "打开文件选择框失败: " + err.Error()})
		return
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "static not found", http.StatusInternalServerError)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(sub, path); err != nil {
		if path == "favicon.ico" || path == "favicon.png" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		path = "index.html"
	}
	// no-cache：每次使用前必须向服务器校验（文件变化即取新版本），
	// 防止 WebView2 用旧缓存的 app.js 配新后端导致页面初始化中断（白屏/空白）。
	// 条件请求命中 304 时开销极小，不影响加载速度。
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, sub, path)
}

func openExternal(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
