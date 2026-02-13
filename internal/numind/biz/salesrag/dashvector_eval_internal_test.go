package salesrag

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/biz/salesrag/service"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/suite"
)

type DashVectorAuditSuite struct {
	suite.Suite
	ctx        context.Context
	ragEx      *service.SalesRAGService
	aliBiz     ali.AliBiz
	store      port.VectorStore
	testUserID uint
}

type CorpusItem struct {
	DocID string `json:"docid"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (s *DashVectorAuditSuite) SetupSuite() {
	s.ctx = context.Background()
	s.testUserID = 999999

	viper.SetConfigFile("/Users/zhiyuchen/Desktop/莫小派/Codes/numind-server/config_dev.yaml")
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Warning: failed to read config: %v\n", err)
	}

	viper.Set("ali.text.timeout", "300s")
	s.aliBiz = ali.NewAliBiz(nil)

	embedder := func(ctx context.Context, text string) ([]float32, error) {
		return s.aliBiz.QianwenEmbedding(text)
	}

	endpoint := viper.GetString("ali.dashvector.endpoint")
	apiKey := viper.GetString("ali.dashvector.api_key")
	collection := viper.GetString("ali.dashvector.collection")

	s.store = adapter.NewDashVectorStore(endpoint, apiKey, collection, embedder)
	llmRouter := adapter.NewLLMRouter()
	s.ragEx = service.NewSalesRAGService(s.store, llmRouter)
}

func (s *DashVectorAuditSuite) TestFullRAGAudit() {
	t := s.T()

	corpusPath := "/Users/zhiyuchen/Desktop/莫小派/Codes/corpus.jsonl/corpus.jsonl"
	topicsPath := "/Users/zhiyuchen/Desktop/莫小派/Codes/corpus.jsonl/topics/test.relevant.tsv"
	qrelsPath := "/Users/zhiyuchen/Desktop/莫小派/Codes/corpus.jsonl/qrels/test.relevant.tsv"

	qrels, _ := s.loadQrels(qrelsPath)
	queries, _ := s.loadTopics(topicsPath)

	targetDocIDs := make(map[string]bool)
	evalQueries := make(map[string]string)
	sampleLimit := 100
	qCount := 0
	for qID, qText := range queries {
		if docs, ok := qrels[qID]; ok {
			evalQueries[qID] = qText
			for _, dID := range docs {
				targetDocIDs[dID] = true
			}
			qCount++
			if qCount >= sampleLimit {
				break
			}
		}
	}

	t.Logf("[AUDIT] 准备精准导入目标文档: %d, 审计采样规模: %d", len(targetDocIDs), len(evalQueries))
	s.importAuditData(corpusPath, targetDocIDs)

	t.Log("[AUDIT] 等待 15s 确保索引强一致性...")
	time.Sleep(15 * time.Second)

	allDocIDs := []uint{}
	for dIDStr := range targetDocIDs {
		var u uint
		fmt.Sscanf(dIDStr, "%d", &u)
		allDocIDs = append(allDocIDs, u)
	}

	var totalRecall float64
	var totalRR float64
	var latencies []time.Duration
	var count int

	for qID, question := range evalQueries {
		expected := qrels[qID]
		startTime := time.Now()

		testCtx := context.WithValue(s.ctx, "userID", s.testUserID)
		verdict, err := s.ragEx.RetrieveForResponseV2(testCtx, question, allDocIDs, nil, "sales", s.testUserID, nil)
		if err != nil {
			t.Logf("[AUDIT] ERROR: Query %s failed: %v", qID, err)
			continue
		}

		duration := time.Since(startTime)
		latencies = append(latencies, duration)

		hit := 0
		rr := 0.0
		foundIDs := []string{}
		for rank, chunk := range verdict.Evidence {
			docIDStr := fmt.Sprintf("%d", chunk.DocumentID)
			foundIDs = append(foundIDs, docIDStr)

			for _, expID := range expected {
				if docIDStr == expID {
					if hit == 0 {
						hit = 1
						rr = 1.0 / float64(rank+1)
					}
					break
				}
			}
		}

		totalRecall += float64(hit)
		totalRR += rr
		count++

		t.Logf("[AUDIT][%d] QID: %s | Recall: %d | RR: %.2f | Latency: %v | Found: %v", count, qID, hit, rr, duration, foundIDs)
	}

	if count > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)*5/10]
		p99 := latencies[len(latencies)*99/100]

		t.Logf("================ [AUDIT REPORT - FINAL METRICS] ================")
		t.Logf("Audit Timestamp: %s", time.Now().Format("2006-01-02 15:04:05"))
		t.Logf("Total Queries:   %d", count)
		t.Logf("Recall@TopK:     %.4f", totalRecall/float64(count))
		t.Logf("MRR:             %.4f", totalRR/float64(count))
		t.Logf("P50 Latency:     %v", p50)
		t.Logf("P99 Latency:     %v", p99)
		t.Logf("================================================================")
	}
}

func (s *DashVectorAuditSuite) importAuditData(path string, targets map[string]bool) {
	file, _ := os.Open(path)
	defer file.Close()
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024*1024)
	scanner.Buffer(buf, 64*1024*1024)

	for scanner.Scan() {
		var item CorpusItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			continue
		}
		docIDStr := strings.Split(item.DocID, "#")[0]
		if !targets[docIDStr] {
			continue
		}

		var dID uint
		fmt.Sscanf(docIDStr, "%d", &dID)
		chunk := domain.KnowledgeChunk{
			ID: item.DocID, DocumentID: dID, UserID: s.testUserID,
			Content: fmt.Sprintf("【标题】%s\n【正文】%s", item.Title, item.Text),
			Summary: item.Title, Tags: []string{"audit_2026"},
		}
		s.store.Upsert(s.ctx, []domain.KnowledgeChunk{chunk})
	}
}

func (s *DashVectorAuditSuite) loadTopics(path string) (map[string]string, error) {
	file, _ := os.Open(path)
	defer file.Close()
	topics := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			topics[parts[0]] = parts[1]
		}
	}
	return topics, nil
}

func (s *DashVectorAuditSuite) loadQrels(path string) (map[string][]string, error) {
	file, _ := os.Open(path)
	defer file.Close()
	qrels := make(map[string][]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "\t")
		if len(parts) >= 4 && (parts[3] == "1" || parts[3] == "2") {
			qID := parts[0]
			docID := strings.Split(parts[2], "#")[0]
			qrels[qID] = append(qrels[qID], docID)
		}
	}
	return qrels, nil
}

func TestDashVectorAuditSuite(t *testing.T) {
	suite.Run(t, new(DashVectorAuditSuite))
}
