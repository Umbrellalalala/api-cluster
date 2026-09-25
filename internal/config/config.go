// Package config 本地配置存储。
// 数据以明文 JSON 保存在用户目录（%USERPROFILE%/.ApiCluster/keys.json），
// API Key 明文保存（用户明确要求本地明文存储）。
package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ModelLimit 单个模型的本地限流配置（0 = 不限制）
type ModelLimit struct {
	Concurrency int `json:"concurrency,omitempty"` // 并发数上限
	RPM         int `json:"rpm,omitempty"`         // 每分钟请求数上限
}

// ProviderConfig 内置厂商的用户配置（key / 类型 / base_url 覆盖 / 路径 / 鉴权 / 模型覆盖 / 额度 / 用量）
type ProviderConfig struct {
	APIKey     string   `json:"api_key"`               // 明文 API Key（向后兼容，单 key 场景）
	APIKeys    []string `json:"api_keys,omitempty"`    // 多密钥列表（优先使用；一个用完自动切下一个）
	KeyNames   []string `json:"key_names,omitempty"`   // 与 APIKeys 一一对应的自定义名称（可空）
	KeyNos     []int    `json:"key_nos,omitempty"`     // 与 APIKeys 一一对应的稳定序号（不随拖拽排序变动）
	Type       string              `json:"type,omitempty"`        // openai / anthropic（空 = openai）
	BaseURL    string              `json:"base_url,omitempty"`    // 覆盖内置厂商的 Base URL（空则用目录默认值）
	Path       string              `json:"path,omitempty"`        // 接口路径（endpoint），留空用类型默认
	AuthHeader string              `json:"auth_header,omitempty"` // 鉴权头，留空按类型默认
	AuthPrefix string              `json:"auth_prefix,omitempty"` // 前缀，留空按类型默认
	Models      []string               `json:"models,omitempty"`       // 覆盖模型列表（空则用目录默认模型）
	ModelCaps   map[string][]string    `json:"model_caps,omitempty"`   // 覆盖模型的能力标签（模型ID -> text/code/image/...；缺省按目录默认，目录也没有则按文本）
	ModelLimits map[string]ModelLimit  `json:"model_limits,omitempty"` // 单个模型的并发/每分钟请求数限制（模型ID -> ModelLimit）
	Quota       int64                  `json:"quota"`                  // 用户填写的额度（tokens）
	Used        int64                  `json:"used"`                   // 累计已用（tokens）
	KeyIndex    int                    `json:"key_index"`              // 当前使用的 key 索引（自动轮换）
}

// ActiveKey 返回当前激活的 API Key
func (pc *ProviderConfig) ActiveKey() string {
	if len(pc.APIKeys) > 0 {
		if pc.KeyIndex >= len(pc.APIKeys) {
			pc.KeyIndex = 0
		}
		return pc.APIKeys[pc.KeyIndex]
	}
	return pc.APIKey
}

// HasAnyKey 判断是否配置了任意 key
func (pc *ProviderConfig) HasAnyKey() bool {
	return pc.ActiveKey() != ""
}

// RotateKey 切换到下一个 key：把当前 key 移到列表末尾（多个 key 时才有意义）。
// 约定：APIKeys 首位即「当前使用」的 key，因此轮换 = 把当前 key 挪到最后。
func (pc *ProviderConfig) RotateKey() bool {
	if len(pc.APIKeys) <= 1 {
		return false
	}
	if pc.KeyIndex < 0 || pc.KeyIndex >= len(pc.APIKeys) {
		pc.KeyIndex = 0
	}
	pc.KeyNames = syncNames(pc.KeyNames, len(pc.APIKeys))
	pc.KeyNos = syncKeyNos(pc.KeyNos, len(pc.APIKeys))
	pc.KeyNames = moveToEnd(pc.KeyNames, pc.KeyIndex)
	pc.KeyNos = moveToEnd(pc.KeyNos, pc.KeyIndex)
	k := pc.APIKeys[pc.KeyIndex]
	rest := append([]string{}, pc.APIKeys[:pc.KeyIndex]...)
	rest = append(rest, pc.APIKeys[pc.KeyIndex+1:]...)
	pc.APIKeys = append(rest, k)
	pc.KeyIndex = 0
	return true
}

