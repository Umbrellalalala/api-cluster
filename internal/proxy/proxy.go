// Package proxy 实现 OpenAI 兼容的本地代理，负责把请求转发到各厂商并支持自动路由。
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"apicluster/internal/catalog"
	"apicluster/internal/config"
)

// errLocalRateLimit 本地限流（并发/每分钟请求数达到上限）哨兵。
// 触发时应向客户端返回 429（而非 502），且不应触发 key 轮换。
var errLocalRateLimit = errors.New("local_rate_limit")

// providerCache 候选厂商列表的短 TTL 缓存，避免每个请求都深拷贝整份配置
type providerCache struct {
	targets []providerTarget
	at      time.Time
}

// videoTask 记录一次视频生成任务对应的转发目标，供异步结果查询时定位厂商与 Key
type videoTask struct {
	target providerTarget
	at     time.Time
}

// providerCacheTTL 缓存有效期：够短以保证配置变更能快速生效，够长以摊薄深拷贝开销
const providerCacheTTL = 2 * time.Second

// Server 代理服务器
type Server struct {
	mu     sync.Mutex
	sticky map[string]int // 每类能力上次成功命中的候选索引（粘性路由，稳定 model 以命中 prompt cache）
	client *http.Client

	cacheMu sync.Mutex
	cache   map[catalog.Capability]providerCache

	taskMu     sync.Mutex
	videoTasks map[string]videoTask

	limiter *modelLimiter
}

// New 创建代理服务器
func New() *Server {
	return &Server{
		sticky:     map[string]int{},
		cache:      map[catalog.Capability]providerCache{},
		videoTasks: map[string]videoTask{},
		limiter:    newModelLimiter(),
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 180 * time.Second},
		},
	}
}

// cachedProviders 读取缓存（未过期时返回副本）
func (s *Server) cachedProviders(cap catalog.Capability) ([]providerTarget, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	c, ok := s.cache[cap]
	if !ok || time.Since(c.at) > providerCacheTTL {
		return nil, false
	}
	out := make([]providerTarget, len(c.targets))
	copy(out, c.targets)
	return out, true
}

// storeProviders 写入缓存
func (s *Server) storeProviders(cap catalog.Capability, targets []providerTarget) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache[cap] = providerCache{targets: targets, at: time.Now()}
}

// stickyIndex 返回该能力类别应优先尝试的候选索引。
// 粘性路由：始终复用上次成功的厂商，避免轮询导致 model 字段跳变、
// 从而让上游 prompt cache 无法命中（model 一变缓存 key 就变，必然 miss）。
func (s *Server) stickyIndex(cat string, n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.sticky[cat]
	if idx >= n {
		idx = 0
	}
	return idx
}

// rememberSticky 记录该能力类别本次成功命中的候选索引
func (s *Server) rememberSticky(cat string, idx int) {
	s.mu.Lock()
	s.sticky[cat] = idx
	s.mu.Unlock()
}

