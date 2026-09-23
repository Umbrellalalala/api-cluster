# ApiCluster · 本地聚合大模型 API 网关

> 把 36 家厂商的 API Key 收进一个本地密钥库，对外只暴露一个 `http://localhost:3003/v1`。Windows 桌面应用，单文件 exe 约 12MB，零运行时依赖。

![License](https://img.shields.io/badge/license-MIT-blue) ![Go](https://img.shields.io/badge/Go-1.27-00ADD8) ![Platform](https://img.shields.io/badge/platform-Windows-lightgrey) ![Runtime](https://img.shields.io/badge/runtime-none-0a0)

任何 AI 客户端（Cursor、OpenWebUI、各类 Agent 脚手架）都要为每家厂商单独配一份 Base URL + Key。ApiCluster 做的事：所有厂商统一收口，客户端只填三行，切厂商由本地代理负责，**请求从你的电脑直连厂商，Key 不出本机**。

## 功能

| | |
| --- | --- |
| **统一入口** | `http://localhost:3003/v1`，OpenAI 兼容，SSE 流式逐行透传 |
| **36 家厂商目录** | Base URL 预置，含模型清单、能力标签、logo、注册地址直达；列表外的一键加自定义厂商 |
| **自动路由** | 客户端只建一条 `inurl` 模型即可，按文本 / 代码 / 图像 / 视频 / 音频分流；某家 5xx 或 429 自动切下一家 |
| **用量统计** | 每个厂商的调用量、Token 数、失败率就地可看 |
| **账号库** | 另存各站登录账号/密码（只有 Google 登录、只有手机验证码这类情况），logo 可拖图上传，密码默认掩码、显示 20 秒后自动收回，可导出成 Markdown |
| **桌面形态** | 系统托盘常驻，关窗即最小化；开机自启写 `HKCU\...\Run`，不需要管理员权限 |
| **SSH 反向隧道** | 本机主动向远程服务器发起连接，把本地代理端口反向映射过去，让远程也能用这份 Key——远程机器不需要开放任何入站端口 |
| **降级兜底** | WebView2 运行时缺失时自动改用系统浏览器打开管理界面 |

## 快速上手

1. 编译或直接运行 exe（本仓库不含二进制，编译见下）。窗口打开，托盘出现图标。
2. 「密钥库」页选厂商 → 粘贴 Key → 保存。已填的厂商打 `✓ 已添加`。
3. 在 AI 客户端填三个值：

| 字段 | 填什么 | 示例 |
| --- | --- | --- |
| Base URL | 本地代理地址 | `http://localhost:3003/v1` |
| API Key | 任意值（本地不鉴权） | `byok` |
| Model | 模型 id 或路由别名 | `qwen-max` / `inurl` |

## 从源码编译

需要 Go 1.27+：

```bat
build.bat
```

等价于：

```bat
rsrc -manifest app.manifest -o rsrc.syso
go build -ldflags "-H windowsgui -s -w" -o ApiCluster.exe .
```

`-H windowsgui` 让它作为 GUI 程序编译（开机自启不弹黑框）。

### 可选：CLIProxyAPI 边车

「CLI 边车」功能会把一个外部可执行文件当子进程拉起，用它来把 Claude Code / Codex / Antigravity / Grok / Kimi 等**订阅账号**包装成 API。

- 该二进制是上游开源项目 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（MIT License）的编译产物，**不由本仓库分发**，请自行下载并放到 `ApiCluster.exe` 同目录，文件名 `CLIProxyAPI.exe`。
- 配置文件由本程序自动生成，账号凭证目录默认在数据目录下的 `cliproxy/auths`。
- 上游的版权声明与许可条款见 [NOTICE](NOTICE)。

## 数据存储

```
%USERPROFILE%\.ApiCluster\keys.json      # API Key + 账号库
%USERPROFILE%\.ApiCluster\apicluster.log # 运行日志
```

**明文存储是有意为之**：这是本地个人工具，明文方便查看、迁移和备份。代价是——`keys.json` 里就是明文密钥与网站密码，请不要把它提交进任何仓库、也不要发给别人。删掉这个文件即清空全部数据。

## 项目结构

```
api_cluster/
├── main.go                     # 入口：代理 + WebView2 窗口 + 托盘 + 边车
├── internal/
│   ├── catalog/catalog.go      # 36 家厂商目录（描述/logo/注册链接/模型/能力标签）
│   ├── config/config.go        # 明文配置读写
│   ├── proxy/proxy.go          # OpenAI 兼容代理：转发、SSE 流式、用量统计
│   ├── proxy/router.go         # 自动路由：inurl 别名、故障切换、能力分类
│   ├── webui/webui.go          # 管理 API
│   ├── webui/static/           # 卡片式界面（go:embed 内嵌）
│   ├── sidecar/sidecar.go      # CLIProxyAPI 子进程管理与配置生成
│   ├── tunnel/tunnel.go        # SSH 反向隧道
│   ├── balance/balance.go      # 厂商余额查询
│   └── autostart/autostart.go  # 开机自启（注册表）
├── app.manifest                # Windows 通用控件 manifest
├── build.bat                   # 一键编译
└── go.mod
```

## License

[MIT](LICENSE)。边车用到的 CLIProxyAPI 属上游项目，许可见 [NOTICE](NOTICE)。

---

觉得有用的话 **点个 Star** ⭐ 问题和建议欢迎开 [Issue](https://github.com/Umbrellalalala/api-cluster/issues)。

配合使用：[Life System](https://github.com/Umbrellalalala/life-system) 的集成工具页可以直接拉起本程序。
