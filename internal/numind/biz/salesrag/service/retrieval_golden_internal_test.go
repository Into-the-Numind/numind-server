//go:build integration

// Package service — exact-match golden harness (NDF task T0.2).
//
// 目的：为 T1.6（salesrag 检索主干改调 internal/pkg/retrieval 底座）建立一个
// 确定性的「逐位一致」安全网。本测试用一个确定性 fake embedder + 固定合成语料
// + 固定 query 集，跑当前 salesrag 检索主干 RetrieveForResponseV2，抓 top-K
// chunk_id，按 (Score desc, chunk.ID asc) 稳定排序后与 golden fixture 比对。
//
// T1.6 改完 salesrag 后重跑本测试：若检索结果（命中的 chunk_id 有序列表）发生
// 任何变化即 FAIL —— 这正是「逐位一致」保护的目的。
//
// 捕获模式：设置环境变量 UPDATE_GOLDEN=1 时，写 golden 而非比对：
//
//	UPDATE_GOLDEN=1 go test -tags=integration -run TestRetrievalGolden ./internal/numind/biz/salesrag/service/...
//
// 比对模式（默认）：
//
//	go test -tags=integration -run TestRetrievalGolden ./internal/numind/biz/salesrag/service/...
//
// 铁律：本文件是纯测试 harness，禁止调用真实 AI（fake embedder 离线确定性）。
// 检索主干内的 LLM 调用（intent rewrite / rerank）在本测试中均被规避或降级为
// 确定性 fallback：
//   - intent rewrite 走 RegexRouter（纯正则，零 LLM，确定性）；
//   - rerank 在 headless gateway（无 provider）下返回 error → 主干 fallback 到
//     原始 top-5（见 sales_rag.go rerankChunks 失败分支），同样确定性。
//
// 适用范围与前提（reviewer P2 记录，刻意的边界）：
//  1. 本 golden 只验「逐位一致」（同输入→同 chunk_id 序列），**不评估语义检索质量**。
//     fake embedder 是 sha256 派生的非语义向量，故命中未必语义最优；Recall@k/MRR
//     质量基线需真实 embedding + 创始人标注集（P2 验收阶段，本 harness 不覆盖）。
//  2. 只覆盖 chatMode="free" 通用主干；不覆盖 sales 模式的 strategy 分支与 opinion
//     第二通道（spec §9.4：strategy/opinion 留 salesrag、T1.6 不改，故风险可接受）。
//  3. 确定性依赖 RegexRouter 只产 1 路 query（parallelSearch 单 goroutine）。若未来
//     改用 LLMRouter（多路 query），degrade 路径的 allChunks[:N] 截断顺序会变 goroutine
//     依赖 → 可能 flaky；届时需在 parallelSearch 出口先 (Score desc, ID asc) 排序再截断，
//     或另立 harness。T1.6 不改本测试的 router，故当前安全。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	aiservice "numind-server/internal/pkg/aiservice"
	radapter "numind-server/internal/pkg/retrieval/adapter"
	"numind-server/internal/pkg/retrieval/domain"
)

// goldenPath 是 golden fixture 的相对路径（相对于本测试文件所在的包目录）。
const goldenPath = "testdata/retrieval_golden.json"

// embedDim 与 sqlite-vec 虚拟表的固定维度一致（vec_chunks embedding float[2048]）。
const embedDim = 2048

// retrievalTopK 每条 query 收集的 top-K chunk_id 数量上限。
// 主干 rerank fallback 截断到 top-5，这里取一致的上限。
const retrievalTopK = 5

// ensureHeadlessAIServiceOnce 保证 headless aiservice 单例已安装。
//
// service 包的外部测试包 service_test（sales_rag_test.go）已有一个 TestMain
// 会 SetDefault 一个 headless gateway，且它在所有 build tag 组合下都会编译进同一
// 个测试二进制 —— 因此正常情况下 aiservice.Default() 已可用。
//
// 但同一个测试二进制只能有一个 TestMain（service 与 service_test 共享同一 binary），
// 故本 integration 文件【不再声明 TestMain】以避免与 sales_rag_test.go 的 TestMain
// 重复定义。作为防御（万一独立运行 / tag 组合变化导致单例未设），这里用 sync.Once
// 探测并按需安装一个等价的 headless gateway（SetDefault 仅存指针，幂等无副作用）。
var ensureHeadlessAIServiceOnce sync.Once