// KeyCount 返回 key 的总数
func (pc *ProviderConfig) KeyCount() int {
	if len(pc.APIKeys) > 0 {
		return len(pc.APIKeys)
	}
	if pc.APIKey != "" {
		return 1
	}
	return 0
}

// AllKeys 返回所有 key 的列表
func (pc *ProviderConfig) AllKeys() []string {
	if len(pc.APIKeys) > 0 {
		return pc.APIKeys
	}
	if pc.APIKey != "" {
		return []string{pc.APIKey}
	}
	return nil
}

// NormalizeKeys 规范化 key：迁移旧格式单 key，并把当前使用的 key 移到列表首位。
// 约定：APIKeys 列表首位即「当前使用」的 key（KeyIndex 恒为 0）。
func (pc *ProviderConfig) NormalizeKeys() {
	if len(pc.APIKeys) == 0 && pc.APIKey != "" {
		pc.APIKeys = []string{pc.APIKey}
	}
	pc.KeyNames = syncNames(pc.KeyNames, len(pc.APIKeys))
	pc.KeyNos = syncKeyNos(pc.KeyNos, len(pc.APIKeys))
	if len(pc.APIKeys) == 0 {
		return
	}
	if pc.KeyIndex < 0 || pc.KeyIndex >= len(pc.APIKeys) {
		pc.KeyIndex = 0
	}
	if pc.KeyIndex != 0 {
		pc.KeyNames = moveToFront(pc.KeyNames, pc.KeyIndex)
		pc.KeyNos = moveToFront(pc.KeyNos, pc.KeyIndex)
		k := pc.APIKeys[pc.KeyIndex]
		rest := append([]string{}, pc.APIKeys[:pc.KeyIndex]...)
		rest = append(rest, pc.APIKeys[pc.KeyIndex+1:]...)
		pc.APIKeys = append([]string{k}, rest...)
		pc.KeyIndex = 0
	}
}

// syncNames 保证 names 与 n 个 key 一一对应（不足补空串，多余截断）
func syncNames(names []string, n int) []string {
	if len(names) < n {
		return append(names, make([]string, n-len(names))...)
	}
	if len(names) > n {
		return names[:n]
	}
	return names
}

// moveToFront 把 s[i] 移到首位（下标无效时原样返回）
func moveToFront[T any](s []T, i int) []T {
	if i <= 0 || i >= len(s) {
		return s
	}
	out := append([]T{s[i]}, s[:i]...)
	return append(out, s[i+1:]...)
}

// moveToEnd 把 s[i] 移到末尾（下标无效时原样返回）
func moveToEnd[T any](s []T, i int) []T {
	if i < 0 || i >= len(s) || len(s) <= 1 {
		return s
	}
	out := append([]T{}, s[:i]...)
	out = append(out, s[i+1:]...)
	return append(out, s[i])
}

// syncKeyNos 保证 KeyNos 与 n 个 key 一一对应：不足时按当前最大值递增补齐，多余截断。
// 编号在 key 创建时分配，之后不随拖拽排序变动，便于用户辨别「同一个 key」。
func syncKeyNos(nos []int, n int) []int {
	if len(nos) > n {
		return nos[:n]
	}
	max := 0
	for _, v := range nos {
		if v > max {
			max = v
		}
	}
	for len(nos) < n {
		max++
		nos = append(nos, max)
	}
	return nos
}