// ServeHTTP 路由分发
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch path {
	case "/v1/models":
		s.handleModels(w, r)
	case "/v1/chat/completions":
		s.handleChat(w, r, "/chat/completions")
	case "/v1/completions":
		s.handleChat(w, r, "/completions")
	case "/v1/embeddings":
		s.handleEmbeddings(w, r)
	case "/v1/images/generations":
		s.handleGenerate(w, r, "/images/generations")
	case "/v1/videos/generations":
		s.handleGenerate(w, r, "/videos/generations")
	case "/healthz":
		s.Healthz(w, r)
	default:
		if strings.HasPrefix(path, "/v1/videos/") {
			s.handleVideoResult(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

// Healthz 健康检查端点（同时供 main 注册到 /healthz）
func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

// handleModels 返回可用模型列表（别名 + 已配 Key 的厂商模型）
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := config.Get()
	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := []modelObj{}
	seen := map[string]bool{}
	add := func(id, owner string) {
		if !seen[id] {
			seen[id] = true
			data = append(data, modelObj{ID: id, Object: "model", OwnedBy: owner})
		}
	}
	for _, a := range allAliases() {
		add(a, "byok")
	}
	for _, p := range catalog.Providers {
		if !p.Compatible {
			continue
		}
		pc, ok := cfg.Keys[p.ID]
		if !ok || !pc.HasAnyKey() {
			continue
		}
		for _, m := range providerModels(p, pc) {
			add(m.ID, p.ID)
		}
	}
	for _, c := range cfg.Custom {
		if !c.HasAnyKey() {
			continue
		}
		for _, mid := range c.Models {
			add(mid, c.ID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// handleChat 处理 /chat/completions 与 /completions
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, endpoint string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 model 字段")
		return
	}
	stream, _ := req["stream"].(bool)

	// 「按 Key 测试」：管理界面通过请求头强制指定使用的 key 序号（-1 = 不指定）
	forceKey := -1
	if v := r.Header.Get("X-ApiCluster-Key-Index"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			forceKey = n
		}
	}

	// 1) 用户自定义方案：别名 → 有序模型列表
	if sc, ok := s.findScheme(model); ok {
		candidates := s.schemeTargets(sc)
		if len(candidates) == 0 {
			s.writeError(w, http.StatusBadGateway, fmt.Sprintf("方案「%s」中的模型均未配置 Key，请先在密钥库添加对应厂商的 API Key", sc.Name))
			return
		}
		if s.tryCandidates(candidates, sc.Name, req, endpoint, stream, w, r, forceKey) {
			return
		}
		s.writeError(w, http.StatusBadGateway, fmt.Sprintf("方案「%s」内所有模型均调用失败，请稍后重试", sc.Name))
		return
	}

	// 2) 内置能力别名（inurl / inurl-code / inurl-image / inurl-video / inurl-audio）
	if cap, ok := routeAlias(model); ok {
		// 「自动路由（inurl）」总开关必须真的起作用：关掉后别名不再轮询厂商，
		// 用户自己命名的方案（上面 1）不受影响，它们有自己的启用开关。
		if !config.Get().Settings.AutoRouteEnabled {
			s.writeError(w, http.StatusBadRequest,
				fmt.Sprintf("自动路由（inurl）已在「自动路由配置」页关闭，请把 model 改成具体模型名或你自己的方案名"))
			return
		}
		candidates := s.collectProviders(cap)
		if len(candidates) == 0 {
			s.writeError(w, http.StatusBadGateway, fmt.Sprintf("当前没有支持「%s」类别且已配置 Key 的厂商，请先在密钥库添加厂商 API Key", cap))
			return
		}
		if s.tryCandidates(candidates, string(cap), req, endpoint, stream, w, r, forceKey) {
			return
		}
		s.writeError(w, http.StatusBadGateway, "所有可用厂商均调用失败，请稍后重试")
		return
	}

	// 3) 具体模型
	t, err := s.findProviderByModel(model)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if forceKey >= 0 && forceKey < len(t.apiKeys) {
		t.keyIndex = forceKey
	}
	w.Header().Set("X-Routed-Provider", encHeader(t.name))
	w.Header().Set("X-Routed-Model", encHeader(t.model))
	w.Header().Set("X-Routed-Key-Index", strconv.Itoa(t.keyIndex))
	status, usage, ferr := s.forwardWithKeyRotation(t, req, endpoint, stream, w, r, forceKey)
	if ferr != nil {
		log.Printf("[proxy] 转发到 %s 失败: %v", t.name, ferr)
		if reason, ok := localLimitReason(ferr); ok {
			s.writeError(w, http.StatusTooManyRequests, reason)
			return
		}
		s.writeError(w, http.StatusBadGateway, fmt.Sprintf("调用厂商「%s」失败：%v", t.name, ferr))
		return
	}
	if status >= 200 && status < 300 && usage > 0 {
		config.AddUsed(t.id, usage)
	}
}

// tryCandidates 按顺序尝试一组候选目标，命中即返回 true（已把响应写给客户端）。
// 粘性路由：优先从上次命中的索引开始，稳定 model 以命中上游 prompt cache。
func (s *Server) tryCandidates(candidates []providerTarget, stickyKey string, req map[string]any, endpoint string, stream bool, w http.ResponseWriter, r *http.Request, forceKey int) bool {
	start := s.stickyIndex(stickyKey, len(candidates))
	var lastErr error
	for i := 0; i < len(candidates); i++ {
		idx := (start + i) % len(candidates)
		t := candidates[idx]
		if forceKey >= 0 && forceKey < len(t.apiKeys) {
			t.keyIndex = forceKey
		}
		// 通过响应头告知实际路由到的厂商与模型
		w.Header().Set("X-Routed-Provider", encHeader(t.name))
		w.Header().Set("X-Routed-Model", encHeader(t.model))
		w.Header().Set("X-Routed-Key-Index", strconv.Itoa(t.keyIndex))
		status, usage, ferr := s.forwardWithKeyRotation(t, req, endpoint, stream, w, r, forceKey)
		// 本地限流：视为该模型繁忙，切换下一个候选
		if errors.Is(ferr, errLocalRateLimit) {
			lastErr = ferr
			if reason, ok := localLimitReason(ferr); ok {
				log.Printf("[route] 厂商 %s 模型 %s 本地限流：%s，切换下一个", t.name, t.model, reason)
			}
			continue
		}
		// 5xx / 429 / 网络错误 → 切换下一个（流式已写出则无法切换，直接返回）
		if ferr == nil && status < 500 && status != 429 {
			if status >= 200 && status < 300 {
				if usage > 0 {
					config.AddUsed(t.id, usage)
				}
				// 记住本次命中的目标，下次优先复用（稳定 model → 命中缓存）
				s.rememberSticky(stickyKey, idx)
			}
			return true
		}
		// 流式响应已开始写出，无法再切换候选
		if stream && status == http.StatusOK {
			log.Printf("[route] 厂商 %s 模型 %s 流式响应已开始，无法切换", t.name, t.model)
			return true
		}
		lastErr = ferr
		if lastErr == nil {
			lastErr = fmt.Errorf("上游返回 %d", status)
		}
		log.Printf("[route] 厂商 %s 模型 %s 调用失败 status=%d err=%v，切换下一个", t.name, t.model, status, ferr)
	}
	if lastErr != nil {
		log.Printf("[route] 方案 %s 所有候选均失败: %v", stickyKey, lastErr)
	}
	return false
}

// handleEmbeddings 转发 embedding 请求
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	model, _ := req["model"].(string)
	stream, _ := req["stream"].(bool)
	t, err := s.findProviderByModel(model)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, _, _ = s.forwardWithKeyRotation(t, req, "/embeddings", stream, w, r, -1)
}

// auth 返回鉴权头名与前缀（未配置时按类型取默认）
func (t *providerTarget) auth() (string, string) {
	if t.authHeader != "" {
		return t.authHeader, t.authPrefix
	}
	if t.providerType == "anthropic" {
		return "x-api-key", ""
	}
	return "Authorization", "Bearer "
}

// handleGenerate 转发文生图 / 文生视频（生成）请求。
// 客户端走 OpenAI 兼容路径：POST /v1/images/generations 或 /v1/videos/generations，
// 与本地聚合代理的 OpenAI 风格保持一致。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request, endpoint string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 model 字段")
		return
	}
	t, err := s.findProviderByModel(model)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, status, ferr := s.forwardGeneration(t, req, endpoint, r)
	if ferr != nil {
		if reason, ok := localLimitReason(ferr); ok {
			s.writeError(w, http.StatusTooManyRequests, reason)
			return
		}
		s.writeError(w, http.StatusBadGateway, fmt.Sprintf("调用厂商「%s」失败：%v", t.name, ferr))
		return
	}
	// 视频生成返回异步任务 id，记录下来供 /v1/videos/{id} 查询结果
	if endpoint == "/videos/generations" && status >= 200 && status < 300 {
		var vr struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &vr) == nil && vr.ID != "" {
			s.rememberVideoTask(vr.ID, t)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// forwardGeneration 转发一次生成请求（图/视频），返回原始响应体与状态码（不写客户端）。
func (s *Server) forwardGeneration(t providerTarget, req map[string]any, endpoint string, r *http.Request) ([]byte, int, error) {
	release, reason := s.limiter.tryAcquire(t.id+"/"+t.model, t.limit)
	if release == nil {
		return nil, http.StatusTooManyRequests, fmt.Errorf("%w: %s", errLocalRateLimit, reason)
	}
	defer release()

	req["model"] = t.model
	outBody, _ := json.Marshal(req)
	upstream := strings.TrimRight(t.baseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, bytes.NewReader(outBody))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	ah, ap := t.auth()
	httpReq.Header.Set(ah, ap+t.currentKey())
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// handleVideoResult 查询视频生成异步任务结果：GET /v1/videos/{id}
func (s *Server) handleVideoResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	t, ok := s.lookupVideoTask(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, "未找到该视频生成任务（应用重启后会丢失，请重新提交生成）")
		return
	}
	upstream := strings.TrimRight(t.baseURL, "/") + "/async-result/" + url.PathEscape(id)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	ah, ap := t.auth()
	httpReq.Header.Set(ah, ap+t.currentKey())
	resp, err := s.client.Do(httpReq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "查询异步结果失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	copyHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

// rememberVideoTask 记录视频生成任务 → 转发目标（顺带清理超过 24h 的旧任务，避免无限增长）
func (s *Server) rememberVideoTask(id string, t providerTarget) {
	s.taskMu.Lock()
	now := time.Now()
	for k, v := range s.videoTasks {
		if now.Sub(v.at) > 24*time.Hour {
			delete(s.videoTasks, k)
		}
	}
	s.videoTasks[id] = videoTask{target: t, at: now}
	s.taskMu.Unlock()
}

// lookupVideoTask 查询视频任务对应的转发目标
func (s *Server) lookupVideoTask(id string) (providerTarget, bool) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	v, ok := s.videoTasks[id]
	return v.target, ok
}

// localLimitReason 从错误中提取本地限流原因；非本地限流返回 ok=false
func localLimitReason(err error) (string, bool) {
	if errors.Is(err, errLocalRateLimit) {
		return strings.TrimPrefix(err.Error(), "local_rate_limit: "), true
	}
	return "", false
}

// forwardWithKeyRotation 转发请求，支持 key 轮换。
// forceKey >= 0 时只使用指定序号的 key（管理界面「按 Key 测试」），不做轮换。
// 当遇到 401/403（key 无效/过期）或 429（限流/额度耗尽）时，自动切换到下一个 key 重试。
func (s *Server) forwardWithKeyRotation(t providerTarget, req map[string]any, endpoint string, stream bool, w http.ResponseWriter, r *http.Request, forceKey int) (int, int64, error) {
	// 指定 key：单次尝试，不轮换，便于单独验证某个 key 是否可用
	if forceKey >= 0 {
		if forceKey >= len(t.apiKeys) {
			return 0, 0, fmt.Errorf("厂商 %s 没有第 %d 个 Key", t.name, forceKey+1)
		}
		t.keyIndex = forceKey
		return s.forward(t, req, t.currentKey(), endpoint, stream, w, r)
	}

	// 记录已尝试的 key 数量，避免无限循环
	maxAttempts := len(t.apiKeys)
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		key := t.currentKey()
		status, usage, ferr := s.forward(t, req, key, endpoint, stream, w, r)

		// 本地限流：不轮换 key，直接返回
		if errors.Is(ferr, errLocalRateLimit) {
			return status, 0, ferr
		}

		// 成功或非 key 相关错误，直接返回
		if ferr == nil && status < 500 && status != 429 && status != 401 && status != 403 {
			return status, usage, ferr
		}

		// 401/403 = key 无效/过期，429 = 限流/额度耗尽
		if status == 401 || status == 403 || status == 429 {
			log.Printf("[key-rotate] 厂商 %s key[%d] 返回 %d，切换下一个 key",
				t.name, t.keyIndex, status)
			// 尝试切换到下一个 key
			if t.rotateKey() {
				// 同步更新 config 中的 KeyIndex 与顺序
				s.updateKeyIndex(t)
				continue
			}
			// 只有一个 key，无法切换
			return status, usage, ferr
		}

		// 5xx 或其他错误，返回给上层处理（切换厂商）
		return status, usage, ferr
	}

	return 0, 0, fmt.Errorf("厂商 %s 所有 key 均尝试失败", t.name)
}

// updateKeyIndex 同步 key 顺序与 index 到 config（内置厂商与自定义厂商都处理）。
// 名称与稳定编号必须一起写回：轮换改变了 apiKeys 的顺序，只写 key 的话
// 「主号 / Key 3」这类标签就会贴到别的 Key 上。
func (s *Server) updateKeyIndex(t providerTarget) {
	cfg := config.Get()
	if pc, ok := cfg.Keys[t.id]; ok {
		pc.APIKeys = append([]string(nil), t.apiKeys...)
		pc.KeyNames = append([]string(nil), t.keyNames...)
		pc.KeyNos = append([]int(nil), t.keyNos...)
		pc.KeyIndex = t.keyIndex
		config.SetKey(t.id, pc)
		return
	}
	for _, c := range cfg.Custom {
		if c.ID == t.id {
			c.APIKeys = append([]string(nil), t.apiKeys...)
			c.KeyNames = append([]string(nil), t.keyNames...)
			c.KeyNos = append([]int(nil), t.keyNos...)
			c.KeyIndex = t.keyIndex
			config.UpsertCustom(c)
			return
		}
	}
}

// parseRetryAfterHeader 从上游响应头解析 Retry-After（秒数）
func parseRetryAfterHeader(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	ra := resp.Header.Get("Retry-After")
	if ra == "" {
		return 0
	}
	// 尝试解析为秒数
	if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
		return secs
	}
	// 尝试解析为 HTTP-date
	if t, err := http.ParseTime(ra); err == nil {
		secs := int(time.Since(t).Seconds())
		if secs > 0 {
			return secs
		}
	}
	return 0
}