func ensureHeadlessAIService() {
	ensureHeadlessAIServiceOnce.Do(func() {
		if defaultGatewaySet() {
			return
		}
		gw := aiservice.Build(aiservice.Deps{}) // 无 DB、无 provider：AI 调用返回 error 而非 panic
		aiservice.SetDefault(gw)
	})
}

// defaultGatewaySet 安全探测 aiservice 单例是否已安装（Default() 未设时会 panic）。
func defaultGatewaySet() (set bool) {
	defer func() {
		if recover() != nil {
			set = false
		}
	}()
	_ = aiservice.Default()
	return true
}

// deterministicEmbedder 是一个离线、确定性的 fake embedder。
//
// 确定性：同一 text → 同一向量（纯 sha256 派生，无随机源、无时间、无外部调用）。
// 区分度：以多个不同盐值（counter）对 text 做 sha256，把 8 字节块解释为 uint64，
// 映射到 [-1,1] 填满 embedDim 维，再做 L2 归一化。不同文本因 hash 雪崩效应得到
// 显著不同的向量方向，cosine 相似度有良好区分度；相同文本得到完全相同向量。
//
// 禁止调用真实 aiservice.Embed（要求离线确定性）。
func deterministicEmbedder(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, embedDim)
	// 每个 sha256 输出 32 字节 → 4 个 uint64（8 字节一组）。
	// 需要 embedDim 个 float，故循环 ceil(embedDim/4) 个 block。
	const floatsPerBlock = 32 / 8 // = 4
	idx := 0
	for block := 0; idx < embedDim; block++ {
		h := sha256.New()
		// 盐 = block 序号（小端 8 字节），保证每个 block 的 hash 互不相同。
		var salt [8]byte
		binary.LittleEndian.PutUint64(salt[:], uint64(block))
		_, _ = h.Write(salt[:])
		_, _ = h.Write([]byte(text))
		sum := h.Sum(nil) // 32 字节
		for j := 0; j < floatsPerBlock && idx < embedDim; j++ {
			u := binary.LittleEndian.Uint64(sum[j*8 : j*8+8])
			// 映射 uint64 → [-1, 1)
			vec[idx] = float32(u)/float32(math.MaxUint64)*2 - 1
			idx++
		}
	}

	// L2 归一化（cosine 度量下方向决定相似度，归一化使距离稳定且良态）。
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		inv := float32(1.0 / norm)
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec, nil
}