// CustomProvider 自定义厂商
type CustomProvider struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Type       string              `json:"type"` // openai / anthropic
	BaseURL    string              `json:"base_url"`
	Path       string              `json:"path"`        // 接口路径（endpoint），留空用类型默认
	AuthHeader string              `json:"auth_header"` // 鉴权头，留空按类型默认
	AuthPrefix string              `json:"auth_prefix"` // 前缀，留空按类型默认
	APIKey     string              `json:"api_key"`     // 向后兼容
	APIKeys    []string            `json:"api_keys,omitempty"`
	KeyNames   []string            `json:"key_names,omitempty"` // 与 APIKeys 一一对应的自定义名称（可空）
	KeyNos     []int               `json:"key_nos,omitempty"`   // 与 APIKeys 一一对应的稳定序号（不随拖拽排序变动）
	Models      []string              `json:"models"`
	Quota       int64                 `json:"quota"`
	Used        int64                 `json:"used"`
	KeyIndex    int                   `json:"key_index"`
	ModelCaps   map[string][]string   `json:"model_caps,omitempty"`   // 模型 -> 能力标签
	ModelLimits map[string]ModelLimit `json:"model_limits,omitempty"` // 模型 -> 并发/每分钟请求数限制
}

// ActiveKey 返回当前激活的 API Key
func (c *CustomProvider) ActiveKey() string {
	if len(c.APIKeys) > 0 {
		if c.KeyIndex >= len(c.APIKeys) {
			c.KeyIndex = 0
		}
		return c.APIKeys[c.KeyIndex]
	}
	return c.APIKey
}

// HasAnyKey 判断是否配置了任意 key
func (c *CustomProvider) HasAnyKey() bool {
	return c.ActiveKey() != ""
}

// RotateKey 切换到下一个 key：把当前 key 移到列表末尾
func (c *CustomProvider) RotateKey() bool {
	if len(c.APIKeys) <= 1 {
		return false
	}
	if c.KeyIndex < 0 || c.KeyIndex >= len(c.APIKeys) {
		c.KeyIndex = 0
	}
	c.KeyNames = syncNames(c.KeyNames, len(c.APIKeys))
	c.KeyNos = syncKeyNos(c.KeyNos, len(c.APIKeys))
	c.KeyNames = moveToEnd(c.KeyNames, c.KeyIndex)
	c.KeyNos = moveToEnd(c.KeyNos, c.KeyIndex)
	k := c.APIKeys[c.KeyIndex]
	rest := append([]string{}, c.APIKeys[:c.KeyIndex]...)
	rest = append(rest, c.APIKeys[c.KeyIndex+1:]...)
	c.APIKeys = append(rest, k)
	c.KeyIndex = 0
	return true
}

// AllKeys 返回所有 key 的列表
func (c *CustomProvider) AllKeys() []string {
	if len(c.APIKeys) > 0 {
		return c.APIKeys
	}
	if c.APIKey != "" {
		return []string{c.APIKey}
	}
	return nil
}

// NormalizeKeys 规范化 key：迁移旧格式，并把当前使用的 key 移到列表首位
func (c *CustomProvider) NormalizeKeys() {
	if len(c.APIKeys) == 0 && c.APIKey != "" {
		c.APIKeys = []string{c.APIKey}
	}
	c.KeyNames = syncNames(c.KeyNames, len(c.APIKeys))
	c.KeyNos = syncKeyNos(c.KeyNos, len(c.APIKeys))
	if len(c.APIKeys) == 0 {
		return
	}
	if c.KeyIndex < 0 || c.KeyIndex >= len(c.APIKeys) {
		c.KeyIndex = 0
	}
	if c.KeyIndex != 0 {
		c.KeyNames = moveToFront(c.KeyNames, c.KeyIndex)
		c.KeyNos = moveToFront(c.KeyNos, c.KeyIndex)
		k := c.APIKeys[c.KeyIndex]
		rest := append([]string{}, c.APIKeys[:c.KeyIndex]...)
		rest = append(rest, c.APIKeys[c.KeyIndex+1:]...)
		c.APIKeys = append([]string{k}, rest...)
		c.KeyIndex = 0
	}
}

