package proxy

import (
	"fmt"
	"strings"

	"apicluster/internal/catalog"
	"apicluster/internal/config"
)

// 路由别名常量
const (
	AliasInURL      = "inurl"
	AliasAuto       = "auto"
	AliasDefault    = "default"
	AliasInURLText  = "inurl-text"
	AliasInURLCode  = "inurl-code"
	AliasInURLImage = "inurl-image"
	AliasInURLVideo = "inurl-video"
	AliasInURLAudio = "inurl-audio"
)

// providerTarget 一次转发的目标厂商。
// 注意：该结构体按值传递，keyIndex 的切换仅作用于本次请求的副本，
// 因此不需要加锁（无共享状态）。
type providerTarget struct {
	id           string
	name         string
	baseURL      string
	path         string // 接口路径（endpoint），空则用类型默认
	apiKey       string // 当前使用的 key（由 keyIndex 指向）
	apiKeys      []string
	keyIndex     int
	authHeader   string // 鉴权头，空则按类型默认
	authPrefix   string // 前缀，空则按类型默认
	providerType string // openai / anthropic
	model        string // 实际调用时替换的模型 ID
	cap          catalog.Capability
	limit        config.ModelLimit // 该模型的本地并发/每分钟限流配置
}

// currentKey 返回当前 key
func (t *providerTarget) currentKey() string {
	if len(t.apiKeys) > 0 {
		if t.keyIndex >= len(t.apiKeys) {
			t.keyIndex = 0
		}
		return t.apiKeys[t.keyIndex]
	}
	return t.apiKey
}

// rotateKey 切换到下一个 key：把当前 key 移到列表末尾，返回是否成功（多个 key 时才有意义）。
// 约定：apiKeys 首位即「当前使用」的 key，因此轮换 = 把当前 key 挪到最后。
func (t *providerTarget) rotateKey() bool {
	if len(t.apiKeys) <= 1 {
		return false
	}
	if t.keyIndex < 0 || t.keyIndex >= len(t.apiKeys) {
		t.keyIndex = 0
	}
	k := t.apiKeys[t.keyIndex]
	rest := append([]string{}, t.apiKeys[:t.keyIndex]...)
	rest = append(rest, t.apiKeys[t.keyIndex+1:]...)
	t.apiKeys = append(rest, k)
	t.keyIndex = 0
	return true
}

func hasCap(caps []catalog.Capability, want catalog.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// allAliases 返回所有已知的自动路由别名
func allAliases() []string {
	return []string{AliasInURL, AliasAuto, AliasDefault, AliasInURLText, AliasInURLCode, AliasInURLImage, AliasInURLVideo, AliasInURLAudio}
}

// routeAlias 判断模型名是否为自动路由别名，返回目标能力类别
func routeAlias(model string) (catalog.Capability, bool) {
	switch strings.ToLower(model) {
	case AliasInURL, AliasAuto, AliasDefault, AliasInURLText:
		return catalog.CapText, true
	case AliasInURLCode:
		return catalog.CapCode, true
	case AliasInURLImage:
		return catalog.CapImage, true
	case AliasInURLVideo:
		return catalog.CapVideo, true
	case AliasInURLAudio:
		return catalog.CapAudio, true
	}
	return "", false
}

// providerModels 返回厂商的有效模型列表（用户覆盖则用覆盖，否则用目录默认）。
// 覆盖模型的能力标签优先级：用户手选（pc.ModelCaps）> 目录默认 > 文本。
func providerModels(p catalog.Provider, pc config.ProviderConfig) []catalog.Model {
	if len(pc.Models) == 0 {
		return p.Models
	}
	models := make([]catalog.Model, 0, len(pc.Models))
	for _, mid := range pc.Models {
		models = append(models, catalog.Model{ID: mid, Capabilities: overrideCaps(p, pc, mid)})
	}
	return models
}

// overrideCaps 解析覆盖模型的能力标签：用户手选 > 目录默认 > 文本。
func overrideCaps(p catalog.Provider, pc config.ProviderConfig, mid string) []catalog.Capability {
	if caps, ok := pc.ModelCaps[mid]; ok && len(caps) > 0 {
		out := make([]catalog.Capability, 0, len(caps))
		for _, c := range caps {
			out = append(out, catalog.Capability(c))
		}
		return out
	}
	for _, m := range p.Models {
		if m.ID == mid && len(m.Capabilities) > 0 {
			return m.Capabilities
		}
	}
	return []catalog.Capability{catalog.CapText}
}

// collectProviders 收集所有已配置 Key 且支持某能力的厂商（带短 TTL 缓存）
func (s *Server) collectProviders(cap catalog.Capability) []providerTarget {
	if cached, ok := s.cachedProviders(cap); ok {
		return cached
	}
	cfg := config.Get()
	var out []providerTarget

	for _, p := range catalog.Providers {
		if !p.Compatible || p.BaseURL == "" {
			continue
		}
		pc, ok := cfg.Keys[p.ID]
		if !ok || !pc.HasAnyKey() {
			continue
		}
		baseURL := p.BaseURL
		if pc.BaseURL != "" {
			baseURL = pc.BaseURL
		}
		ptype := pc.Type
		if ptype == "" {
			ptype = "openai"
		}
		for _, m := range providerModels(p, pc) {
			if hasCap(m.Capabilities, cap) {
				out = append(out, providerTarget{
					id: p.ID, name: p.Name, baseURL: baseURL, path: pc.Path,
					apiKey: pc.ActiveKey(), apiKeys: pc.AllKeys(), keyIndex: pc.KeyIndex,
					authHeader: pc.AuthHeader, authPrefix: pc.AuthPrefix,
					providerType: ptype, model: m.ID, cap: cap,
					limit: pc.ModelLimits[m.ID],
				})
				break
			}
		}
	}

	for _, c := range cfg.Custom {
		if !c.HasAnyKey() || c.BaseURL == "" || len(c.Models) == 0 {
			continue
		}
		for _, mid := range c.Models {
			if customModelHasCap(c, mid, cap) {
				out = append(out, providerTarget{
					id: c.ID, name: c.Name, baseURL: c.BaseURL, path: c.Path,
					apiKey: c.ActiveKey(), apiKeys: c.AllKeys(), keyIndex: c.KeyIndex,
					authHeader: c.AuthHeader, authPrefix: c.AuthPrefix,
					providerType: c.Type, model: mid, cap: cap,
					limit: c.ModelLimits[mid],
				})
				break
			}
		}
	}

	// 按 AutoProviderOrder 排序（稳定）
	if order := strings.TrimSpace(cfg.Settings.AutoProviderOrder); order != "" {
		rank := map[string]int{}
		for i, id := range strings.Split(order, ",") {
			rank[strings.TrimSpace(id)] = i
		}
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				rj, okj := rank[out[j].id]
				rj1, okj1 := rank[out[j-1].id]
				if !okj {
					rj = 1 << 20
				}
				if !okj1 {
					rj1 = 1 << 20
				}
				if rj < rj1 {
					out[j], out[j-1] = out[j-1], out[j]
				} else {
					break
				}
			}
		}
	}
	s.storeProviders(cap, out)
	return out
}