// goldenCorpus 是固定合成语料：~6 篇短文档，每篇切成若干 chunk，主题各异。
// 直接构造 []domain.KnowledgeChunk（不带 Vector，由 Upsert 调 fake embedder 生成）。
// 全部归属 userID=1，DocumentID 与文档对应。
func goldenCorpus() []domain.KnowledgeChunk {
	return []domain.KnowledgeChunk{
		// 文档 1：产品定价
		{ID: "d1c1", DocumentID: 1, UserID: 1, Content: "我们的旗舰版年费定价为每年 5000 元，包含全部高级功能。"},
		{ID: "d1c2", DocumentID: 1, UserID: 1, Content: "基础版每月 99 元，适合小团队入门使用。"},
		{ID: "d1c3", DocumentID: 1, UserID: 1, Content: "企业版采用阶梯报价，按席位数量批量优惠，详情联系销售。"},

		// 文档 2：竞品对比
		{ID: "d2c1", DocumentID: 2, UserID: 1, Content: "相比竞品 A，我们在数据安全合规方面通过了更多认证。"},
		{ID: "d2c2", DocumentID: 2, UserID: 1, Content: "竞品 B 的优势在于价格更低，但缺少自动化工作流能力。"},
		{ID: "d2c3", DocumentID: 2, UserID: 1, Content: "与同类产品对比，我们的核心差异是端到端的私有化部署支持。"},

		// 文档 3：技术参数
		{ID: "d3c1", DocumentID: 3, UserID: 1, Content: "系统支持每秒处理一万条并发请求，平均延迟低于 50 毫秒。"},
		{ID: "d3c2", DocumentID: 3, UserID: 1, Content: "支持私有化部署到客户内网，兼容主流国产化操作系统。"},
		{ID: "d3c3", DocumentID: 3, UserID: 1, Content: "提供完整的 REST API 与 Webhook 集成能力，方便二次开发。"},

		// 文档 4：客户成功案例
		{ID: "d4c1", DocumentID: 4, UserID: 1, Content: "某零售连锁客户上线后，门店运营效率提升了百分之三十。"},
		{ID: "d4c2", DocumentID: 4, UserID: 1, Content: "一家制造业客户通过我们的方案将质检流程自动化，节省了大量人力。"},
		{ID: "d4c3", DocumentID: 4, UserID: 1, Content: "金融行业头部客户采用我们的合规审计模块，顺利通过监管检查。"},

		// 文档 5：售后服务与 FAQ
		{ID: "d5c1", DocumentID: 5, UserID: 1, Content: "我们提供 7x24 小时的技术支持热线和专属客户成功经理。"},
		{ID: "d5c2", DocumentID: 5, UserID: 1, Content: "合同签订后通常在三个工作日内完成账号开通与初始化配置。"},
		{ID: "d5c3", DocumentID: 5, UserID: 1, Content: "如对服务不满意，签约首月内支持无理由全额退款。"},

		// 文档 6：异议处理话术
		{ID: "d6c1", DocumentID: 6, UserID: 1, Content: "当客户说太贵时，先认同感受再用 ROI 测算重新锚定价值。"},
		{ID: "d6c2", DocumentID: 6, UserID: 1, Content: "面对暂时不需要的异议，挖掘客户当前痛点并放大未来风险。"},
		{ID: "d6c3", DocumentID: 6, UserID: 1, Content: "客户犹豫时，提供限时优惠和成功案例增强其决策信心。"},
	}
}

// goldenDocIDs 返回语料覆盖的全部文档 ID（去重升序），用作检索的 docIDs 白名单。
func goldenDocIDs() []uint {
	return []uint{1, 2, 3, 4, 5, 6}
}

// goldenQueries 是固定 query 集（~10 条），覆盖不同主题以便区分命中。
// 硬编码于此（确定性，不依赖外部）。
func goldenQueries() []string {
	return []string{
		"旗舰版一年多少钱",
		"基础版的月费价格",
		"和竞品相比有什么优势",
		"系统的并发性能和延迟",
		"能不能私有化部署到内网",
		"有没有零售行业的成功案例",
		"售后服务和技术支持怎么样",
		"客户说太贵了怎么回应",
		"签约后多久能开通账号",
		"对服务不满意可以退款吗",
	}
}

