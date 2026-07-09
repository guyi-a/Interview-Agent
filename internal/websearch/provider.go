// Package websearch 是本项目的联网搜索能力，参考 PentaLoom 的双 provider 架构：
//
//   - Tavily —— 海外源，英文为主，1000 次/月免费（https://app.tavily.com）
//   - Bocha  —— 国内源，中文源覆盖好，1000 次试用（https://open.bochaai.com）
//
// region 三档：
//   - "global" → 单 Tavily
//   - "cn"     → 单 Bocha
//   - "both"   → 两路并发，按 href 去重合并（跨域话题用，默认）
//
// "both" 部分失败容忍：一边挂另一边还能用；两边都挂才抛 Error。
// 没配 key 的 provider 当"失败"处理（返空、不参与合并），不阻塞另一边。
package websearch

// TextSearchResult 是一条搜索结果。3 字段最小够用，足够 agent 决策
// "看完就用" 还是 "打开链接深读"。
type TextSearchResult struct {
	Title string `json:"title"`
	Href  string `json:"href"`
	Body  string `json:"body"`
}

// Provider 是所有搜索 provider 的统一接口。加新 provider（Brave / Serper /
// SerpAPI …）只要实现 buildRequest + parseResponse，service 层的路由 / 并发
// / 去重代码不动。
type Provider interface {
	Name() string
	Endpoint() string

	// BuildRequest 返回 (headers, jsonBody)，用 POST 打 Endpoint。
	BuildRequest(
		query string,
		maxResults int,
		timelimit string,
		allowedDomains []string,
		blockedDomains []string,
		topic string,
	) (headers map[string]string, body map[string]any)

	// ParseResponse 从 provider 响应里挖出结果列表。不同 provider 的响应
	// 结构差别很大，parse 逻辑各自实现。
	ParseResponse(data map[string]any) []TextSearchResult
}
