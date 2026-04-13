package service

import (
	"context"
	"sync"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/log"
)

// StrategyService 策略服务
// 负责加载策略数据并进行双层路由
type StrategyService struct {
	router     *adapter.StrategyRouter
	metas      []domain.MetaStrategy
	basics     []domain.BasicStrategy
	metaMap    map[string]domain.MetaStrategy    // ID -> MetaStrategy
	basicMap   map[string]domain.BasicStrategy   // ID -> BasicStrategy
	metaBasics map[string][]domain.BasicStrategy // MetaID -> 该综合策略下的所有基础策略
	once       sync.Once
}

// NewStrategyService 创建新的策略服务
func NewStrategyService() *StrategyService {
	svc := &StrategyService{
		router:     adapter.NewStrategyRouter(),
		metaMap:    make(map[string]domain.MetaStrategy),
		basicMap:   make(map[string]domain.BasicStrategy),
		metaBasics: make(map[string][]domain.BasicStrategy),
	}
	svc.loadData()
	return svc
}

// loadData 加载策略数据到内存
func (s *StrategyService) loadData() {
	s.once.Do(func() {
		s.metas, s.basics = LoadStrategies()

		// 尝试从指定目录加载策略内容（覆盖默认内容）
		// TODO: 路径应从配置读取
		strategyDir := "/Users/zhiyuchen/Desktop/莫小派/Codes/基础策略"
		LoadStrategyContentsFromDir(strategyDir, s.basics)

		// 构建索引
		for _, m := range s.metas {
			s.metaMap[m.ID] = m
		}
		for _, b := range s.basics {
			s.basicMap[b.ID] = b
			s.metaBasics[b.MetaID] = append(s.metaBasics[b.MetaID], b)
		}
	})
}

// DetermineStrategy 执行双层路由，返回最终选定的策略
// 流程：用户输入 -> 选择综合策略 -> 选择基础策略 -> 返回完整策略内容
func (s *StrategyService) DetermineStrategy(ctx context.Context, query string, history []string) (*domain.BasicStrategy, error) {
	// Stage 1: 选择综合策略
	metaID, err := s.router.SelectMetaStrategy(ctx, query, history, s.metas)
	if err != nil {
		log.C(ctx).Warnw("Failed to select meta strategy", "error", err)
		// Fallback: 使用第一个综合策略
		if len(s.metas) > 0 {
			metaID = s.metas[0].ID
		}
	}

	log.C(ctx).Infow("Meta strategy determined", "meta_id", metaID)

	// Stage 2: 获取该综合策略下的基础策略
	basics := s.GetBasicsByMetaID(metaID)
	if len(basics) == 0 {
		log.C(ctx).Warnw("No basic strategies found for meta", "meta_id", metaID)
		// Fallback: 返回全局第一个基础策略
		if len(s.basics) > 0 {
			result := s.basics[0]
			return &result, nil
		}
		return nil, nil
	}

	// Stage 3: 从基础策略中选择最匹配的一个
	// 获取当前综合策略的决策树逻辑
	decisionTree := ""
	if meta, ok := s.metaMap[metaID]; ok {
		decisionTree = meta.DecisionTree
	}

	basicID, err := s.router.SelectBasicStrategy(ctx, query, history, decisionTree, basics)
	if err != nil {
		log.C(ctx).Warnw("Failed to select basic strategy", "error", err)
		basicID = basics[0].ID
	}

	log.C(ctx).Infow("Basic strategy determined", "basic_id", basicID)

	// 返回完整的基础策略
	if strategy, exists := s.basicMap[basicID]; exists {
		return &strategy, nil
	}

	// 如果找不到，返回该综合策略下的第一个
	result := basics[0]
	return &result, nil
}

// GetBasicsByMetaID 获取指定综合策略下的所有基础策略
func (s *StrategyService) GetBasicsByMetaID(metaID string) []domain.BasicStrategy {
	return s.metaBasics[metaID]
}

// GetAllMetas 获取所有综合策略
func (s *StrategyService) GetAllMetas() []domain.MetaStrategy {
	return s.metas
}

// GetAllBasics 获取所有基础策略
func (s *StrategyService) GetAllBasics() []domain.BasicStrategy {
	return s.basics
}

// GetMetaByID 根据ID获取综合策略
func (s *StrategyService) GetMetaByID(id string) (domain.MetaStrategy, bool) {
	m, ok := s.metaMap[id]
	return m, ok
}

// GetBasicByID 根据ID获取基础策略
func (s *StrategyService) GetBasicByID(id string) (domain.BasicStrategy, bool) {
	b, ok := s.basicMap[id]
	return b, ok
}