// TestRetrievalGolden 是 exact-match golden harness 的主测试。
func TestRetrievalGolden(t *testing.T) {
	ensureHeadlessAIService()

	ctx := context.Background()

	// 1. 临时 sqlite-vec store（t.TempDir 自动清理目录；显式 Close 释放连接）。
	dbPath := filepath.Join(t.TempDir(), "golden.db")
	store, err := radapter.NewSQLiteVecStore(dbPath, deterministicEmbedder)
	if err != nil {
		t.Fatalf("NewSQLiteVecStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 2. 写入固定合成语料（Upsert 用 fake embedder 计算向量）。
	corpus := goldenCorpus()
	if err := store.Upsert(ctx, corpus); err != nil {
		t.Fatalf("Upsert corpus: %v", err)
	}

	// 3. 构造 SalesRAGService：RegexRouter（确定性、零 LLM）+ free 模式。
	//    free 模式 + opinionDocIDs=nil → 只走通用检索主干（无 strategy / opinion 分支）。
	router := adapter.NewRegexRouter()
	svc := NewSalesRAGService(store, router)

	docIDs := goldenDocIDs()
	queries := goldenQueries()

	// 4. 跑检索，收集 query → 有序 chunk_id 列表。
	got := make(map[string][]string, len(queries))
	for _, q := range queries {
		ids, rerr := runRetrieval(t, ctx, svc, q, docIDs)
		if rerr != nil {
			t.Fatalf("runRetrieval(%q): %v", q, rerr)
		}
		got[q] = ids
	}

	// 5. 捕获模式 vs 比对模式。
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeGolden(t, got)
		t.Logf("UPDATE_GOLDEN=1: wrote golden fixture to %s (%d queries)", goldenPath, len(got))
		return
	}

	want := readGolden(t)
	compareGolden(t, want, got)
}

// runRetrieval 跑一次检索主干并返回按 (Score desc, chunk.ID asc) 稳定排序后的
// 前 retrievalTopK 个 chunk_id。
//
// 调用 RetrieveForResponseV2(ctx, query, docIDs, opinionDocIDs=nil, history=nil,
// chatMode="free", userID=1, onStatus=nil)。
func runRetrieval(
	t *testing.T,
	ctx context.Context,
	svc *SalesRAGService,
	query string,
	docIDs []uint,
) ([]string, error) {
	t.Helper()

	verdict, err := svc.RetrieveForResponseV2(
		ctx,
		query,
		docIDs,
		nil,    // opinionDocIDs — 不走观点库独立通道
		nil,    // history
		"free", // chatMode — 仅通用主干（无 strategy）
		1,      // userID — 与语料一致
		nil,    // onStatus
	)
	if err != nil {
		return nil, err
	}

	evidence := verdict.Evidence
	// 稳定全序：先按 Score 降序，Score 相等再按 chunk.ID 升序（消除任何 tie 的不确定性）。
	sort.SliceStable(evidence, func(i, j int) bool {
		if evidence[i].Score != evidence[j].Score {
			return evidence[i].Score > evidence[j].Score
		}
		return evidence[i].ID < evidence[j].ID
	})

	ids := make([]string, 0, len(evidence))
	for i, ch := range evidence {
		if i >= retrievalTopK {
			break
		}
		ids = append(ids, ch.ID)
	}
	return ids, nil
}

// writeGolden 把当前检索结果写入 golden fixture（捕获模式）。
func writeGolden(t *testing.T, got map[string][]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(goldenPath, data, 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

// readGolden 读取并解析 golden fixture（比对模式）。
func readGolden(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (%s): %v — 先用 UPDATE_GOLDEN=1 生成", goldenPath, err)
	}
	var want map[string][]string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	return want
}

// compareGolden 逐 query 比对 want 与 got，不一致则 t.Errorf 列出 diff。
func compareGolden(t *testing.T, want, got map[string][]string) {
	t.Helper()

	// 覆盖性检查：query 集发生增减时显式报错（防止漏测 / golden 陈旧）。
	if len(want) != len(got) {
		t.Errorf("golden query 数量不一致: want=%d got=%d", len(want), len(got))
	}

	// 稳定顺序遍历（按 query 排序）以获得确定性的失败输出。
	queries := make([]string, 0, len(got))
	for q := range got {
		queries = append(queries, q)
	}
	sort.Strings(queries)

	for _, q := range queries {
		w, ok := want[q]
		if !ok {
			t.Errorf("query %q 在 golden 中缺失（新增 query？需 UPDATE_GOLDEN=1 重新捕获）", q)
			continue
		}
		g := got[q]
		if !reflect.DeepEqual(w, g) {
			t.Errorf("query %q 检索结果与 golden 不一致:\n  golden: %v\n  actual: %v", q, w, g)
		}
	}

	// 反向检查 golden 中存在但本次未产出的 query。
	for q := range want {
		if _, ok := got[q]; !ok {
			t.Errorf("query %q 仅存在于 golden（query 集被删除？）", q)
		}
	}
}