// forward 转发一次请求到目标厂商。返回状态码、用量、错误。
// 5xx / 429 不会写响应，返回给上层用于故障切换。
func (s *Server) forward(t providerTarget, req map[string]any, key, endpoint string, stream bool, w http.ResponseWriter, r *http.Request) (int, int64, error) {
	// 本地限流：并发 + 每分钟请求数（在真正发起上游请求前拦截）
	release, reason := s.limiter.tryAcquire(t.id+"/"+t.model, t.limit)
	if release == nil {
		return http.StatusTooManyRequests, 0, fmt.Errorf("%w: %s", errLocalRateLimit, reason)
	}
	defer release()

	req["model"] = t.model

	ep := endpoint
	if t.path != "" {
		ep = t.path
	}

	// 鉴权头（未配置时按类型取默认）
	authHeader := t.authHeader
	authPrefix := t.authPrefix
	if authHeader == "" {
		if t.providerType == "anthropic" {
			authHeader, authPrefix = "x-api-key", ""
		} else {
			authHeader, authPrefix = "Authorization", "Bearer "
		}
	}

	var outBody []byte
	if t.providerType == "anthropic" {
		// Anthropic：请求格式转换，统一非流式调用（响应按需包装为 SSE）
		outBody = convertToAnthropic(req)
		if t.path == "" && (ep == "/chat/completions" || ep == "/completions") {
			ep = "/v1/messages"
		}
	} else if stream {
		req["stream"] = true
		outBody, _ = json.Marshal(req)
	} else {
		outBody, _ = json.Marshal(req)
	}

	url := strings.TrimRight(t.baseURL, "/") + "/" + strings.TrimLeft(ep, "/")
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(outBody))
	if err != nil {
		return 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(authHeader, authPrefix+key)
	if t.providerType == "anthropic" {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for _, h := range []string{"HTTP-Referer", "X-Title"} {
		if v := r.Header.Get(h); v != "" {
			httpReq.Header.Set(h, v)
		}
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	status := resp.StatusCode

	// 解析 Retry-After 头（用于日志）
	if status == 429 {
		if secs := parseRetryAfterHeader(resp); secs > 0 {
			log.Printf("[forward] 厂商 %s 返回 429，Retry-After: %ds", t.name, secs)
		}
	}

	// 可重试错误：5xx / 429 / 401 / 403，不写响应体给客户端
	if status >= 500 || status == 429 || status == 401 || status == 403 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return status, 0, fmt.Errorf("上游返回 %d", status)
	}

	// 流式：必须在读取 body 之前逐行转发，否则 body 被 ReadAll 读空导致无输出
	if stream && status == http.StatusOK && t.providerType != "anthropic" {
		copyHeaders(w, resp)
		w.WriteHeader(status)
		return s.streamCopy(w, resp.Body)
	}

	data, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")

	// Anthropic：响应格式转换
	if t.providerType == "anthropic" {
		return s.writeAnthropicResponse(t, status, data, stream, w)
	}

	// 厂商返回 HTML 错误页（模型 ID / Base URL 配置错误），转成可读 JSON 错误
	if strings.Contains(ct, "text/html") || strings.HasPrefix(strings.TrimSpace(string(data)), "<") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("厂商「%s」返回了非预期响应（HTTP %d）。通常是模型 ID 或 Base URL 配置错误，请点该厂商卡片的「编辑」核对模型名与地址。", t.name, status),
				"type":    "upstream_html_error",
			},
		})
		return status, 0, nil
	}

	copyHeaders(w, resp)
	w.WriteHeader(status)
	_, _ = w.Write(data)
	if cached := extractCachedTokens(data); cached > 0 {
		log.Printf("[cache] %s/%s 命中缓存 cached_tokens=%d", t.name, t.model, cached)
	}
	return status, extractUsage(data), nil
}

