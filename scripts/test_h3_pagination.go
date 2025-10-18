package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"

	card "numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/util"
)

// Example markdown containing multiple H3 headings to verify typography and pagination
const sampleMarkdown = `# 封面标题（仅封面显示）

## 二级标题：章节一

### 三级标题：要点 A
这里是一段较长的正文内容，用于模拟真实文章。为了测试分页效果，我们需要足够多的文本来填充页面。请注意，这些文本会用于验证 h3 的字体大小、行高以及段后距是否与配置一致。

### 三级标题：要点 B
继续添加更多的正文内容，确保分页会在段间和段内进行合理分割。三级标题应该与副标题保持一致的风格，除非在配置中单独覆盖。

### 三级标题：要点 C
为了更全面地测试，我们再添加一段内容。这段内容应该足以跨越多个页面，以验证 FlowRenderer 的分页逻辑在 h3 标题与段落之间的衔接是否自然。

## 二级标题：章节二

### 三级标题：要点 D
最后一段测试文本，确保在不同页面边界附近也能保持正确的样式与分页行为。`

func main() {
	// 1) Load config.yaml (local/dev/qa/prod) - default to current directory
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	_ = viper.ReadInConfig() // best-effort; rely on defaults if not found

	// 2) Initialize logger minimally
	log.Init(&log.Options{Level: "debug", Format: "console", OutputPaths: []string{"stdout"}})
	defer log.Sync()

	// 3) Build FlowRenderer (loads pagination + typography via viper)
	renderer := card.NewFlowRenderer(pagination.LoadConfigFromViper())

	// 4) Paginate sample markdown
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pages, err := renderer.PaginateMarkdownWithBackground(ctx, sampleMarkdown, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "paginate failed: %v\n", err)
		os.Exit(1)
	}

	// 5) Render each page to image
	outDir := filepath.Join("res", "upload", "card", "h3_test")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output dir: %v\n", err)
		os.Exit(1)
	}

	wk := util.NewWkhtmltoimageRenderer(util.DefaultConfig())
	for i, inner := range pages {
		// wrap into full page html skeleton using the same CSS generator
		cfg := pagination.LoadConfigFromViper()
		html := card.GenerateUnifiedCSS(cfg, "")
		pageHTML := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>%s</style></head>
<body><div class="page"><div class="content">%s</div></div></body></html>`, html, inner)

		outPath := filepath.Join(outDir, fmt.Sprintf("h3_page_%02d.webp", i+1))
		if err := wk.RenderHTMLStringToImage(ctx, pageHTML, outPath); err != nil {
			fmt.Fprintf(os.Stderr, "render page %d failed: %v\n", i+1, err)
			os.Exit(1)
		}
		fmt.Printf("✅ rendered %s\n", outPath)
	}

	fmt.Printf("Done. Generated %d pages under %s\n", len(pages), outDir)
}