// CliProxySettings 内置 CLIProxyAPI 边车（Sidecar）设置。
// 用于把 Claude Code / Codex / Antigravity / Grok / Kimi 等订阅账号包装成 API。
type CliProxySettings struct {
	Enabled    bool   `json:"enabled"`     // 是否随 ApiCluster 一起启动
	Port       int    `json:"port"`        // 边车监听端口（默认 8317）
	ExePath    string `json:"exe_path"`    // CLIProxyAPI.exe 路径（空 = 自动查找）
	ConfigPath string `json:"config_path"` // 边车 config.yaml 路径（空 = 自动生成到数据目录）
	AuthDir    string `json:"auth_dir"`    // 账号凭证目录（空 = 数据目录下 cliproxy/auths）
	APIKey     string `json:"api_key"`     // 供客户端调用边车的本地 API Key（自动生成）
	MgmtKey    string `json:"mgmt_key"`    // 边车管理密钥（自动生成，仅供本地管理接口使用）
}

// Settings 全局设置
type Settings struct {
	AutoStart         bool             `json:"autostart"`           // 开机自启
	ProxyPort         int              `json:"proxy_port"`          // 代理端口（默认 3003）
	AutoOpenBrowser   bool             `json:"auto_open_browser"`   // 启动后自动打开管理界面
	AutoRouteEnabled  bool             `json:"auto_route_enabled"`  // 启用 inurl 自动路由
	AutoModels        string           `json:"auto_models"`         // 限定路由模型（逗号分隔，空=全部）
	AutoProviderOrder string           `json:"auto_provider_order"` // 厂商优先级（逗号分隔）
	CliProxy          CliProxySettings `json:"cliproxy"`            // 内置 CLIProxyAPI 边车设置
	Tunnel            TunnelSettings   `json:"tunnel"`              // SSH 反向隧道设置（远程访问本地 API）
}

// TunnelSettings SSH 反向隧道设置。
// 本地主动向远程发起 SSH 连接，把本地代理端口「反向映射」到远程的某个端口，
// 使远程服务器通过 localhost:<RemotePort> 即可访问本地的 ApiCluster API。
type TunnelSettings struct {
	Enabled    bool   `json:"enabled"`     // 随 ApiCluster 启动自动建立隧道
	PemPath    string `json:"pem_path"`    // 私钥文件路径（.pem / 无密码的 id_rsa）
	Host       string `json:"host"`        // 远程主机 IP 或域名
	User       string `json:"user"`        // 远程登录用户名
	RemotePort int    `json:"remote_port"` // 远程端口（远程 localhost 上映射到的端口）
	LocalPort  int    `json:"local_port"`  // 本地端口（默认 = 代理端口，即要暴露出去的本地服务端口）
	// DisableAutoReconnect 禁用「断开自动重连」。默认（false）＝自动重连，
	// 隧道断开后按退避策略重试（用户总结中的「5 秒循环」行为）。
	DisableAutoReconnect bool   `json:"disable_auto_reconnect,omitempty"`
	ExtraArgs            string `json:"extra_args"` // 额外 ssh 参数（高级，留空 = 默认保活参数）
}

// RouteScheme 自动路由方案：方案名即客户端 model 名 → 一组有序模型。
// 客户端把 model 填成方案名，代理按 Models 顺序依次尝试（某家 5xx/429 自动切下一个）。
type RouteScheme struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`    // 方案名（即客户端填写的 model 名，唯一），如 mytext / inurl-vision
	Models  []string            `json:"models"`  // 有序模型 ID（顺序即优先级）
	Caps    map[string][]string `json:"caps,omitempty"` // 每个模型的能力标签（用户手动选择；缺省用厂商目录默认）
	Enabled bool                `json:"enabled"` // 是否启用
}

// VaultAccount 账号库里某站点下的一组登录凭证。
// 密码可空：只有「手机验证码 / Google 授权」等无密码登录方式时留空。
type VaultAccount struct {
	Label    string `json:"label"`    // 账号别名，如「主号」「Google 登录」
	User     string `json:"user"`     // 账号（邮箱 / 手机号 / 用户名）
	Password string `json:"password"` // 密码，明文（与 API Key 一致的本地明文策略）
	Note     string `json:"note"`     // 该账号自己的备注（如「公司号，月底到期」「绑了 xx 邮箱」）
}

// VaultSite 账号库里的一个站点，下挂多组账号
type VaultSite struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`   // 站点名，如 OpenAI / Qoder
	URL      string         `json:"url"`    // 登录页或官网，用于「打开网站」
	Logo     string         `json:"logo"`   // 用户上传的 data:image 或默认图标 URL（空 = 首字母色块）
	Note     string         `json:"note"`   // 备注（如「走公司阿里云账号，手机号登录」）
	Accounts []VaultAccount `json:"accounts"`
}

