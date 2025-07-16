package image

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// OCRResult 百度OCR返回结果的结构
type OCRResult struct {
	WordsResult []struct {
		Words string `json:"words"`
	} `json:"words_result"`
	WordsResultNum int   `json:"words_result_num"`
	LogID          int64 `json:"log_id"`
}

// getUserIDFromToken 从JWT token中获取用户ID
func getUserIDFromToken(c *gin.Context) (uint, error) {
	header := c.Request.Header.Get("Authorization")
	if len(header) == 0 {
		return 0, fmt.Errorf("missing authorization header")
	}

	var tokenString string
	fmt.Sscanf(header, "Bearer %s", &tokenString)

	// 使用viper获取JWT密钥
	jwtSecret := viper.GetString("jwt.secret")
	if jwtSecret == "" {
		return 0, fmt.Errorf("jwt secret not configured")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if userID, exists := claims["user_id"]; exists {
			return uint(userID.(float64)), nil
		}
	}

	return 0, fmt.Errorf("invalid token or missing user_id")
}

// BatchCreate 批量创建图片记录
func (ctrl *ImageController) BatchCreate(c *gin.Context) {
	log.C(c).Infow("Batch create images function called")

	var req []*model.ImageM
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	for _, img := range req {
		if _, err := govalidator.ValidateStruct(img); err != nil {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
			return
		}
	}

	if err := ctrl.b.Images().BatchCreate(c, req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// BatchUpload 支持批量上传图片文件
func (ctrl *ImageController) BatchUpload(c *gin.Context) {
	log.C(c).Infow("Batch upload images function called")

	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("Invalid multipart form"), nil)
		return
	}

	files := form.File["files"]
	if len(files) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("No files uploaded"), nil)
		return
	}

	// 从JWT token中获取用户ID
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Failed to get user ID from token: "+err.Error()), nil)
		return
	}

	// 确保目录存在
	os.MkdirAll("uploads", os.ModePerm)

	// 定义处理结果结构
	type ProcessResult struct {
		Filename      string
		URL           string
		CombinedText  string
		QianwenResult string
		Error         error
	}

	// 使用channel收集结果
	resultChan := make(chan ProcessResult, len(files))

	// 并发处理每个文件
	for _, fileHeader := range files {
		go func(fh *multipart.FileHeader) {
			result := ProcessResult{
				Filename: fmt.Sprintf("%d_%s", time.Now().UnixNano(), fh.Filename),
			}

			// 打开文件并读取数据
			file, err := fh.Open()
			if err != nil {
				result.Error = fmt.Errorf("open file failed: %w", err)
				resultChan <- result
				return
			}

			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				result.Error = fmt.Errorf("read file failed: %w", err)
				resultChan <- result
				return
			}

			// 并发调用百度OCR
			ocrResultStr, err := ctrl.b.Baidu().OCRImage(data)
			if err != nil {
				result.Error = fmt.Errorf("OCR failed: %w", err)
				resultChan <- result
				return
			}

			log.C(c).Infow("ocrResult", "filename", result.Filename, "ocrResult", ocrResultStr)

			// 保存文件
			savePath := filepath.Join("uploads", result.Filename)
			if err := c.SaveUploadedFile(fh, savePath); err != nil {
				result.Error = fmt.Errorf("upload failed: %w", err)
				resultChan <- result
				return
			}

			result.URL = "/static/" + result.Filename

			// 保存OCR结果到数据库
			imageRecord := &model.ImageM{
				UserID:      userID,
				FileName:    result.Filename,
				FileSize:    fh.Size,
				OriginalURL: result.URL,
				Status:      "processed",
				OCResult:    ocrResultStr,
			}

			if err := ctrl.b.Images().Create(c, imageRecord); err != nil {
				log.C(c).Errorw("Failed to save image record", "error", err.Error())
			}

			// 解析OCR结果
			var ocrResult OCRResult
			if err := json.Unmarshal([]byte(ocrResultStr), &ocrResult); err != nil {
				result.Error = fmt.Errorf("parse OCR result failed: %w", err)
				resultChan <- result
				return
			}

			// 提取words内容并拼接成字符串
			var wordsList []string
			for _, word := range ocrResult.WordsResult {
				wordsList = append(wordsList, word.Words)
			}
			result.CombinedText = strings.Join(wordsList, "")

			log.C(c).Infow("Combined text from OCR", "filename", result.Filename, "text", result.CombinedText)

			// 调用阿里千问文字模型
			prompt := `# 角色
你是一位顶级的知识架构师与内容编辑。你的专长在于理解、拆解和重构信息。你拥有极高的逻辑分析能力和文学审美能力，能够将任何形式的零散文本片段，转化为结构清晰、阅读流畅、价值最大化的最终作品。

# 核心任务
你的任务是接收一组用户提供的、已经由OCR处理好的、离散的、可能乱序的纯文本片段，对它们进行分析、归类、排序和重构，并以最合适的形式呈现出来。

# 工作流程与步骤

## **第一步：内容性质诊断 (最关键步骤)**
首先，完整阅读所有片段，对这批素材的整体性质做出判断。你必须将其归类为以下两种类型之一：
* **A类 - 逻辑驱动型：** 内容主要目的是为了阐述观点、解释概念、提供信息或进行论证。文本之间存在明确的因果、总分、递进等逻辑关系。例如：商业笔记、知识科普、方法论总结等。
* **B类 - 情绪驱动型：** 内容主要目的是为了记录感受、表达情绪、传递意象或引发共鸣。文本之间更多是情感上或意境上的关联。例如：文学摘抄、个人感悟、诗意短句、日记片段等。

## **第二步：选择处理路径**
根据第一步的诊断结果，选择对应的处理路径：
* 如果判断为 **A类 - 逻辑驱动型**，则遵循 **【路径A：逻辑重构】** 的所有指令。
* 如果判断为 **B类 - 情绪驱动型**，则遵循 **【路径B：情绪策展】** 的所有指令。

## **第三步：执行路径**

**--- 路径A：逻辑重构 (为逻辑驱动型内容) ---**

1. **过滤文本噪音：** 识别并永久丢弃所有与正文无关的文本信息，例如页码、页眉、书名等。
2. **构建逻辑框架：** 为整合后的文章构思一个简洁、精炼的主标题。如果内容可以自然地分为几个部分，为每个部分构思一个能体现其核心思想的小标题。
3. **排序与串联成文：** 在每个子主题下，将相关的原文片段按照最符合逻辑的顺序排列。然后，将它们融合成自然的段落。你可以添加必要的、中性的文字来承上启下、引入主题或进行简要总结，以确保逻辑流畅，让初次阅读者能无障碍地理解上下文。你的原创部分应扮演"水泥"的角色，将原文的"砖块"牢固地粘合起来，而不是自己成为新的"砖块"。

**--- 路径B：情绪策展 (为情绪驱动型内容) ---**

1. **过滤文本噪音：** 同路径A，过滤所有无关文本。
2. **寻找情感共鸣：** 寻找并识别片段之间的情感关联、意象关联或主题关联（例如，都与"希望"有关，或都用了"光/影"的意象）。
3. **分组与命名：** 将有共鸣的片段归为一组。为每一组内容，起一个同样充满意象、能概括其核心情绪或主题的标题。
4. **呈现与留白：** 在每个标题下，直接呈现原文片段。不添加任何串联词。片段之间用空行隔开，保持足够的视觉"留白"，让每个片段都能独立呼吸，同时在同一个标题下形成整体的氛围。

# 全局规则与约束

* **输入格式：** 用户提供的每个片段都由 \[片段X]\ 开始。这是你区分片段的唯一依据。
* **异常值处理：** 如果有少数片段与绝大多数内容的主题或性质完全不符，请将其分离出来，在主文案完成后，单独列在"--- 其他零散记录 ---"部分。
* **原文信息保留：**
    * 如果原文片段本身包含对他人的引用或出处（例如"张春说："），必须予以保留。
    * 如果原文片段包含有意义的格式（如**加粗**），在最终输出时应予以保留。
* **体量适应性：** 输出的结构应与输入内容的体量相匹配。少量片段应整合成一篇简洁短文或一个意象集；大量片段则可以构建成一个包含多个章节的长文或多个意象集。
* **观点冲突处理：** 如果发现观点存在矛盾或张力，采用并列呈现的方式。在路径A中，可以放在"两种视角"的小标题下；在路径B中，可以将它们并置于同一个情绪主题下，让读者自行体会。
* **无法分组处理：** 如果所有片段都无法形成一个统一的主题，则放弃整合。转而为每一个片段单独起一个合适的标题，然后逐一展示，用清晰的分割线隔开。

# 其他内容类型的处理规则
* **实用清单与指南 (如待办清单, 菜谱)：** 视为逻辑驱动型内容，但应优先使用编号列表来呈现其步骤，以保持其工具性。
* **文学创作类 (如诗歌, 小说片段)：** 视为情绪驱动型内容，严格保持其原有的分行和格式。

# 最终输出规定
1. **直接输出：** 你的回复必须直接以最终成品的标题开始。**严禁**包含任何"好的，这是为您整理的结果："之类的开场白或解释性文字。
2. **内容完整性：** 你必须输出所有被处理过的内容。任何一个被判断为有效的片段，都必须出现在最终的成品中（无论是在主体部分还是在"其他零散记录"部分）。
3. **格式纯粹性：** 最终输出只允许包含标题、小标题、自然段落、以及在特定规则下允许的项目/编号列表。

# 开始工作
现在，请确认你已完全理解以上所有规则。然后，处理我接下来提供给你的文本片段。

[片段1] ` + result.CombinedText
			messages := []map[string]string{
				{"role": "user", "content": prompt},
			}
			qianwenResult, err := ctrl.b.Ali().QianwenTextStream(messages, 1024, 0.5)
			if err != nil {
				log.C(c).Errorw("QianwenTextStream failed", "filename", result.Filename, "error", err.Error())
			} else {
				log.C(c).Infow("QianwenTextStream result", "filename", result.Filename, "result", qianwenResult)
				result.QianwenResult = qianwenResult
			}

			resultChan <- result
		}(fileHeader)
	}

	// 收集所有结果
	var urls []string
	var allResults []map[string]interface{}
	var allCombinedTexts []string
	var hasError bool

	for i := 0; i < len(files); i++ {
		result := <-resultChan

		if result.Error != nil {
			log.C(c).Errorw("File processing failed", "filename", result.Filename, "error", result.Error.Error())
			hasError = true
			continue
		}

		urls = append(urls, result.URL)
		allCombinedTexts = append(allCombinedTexts, result.CombinedText)

		processResult := map[string]interface{}{
			"filename":       result.Filename,
			"original_text":  result.CombinedText,
			"qianwen_result": result.QianwenResult,
			"url":            result.URL,
		}
		allResults = append(allResults, processResult)
	}

	// 如果所有文件都处理失败，返回错误
	if hasError && len(allResults) == 0 {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("All files processing failed"), nil)
		return
	}

	// 所有图片处理完成后，整合所有文本并调用万象模型
	var finalWanxiangResult string
	if len(allCombinedTexts) > 0 {
		// 将所有文本整合成一个完整的文本
		finalCombinedText := strings.Join(allCombinedTexts, "\n\n")
		log.C(c).Infow("Final combined text for image generation", "text", finalCombinedText)

		// 调用阿里万象图像模型生成图片
		wanxiangResult, err := ctrl.b.Ali().WanxiangImageAsync("基于以下所有文本内容生成一张综合图片："+finalCombinedText, "", "1024*1024")
		if err != nil {
			log.C(c).Errorw("WanxiangImageAsync failed", "error", err.Error())
		} else {
			log.C(c).Infow("WanxiangImageAsync result", "result", wanxiangResult)
			finalWanxiangResult = wanxiangResult

			// 万象模型调用成功，创建书籍记录
			// 从JWT token中获取用户ID
			userID, err := getUserIDFromToken(c)
			if err != nil {
				log.C(c).Errorw("Failed to get user ID from token for book creation", "error", err.Error())
			} else {
				// 创建书籍记录
				bookRecord := &model.BookM{
					UserID:      userID,
					Title:       "AI生成的书籍",
					Description: "基于OCR识别内容生成的书籍",
					Content:     finalCombinedText,
					CoverURL:    wanxiangResult,
					Category:    "AI生成",
					Status:      "published",
					IsPublic:    false,
					CardCount:   len(allCombinedTexts), // 使用处理的图片数量作为卡片数量
				}

				if err := ctrl.b.Books().Create(c, bookRecord); err != nil {
					log.C(c).Errorw("Failed to create book record", "error", err.Error())
				} else {
					log.C(c).Infow("Book created successfully", "book_id", bookRecord.ID, "user_id", userID)
				}
			}
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"urls":                  urls,
		"results":               allResults,
		"final_wanxiang_result": finalWanxiangResult,
		"total_texts_count":     len(allCombinedTexts),
	})
}
