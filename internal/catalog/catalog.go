// Package catalog 内置厂商与模型目录。
// 所有厂商均为 OpenAI 兼容格式（BaseURL + /chat/completions）。
// 模型能力标签用于自动路由（inurl-code / inurl-image / inurl-video / inurl-audio）。
package catalog

// Capability 模型能力标签
type Capability string

const (
	CapText  Capability = "text"
	CapCode  Capability = "code"
	CapImage Capability = "image"
	CapVideo Capability = "video"
	CapAudio Capability = "audio"
	// 生成类能力（文生图 / 文生视频）。它们走独立端点 /images/generations、/videos/generations，
	// 不参与 chat 自动路由（inurl-text/code/image/video/audio 均不会命中）。
	CapImageGen Capability = "image_gen"
	CapVideoGen Capability = "video_gen"
)

// Model 一个具体模型
type Model struct {
	ID           string       `json:"id"`
	Capabilities []Capability `json:"capabilities"`
}

// Provider 一个内置厂商
type Provider struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	BaseURL     string  `json:"base_url"`
	Models      []Model `json:"models"`
	Free        bool    `json:"free"`
	Country     string  `json:"country"`
	Note        string  `json:"note"`
	Compatible  bool    `json:"compatible"`
	// 展示字段（仿 token.inurl.link 卡片目录）
	Description string `json:"description"` // 卡片描述文案
	SignupURL   string `json:"signup_url"`  // 前往获取 Key 的链接
	LogoURL     string `json:"logo_url"`    // 厂商 logo（官网 favicon）
	Recommended bool   `json:"recommended"` // 是否标注「推荐」
	FreeType    string `json:"free_type"`   // 完全免费 / 注册送额度 / 无需 Key / 付费
	BalanceURL  string `json:"balance_url,omitempty"` // 余额查询端点（留空=不支持查询）
}

func caps(c ...Capability) []Capability {
	if len(c) == 0 {
		return []Capability{CapText}
	}
	return c
}

func text(id string) Model  { return Model{ID: id, Capabilities: caps(CapText)} }
func code(id string) Model  { return Model{ID: id, Capabilities: caps(CapText, CapCode)} }
func image(id string) Model { return Model{ID: id, Capabilities: caps(CapText, CapImage)} }
func video(id string) Model { return Model{ID: id, Capabilities: caps(CapText, CapVideo)} }
func audio(id string) Model { return Model{ID: id, Capabilities: caps(CapText, CapAudio)} }

// 生成类模型（文生图 / 文生视频）
func imageGen(id string) Model { return Model{ID: id, Capabilities: caps(CapImageGen)} }
func videoGen(id string) Model { return Model{ID: id, Capabilities: caps(CapVideoGen)} }