func customModelHasCap(c config.CustomProvider, modelID string, cap catalog.Capability) bool {
	if len(c.ModelCaps) == 0 {
		return cap == catalog.CapText
	}
	caps, ok := c.ModelCaps[modelID]
	if !ok || len(caps) == 0 {
		return cap == catalog.CapText
	}
	for _, cstr := range caps {
		if catalog.Capability(cstr) == cap {
			return true
		}
	}
	return false
}

// findProviderByModel 按具体模型 ID 查找有 Key 的厂商
func (s *Server) findProviderByModel(modelID string) (providerTarget, error) {
	return s.findProviderByModelCfg(config.Get(), modelID)
}

// findProviderByModelCfg 使用传入的配置查找模型对应的转发目标（避免重复 Get）
func (s *Server) findProviderByModelCfg(cfg *config.Config, modelID string) (providerTarget, error) {
	for _, p := range catalog.Providers {
		if !p.Compatible || p.BaseURL == "" {
			continue
		}
		pc, ok := cfg.Keys[p.ID]
		if !ok || !pc.HasAnyKey() {
			continue
		}
		baseURL := p.BaseURL
		if pc.BaseURL != "" {
			baseURL = pc.BaseURL
		}
		ptype := pc.Type
		if ptype == "" {
			ptype = "openai"
		}
		for _, m := range providerModels(p, pc) {
			if m.ID == modelID {
				cap := catalog.CapText
				if len(m.Capabilities) > 0 {
					cap = m.Capabilities[0]
				}
				return providerTarget{
					id: p.ID, name: p.Name, baseURL: baseURL, path: pc.Path,
					apiKey: pc.ActiveKey(), apiKeys: pc.AllKeys(), keyIndex: pc.KeyIndex,
					authHeader: pc.AuthHeader, authPrefix: pc.AuthPrefix,
					providerType: ptype, model: modelID, cap: cap,
					limit: pc.ModelLimits[modelID],
				}, nil
			}
		}
	}

	for _, c := range cfg.Custom {
		if !c.HasAnyKey() || c.BaseURL == "" {
			continue
		}
		for _, mid := range c.Models {
			if mid == modelID {
				return providerTarget{
					id: c.ID, name: c.Name, baseURL: c.BaseURL, path: c.Path,
					apiKey: c.ActiveKey(), apiKeys: c.AllKeys(), keyIndex: c.KeyIndex,
					authHeader: c.AuthHeader, authPrefix: c.AuthPrefix,
					providerType: c.Type, model: modelID, cap: catalog.CapText,
					limit: c.ModelLimits[modelID],
				}, nil
			}
		}
	}

	return providerTarget{}, fmt.Errorf("未找到模型 %q 对应的已配置 Key 的厂商，请先在密钥库添加对应厂商的 API Key", modelID)
}

// findScheme 返回方案名匹配且已启用的方案
func (s *Server) findScheme(name string) (config.RouteScheme, bool) {
	cfg := config.Get()
	for _, sc := range cfg.Schemes {
		if sc.Enabled && strings.EqualFold(sc.Name, name) {
			return sc, true
		}
	}
	return config.RouteScheme{}, false
}

// schemeTargets 把方案里的有序模型 ID 解析成转发目标（未配 Key 的模型跳过）
func (s *Server) schemeTargets(sc config.RouteScheme) []providerTarget {
	cfg := config.Get()
	out := make([]providerTarget, 0, len(sc.Models))
	for _, mid := range sc.Models {
		if t, err := s.findProviderByModelCfg(cfg, mid); err == nil {
			out = append(out, t)
		}
	}
	return out
}
