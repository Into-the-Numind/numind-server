// migrate-vectors 是一次性迁移工具，从 MySQL knowledge_chunk 表重建 sqlite-vec 向量库
//
// 使用方法:
//
//	CONFIG_FILE=config_dev.yaml VECTOR_DB_PATH=/opt/numind/dev/sales_vector.db go run ./cmd/migrate-vectors/
//
// 环境变量:
//   - CONFIG_FILE: 配置文件路径（默认 config_dev.yaml）
//   - VECTOR_DB_PATH: sqlite-vec 数据库输出路径（默认基于 resource.image_path 计算）
//   - BATCH_SIZE: 每批处理的切片数（默认 20）
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/pkg/model"

	"github.com/spf13/viper"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	// 加载配置
	configFile := os.Getenv("CONFIG_FILE")
	if configFile == "" {
		configFile = "config_dev.yaml"
	}
	viper.SetConfigFile(configFile)
	if err := viper.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config %s: %v\n", configFile, err)
		os.Exit(1)
	}
	fmt.Printf("Loaded config: %s\n", configFile)

	// 连接 MySQL
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		viper.GetString("db.username"),
		viper.GetString("db.password"),
		viper.GetString("db.host"),
		viper.GetString("db.database"),
	)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to MySQL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Connected to MySQL: %s/%s\n", viper.GetString("db.host"), viper.GetString("db.database"))

	// 初始化 embedder
	aliBiz := ali.NewAliBiz(nil)
	embedder := func(ctx context.Context, text string) ([]float32, error) {
		return aliBiz.QianwenEmbedding(text)
	}

	// 初始化 sqlite-vec store
	dbPath := os.Getenv("VECTOR_DB_PATH")
	if dbPath == "" {
		imagePath := viper.GetString("resource.image_path")
		parentDir := filepath.Dir(imagePath) // 移除 upload
		if filepath.Base(parentDir) == "image" {
			baseDir := filepath.Dir(parentDir)
			dbPath = filepath.Join(baseDir, "sales_vector.db")
		} else {
			dbPath = filepath.Join(parentDir, "sales_vector.db")
		}
	}

	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directory: %v\n", err)
		os.Exit(1)
	}

	store, err := adapter.NewSQLiteVecStore(dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create sqlite-vec store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	fmt.Printf("SQLiteVec store: %s\n", dbPath)

	// 查询 MySQL 中所有有效切片
	var chunks []model.KnowledgeChunk
	result := db.Where("embedding_status = ?", "COMPLETED").
		Order("document_id, sequence").
		Find(&chunks)
	if result.Error != nil {
		fmt.Fprintf(os.Stderr, "Failed to query chunks: %v\n", result.Error)
		os.Exit(1)
	}
	fmt.Printf("Found %d chunks to migrate\n", len(chunks))

	if len(chunks) == 0 {
		fmt.Println("No chunks to migrate. Done!")
		return
	}

	// 批量处理
	batchSize := 20
	if bs := os.Getenv("BATCH_SIZE"); bs != "" {
		if v, err := strconv.Atoi(bs); err == nil && v > 0 {
			batchSize = v
		}
	}

	ctx := context.Background()
	startTime := time.Now()
	successCount := 0
	errorCount := 0

	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		// 转换为 domain 模型
		domainChunks := make([]domain.KnowledgeChunk, 0, len(batch))
		for _, c := range batch {
			id := c.VectorID
			if id == "" {
				// 如果没有 VectorID，使用 MySQL ID 生成
				id = fmt.Sprintf("%d", c.ID)
			}
			var tags []string
			if c.Tags != "" {
				tags = strings.Split(c.Tags, ",")
			}

			domainChunks = append(domainChunks, domain.KnowledgeChunk{
				ID:         id,
				DocumentID: c.DocumentID,
				UserID:     c.UserID,
				Content:    c.Content,
				Summary:    c.Summary,
				SourceRef:  c.SourceRef,
				Tags:       tags,
			})
		}

		if err := store.Upsert(ctx, domainChunks); err != nil {
			fmt.Fprintf(os.Stderr, "  [ERROR] Batch %d-%d failed: %v\n", i+1, end, err)
			errorCount += len(batch)
			// 单个 chunk 重试
			for _, dc := range domainChunks {
				if retryErr := store.Upsert(ctx, []domain.KnowledgeChunk{dc}); retryErr != nil {
					fmt.Fprintf(os.Stderr, "  [ERROR] Chunk %s retry failed: %v\n", dc.ID, retryErr)
				} else {
					errorCount--
					successCount++
				}
			}
			continue
		}

		successCount += len(batch)
		elapsed := time.Since(startTime)
		rate := float64(successCount) / elapsed.Seconds()
		eta := time.Duration(float64(len(chunks)-end) / rate * float64(time.Second))
		fmt.Printf("  [%d/%d] Migrated (%.1f chunks/s, ETA: %v)\n", end, len(chunks), rate, eta.Round(time.Second))

		// 控制 embedding API 请求速率
		time.Sleep(50 * time.Millisecond)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\n========== Migration Complete ==========\n")
	fmt.Printf("Total chunks:   %d\n", len(chunks))
	fmt.Printf("Success:        %d\n", successCount)
	fmt.Printf("Errors:         %d\n", errorCount)
	fmt.Printf("Duration:       %v\n", elapsed.Round(time.Second))
	fmt.Printf("Output:         %s\n", dbPath)
	fmt.Printf("========================================\n")
}
