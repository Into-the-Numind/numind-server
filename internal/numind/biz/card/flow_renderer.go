package card

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/markdown"
	"numind-server/internal/numind/biz/pagination"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// FlowRenderer implements flow-based pagination using real browser layout.
// It does NOT pre-split by blocks nor by lines. Instead, it incrementally appends
// content and measures overflow, with binary search rollback to find the largest
// prefix that fits a page.
type FlowRenderer struct {
	config *pagination.PaginationConfig
}

func NewFlowRenderer(config *pagination.PaginationConfig) *FlowRenderer {
	if config == nil {
		config = pagination.GetDefaultConfig()
	}
	return &FlowRenderer{config: config}
}

// PaginateMarkdown returns a list of per-page HTML fragments (innerHTML of the page content).
// The caller can then wrap each fragment in a fixed-size (1080x1440) HTML skeleton and render to WebP.
func (r *FlowRenderer) PaginateMarkdown(ctx context.Context, markdownText string) ([]string, error) {
	return r.PaginateMarkdownWithBackground(ctx, markdownText, "")
}

// PaginateMarkdownWithBackground returns a list of per-page HTML fragments with background support.
func (r *FlowRenderer) PaginateMarkdownWithBackground(ctx context.Context, markdownText, backgroundImage string) ([]string, error) {
	// 1) Convert markdown to styled HTML content (without full page wrapper)
	conv := markdown.NewHTMLConverter()
	contentHTML, err := conv.ConvertToHTML(markdownText)
	if err != nil {
		// Fallback: wrap as paragraph
		contentHTML = fmt.Sprintf("<p>%s</p>", markdownEscape(markdownText))
	}

	// 2) Build a minimal HTML document that contains both the source content and the pagination script
	// The page size and paddings follow r.config
	doc := r.buildFlowPagingHTML(contentHTML, backgroundImage)

	// 3) Run in headless Chrome and execute pagination, returning array of page HTML fragments
	pages, err := r.runPaginationInChrome(ctx, doc)
	if err != nil {
		return nil, err
	}

	return pages, nil
}

