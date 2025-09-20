package card

import (
	"fmt"
	"html/template"
	"strings"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

// OptimizedHTMLTemplate 优化的HTML模板生成器
// 专门针对wkhtmltoimage进行优化，确保样式兼容性和渲染准确性
type OptimizedHTMLTemplate struct {
	config *pagination.PaginationConfig
}

// NewOptimizedHTMLTemplate 创建优化的HTML模板生成器
func NewOptimizedHTMLTemplate(config *pagination.PaginationConfig) *OptimizedHTMLTemplate {
	return &OptimizedHTMLTemplate{
		config: config,
	}
}

// GenerateFullBookHTML 生成完整书籍HTML，优化用于wkhtmltoimage
func (t *OptimizedHTMLTemplate) GenerateFullBookHTML(book *model.BookM, cards []*model.CardM) (string, error) {
	templateData := struct {
		Book   *model.BookM
		Cards  []*model.CardM
		Config *pagination.PaginationConfig
	}{
		Book:   book,
		Cards:  cards,
		Config: t.config,
	}

	tmpl, err := template.New("optimized_book").Funcs(template.FuncMap{
		"escapeHTML": func(s string) string {
			return t.escapeHTML(s)
		},
		"formatContent": func(content interface{}) string {
			return fmt.Sprintf("%v", content)
		},
		"splitList": func(content interface{}) []string {
			if str, ok := content.(string); ok {
				items := strings.Split(str, "\n")
				var result []string
				for _, item := range items {
					if trimmed := strings.TrimSpace(item); trimmed != "" {
						result = append(result, trimmed)
					}
				}
				return result
			}
			if list, ok := content.([]interface{}); ok {
				var result []string
				for _, item := range list {
					result = append(result, fmt.Sprintf("%v", item))
				}
				return result
			}
			return []string{}
		},
	}).Parse(t.getOptimizedHTMLTemplate())

	if err != nil {
		return "", fmt.Errorf("解析HTML模板失败: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return "", fmt.Errorf("执行HTML模板失败: %v", err)
	}

	return buf.String(), nil
}

// getOptimizedHTMLTemplate 获取优化的HTML模板
func (t *OptimizedHTMLTemplate) getOptimizedHTMLTemplate() string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Book.Title}}</title>
    <style>
        /* 重置样式 - 确保跨平台一致性 */
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        /* 思源宋体字体定义 - 容器环境优化 */
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf") format("opentype"),
                 local("Source Han Serif SC"),
                 local("SourceHanSerifSC"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei"),
                 local("sans-serif");
            font-weight: normal;
            font-style: normal;
        }
        
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf") format("opentype"),
                 local("Source Han Serif SC Bold"),
                 local("SourceHanSerifSC-Bold"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC Semibold"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei Bold"),
                 local("sans-serif");
            font-weight: bold;
            font-style: normal;
        }
        
        
        /* 根元素配置 */
        html {
            font-size: 16px;  /* 基础字体大小 */
            line-height: 1.6; /* 基础行高 */
        }
        
        body {
            font-family: "SourceHanSerifSC", "STFangsong", "PingFang SC", "Helvetica Neue", Arial, sans-serif;
            background: #ffffff;
            color: #333333;
            line-height: 1.6;
            width: {{.Config.Card.Width}}px;
            margin: 0;
            padding: 0;
            font-feature-settings: "kern" 1;  /* 启用字间距调整 */
            text-rendering: optimizeLegibility; /* 优化文本渲染 */
            -webkit-font-smoothing: antialiased; /* 抗锯齿 */
            -moz-osx-font-smoothing: grayscale;
        }
        
        /* 卡片容器 - 针对wkhtmltoimage优化 */
        .card-container {
            width: {{.Config.Card.Width}}px;
            min-height: {{.Config.Card.Height}}px;
            padding: {{.Config.Card.Padding.Top}}px {{.Config.Card.Padding.Right}}px {{.Config.Card.Padding.Bottom}}px {{.Config.Card.Padding.Left}}px;
            background: #ffffff;
            page-break-inside: avoid; /* 防止页面分割 */
            box-sizing: border-box;
            margin: 0;
            overflow: hidden; /* 防止内容溢出 */
        }
        
        /* 内容元素基础样式 */
        .content-element {
            margin-bottom: 0;
            page-break-inside: avoid; /* 防止元素被分割 */
            word-wrap: break-word;    /* 长单词换行 */
            word-break: break-all;    /* 中英文混排换行 */
            hyphens: auto;            /* 自动连字符 */
        }
        
        .content-element:last-child {
            margin-bottom: 0;
        }
        
        /* 标题样式 - 优化字体渲染 */
        .element-title {
            font-size: {{index .Config.Styles "title" "FontSize"}}px;
            color: {{index .Config.Styles "title" "Color"}};
            line-height: {{div (index .Config.Styles "title" "LineHeight") (index .Config.Styles "title" "FontSize")}};
            text-align: justify;
            margin: 0 0 {{index .Config.Styles "title" "MarginBottom"}}px 0;
            font-weight: bold;
            letter-spacing: 0.5px;  /* 字母间距 */
            text-shadow: 0 0 1px rgba(0,0,0,0.1); /* 轻微阴影增强可读性 */
        }
        
        /* 副标题样式 */
        .element-subtitle {
            font-size: {{index .Config.Styles "subtitle" "FontSize"}}px;
            color: {{index .Config.Styles "subtitle" "Color"}};
            line-height: {{div (index .Config.Styles "subtitle" "LineHeight") (index .Config.Styles "subtitle" "FontSize")}};
            text-align: justify;
            margin: 0 0 {{index .Config.Styles "subtitle" "MarginBottom"}}px 0;
            font-weight: normal;
            letter-spacing: 0.3px;
        }
        
        /* 正文样式 */
        .element-body {
            font-size: {{index .Config.Styles "body" "FontSize"}}px;
            color: {{index .Config.Styles "body" "Color"}};
            line-height: {{div (index .Config.Styles "body" "LineHeight") (index .Config.Styles "body" "FontSize")}};
            text-align: justify;
            margin: 0 0 {{index .Config.Styles "body" "MarginBottom"}}px 0;
            text-indent: 0; /* 取消首行缩进，避免wkhtmltoimage渲染问题 */
        }
        
        /* 引用样式 - 增强视觉效果 */
        .element-quote {
            font-size: {{index .Config.Styles "quote" "FontSize"}}px;
            color: #1E90FF;
            line-height: {{div (index .Config.Styles "quote" "LineHeight") (index .Config.Styles "quote" "FontSize")}};
            text-align: justify;
            margin: 0 0 {{index .Config.Styles "quote" "MarginBottom"}}px 0;
            font-style: italic;
            padding: 20px;
            background: linear-gradient(135deg, #EAF2FF 0%, #FAFCFF 100%);
            border-left: 4px solid #1E90FF;
            border-radius: 0 8px 8px 0;
            position: relative;
            box-shadow: 0 2px 8px rgba(30, 144, 255, 0.1); /* 轻微阴影 */
        }
        
        /* 引用前的装饰 */
        .element-quote:before {
            content: """;
            font-size: 48px;
            color: #1E90FF;
            position: absolute;
            top: 10px;
            left: 15px;
            opacity: 0.3;
            font-family: serif;
        }
        
        /* 列表样式 - 优化项目符号 */
        .element-list {
            font-size: {{index .Config.Styles "list" "FontSize"}}px;
            color: {{index .Config.Styles "list" "Color"}};
            line-height: {{div (index .Config.Styles "list" "LineHeight") (index .Config.Styles "list" "FontSize")}};
            text-align: left; /* 列表左对齐更美观 */
            margin: 0 0 {{index .Config.Styles "list" "MarginBottom"}}px 0;
            padding-left: 40px;
            list-style: none;
        }
        
        .list-item {
            margin-bottom: 8px;
            position: relative;
            padding-left: 0;
        }
        
        .list-item:before {
            content: "•";
            color: #1E90FF; /* 使用主题色 */
            font-weight: bold;
            position: absolute;
            left: -20px;
            top: 0;
            font-size: 1.2em;
        }
        
        .list-item:last-child {
            margin-bottom: 0;
        }
        
        /* 标签样式 */
        .element-tag {
            font-size: {{index .Config.Styles "tag" "FontSize"}}px;
            color: {{index .Config.Styles "tag" "Color"}};
            background: #f0f0f0;
            padding: 4px 8px;
            border-radius: 4px;
            display: inline-block;
            margin: 2px;
        }
        
        /* 数字样式 */
        .element-number {
            font-size: {{index .Config.Styles "number" "FontSize"}}px;
            color: {{index .Config.Styles "number" "Color"}};
            font-weight: bold;
            font-family: "Times New Roman", serif; /* 数字使用更清晰的字体 */
        }
        
        /* wkhtmltoimage 特定优化 */
        @media print {
            body {
                -webkit-print-color-adjust: exact; /* 保持颜色 */
                print-color-adjust: exact;
            }
            
            .card-container {
                page-break-before: auto;
                page-break-after: auto;
                page-break-inside: avoid;
            }
        }
        
        /* 确保所有元素在容器内 */
        .content-area {
            width: 100%;
            height: auto;
            overflow: visible;
        }
        
        /* 防止空白折叠 */
        .content-element:empty {
            display: none;
        }
        
        /* 优化小字体渲染 */
        @media screen and (-webkit-min-device-pixel-ratio: 2) {
            body {
                -webkit-font-smoothing: antialiased;
            }
        }
    </style>
</head>
<body>
    {{range .Cards}}
    <div class="card-container">
        <div class="content-area">
            {{$card := .}}
            {{$processedText := .ProcessedText}}
            {{if $processedText}}
                {{/* 解析卡片内容为JSON */}}
                {{/* 注意：这里需要在Go代码中预处理JSON */}}
                <div class="content-element element-body">
                    <p class="element-body">{{escapeHTML $processedText}}</p>
                </div>
            {{else}}
                <div class="content-element element-body">
                    <p class="element-body">空内容</p>
                </div>
            {{end}}
        </div>
    </div>
    {{end}}
    
    <!-- 脚本用于字体加载检测 -->
    <script type="text/javascript">
        // 确保字体加载完成
        if (document.fonts && document.fonts.ready) {
            document.fonts.ready.then(function() {
                console.log('字体加载完成');
                document.body.classList.add('fonts-loaded');
            });
        }
        
        // 兼容性处理
        setTimeout(function() {
            document.body.classList.add('fonts-loaded');
        }, 1000);
    </script>
</body>
</html>`
}

// escapeHTML HTML转义
func (t *OptimizedHTMLTemplate) escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

// ValidateHTMLTemplate 验证HTML模板的有效性
func (t *OptimizedHTMLTemplate) ValidateHTMLTemplate() error {
	// 创建测试数据
	testBook := &model.BookM{
		Title: "测试书籍",
		Tags:  "测试,模板",
	}
	testBook.ID = 1

	testCards := []*model.CardM{
		{
			ProcessedText: `[{"type":"title","content":"测试标题"},{"type":"body","content":"测试内容"}]`,
			SortOrder:     1,
		},
	}
	testCards[0].ID = 1

	// 尝试生成HTML
	_, err := t.GenerateFullBookHTML(testBook, testCards)
	if err != nil {
		return fmt.Errorf("HTML模板验证失败: %v", err)
	}

	return nil
}