// encHeader 对可能含非 ASCII 字符的响应头值做百分号编码。
// 浏览器按 Latin-1 解码 HTTP 头，中文（UTF-8 字节）直接写入会显示成乱码，
// 因此先转义、由前端 decodeURIComponent 还原。
func encHeader(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return url.PathEscape(s)
		}
	}
	return s
}

func copyHeaders(w http.ResponseWriter, resp *http.Response) {
	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

// convertToAnthropic 把 OpenAI 请求转换为 Anthropic /v1/messages 请求（强制非流式）
func convertToAnthropic(req map[string]any) []byte {
	system := ""
	var msgs []map[string]any
	if arr, ok := req["messages"].([]any); ok {
		for _, m := range arr {
			mm, _ := m.(map[string]any)
			if mm == nil {
				continue
			}
			role, _ := mm["role"].(string)
			content, _ := mm["content"].(string)
			if role == "system" {
				if system != "" {
					system += "\n"
				}
				system += content
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			msgs = append(msgs, map[string]any{"role": role, "content": content})
		}
	}
	out := map[string]any{
		"model":      req["model"],
		"messages":   msgs,
		"max_tokens": 4096,
	}
	if system != "" {
		out["system"] = system
	}
	if mt, ok := req["max_tokens"].(float64); ok && mt > 0 {
		out["max_tokens"] = int(mt)
	}
	if tmp, ok := req["temperature"].(float64); ok {
		out["temperature"] = tmp
	}
	b, _ := json.Marshal(out)
	return b
}

// writeAnthropicResponse 把 Anthropic 响应转换为 OpenAI 格式（流式请求包装为单 chunk SSE）
func (s *Server) writeAnthropicResponse(t providerTarget, status int, data []byte, wantStream bool, w http.ResponseWriter) (int, int64, error) {
	if status >= 400 {
		var ae struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &ae)
		msg := ae.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
		return status, 0, nil
	}

	var ar struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &ar); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "Anthropic 响应解析失败: " + err.Error()}})
		return status, 0, nil
	}

	text := ""
	for _, c := range ar.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	usage := ar.Usage.InputTokens + ar.Usage.OutputTokens

	openai := map[string]any{
		"id":     fmt.Sprintf("chatcmpl-anthropic-%d", time.Now().UnixNano()),
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     ar.Usage.InputTokens,
			"completion_tokens": ar.Usage.OutputTokens,
			"total_tokens":      usage,
		},
	}
	body, _ := json.Marshal(openai)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if wantStream {
		full := map[string]any{}
		_ = json.Unmarshal(body, &full)
		full["object"] = "chat.completion.chunk"
		if choices, ok := full["choices"].([]any); ok && len(choices) > 0 {
			if c, ok := choices[0].(map[string]any); ok {
				if msg, ok := c["message"].(map[string]any); ok {
					c["delta"] = msg
					delete(c, "message")
				}
			}
		}
		chunkJSON, _ := json.Marshal(full)
		_, _ = w.Write([]byte("data: " + string(chunkJSON) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	} else {
		_, _ = w.Write(body)
	}
	return status, usage, nil
}

var totalTokensRe = regexp.MustCompile(`"total_tokens"\s*:\s*(\d+)`)

// streamCopy 逐行转发 SSE 流，并尝试从末尾解析 usage
func (s *Server) streamCopy(w http.ResponseWriter, body io.Reader) (int, int64, error) {
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var usage int64
	var cachedLogged bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.Contains(line, []byte("total_tokens")) {
			if m := totalTokensRe.FindSubmatch(line); len(m) > 1 {
				var n int64
				_, _ = fmt.Sscanf(string(m[1]), "%d", &n)
				if n > usage {
					usage = n
				}
			}
		}
		if bytes.Contains(line, []byte("cached_tokens")) {
			if m := cachedTokensRe.FindSubmatch(line); len(m) > 1 {
				var n int64
				_, _ = fmt.Sscanf(string(m[1]), "%d", &n)
				if n > 0 && !cachedLogged {
					cachedLogged = true
					log.Printf("[cache] 流式响应命中缓存 cached_tokens=%d", n)
				}
			}
		}
		_, _ = w.Write(line)
		_, _ = w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	return http.StatusOK, usage, nil
}

func extractUsage(data []byte) int64 {
	var resp struct {
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &resp) == nil {
		return resp.Usage.TotalTokens
	}
	return 0
}

var cachedTokensRe = regexp.MustCompile(`"cached_tokens"\s*:\s*(\d+)`)

// extractCachedTokens 从 OpenAI 兼容响应中提取命中的缓存 token 数
// （位于 usage.prompt_tokens_details.cached_tokens，不同厂商可能还叫 cache_read_input_tokens）
func extractCachedTokens(data []byte) int64 {
	var resp struct {
		Usage struct {
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &resp) == nil && resp.Usage.PromptTokensDetails.CachedTokens > 0 {
		return resp.Usage.PromptTokensDetails.CachedTokens
	}
	// 兜底：Anthropic 风格 cache_read_input_tokens
	if m := regexp.MustCompile(`"cache_read_input_tokens"\s*:\s*(\d+)`).FindSubmatch(data); len(m) > 1 {
		var n int64
		_, _ = fmt.Sscanf(string(m[1]), "%d", &n)
		return n
	}
	return 0
}

func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