// LibraryMark 密钥库卡片的收藏与分组标记。
// 单独按厂商 ID 存一份，而不是塞进 ProviderConfig：后者的保存路径是整条记录覆盖，
// 塞进去很容易被别的写回清掉（历史上已经因此丢过配置），分组/收藏与 Key 配置互不干扰更安全。
type LibraryMark struct {
	Favorite bool   `json:"favorite"`
	Group    string `json:"group,omitempty"` // 分组名，空 = 未分组
}

// Config 完整配置
type Config struct {
	Keys     map[string]ProviderConfig `json:"keys"`
	Custom   []CustomProvider          `json:"custom"`
	Settings Settings                  `json:"settings"`
	Schemes  []RouteScheme             `json:"schemes"`   // 自动路由方案
	Vault    []VaultSite               `json:"vault"`     // 账号库（网站登录账号密码）
	Marks    map[string]LibraryMark    `json:"marks"`     // 密钥库收藏 / 分组
}

var (
	mu         sync.RWMutex
	dataDir    string
	config     = &Config{Keys: map[string]ProviderConfig{}, Marks: map[string]LibraryMark{}, Settings: Settings{ProxyPort: 3003, AutoOpenBrowser: true, AutoRouteEnabled: true}}
	configPath string
	// dirty 标记内存中的配置已被修改但尚未落盘（用量统计、key 轮换位置等高频变更）
	dirty atomic.Bool
)

// StartAutoFlush 启动后台定时落盘。
// 用量（used）与 key 轮换位置是高频内存变更，旧实现只在「用户改配置」或
// 「正常退出」时才写盘，一旦崩溃/断电就全部回退。这里每 interval 检查一次
// 脏标记并落盘，最坏只丢一个周期的数据。
func StartAutoFlush(interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if !dirty.Load() {
				continue
			}
			if err := Save(); err == nil {
				dirty.Store(false)
			} else {
				log.Printf("[config] 定时落盘失败：%v", err)
			}
		}
	}()
}

