// Package xhs 实现小红书选题采集库（xhs-collector）的业务逻辑层。
//
// 客户浏览器插件采集小红书笔记后上送结构化 payload，本层负责校验、去重
// （content_hash）与落库，并在内容变化或新增时把 enrich_status 置为 pending，
// 供后续 LLM 富化流水线（T4/T5）消费。业务逻辑统一在 biz 层，store 层只做持久化。
package xhs

import (
	"numind-server/internal/numind/store"
)

// XhsBiz 持有 store 依赖，承载选题库的业务逻辑（采集摄入 / 富化编排等）。
//
// enricher 为可选的异步富化框架（T3b）：非 nil 时 Ingest 落库置 pending 后投递
// 富化队列。为 nil 时仅落库置 pending，由后续 ListPendingEnrich 扫描兜底捡起，
// 不丢数据（便于单测注入 nil enricher 隔离富化逻辑）。
type XhsBiz struct {
	store    store.IXhsTopicStore
	enricher *Enricher
}

// NewXhsBiz 创建一个 XhsBiz 实例（不带富化框架，仅落库）。
func NewXhsBiz(s store.IXhsTopicStore) *XhsBiz {
	return &XhsBiz{store: s}
}

// NewXhsBizWithEnricher 创建一个 XhsBiz 实例并挂上异步富化框架。
// 应用启动时使用此构造函数，使 Ingest 在落库后自动投递富化队列。
func NewXhsBizWithEnricher(s store.IXhsTopicStore, e *Enricher) *XhsBiz {
	return &XhsBiz{store: s, enricher: e}
}
