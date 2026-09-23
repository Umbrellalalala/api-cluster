// Package balance 提供各厂商账户余额/额度查询。
// 仅支持「用 API Key 即可直接查询」的厂商，返回可读的余额文本。
package balance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result 查询结果
type Result struct {
	OK    bool   `json:"ok"`
	Text  string `json:"text,omitempty"`  // 可读余额文本
	Error string `json:"error,omitempty"` // 失败原因
}

// Query 按厂商 ID 查询余额
func Query(providerID, apiKey string) Result {
	if apiKey == "" {
		return Result{OK: false, Error: "该厂商未配置 API Key"}
	}
	switch providerID {
	case "deepseek":
		return queryDeepSeek(apiKey)
	case "siliconflow":
		return querySiliconFlow(apiKey)
	case "openrouter":
		return queryOpenRouter(apiKey)
	case "kimi":
		return queryKimi(apiKey)
	default:
		return Result{OK: false, Error: "该厂商暂不支持余额查询"}
	}
}

// doJSON 发起 GET 请求并解析 JSON（Bearer 鉴权）
func doJSON(url, apiKey string, out any) error {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := truncate(string(body), 200)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析响应失败: %v", err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// DeepSeek: GET /user/balance → balance_infos[].total_balance（人民币）
func queryDeepSeek(apiKey string) Result {
	var resp struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string `json:"currency"`
			TotalBalance    string `json:"total_balance"`
			GrantedBalance  string `json:"granted_balance"`
			ToppedUpBalance string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := doJSON("https://api.deepseek.com/user/balance", apiKey, &resp); err != nil {
		return Result{OK: false, Error: err.Error()}
	}
	if !resp.IsAvailable || len(resp.BalanceInfos) == 0 {
		return Result{OK: true, Text: "余额 0（账户不可用或未充值）"}
	}
	b := resp.BalanceInfos[0]
	return Result{OK: true, Text: fmt.Sprintf("余额 %s %s（赠送 %s + 充值 %s）", b.TotalBalance, b.Currency, b.GrantedBalance, b.ToppedUpBalance)}
}

// 硅基流动: GET /v1/user/info → data.balance / totalBalance（人民币）。
// 注意：国内站 api.siliconflow.cn 的 /v1/user/info 已于 2026 年正式停用（HTTP 410，code 20092），
// 官方文档与实际行为不一致，暂无公开替代接口。国际站 api.siliconflow.com 仍可用但 Key 与国内站不通用。
// 这里依次尝试两个域名，都失败时返回友好提示，避免把 410 误报成「Key 无效」。
func querySiliconFlow(apiKey string) Result {
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Balance       string `json:"balance"`
			ChargeBalance string `json:"chargeBalance"`
			TotalBalance  string `json:"totalBalance"`
		} `json:"data"`
	}
	for _, base := range []string{"https://api.siliconflow.com", "https://api.siliconflow.cn"} {
		if err := doJSON(base+"/v1/user/info", apiKey, &resp); err != nil {
			continue
		}
		if resp.Code != 20000 {
			continue
		}
		total := resp.Data.TotalBalance
		if total == "" {
			total = resp.Data.Balance
		}
		return Result{OK: true, Text: fmt.Sprintf("余额 ¥%s（总余额 ¥%s）", resp.Data.Balance, total)}
	}
	return Result{OK: false, Error: "硅基流动已停用 API 余额查询接口，请在控制台 cloud.siliconflow.cn 查看余额"}
}

// OpenRouter: GET /api/v1/auth/key → data.limit / data.usage（美元信用额度）
func queryOpenRouter(apiKey string) Result {
	var resp struct {
		Data struct {
			Label      string  `json:"label"`
			Usage      float64 `json:"usage"`
			Limit      float64 `json:"limit"`
			IsFreeTier bool    `json:"is_free_tier"`
		} `json:"data"`
	}
	if err := doJSON("https://openrouter.ai/api/v1/auth/key", apiKey, &resp); err != nil {
		return Result{OK: false, Error: err.Error()}
	}
	remain := resp.Data.Limit - resp.Data.Usage
	if remain < 0 {
		remain = 0
	}
	return Result{OK: true, Text: fmt.Sprintf("额度 $%.4f，已用 $%.4f，剩余 $%.4f", resp.Data.Limit, resp.Data.Usage, remain)}
}

// Kimi: GET /v1/users/me/balance → data.available_balance（人民币）
func queryKimi(apiKey string) Result {
	var resp struct {
		Data struct {
			AvailableBalance float64 `json:"available_balance"`
			VoucherBalance   float64 `json:"voucher_balance"`
			CashBalance      float64 `json:"cash_balance"`
		} `json:"data"`
	}
	if err := doJSON("https://api.moonshot.cn/v1/users/me/balance", apiKey, &resp); err != nil {
		return Result{OK: false, Error: err.Error()}
	}
	return Result{OK: true, Text: fmt.Sprintf("余额 ¥%.2f（现金 ¥%.2f + 代金券 ¥%.2f）", resp.Data.AvailableBalance, resp.Data.CashBalance, resp.Data.VoucherBalance)}
}