// DataDir 返回数据目录（确保存在）
func DataDir() string {
	if dataDir != "" {
		return dataDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".ApiCluster")
	oldDir := filepath.Join(home, ".ApiRouter")
	// 兼容旧版本：首次升级时把旧数据目录 .ApiRouter 迁移为 .ApiCluster，
	// 保留已有 API Key / 设置 / 订阅账号凭证，避免升级后配置"丢失"。
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		if _, oldErr := os.Stat(oldDir); oldErr == nil {
			// 迁移失败不能静默：否则新目录为空，用户会以为 Key 全丢了
			if rerr := os.Rename(oldDir, dir); rerr != nil {
				log.Printf("[config] 旧数据目录迁移失败：%s -> %s：%v（请手动复制后再启动）", oldDir, dir, rerr)
			} else {
				log.Printf("[config] 已把旧数据目录迁移到 %s", dir)
			}
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	dataDir = dir
	return dir
}

// Path 配置文件完整路径
func Path() string {
	if configPath == "" {
		configPath = filepath.Join(DataDir(), "keys.json")
	}
	return configPath
}

// finalize 补齐默认值并规范化后装载为当前配置（调用方需持有 mu）
func finalize(loaded *Config) *Config {
	if loaded.Keys == nil {
		loaded.Keys = map[string]ProviderConfig{}
	}
	if loaded.Marks == nil {
		loaded.Marks = map[string]LibraryMark{}
	}
	if loaded.Settings.ProxyPort == 0 {
		loaded.Settings.ProxyPort = 3003
	}
	if loaded.Settings.CliProxy.Port == 0 {
		loaded.Settings.CliProxy.Port = 8317
	}
	// 规范化 key：将旧格式单 key 迁移到多 key 列表
	for id, pc := range loaded.Keys {
		pc.NormalizeKeys()
		loaded.Keys[id] = pc
	}
	for i, c := range loaded.Custom {
		c.NormalizeKeys()
		loaded.Custom[i] = c
	}
	config = loaded
	return config
}

// Load 从磁盘加载配置（不存在则用默认值）。
//
// keys.json 解析失败时绝不静默返回空配置——那样用户所有 Key 会从界面消失，
// 且随后任意一次保存都会把损坏文件覆盖掉、再也无法找回。
// 现在的处理顺序：保留损坏现场 → 回滚 keys.json.bak → 最后才用空配置。
func Load() *Config {
	mu.Lock()
	defer mu.Unlock()
	data, err := os.ReadFile(Path())
	if err != nil {
		return config
	}
	loaded := &Config{Keys: map[string]ProviderConfig{}}
	if jerr := json.Unmarshal(data, loaded); jerr != nil {
		log.Printf("[config] keys.json 解析失败：%v；保留损坏文件并尝试回滚备份", jerr)
		markCorrupt(Path())
		if bak, berr := os.ReadFile(Path() + ".bak"); berr == nil {
			rec := &Config{Keys: map[string]ProviderConfig{}}
			if json.Unmarshal(bak, rec) == nil {
				log.Printf("[config] 已从 keys.json.bak 恢复配置")
				return finalize(rec)
			}
		}
		log.Printf("[config] 备份同样不可用，将以空配置启动（原文件保留为 keys.json.corrupt）")
		return config
	}
	return finalize(loaded)
}

// markCorrupt 把损坏的配置文件另存为 *.corrupt，避免被后续保存覆盖
func markCorrupt(path string) {
	dst := path + ".corrupt"
	_ = os.Rename(path, dst)
}

// Save 保存配置到磁盘（明文，原子写 + 上一版备份）
func Save() error {
	mu.RLock()
	data, err := json.MarshalIndent(config, "", "  ")
	mu.RUnlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(Path(), data)
}

// writeFileAtomic 先写临时文件再原子替换并 fsync。
// 旧实现用 os.WriteFile（O_TRUNC 先截断），写入中途崩溃/断电会留下
// 半截 JSON，而 Load 解析失败又会静默返回空配置 —— 等同全量 Key 丢失。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".keys-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 替换前先把上一版存为 keys.json.bak，新文件写坏时仍可回滚
	if old, rerr := os.ReadFile(path); rerr == nil && len(old) > 0 {
		_ = os.WriteFile(path+".bak", old, 0o600)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// 极端情况（目标被占用）：退化为直接写，至少不丢这次修改
		return os.WriteFile(path, data, 0o600)
	}
	tmpName = ""
	// 首次保存后补一份备份，保证之后任何时候都能回滚
	if _, err := os.Stat(path + ".bak"); err != nil {
		_ = os.WriteFile(path+".bak", data, 0o600)
	}
	return nil
}

