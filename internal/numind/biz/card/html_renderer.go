package card

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
)

// HTMLRenderer HTML渲染器
type HTMLRenderer struct {
	config *pagination.PaginationConfig
}

// NewHTMLRenderer 创建新的HTML渲染器
func NewHTMLRenderer(config *pagination.PaginationConfig) *HTMLRenderer {
	return &HTMLRenderer{
		config: config,
	}
}

// RenderBookToHTML 将书籍渲染为完整的HTML页面
func (r *HTMLRenderer) RenderBookToHTML(book *model.BookM, cards []*model.CardM) (string, error) {
	// 解析所有卡片数据
	var allCards []CardData
	for _, card := range cards {
		var elements []pagination.Element
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			continue
		}

		cardData := CardData{
			ID:        card.ID,
			SortOrder: card.SortOrder,
			Elements:  r.convertElements(elements),
		}
		allCards = append(allCards, cardData)
	}

	// 按sort_order排序
	allCards = r.sortCards(allCards)

	// 准备模板数据
	data := BookTemplateData{
		Book: BookData{
			ID:        book.ID,
			Title:     book.Title,
			ImageURL:  book.ImageUrl,
			CardCount: book.CardCount,
			CreatedAt: book.CreatedAt.Format("2006-01-02 15:04:05"),
		},
		Cards:  allCards,
		Config: r.config,
	}

	// 生成HTML
	return r.generateBookHTML(data)
}

// CardData 卡片数据
type CardData struct {
	ID        uint
	SortOrder int
	Elements  []ElementData
}

// BookData 书籍数据
type BookData struct {
	ID        uint
	Title     string
	ImageURL  string
	CardCount int
	CreatedAt string
}

// ElementData 元素数据
type ElementData struct {
	Type    string
	Content string
	Items   []string
}

// BookTemplateData 书籍模板数据
type BookTemplateData struct {
	Book   BookData
	Cards  []CardData
	Config *pagination.PaginationConfig
}

// convertElements 转换分页元素为模板元素
func (r *HTMLRenderer) convertElements(elements []pagination.Element) []ElementData {
	var result []ElementData

	for _, element := range elements {
		elementData := ElementData{
			Type: string(element.Type),
		}

		switch element.Type {
		case pagination.ElementTypeList:
			switch v := element.Content.(type) {
			case []string:
				elementData.Items = v
			case []interface{}:
				for _, item := range v {
					elementData.Items = append(elementData.Items, fmt.Sprintf("%v", item))
				}
			default:
				elementData.Items = []string{fmt.Sprintf("%v", element.Content)}
			}
		default:
			elementData.Content = fmt.Sprintf("%v", element.Content)
		}

		result = append(result, elementData)
	}

	return result
}

// sortCards 按sort_order排序卡片
func (r *HTMLRenderer) sortCards(cards []CardData) []CardData {
	for i := 0; i < len(cards)-1; i++ {
		for j := 0; j < len(cards)-i-1; j++ {
			if cards[j].SortOrder > cards[j+1].SortOrder {
				cards[j], cards[j+1] = cards[j+1], cards[j]
			}
		}
	}
	return cards
}

