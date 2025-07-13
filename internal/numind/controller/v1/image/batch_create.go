package image

import (
	"encoding/json"
	"fmt"
	"io"
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

	var urls []string
	var allResults []map[string]interface{}
	var allCombinedTexts []string // 收集所有图片的OCR文本

	for _, fileHeader := range files {
		filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), fileHeader.Filename)
		savePath := filepath.Join("uploads", filename)

		// baidu ocr
		file, err := fileHeader.Open()
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("Open file failed: "+err.Error()), nil)
			return
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("Read file failed: "+err.Error()), nil)
			return
		}

		ocrResultStr, err := ctrl.b.Baidu().OCRImage(data)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("OCR failed: "+err.Error()), nil)
			return
		}
		log.C(c).Infow("ocrResult", "filename", filename, "ocrResult", ocrResultStr)

		// 获取文件大小
		fileSize := fileHeader.Size

		// 从JWT token中获取用户ID
		userID, err := getUserIDFromToken(c)
		if err != nil {
			log.C(c).Errorw("Failed to get user ID from token", "error", err.Error())
			// 如果获取用户ID失败，跳过保存图片记录
			continue
		}

		// 保存OCR结果
		imageRecord := &model.ImageM{
			UserID:      userID,
			FileName:    filename,
			FileSize:    fileSize,
			OriginalURL: "/static/" + filename,
			Status:      "processed",
			OCResult:    ocrResultStr,
		}

		if err := ctrl.b.Images().Create(c, imageRecord); err != nil {
			log.C(c).Errorw("Failed to save image record", "error", err.Error())
			// 不中断流程，继续处理
		}

		// 解析OCR结果
		var ocrResult OCRResult
		if err := json.Unmarshal([]byte(ocrResultStr), &ocrResult); err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("Parse OCR result failed: "+err.Error()), nil)
			return
		}

		// 提取words内容并拼接成字符串
		var wordsList []string
		for _, word := range ocrResult.WordsResult {
			wordsList = append(wordsList, word.Words)
		}
		combinedText := strings.Join(wordsList, "")

		log.C(c).Infow("Combined text from OCR", "filename", filename, "text", combinedText)

		// 收集所有文本
		allCombinedTexts = append(allCombinedTexts, combinedText)

		// 调用阿里千问文字模型
		messages := []map[string]string{
			{"role": "user", "content": "请分析以下文本内容：" + combinedText},
		}
		qianwenResult, err := ctrl.b.Ali().QianwenTextStream(messages, 1024, 0.5)
		if err != nil {
			log.C(c).Errorw("QianwenTextStream failed", "filename", filename, "error", err.Error())
			// 不中断流程，继续处理
		} else {
			log.C(c).Infow("QianwenTextStream result", "filename", filename, "result", qianwenResult)
		}

		// 确保目录存在
		os.MkdirAll("uploads", os.ModePerm)

		if err := c.SaveUploadedFile(fileHeader, savePath); err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("Upload failed: "+err.Error()), nil)
			return
		}

		// 假设图片可通过 /static/ 访问
		url := "/static/" + filename
		urls = append(urls, url)

		// 收集处理结果
		result := map[string]interface{}{
			"filename":       filename,
			"original_text":  combinedText,
			"qianwen_result": qianwenResult,
			"url":            url,
		}
		allResults = append(allResults, result)
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