// copyCaps 深拷贝模型能力标签表
func copyCaps(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// copyLimits 深拷贝模型限流配置表
func copyLimits(m map[string]ModelLimit) map[string]ModelLimit {
	out := make(map[string]ModelLimit, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Get 返回当前配置副本（线程安全，深拷贝 slice 避免外部修改污染内部状态）
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	cp := &Config{
		Keys:     make(map[string]ProviderConfig, len(config.Keys)),
		Custom:   make([]CustomProvider, 0, len(config.Custom)),
		Settings: config.Settings,
		Schemes:  make([]RouteScheme, 0, len(config.Schemes)),
		Vault:    make([]VaultSite, 0, len(config.Vault)),
		Marks:    make(map[string]LibraryMark, len(config.Marks)),
	}
	for k, v := range config.Marks {
		cp.Marks[k] = v
	}
	for k, v := range config.Keys {
		// 深拷贝 slice 字段
		if v.Models != nil {
			v.Models = append([]string(nil), v.Models...)
		}
		if v.APIKeys != nil {
			v.APIKeys = append([]string(nil), v.APIKeys...)
		}
		if v.KeyNames != nil {
			v.KeyNames = append([]string(nil), v.KeyNames...)
		}
		if v.KeyNos != nil {
			v.KeyNos = append([]int(nil), v.KeyNos...)
		}
		if v.ModelCaps != nil {
			v.ModelCaps = copyCaps(v.ModelCaps)
		}
		if v.ModelLimits != nil {
			v.ModelLimits = copyLimits(v.ModelLimits)
		}
		cp.Keys[k] = v
	}
	for _, c := range config.Custom {
		if c.Models != nil {
			c.Models = append([]string(nil), c.Models...)
		}
		if c.APIKeys != nil {
			c.APIKeys = append([]string(nil), c.APIKeys...)
		}
		if c.KeyNames != nil {
			c.KeyNames = append([]string(nil), c.KeyNames...)
		}
		if c.KeyNos != nil {
			c.KeyNos = append([]int(nil), c.KeyNos...)
		}
		if c.ModelCaps != nil {
			c.ModelCaps = copyCaps(c.ModelCaps)
		}
		if c.ModelLimits != nil {
			c.ModelLimits = copyLimits(c.ModelLimits)
		}
		cp.Custom = append(cp.Custom, c)
	}
	for _, sc := range config.Schemes {
		if sc.Models != nil {
			sc.Models = append([]string(nil), sc.Models...)
		}
		cp.Schemes = append(cp.Schemes, sc)
	}
	for _, v := range config.Vault {
		if v.Accounts != nil {
			v.Accounts = append([]VaultAccount(nil), v.Accounts...)
		}
		cp.Vault = append(cp.Vault, v)
	}
	return cp
}

// SetKey 设置某厂商的 key / 额度
func SetKey(providerID string, pc ProviderConfig) {
	mu.Lock()
	config.Keys[providerID] = pc
	mu.Unlock()
	dirty.Store(true)
}

// DeleteKey 删除某厂商的 key
func DeleteKey(providerID string) {
	mu.Lock()
	delete(config.Keys, providerID)
	mu.Unlock()
	dirty.Store(true)
}

// AddUsed 累加某厂商已用 tokens（内存累加，由 StartAutoFlush 定时落盘）
func AddUsed(providerID string, n int64) {
	mu.Lock()
	if pc, ok := config.Keys[providerID]; ok {
		pc.Used += n
		config.Keys[providerID] = pc
	}
	mu.Unlock()
	dirty.Store(true)
}

// UpsertCustom 新增或更新自定义厂商
func UpsertCustom(p CustomProvider) {
	mu.Lock()
	for i, c := range config.Custom {
		if c.ID == p.ID {
			config.Custom[i] = p
			mu.Unlock()
			dirty.Store(true)
			return
		}
	}
	config.Custom = append(config.Custom, p)
	mu.Unlock()
	dirty.Store(true)
}

// DeleteCustom 删除自定义厂商
func DeleteCustom(id string) {
	mu.Lock()
	out := config.Custom[:0]
	for _, c := range config.Custom {
		if c.ID != id {
			out = append(out, c)
		}
	}
	config.Custom = out
	mu.Unlock()
	dirty.Store(true)
}

// SetSettings 更新设置
func SetSettings(s Settings) {
	mu.Lock()
	config.Settings = s
	mu.Unlock()
	dirty.Store(true)
}

// SetSchemes 整体替换自动路由方案列表
func SetSchemes(schemes []RouteScheme) {
	mu.Lock()
	config.Schemes = schemes
	mu.Unlock()
	dirty.Store(true)
}

// SetVault 整体替换账号库（网站登录账号密码）列表
func SetVault(vault []VaultSite) {
	mu.Lock()
	config.Vault = vault
	mu.Unlock()
	dirty.Store(true)
}

// SetMarks 整体替换密钥库的收藏 / 分组标记
func SetMarks(marks map[string]LibraryMark) {
	mu.Lock()
	if marks == nil {
		marks = map[string]LibraryMark{}
	}
	config.Marks = marks
	mu.Unlock()
	dirty.Store(true)
}