// generateBookHTML 生成完整的书籍HTML页面
func (r *HTMLRenderer) generateBookHTML(data BookTemplateData) (string, error) {
	const htmlTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Book.Title}}</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: "SourceHanSerifSC", "STFangsong", -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei';
            background: #f5f5f5;
            color: #333333;
            line-height: 1.6;
        }
        .book-container { width: 100%; max-width: 100vw; margin: 0 auto; }
        .card-page {
            width: 100vw;
            height: 133.33vw;
            background: #ffffff;
            margin-bottom: 20rpx;
            box-sizing: border-box;
            overflow: hidden;
            position: relative;
        }
        .cover-page { display: flex; flex-direction: column; }
        .cover-image-container { flex: 1; width: 100%; overflow: hidden; }
        .cover-image { width: 100%; height: 100%; object-fit: cover; }
        .cover-title-container { padding: 60rpx 50rpx; background: #ffffff; }
        .cover-title {
            font-size: 64rpx;
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0;
            font-weight: bold;
        }
        .content-page { padding: 60rpx 50rpx; box-sizing: border-box; }
        .card-content { height: 100%; overflow-y: auto; }
        .content-element { margin-bottom: 0; }
        .content-element:last-child { margin-bottom: 0; }
        .element-title {
            font-size: 64rpx;
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0 0 30rpx 0;
            font-weight: bold;
        }
        .element-subtitle {
            font-size: 48rpx;
            color: #666666;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 25rpx 0;
            font-weight: normal;
        }
        .element-body {
            font-size: 36rpx;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30rpx 0;
        }
        .element-quote {
            font-size: 36rpx;
            color: #1E90FF;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 30rpx 0;
            font-style: italic;
            padding: 20rpx;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 4rpx solid #1E90FF;
            border-radius: 0 8rpx 8rpx 0;
        }
        .element-list {
            font-size: 36rpx;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30rpx 0;
            padding-left: 40rpx;
            list-style: none;
        }
        .list-item { margin-bottom: 8rpx; position: relative; }
        .list-item:before {
            content: "•";
            position: absolute;
            left: -20rpx;
            color: #333333;
        }
        .list-item:last-child { margin-bottom: 0; }
    </style>
</head>
<body>
    <div class="book-container">
        <!-- 封面页 -->
        <div class="card-page cover-page">
            <div class="cover-image-container">
                <img src="{{.Book.ImageURL}}" alt="{{.Book.Title}}" class="cover-image" onerror="this.style.display='none'" />
            </div>
            <div class="cover-title-container">
                <h1 class="cover-title">{{.Book.Title}}</h1>
            </div>
        </div>

        <!-- 内容页 -->
        {{range .Cards}}
        <div class="card-page content-page">
            <div class="card-content">
                {{range .Elements}}
                    {{if eq .Type "title"}}
                        <div class="content-element element-title">
                            <h2 class="element-title">{{.Content}}</h2>
                        </div>
                    {{else if eq .Type "subtitle"}}
                        <div class="content-element element-subtitle">
                            <h3 class="element-subtitle">{{.Content}}</h3>
                        </div>
                    {{else if eq .Type "body"}}
                        <div class="content-element element-body">
                            <p class="element-body">{{.Content}}</p>
                        </div>
                    {{else if eq .Type "list"}}
                        <div class="content-element element-list">
                            <ul class="element-list">
                                {{range .Items}}
                                    <li class="list-item">{{.}}</li>
                                {{end}}
                            </ul>
                        </div>
                    {{else if eq .Type "quote"}}
                        <div class="content-element element-quote">
                            <blockquote class="element-quote">{{.Content}}</blockquote>
                        </div>
                    {{else}}
                        <div class="content-element element-body">
                            <p class="element-body">{{.Content}}</p>
                        </div>
                    {{end}}
                {{end}}
            </div>
        </div>
        {{end}}
    </div>
</body>
</html>`

	tmpl, err := template.New("book").Parse(htmlTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.String(), nil
}

// RenderCardToHTML 将单个卡片渲染为HTML
func (r *HTMLRenderer) RenderCardToHTML(card *model.CardM) (string, error) {
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return "", fmt.Errorf("failed to parse card data: %v", err)
	}

	cardData := CardData{
		ID:        card.ID,
		SortOrder: card.SortOrder,
		Elements:  r.convertElements(elements),
	}

	// 生成单个卡片的HTML
	return r.generateCardHTML(cardData)
}

// generateCardHTML 生成单个卡片的HTML
func (r *HTMLRenderer) generateCardHTML(card CardData) (string, error) {
	const cardTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Card {{.ID}}</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: "SourceHanSerifSC", "STFangsong", -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei';
            background: #ffffff;
            color: #333333;
            line-height: 1.6;
            width: 1080px; /* 固定宽度，与右边图片保持一致 */
            height: 1440px; /* 固定高度，与右边图片保持一致 */
            padding: 60px 50px; /* 统一使用px单位 */
            overflow: hidden;
        }
        
        .card-content {
            height: 100%;
            overflow-y: auto;
        }
        
        .content-element {
            margin-bottom: 0;
        }
        
        .content-element:last-child {
            margin-bottom: 0;
        }
        
        .element-title {
            font-size: 64px; /* 统一使用px单位 */
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0 0 30px 0; /* 统一使用px单位 */
            font-weight: bold;
        }
        
        .element-subtitle {
            font-size: 48px; /* 统一使用px单位 */
            color: #666666;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 25px 0; /* 统一使用px单位 */
            font-weight: normal;
        }
        
        .element-body {
            font-size: 36px; /* 统一使用px单位 */
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30px 0; /* 统一使用px单位 */
        }
        
        .element-quote {
            font-size: 36px; /* 统一使用px单位 */
            color: #1E90FF;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 30px 0; /* 统一使用px单位 */
            font-style: italic;
            padding: 20px; /* 统一使用px单位 */
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 4px solid #1E90FF; /* 统一使用px单位 */
            border-radius: 0 8px 8px 0; /* 统一使用px单位 */
        }
        
        .element-list {
            font-size: 36px; /* 统一使用px单位 */
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30px 0; /* 统一使用px单位 */
            padding-left: 40px; /* 统一使用px单位 */
            list-style: none;
        }
        
        .list-item {
            margin-bottom: 8px; /* 统一使用px单位 */
            position: relative;
        }
        
        .list-item:before {
            content: "•";
            position: absolute;
            left: -20px; /* 统一使用px单位 */
            color: #333333;
        }
        
        .list-item:last-child {
            margin-bottom: 0;
        }
    </style>
</head>
<body>
    <div class="card-content">
        {{range .Elements}}
            {{if eq .Type "title"}}
                <div class="content-element element-title">
                    <h2 class="element-title">{{.Content}}</h2>
                </div>
            {{else if eq .Type "subtitle"}}
                <div class="content-element element-subtitle">
                    <h3 class="element-subtitle">{{.Content}}</h3>
                </div>
            {{else if eq .Type "body"}}
                <div class="content-element element-body">
                    <p class="element-body">{{.Content}}</p>
                </div>
            {{else if eq .Type "list"}}
                <div class="content-element element-list">
                    <ul class="element-list">
                        {{range .Items}}
                            <li class="list-item">{{.}}</li>
                        {{end}}
                    </ul>
                </div>
            {{else if eq .Type "quote"}}
                <div class="content-element element-quote">
                    <blockquote class="element-quote">{{.Content}}</blockquote>
                </div>
            {{else}}
                <div class="content-element element-body">
                    <p class="element-body">{{.Content}}</p>
                </div>
            {{end}}
        {{end}}
    </div>
</body>
</html>`

	// 解析模板
	tmpl, err := template.New("card").Parse(cardTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse card template: %v", err)
	}

	// 执行模板
	var buf strings.Builder
	if err := tmpl.Execute(&buf, card); err != nil {
		return "", fmt.Errorf("failed to execute card template: %v", err)
	}

	return buf.String(), nil
}