func (r *FlowRenderer) buildFlowPagingHTML(contentHTML, backgroundImage string) string {
	// Card and padding
	w := r.config.Card.Width
	h := r.config.Card.Height
	padTop := r.config.Card.Padding.Top
	padRight := r.config.Card.Padding.Right
	padBottom := r.config.Card.Padding.Bottom
	padLeft := r.config.Card.Padding.Left

	// Typography (basic)
	title := r.config.Styles[pagination.ElementTypeTitle]
	subtitle := r.config.Styles[pagination.ElementTypeSubtitle]
	body := r.config.Styles[pagination.ElementTypeBody]
	list := r.config.Styles[pagination.ElementTypeList]
	quote := r.config.Styles[pagination.ElementTypeQuote]

	// Format background style
	bgStyle := r.formatBackgroundStyle(backgroundImage)

	css := fmt.Sprintf(`
        @font-face {
            font-family: "SourceHanSerifSC";
            src: local("Source Han Serif SC"), local("SourceHanSerifSC"), local("STFangsong"), local("Noto Sans CJK SC"), local("PingFang SC"), local("Hiragino Sans GB"), local("Microsoft YaHei"), local("sans-serif");
            font-weight: normal;
            font-style: normal;
        }
        html, body {
            margin: 0; padding: 0;
            font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", Arial, sans-serif;
        }
        .page {
            width: %dpx; height: %dpx;
            box-sizing: border-box;
            position: relative;
            overflow: hidden;
            %s
        }
        .content {
            position: absolute;
            top: %dpx; left: %dpx; right: %dpx; bottom: %dpx;
            overflow: hidden;
        }
        /* styles for content */
        h1 { font-size: %dpx; line-height: %dpx; color: %s; margin: %dpx 0 %dpx 0; text-align: justify; font-weight: 700; }
        h2 { font-size: %dpx; line-height: %dpx; color: %s; margin: %dpx 0 %dpx 0; text-align: justify; font-weight: 700; }
        p  { font-size: %dpx; line-height: %dpx; color: %s; margin: %dpx 0 %dpx 0; text-align: justify; }
        ul { margin: %dpx 0 %dpx 0; padding-left: %dpx; }
        li { font-size: %dpx; line-height: %dpx; color: %s; text-align: justify; list-style-type: none; position: relative; }
        li::before { content: "•"; position: absolute; left: -%dpx; }
        blockquote { font-size: %dpx; line-height: %dpx; color: %s; margin: %dpx 0 %dpx 0; padding-left: 20px; border-left: 4px solid %s; text-align: justify; }
    `,
		w, h,
		bgStyle,
		padTop, padLeft, padRight, padBottom,
		title.FontSize, title.LineHeight, nz(title.Color, "#333333"), title.MarginTop, title.MarginBottom,
		subtitle.FontSize, subtitle.LineHeight, nz(subtitle.Color, "#666666"), subtitle.MarginTop, subtitle.MarginBottom,
		body.FontSize, body.LineHeight, nz(body.Color, "#333333"), body.MarginTop, body.MarginBottom,
		list.MarginTop, list.MarginBottom, maxInt(list.Indent, 40),
		list.FontSize, list.LineHeight, nz(list.Color, "#333333"), maxInt(list.Indent, 20),
		quote.FontSize, quote.LineHeight, nz(quote.Color, "#1E90FF"), quote.MarginTop, quote.MarginBottom, nz(quote.Color, "#1E90FF"),
	)

	// JS pagination using flow-based packing
	// We maintain a hidden source container (#src) with the full HTML, and iteratively create .page/.content containers,
	// moving nodes over and splitting when overflow occurs using binary search on text length for block-level nodes.
	script := `
        function cloneShallowKeepAttrs(node) {
            const c = node.cloneNode(false);
            if (node.nodeType === Node.ELEMENT_NODE) {
                for (const attr of node.attributes) c.setAttribute(attr.name, attr.value);
            }
            return c;
        }

        function fits(contentEl) {
            return contentEl.scrollHeight <= contentEl.clientHeight + 1; // tolerance
        }

        function textLen(node) {
            return (node.textContent || '').length;
        }

        function splitTextNode(original, keepLen) {
            const text = original.textContent || '';
            const left = document.createTextNode(text.slice(0, keepLen));
            const rightText = text.slice(keepLen);
            const right = rightText ? document.createTextNode(rightText) : null;
            return { left, right };
        }

        function splitBlockByText(node, contentEl) {
            // Binary search maximum prefix length that fits
            const total = textLen(node);
            if (total === 0) return { fit: null, rest: node };
            let lo = 1, hi = total, best = 0;
            let bestNode = null;
            while (lo <= hi) {
                const mid = (lo + hi) >> 1;
                const clone = cloneShallowKeepAttrs(node);
                // Build a flat text-only clone up to mid
                const span = document.createElement('span');
                span.textContent = (node.textContent || '').slice(0, mid);
                clone.appendChild(span);
                contentEl.appendChild(clone);
                if (fits(contentEl)) { best = mid; bestNode = clone; } 
                contentEl.removeChild(clone);
                if (fits(contentEl)) { lo = mid + 1; } else { hi = mid - 1; }
            }
            if (best === 0) return { fit: null, rest: node };
            // Build final fit/rest nodes
            const fitNode = cloneShallowKeepAttrs(node);
            const leftSpan = document.createElement('span');
            leftSpan.textContent = (node.textContent || '').slice(0, best);
            fitNode.appendChild(leftSpan);
            const restStr = (node.textContent || '').slice(best);
            let restNode = null;
            if (restStr.length > 0) {
                restNode = cloneShallowKeepAttrs(node);
                const rightSpan = document.createElement('span');
                rightSpan.textContent = restStr;
                restNode.appendChild(rightSpan);
            }
            return { fit: fitNode, rest: restNode };
        }

        function splitListItem(node, contentEl) {
            // node is <li> possibly long; split by text
            return splitBlockByText(node, contentEl);
        }

        function appendMaxFit(node, contentEl, srcEl) {
            if (!node) return false;
            contentEl.appendChild(node);
            if (fits(contentEl)) return true;
            contentEl.removeChild(node);

            if (node.nodeType === Node.TEXT_NODE) {
                // text node binary search
                const total = (node.textContent || '').length;
                let lo = 1, hi = total, best = 0;
                while (lo <= hi) {
                    const mid = (lo + hi) >> 1;
                    const left = document.createTextNode((node.textContent || '').slice(0, mid));
                    contentEl.appendChild(left);
                    if (fits(contentEl)) { best = mid; }
                    contentEl.removeChild(left);
                    if (fits(contentEl)) lo = mid + 1; else hi = mid - 1;
                }
                if (best > 0) {
                    const left = document.createTextNode((node.textContent || '').slice(0, best));
                    contentEl.appendChild(left);
                    const rightStr = (node.textContent || '').slice(best);
                    if (rightStr.length) srcEl.insertBefore(document.createTextNode(rightStr), srcEl.firstChild);
                    return true;
                }
                // cannot fit even 1 char, leave page full
                srcEl.insertBefore(node, srcEl.firstChild);
                return false;
            }

            if (node.nodeType === Node.ELEMENT_NODE) {
                const tag = node.tagName;
                if (tag === 'P' || tag === 'H1' || tag === 'H2' || tag === 'BLOCKQUOTE') {
                    const { fit, rest } = splitBlockByText(node, contentEl);
                    if (fit) contentEl.appendChild(fit);
                    if (rest) srcEl.insertBefore(rest, srcEl.firstChild);
                    return !!fit;
                }
                if (tag === 'UL' || tag === 'OL') {
                    // move <li> one by one; the overflowing li may be split by text
                    const ulLeft = cloneShallowKeepAttrs(node);
                    const li = node.firstElementChild;
                    if (!li) return false;
                    // try adding li
                    const attempt = cloneShallowKeepAttrs(li);
                    attempt.textContent = li.textContent || '';
                    ulLeft.appendChild(attempt);
                    contentEl.appendChild(ulLeft);
                    if (fits(contentEl)) {
                        // accept and remove li from original list
                        contentEl.removeChild(ulLeft);
                        contentEl.appendChild(ulLeft);
                        node.removeChild(li);
                        if (node.children.length > 0) srcEl.insertBefore(node, srcEl.firstChild);
                        return true;
                    } else {
                        // try split li
                        contentEl.removeChild(ulLeft);
                        const { fit, rest } = splitListItem(li, contentEl);
                        if (fit) {
                            const newUL = cloneShallowKeepAttrs(node);
                            newUL.appendChild(fit);
                            contentEl.appendChild(newUL);
                        }
                        if (rest) {
                            const remainingUL = cloneShallowKeepAttrs(node);
                            remainingUL.appendChild(rest);
                            // if original node still has more children after the first li, keep them too
                            let next = li.nextSibling;
                            while (next) { remainingUL.appendChild(next.cloneNode(true)); next = next.nextSibling; }
                            srcEl.insertBefore(remainingUL, srcEl.firstChild);
                        } else {
                            // rest is null: if original node still has remaining li(s), put them back
                            if (node.children && node.children.length > 1) srcEl.insertBefore(node, srcEl.firstChild);
                        }
                        return !!fit;
                    }
                }
                // Generic fallback: treat as block by text content
                const { fit, rest } = splitBlockByText(node, contentEl);
                if (fit) contentEl.appendChild(fit);
                if (rest) srcEl.insertBefore(rest, srcEl.firstChild);
                return !!fit;
            }
            // Unknown node type; skip
            return false;
        }

        function paginate() {
            const pages = [];
            const src = document.getElementById('src');
            while (src.childNodes.length > 0) {
                const page = document.createElement('div');
                page.className = 'page';
                const content = document.createElement('div');
                content.className = 'content';
                page.appendChild(content);
                document.body.appendChild(page);

                // exponential growth then binary backoff happens inside appendMaxFit
                let step = 8; // initial batch size (nodes)
                while (src.childNodes.length > 0) {
                    // batch move nodes quickly if possible
                    let moved = 0;
                    while (moved < step && src.childNodes.length > 0) {
                        const node = src.removeChild(src.firstChild);
                        content.appendChild(node);
                        if (!fits(content)) {
                            // rollback this node and split
                            content.removeChild(node);
                            // put back node at head so split can handle it
                            src.insertBefore(node, src.firstChild);
                            appendMaxFit(src.removeChild(src.firstChild), content, src);
                            break;
                        }
                        moved++;
                    }
                    if (!fits(content)) break;
                    if (src.childNodes.length === 0) break;
                    // grow step if still fitting
                    step = Math.min(step * 2, 64);
                    // safety: if next single node cannot fit at all, stop to finalize page
                    const probe = src.firstChild;
                    if (probe) {
                        content.appendChild(probe.cloneNode(true));
                        const ok = fits(content);
                        content.removeChild(content.lastChild);
                        if (!ok) {
                            // try to split the probe at most once here
                            appendMaxFit(src.removeChild(src.firstChild), content, src);
                            break;
                        }
                    }
                }

                pages.push(content.innerHTML);
                document.body.removeChild(page);
            }
            return pages;
        }

        (function(){
            // After fonts are ready (best-effort)
            if (document.fonts && document.fonts.ready) {
                document.fonts.ready.then(() => {
                    window.__pages = paginate();
                    window.__ready = true;
                });
            } else {
                window.__pages = paginate();
                window.__ready = true;
            }
        })();
    `

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>Flow Pagination</title>
<style>%s</style>
</head>
<body>
  <div id="src" style="visibility:hidden; position:absolute; left:-99999px; top:-99999px; width:%dpx;">
    %s
  </div>
  <script>%s</script>
</body>
</html>`, css, w-padLeft-padRight, contentHTML, script)

	return html
}

func (r *FlowRenderer) runPaginationInChrome(ctx context.Context, htmlDoc string) ([]string, error) {
	// Create Chrome allocator with sane defaults similar to util renderer
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", r.config.Card.Width, r.config.Card.Height)),
	)
	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancel2 := chromedp.NewContext(allocCtx)
	defer cancel2()

	// Timeout
	cctx, cancel3 := context.WithTimeout(taskCtx, 60*time.Second)
	defer cancel3()

	// data URL
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlDoc))
	dataURL := "data:text/html;base64," + encoded

	// Navigate and wait
	var ready bool
	var pages []string
	err := chromedp.Run(cctx,
		chromedp.Navigate(dataURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// small delay to allow paginate() to run
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				var v bool
				if err := chromedp.EvaluateAsDevTools("window.__ready === true", &v).Do(ctx); err == nil && v {
					ready = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !ready {
				return fmt.Errorf("flow pagination did not signal ready in time")
			}
			return nil
		}),
		chromedp.EvaluateAsDevTools("window.__pages", &pages),
		// noop screenshot to ensure page fully rendered in headless
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _ = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).Do(ctx)
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		// at least one page (may be empty)
		pages = []string{""}
	}
	return pages, nil
}

func markdownEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

func nz(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// formatBackgroundStyle 将背景图路径转为内联 CSS 样式，支持 http(s)、data、本地绝对/相对路径
func (r *FlowRenderer) formatBackgroundStyle(background string) string {
	if strings.TrimSpace(background) == "" {
		return "background: #ffffff;"
	}
	src := background
	lower := strings.ToLower(background)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		// remote or data url
		src = background
	} else if filepath.IsAbs(background) {
		src = "file://" + background
	} else {
		if absPath, err := filepath.Abs(background); err == nil {
			src = "file://" + absPath
		}
	}
	// 背景图居中、cover 铺满
	return fmt.Sprintf("background: url('%s') center center / cover no-repeat;", src)
}
