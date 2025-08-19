package card

import (
	"fmt"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

// CardRendererCoordinator 卡片渲染协调器
// 整合所有渲染功能，提供统一的接口
type CardRendererCoordinator struct {
	config                   *pagination.PaginationConfig
	enhancedRenderer         *EnhancedCardRenderer
	superLongImageProcessor  *SuperLongImageProcessor
	preciseMeasurementEngine *PreciseMeasurementEngine
}

// NewCardRendererCoordinator 创建新的卡片渲染协调器
func NewCardRendererCoordinator(config *pagination.PaginationConfig) *CardRendererCoordinator {
	return &CardRendererCoordinator{
		config:                   config,
		enhancedRenderer:         NewEnhancedCardRenderer(config),
		superLongImageProcessor:  NewSuperLongImageProcessor(config),
		preciseMeasurementEngine: NewPreciseMeasurementEngine(config),
	}
}

// RenderingStrategy 渲染策略
type RenderingStrategy int

const (
	// StrategyEnhanced 增强渲染策略（逐张渲染）
	StrategyEnhanced RenderingStrategy = iota
	// StrategySuperLongImage 超长图渲染策略（先拼接后切分）
	StrategySuperLongImage
	// StrategyPreciseMeasurement 精确测量策略（先测量后渲染）
	StrategyPreciseMeasurement
)

// RenderBookOptions 渲染选项
type RenderBookOptions struct {
	Strategy           RenderingStrategy `json:"strategy"`
	EnableMeasurement  bool              `json:"enable_measurement"`
	EnableSuperLong    bool              `json:"enable_super_long"`
	EnableOptimization bool              `json:"enable_optimization"`
	DebugMode          bool              `json:"debug_mode"`
}

// RenderBookWithStrategy 根据策略渲染整本书
func (c *CardRendererCoordinator) RenderBookWithStrategy(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
	options RenderBookOptions,
) ([]*RenderedCard, error) {
	startTime := time.Now()
	fmt.Printf("🚀 开始卡片渲染协调，策略: %d, 书籍: %s\n", options.Strategy, book.Title)

	var renderedCards []*RenderedCard
	var err error

	switch options.Strategy {
	case StrategyEnhanced:
		renderedCards, err = c.renderWithEnhancedStrategy(book, structuredTextArray, imagePromptURL, options)
	case StrategySuperLongImage:
		renderedCards, err = c.renderWithSuperLongImageStrategy(book, structuredTextArray, imagePromptURL, options)
	case StrategyPreciseMeasurement:
		renderedCards, err = c.renderWithPreciseMeasurementStrategy(book, structuredTextArray, imagePromptURL, options)
	default:
		return nil, fmt.Errorf("不支持的渲染策略: %d", options.Strategy)
	}

	if err != nil {
		return nil, fmt.Errorf("渲染失败: %v", err)
	}

	duration := time.Since(startTime)
	fmt.Printf("✅ 卡片渲染协调完成，耗时: %v, 生成卡片数: %d\n", duration, len(renderedCards))

	// 后处理：验证和优化
	if options.EnableOptimization {
		renderedCards = c.optimizeRenderedCards(renderedCards)
	}

	return renderedCards, nil
}

// renderWithEnhancedStrategy 使用增强渲染策略
func (c *CardRendererCoordinator) renderWithEnhancedStrategy(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
	options RenderBookOptions,
) ([]*RenderedCard, error) {
	fmt.Printf("🎨 使用增强渲染策略\n")

	if options.EnableMeasurement {
		// 先进行精确测量
		measurements, err := c.preciseMeasurementEngine.MeasureAllElements(structuredTextArray, imagePromptURL)
		if err != nil {
			fmt.Printf("⚠️ 精确测量失败，继续使用估算: %v\n", err)
		} else {
			fmt.Printf("📏 精确测量完成，共 %d 个测量结果\n", len(measurements))
		}
	}

	// 使用增强渲染器进行渲染
	return c.enhancedRenderer.RenderBookWithPagination(book, structuredTextArray, imagePromptURL)
}

// renderWithSuperLongImageStrategy 使用超长图渲染策略
func (c *CardRendererCoordinator) renderWithSuperLongImageStrategy(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
	options RenderBookOptions,
) ([]*RenderedCard, error) {
	fmt.Printf("📏 使用超长图渲染策略\n")

	// 使用超长图处理器进行渲染
	return c.superLongImageProcessor.ProcessBookAsSuperLongImage(book, structuredTextArray, imagePromptURL)
}

// renderWithPreciseMeasurementStrategy 使用精确测量渲染策略
func (c *CardRendererCoordinator) renderWithPreciseMeasurementStrategy(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
	options RenderBookOptions,
) ([]*RenderedCard, error) {
	fmt.Printf("🔬 使用精确测量渲染策略\n")

	// 1. 精确测量所有元素
	measurements, err := c.preciseMeasurementEngine.MeasureAllElements(structuredTextArray, imagePromptURL)
	if err != nil {
		return nil, fmt.Errorf("精确测量失败: %v", err)
	}

	// 2. 基于测量结果进行优化分页
	optimizedPages, err := c.preciseMeasurementEngine.OptimizePagination(measurements)
	if err != nil {
		return nil, fmt.Errorf("优化分页失败: %v", err)
	}

	// 3. 根据优化结果进行渲染
	return c.preciseMeasurementEngine.RenderOptimizedPages(book, optimizedPages, imagePromptURL)
}

// optimizeRenderedCards 优化渲染结果
func (c *CardRendererCoordinator) optimizeRenderedCards(cards []*RenderedCard) []*RenderedCard {
	fmt.Printf("🔧 开始优化渲染结果\n")

	// 检查和修复排序
	for i, card := range cards {
		if card.SortOrder != i+1 {
			fmt.Printf("🔧 修复卡片排序：%d -> %d\n", card.SortOrder, i+1)
			card.SortOrder = i + 1
		}
	}

	// 检查尺寸一致性
	for _, card := range cards {
		if card.Width != 1080 || card.Height != 1440 {
			fmt.Printf("⚠️ 检测到非标准尺寸卡片：%dx%d\n", card.Width, card.Height)
		}
	}

	fmt.Printf("✅ 渲染结果优化完成\n")
	return cards
}

// GetOptimalStrategy 根据内容特征推荐最佳渲染策略
func (c *CardRendererCoordinator) GetOptimalStrategy(
	structuredTextArray []pagination.Element,
) (RenderingStrategy, RenderBookOptions) {
	elementCount := len(structuredTextArray)

	// 估算内容复杂度
	complexityScore := c.calculateContentComplexity(structuredTextArray)

	fmt.Printf("📊 内容分析 - 元素数: %d, 复杂度: %.2f\n", elementCount, complexityScore)

	var strategy RenderingStrategy
	options := RenderBookOptions{
		EnableMeasurement:  true,
		EnableOptimization: true,
		DebugMode:          false,
	}

	if elementCount <= 5 && complexityScore < 0.3 {
		// 简单内容，使用增强策略
		strategy = StrategyEnhanced
		options.EnableMeasurement = false
		fmt.Printf("💡 推荐策略：增强渲染（简单内容）\n")
	} else if elementCount > 20 || complexityScore > 0.7 {
		// 复杂内容，使用超长图策略
		strategy = StrategySuperLongImage
		options.EnableSuperLong = true
		fmt.Printf("💡 推荐策略：超长图渲染（复杂内容）\n")
	} else {
		// 中等复杂度，使用精确测量策略
		strategy = StrategyPreciseMeasurement
		options.EnableMeasurement = true
		fmt.Printf("💡 推荐策略：精确测量渲染（中等复杂度）\n")
	}

	options.Strategy = strategy
	return strategy, options
}

// calculateContentComplexity 计算内容复杂度
func (c *CardRendererCoordinator) calculateContentComplexity(elements []pagination.Element) float64 {
	if len(elements) == 0 {
		return 0.0
	}

	var totalComplexity float64

	for _, element := range elements {
		var elementComplexity float64

		switch element.Type {
		case pagination.ElementTypeTitle:
			elementComplexity = 0.1
		case pagination.ElementTypeSubtitle:
			elementComplexity = 0.2
		case pagination.ElementTypeBody:
			// 基于内容长度计算复杂度
			contentLen := len(fmt.Sprintf("%v", element.Content))
			elementComplexity = float64(contentLen) / 1000.0
			if elementComplexity > 1.0 {
				elementComplexity = 1.0
			}
		case pagination.ElementTypeList:
			// 基于列表项数量计算复杂度
			if items, ok := element.Content.([]string); ok {
				elementComplexity = float64(len(items)) * 0.1
			} else {
				elementComplexity = 0.3
			}
		case pagination.ElementTypeQuote:
			elementComplexity = 0.4
		default:
			elementComplexity = 0.2
		}

		totalComplexity += elementComplexity
	}

	// 归一化复杂度分数
	avgComplexity := totalComplexity / float64(len(elements))
	if avgComplexity > 1.0 {
		avgComplexity = 1.0
	}

	return avgComplexity
}

// ValidateRenderingResult 验证渲染结果
func (c *CardRendererCoordinator) ValidateRenderingResult(cards []*RenderedCard) error {
	if len(cards) == 0 {
		return fmt.Errorf("渲染结果为空")
	}

	// 检查排序连续性
	for i, card := range cards {
		expectedOrder := i + 1
		if card.SortOrder != expectedOrder {
			return fmt.Errorf("卡片排序错误：期望 %d，实际 %d", expectedOrder, card.SortOrder)
		}
	}

	// 检查尺寸一致性
	for i, card := range cards {
		if card.Width != 1080 || card.Height != 1440 {
			return fmt.Errorf("卡片 %d 尺寸错误：%dx%d（期望 1080x1440）", i+1, card.Width, card.Height)
		}
	}

	// 检查图片URL有效性
	for i, card := range cards {
		if card.ImageURL == "" {
			return fmt.Errorf("卡片 %d 图片URL为空", i+1)
		}
	}

	fmt.Printf("✅ 渲染结果验证通过，共 %d 张卡片\n", len(cards))
	return nil
}