// Providers 内置厂商目录
var Providers = []Provider{
	// ---------- 免费 ----------
	{
		ID: "agnes", Name: "AGNES AI", Free: true, Country: "美国", Compatible: true,
		BaseURL: "https://agnes.ai/api/v1",
		Note:    "Sapiens AI 全模态免费网关",
		Description: "Sapiens AI 出品的全模态免费网关，兼容 OpenAI 格式，文本模型 agnes-2.5-flash 支持百万级上下文，注册即送免费 API Key。",
		SignupURL:   "https://agnes.ai",
		LogoURL:     "https://agnes.ai/favicon.ico",
		Recommended: true, FreeType: "完全免费",
		Models: []Model{text("agnes-2.5-flash"), text("agnes-2.0-flash")},
	},
	{
		ID: "zhipu-free", Name: "智谱 GLM-4-Flash", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Note:    "GLM-4-Flash 按量计费 ¥0",
		Description: "智谱 GLM-4-Flash 模型按量计费为 ¥0，画多少都不花钱，中文理解能力强。按国内法规要求，输出图片会带「AI 生成」角标。",
		SignupURL:   "https://open.bigmodel.cn",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/zhipu-color.png",
		Recommended: true, FreeType: "完全免费",
		Models: []Model{text("glm-4-flash"), text("glm-4-air"), imageGen("cogview-3-flash"), videoGen("cogvideox-flash")},
	},
	{
		ID: "siliconflow", Name: "硅基流动", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://api.siliconflow.cn/v1",
		BalanceURL: "https://api.siliconflow.cn/v1/user/info",
		Note:    "注册送额度",
		Description: "硅基流动为新老用户提供免费 Token 额度，可零成本体验 Qwen、DeepSeek、GLM 等开源模型。",
		SignupURL:   "https://cloud.siliconflow.cn",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/siliconcloud-color.png",
		Recommended: true, FreeType: "注册送额度",
		Models: []Model{text("Qwen/Qwen2.5-7B-Instruct"), text("deepseek-ai/DeepSeek-V2.5"), text("THUDM/glm-4-9b-chat")},
	},
	{
		ID: "longcat", Name: "美团 LongCat", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://api.longcat.chat/v1",
		Note:    "注册送额度",
		Description: "美团自研万亿参数 MoE 大模型，新用户注册送 1000 万 Tokens，原生 1M 上下文，兼容 OpenAI 格式。",
		SignupURL:   "https://longcat.chat",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/longcat-color.png",
		Recommended: true, FreeType: "注册送额度",
		Models: []Model{text("LongCat-2.0"), text("LongCat-Flash-Chat")},
	},
	{
		ID: "stablehorde", Name: "StableHorde 众包算力", Free: true, Country: "境外", Compatible: false,
		Note:    "非标准 OpenAI 格式，暂不支持直连",
		Description: "一个去中心化的免费文本生成社区，靠全球志愿者贡献算力运转。无需账号即可通过匿名方式排队生成，零成本但需耐心等待。",
		SignupURL:   "https://stablehorde.net",
		LogoURL:     "https://stablehorde.net/favicon.ico",
		FreeType:    "无需 Key",
		Models:      []Model{text("koboldllama-2-70b-chat")},
	},
	{
		ID: "ollama", Name: "Ollama（本机）", Free: true, Country: "本机", Compatible: true,
		BaseURL: "http://localhost:11434/v1",
		Note:    "需本机已安装 Ollama",
		Description: "本机运行开源模型，零网络成本，适合本地开发与隐私场景。",
		SignupURL:   "https://ollama.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/ollama.png",
		FreeType:    "完全免费",
		Models:      []Model{text("llama3.1"), text("qwen2.5"), text("deepseek-r1")},
	},
	{
		ID: "bailian", Name: "阿里云百炼", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Note:    "注册送额度",
		Description: "阿里云百炼为新用户提供免费体验额度，可调用 qwen-turbo 及轻量版 Qwen2.5 模型。",
		SignupURL:   "https://www.aliyun.com/product/bailian",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/alibaba-color.png",
		Recommended: true, FreeType: "注册送额度",
		Models: []Model{text("qwen-turbo"), text("qwen2.5-7b-instruct"), code("qwen2.5-coder-7b-instruct")},
	},
	{
		ID: "pollinations", Name: "Pollinations 公共通道", Free: true, Country: "境外", Compatible: true,
		BaseURL: "https://text.pollinations.ai/openai",
		Note:    "服务器在境外，国内网络可能需代理",
		Description: "开放的免费文本生成通道，无需注册也不需要 Key，适合先试试手感。",
		SignupURL:   "https://pollinations.ai",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/pollinations.png",
		Recommended: true, FreeType: "无需 Key",
		Models:      []Model{text("openai"), text("mistral"), text("llama")},
	},
	{
		ID: "qianfan", Name: "百度千帆", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://qianfan.baidubce.com/v2",
		Note:    "注册送额度",
		Description: "百度智能云千帆大模型平台，兼容 OpenAI v2 格式，新用户每月赠送 100 万 tokens 免费额度。",
		SignupURL:   "https://cloud.baidu.com/product/wenxinworkshop",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/baiducloud-color.png",
		FreeType:    "注册送额度",
		Models:      []Model{text("ernie-3.5-8k"), text("ernie-4.0-8k")},
	},
	{
		ID: "groq", Name: "Groq", Free: true, Country: "美国", Compatible: true,
		BaseURL: "https://api.groq.com/openai/v1",
		Note:    "注册送额度",
		Description: "以 LPU 推理加速著称，Llama 系列响应极快，适合高频低成本场景。",
		SignupURL:   "https://console.groq.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/groq.png",
		FreeType:    "注册送额度",
		Models:      []Model{text("llama-3.3-70b-versatile"), text("llama-3.1-8b-instant")},
	},
	{
		ID: "openrouter", Name: "OpenRouter", Free: true, Country: "美国", Compatible: true,
		BaseURL: "https://openrouter.ai/api/v1",
		BalanceURL: "https://openrouter.ai/api/v1/auth/key",
		Note:    "聚合 200+ 模型",
		Description: "聚合 200+ 模型的统一路由平台，支持免费/付费模型一站式调用。",
		SignupURL:   "https://openrouter.ai/settings/keys",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/openrouter-color.png",
		Recommended: true, FreeType: "注册送额度",
		Models: []Model{text("google/gemini-2.5-flash:free"), text("meta-llama/llama-3.3-70b-instruct:free")},
	},
	{
		ID: "akash", Name: "Akash Chat", Free: true, Country: "美国", Compatible: true,
		BaseURL: "https://chatapi.akash.network/api/v1",
		Note:    "去中心化低成本",
		Description: "去中心化算力网络 Akash，免费/低成本运行开源模型。",
		SignupURL:   "https://chatapi.akash.network",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/akashchat-color.png",
		FreeType:    "注册送额度",
		Models:      []Model{text("Meta-Llama-3-1-8B-Instruct-Q6_K"), image("Meta-Llama-3-2-11B-Vision-Instruct"), text("Meta-Llama-3-3-70B-Instruct")},
	},
	{
		ID: "gemini-free", Name: "Google Gemini（免费层）", Free: true, Country: "境外", Compatible: true,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Note:    "每日少量免费额度",
		Description: "通过 Google AI Studio 接入 Gemini 系列，每日有少量免费额度。",
		SignupURL:   "https://aistudio.google.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/gemini-color.png",
		FreeType:    "注册送额度",
		Models:      []Model{image("gemini-1.5-flash"), text("gemini-1.5-pro-002"), image("gemini-2.0-flash-exp")},
	},
	{
		ID: "jingdong", Name: "京东言犀", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://llm-cn.jdcloud.com/v1",
		Note:    "注册送额度",
		Description: "京东自研言犀大模型（ChatRhino），通过京东云提供 OpenAI 兼容接口，新用户通常可领试用额度。",
		SignupURL:   "https://www.jdcloud.com",
		LogoURL:     "https://www.jdcloud.com/favicon.ico",
		FreeType:    "注册送额度",
		Models:      []Model{text("chatrhino-81b-pro")},
	},
	{
		ID: "mimo", Name: "小米 MiMo", Free: true, Country: "中国", Compatible: true,
		BaseURL: "https://api.xiaomimimo.com/v1",
		Note:    "限时免费",
		Description: "小米自研大模型 MiMo 开放平台，兼容 OpenAI 格式（同时兼容 Anthropic 格式），限时免费开放 mimo-v2-flash / mimo-v2.5-pro 等模型。",
		SignupURL:   "https://www.mi.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/xiaomimimo.png",
		Recommended: true, FreeType: "完全免费",
		Models:      []Model{text("mimo-v2-flash"), text("mimo-v2.5-pro")},
	},
	{
		ID: "netease", Name: "网易有灵妙启", Free: true, Country: "中国", Compatible: false,
		Note:    "HMAC-SHA256 签名鉴权（非 OpenAI 兼容），暂不支持直连",
		Description: "网易有灵妙启（MoA）大模型，采用 HMAC-SHA256 签名鉴权；注册可申请体验额度。",
		SignupURL:   "https://youling.netease.im",
		LogoURL:     "https://www.163.com/favicon.ico",
		FreeType:    "注册送额度",
		Models:      []Model{text("yuyan-plus")},
	},
	{
		ID: "pangu", Name: "华为盘古", Free: true, Country: "中国", Compatible: false,
		Note:    "华为云 AK-SK 鉴权（非 OpenAI 兼容），暂不支持直连",
		Description: "华为云盘古大模型，需通过 ModelArts SDK / AK-SK 签名鉴权调用；每月赠送 50 万–100 万 tokens 免费额度。",
		SignupURL:   "https://www.huaweicloud.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/huaweicloud-color.png",
		FreeType:    "注册送额度",
		Models:      []Model{text("pangu-nlp-3.0")},
	},

	// ---------- 付费 ----------
	{
		ID: "openai", Name: "OpenAI", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://api.openai.com/v1",
		Description: "全球领先的 GPT 系列模型，覆盖通用对话、代码、多模态与创意写作。",
		SignupURL:   "https://platform.openai.com/api-keys",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/openai.png",
		FreeType:    "付费",
		Models:      []Model{image("gpt-4o-mini"), image("gpt-4o"), text("gpt-3.5-turbo")},
	},
	{
		ID: "deepseek", Name: "DeepSeek", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.deepseek.com/v1",
		BalanceURL: "https://api.deepseek.com/user/balance",
		Description: "国产高性价比推理模型，V4 系列在长文本与代码场景表现突出。",
		SignupURL:   "https://platform.deepseek.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/deepseek-color.png",
		Recommended: true, FreeType: "付费",
		Models:      []Model{code("deepseek-v4-flash"), code("deepseek-v4-pro"), text("deepseek-reasoner")},
	},
	{
		ID: "mistral", Name: "Mistral", Free: false, Country: "法国", Compatible: true,
		BaseURL: "https://api.mistral.ai/v1",
		Description: "欧洲开源大模型代表，Mistral Large / Small 在推理与多语言上均衡。",
		SignupURL:   "https://console.mistral.ai",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/mistral-color.png",
		FreeType:    "付费",
		Models:      []Model{text("mistral-large-latest"), text("mistral-small-latest")},
	},
	{
		ID: "claude", Name: "Claude", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://api.anthropic.com/v1",
		Description: "Claude 系列以长上下文与安全性著称，适合企业级复杂任务。",
		SignupURL:   "https://console.anthropic.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/claude-color.png",
		FreeType:    "付费",
		Models:      []Model{text("claude-3-5-sonnet-latest"), text("claude-3-5-haiku-latest"), text("claude-3-opus-latest")},
	},
	{
		ID: "gemini", Name: "Google Gemini", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Description: "Google 多模态大模型，原生支持超长上下文与图像/视频理解。",
		SignupURL:   "https://aistudio.google.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/gemini-color.png",
		FreeType:    "付费",
		Models:      []Model{image("gemini-1.5-flash"), image("gemini-1.5-pro"), image("gemini-2.0-flash-exp")},
	},
	{
		ID: "qwen", Name: "阿里云通义千问", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Description: "阿里云通义千问，中文理解与代码能力强，新用户免费额度即时体验。",
		SignupURL:   "https://bailian.console.aliyun.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/qwen-color.png",
		FreeType:    "付费",
		Models: []Model{
			text("qwen-plus"), text("qwen-max"), text("qwen-turbo"),
			code("qwen2.5-coder-plus"), image("qwen-vl-max"), text("qwen-long"),
		},
	},
	{
		ID: "zhipu", Name: "智谱 AI（Zhipu GLM）", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Description: "智谱 GLM 系列，中文与开源生态强，GLM-4V 支持图文多模态。",
		SignupURL:   "https://open.bigmodel.cn",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/zhipu-color.png",
		FreeType:    "付费",
		Models:      []Model{text("glm-4-plus"), text("glm-4-air"), text("glm-4-flash"), image("glm-4v")},
	},
	{
		ID: "kimi", Name: "月之暗面（Kimi）", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.moonshot.cn/v1",
		BalanceURL: "https://api.moonshot.cn/v1/users/me/balance",
		Description: "月之暗面 Kimi，以超长上下文与中文办公场景见长。",
		SignupURL:   "https://platform.moonshot.cn",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/kimi-color.png",
		FreeType:    "付费",
		Models:      []Model{text("moonshot-v1-8k"), text("moonshot-v1-32k"), text("moonshot-v1-128k")},
	},
	{
		ID: "doubao", Name: "火山引擎豆包", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Description: "字节火山引擎豆包，视频/语音/视觉模型丰富，企业级 SLA 稳定。",
		SignupURL:   "https://console.volcengine.com/ark",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/doubao-color.png",
		FreeType:    "付费",
		Models:      []Model{text("doubao-pro-32k"), text("doubao-pro-256k"), text("doubao-lite-32k"), video("doubao-video"), audio("doubao-tts")},
	},
	{
		ID: "minimax", Name: "MiniMax", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.minimax.chat/v1",
		Description: "MiniMax abab 系列，中文对话与语音合成体验流畅。",
		SignupURL:   "https://platform.minimaxi.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/minimax-color.png",
		FreeType:    "付费",
		Models:      []Model{text("abab6.5s-chat"), text("abab6.5t-chat"), text("abab7-chat-preview")},
	},
	{
		ID: "baichuan", Name: "百川智能", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.baichuan-ai.com/v1",
		Description: "百川智能，中文场景与搜索增强表现优异。",
		SignupURL:   "https://platform.baichuan-ai.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/baichuan-color.png",
		FreeType:    "付费",
		Models:      []Model{text("baichuan4"), text("baichuan3-turbo"), text("baichuan2-53b")},
	},
	{
		ID: "stepfun", Name: "阶跃星辰（StepFun）", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.stepfun.com/v1",
		Description: "阶跃星辰 Step 系列，多模态与推理能力并重。",
		SignupURL:   "https://platform.stepfun.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/stepfun-color.png",
		FreeType:    "付费",
		Models:      []Model{text("step-1-flash"), image("step-1v-8k"), text("step-2-16k")},
	},
	{
		ID: "iflytek", Name: "讯飞星火", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://spark-api-open.xf-yun.com/v1",
		Description: "讯飞星火，赠送 500 万 tokens，中文语音与长文本能力突出，教育/办公场景成熟。",
		SignupURL:   "https://console.xfyun.cn",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/iflytekcloud-color.png",
		Recommended: true, FreeType: "付费",
		Models:      []Model{text("generalv3.5"), text("generalv3"), text("pro-128k")},
	},
	{
		ID: "hunyuan", Name: "腾讯混元", Free: false, Country: "中国", Compatible: true,
		BaseURL: "https://api.hunyuan.cloud.tencent.com/v1",
		Description: "腾讯混元，中文多模态与腾讯生态集成良好。",
		SignupURL:   "https://console.cloud.tencent.com/hunyuan",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/hunyuan-color.png",
		FreeType:    "付费",
		Models:      []Model{text("hunyuan-pro"), text("hunyuan-standard"), text("hunyuan-turbo")},
	},
	{
		ID: "cohere", Name: "Cohere", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://api.cohere.com/v1",
		Description: "Cohere Command 系列，面向企业的文本生成与嵌入服务。",
		SignupURL:   "https://dashboard.cohere.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/cohere-color.png",
		FreeType:    "付费",
		Models:      []Model{text("command-r-plus"), text("command-r"), text("command-light")},
	},
	{
		ID: "perplexity", Name: "Perplexity", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://api.perplexity.ai",
		Description: "Perplexity Sonar 系列，联网搜索与实时信息检索能力强。",
		SignupURL:   "https://www.perplexity.ai/settings/api",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/perplexity-color.png",
		FreeType:    "付费",
		Models:      []Model{text("sonar"), text("sonar-pro"), text("sonar-reasoning")},
	},
	{
		ID: "xai", Name: "xAI（Grok）", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://api.x.ai/v1",
		Description: "xAI Grok，推特/X 生态原生，实时信息与长上下文并重。",
		SignupURL:   "https://console.x.ai",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/grok.png",
		FreeType:    "付费",
		Models:      []Model{text("grok-2"), text("grok-2-mini")},
	},
	{
		ID: "nvidia", Name: "NVIDIA NIM", Free: false, Country: "美国", Compatible: true,
		BaseURL: "https://integrate.api.nvidia.com/v1",
		Description: "NVIDIA NIM，针对 NVIDIA GPU 优化的企业级模型推理服务。",
		SignupURL:   "https://build.nvidia.com",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/nvidia-color.png",
		FreeType:    "付费",
		Models:      []Model{text("meta/llama-3.1-70b-instruct"), text("nvidia/llama-3.1-nemotron-70b-instruct")},
	},
	{
		ID: "upstage", Name: "Upstage（Solar）", Free: false, Country: "韩国", Compatible: true,
		BaseURL: "https://api.upstage.ai/v1/solar",
		Description: "Upstage Solar 系列，韩厂出品，韩语与英语任务表现优秀。",
		SignupURL:   "https://console.upstage.ai",
		LogoURL:     "https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/upstage-color.png",
		FreeType:    "付费",
		Models:      []Model{text("solar-pro"), text("solar-mini")},
	},
}

// ByID 按 ID 查找内置厂商
func ByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// FindModel 在全部内置厂商中查找某个模型 ID 归属的厂商
func FindModel(modelID string) (Provider, bool) {
	for _, p := range Providers {
		for _, m := range p.Models {
			if m.ID == modelID {
				return p, true
			}
		}
	}
	return Provider{}, false
}

// AllModels 返回全部内置模型（去重）
func AllModels() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range Providers {
		for _, m := range p.Models {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m.ID)
			}
		}
	}
	return out
}
