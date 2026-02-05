package numind

import (
	"context"
	"fmt"
	configbiz "numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/wecom"
	"numind-server/internal/numind/config"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/redis"
	"numind-server/internal/pkg/util"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/db"
)

const (
	// recommendedHomeDir 定义放置 miniblog 服务配置的默认目录.
	recommendedHomeDir = ".numind"

	// defaultConfigName 指定了 miniblog 服务的默认配置文件名.
	defaultConfigName = "config.yaml"
)

// initConfig 设置需要读取的配置文件名、环境变量，并读取配置文件内容到 viper 中.
func initConfig() {
	if cfgFile != "" {
		// 从命令行选项指定的配置文件中读取
		viper.SetConfigFile(cfgFile)
	} else {
		// 查找用户主目录
		home, err := os.UserHomeDir()
		// 如果获取用户主目录失败，打印 `'Error: xxx` 错误，并退出程序（退出码为 1）
		cobra.CheckErr(err)

		// 将用 `$HOME/<recommendedHomeDir>` 目录加入到配置文件的搜索路径中
		viper.AddConfigPath(filepath.Join(home, recommendedHomeDir))

		// 把当前目录加入到配置文件的搜索路径中
		viper.AddConfigPath(".")

		// 设置配置文件格式为 YAML (YAML 格式清晰易读，并且支持复杂的配置结构)
		viper.SetConfigType("yaml")

		// 配置文件名称（没有文件扩展名）
		viper.SetConfigName(defaultConfigName)
	}

	// 读取匹配的环境变量
	viper.AutomaticEnv()

	// 读取环境变量的前缀为 MINIBLOG，如果是 miniblog，将自动转变为大写。
	viper.SetEnvPrefix("NUMIND")

	// 以下 2 行，将 viper.Get(key) key 字符串中 '.' 和 '-' 替换为 '_'
	replacer := strings.NewReplacer(".", "_")
	viper.SetEnvKeyReplacer(replacer)

	// 读取配置文件。如果指定了配置文件名，则使用指定的配置文件，否则在注册的搜索路径中搜索
	if err := viper.ReadInConfig(); err != nil {
		log.Errorw("Failed to read viper configuration file", "err", err)
	}

	// 打印 viper 当前使用的配置文件，方便 Debug.
	log.Debugw("Using config file", "file", viper.ConfigFileUsed())
}

// logOptions 从 viper 中读取日志配置，构建 `*log.Options` 并返回.
// 注意：`viper.Get<Type>()` 中 key 的名字需要使用 `.` 分割，以跟 YAML 中保持相同的缩进.
func logOptions() *log.Options {
	return &log.Options{
		DisableCaller:     viper.GetBool("log.disable-caller"),
		DisableStacktrace: viper.GetBool("log.disable-stacktrace"),
		Level:             viper.GetString("log.level"),
		Format:            viper.GetString("log.format"),
		OutputPaths:       viper.GetStringSlice("log.output-paths"),
	}
}

// initStore 读取 db 配置，创建 gorm.DB 实例，并初始化 miniblog store 层.
func initStore() error {
	dbOptions := &db.MySQLOptions{
		Host:                  viper.GetString("db.host"),
		Username:              viper.GetString("db.username"),
		Password:              viper.GetString("db.password"),
		Database:              viper.GetString("db.database"),
		MaxIdleConnections:    viper.GetInt("db.max-idle-connections"),
		MaxOpenConnections:    viper.GetInt("db.max-open-connections"),
		MaxConnectionLifeTime: viper.GetDuration("db.max-connection-life-time"),
		LogLevel:              viper.GetInt("db.log-level"),
	}

	ins, err := db.NewMySQL(dbOptions)
	if err != nil {
		return err
	}

	err = autoMigrate(ins)
	if err != nil {
		return err
	}

	storeInstance := store.NewStore(ins)

	// 初始化Redis
	if err := redis.Init(); err != nil {
		log.Warnw("Failed to initialize Redis", "error", err)
		// Redis初始化失败不影响应用启动，但配置缓存功能将不可用
	} else {
		log.Infow("Redis initialized successfully")
	}

	// 同步系统配置（启动时自动同步）
	ctx := context.Background()
	configBiz := configbiz.New(storeInstance)
	if err := configBiz.InitDefaultConfigs(ctx); err != nil {
		log.Warnw("Failed to sync system configs", "error", err)
		// 配置同步失败不影响应用启动
	} else {
		log.Infow("System configs synchronized successfully")
	}

	// 启动配置变更监听器
	if err := configBiz.StartConfigChangeListener(ctx); err != nil {
		log.Warnw("Failed to start config change listener", "error", err)
		// 监听器启动失败不影响应用启动
	} else {
		log.Infow("Config change listener started successfully")
	}

	return nil
}

func autoMigrate(db *gorm.DB) error {
	log.Infow("Migrating database...")

	// 获取数据库字符集配置
	charsetConfig := getDatabaseCharsetConfig()

	// 1.5 企业微信相关表迁移前的字段处理 (幂等更名: numind_user_id -> user_id)
	log.Infow("Running custom migrations for Wecom tables...")
	if err := migrateWecomUsersTable(db); err != nil {
		log.Errorw("Failed to run custom Wecom migration", "error", err)
		// 仍然尝试继续 AutoMigrate
	}

	// 2. 自动迁移所有模型
	log.Infow("Starting database schema migration...")

	// 先迁移基础表
	err := db.AutoMigrate(
		// &model.User{}, // 暂时跳过 User 表迁移以避免 Error 3780 外键冲突
		&model.CategoryM{},
		&model.ArticleM{},
		&model.Favorite{},
		&model.SystemConfigM{},
		&model.ProxyServerM{},
		&model.Feedback{},
		&model.AboutUsM{},
		&model.Agreement{},
		&model.BookM{},
		&model.CardM{},
		&model.ImageM{},
		&model.Template{},
		&model.ChatSession{},
		&model.ChatMessage{},
		&model.AccountRecord{},
		&model.PaymentM{},
		&model.Admin{},
		&model.KnowledgeDocument{},
		&model.SalesSession{},
		&model.SalesMessage{},
		&model.LanguageStyle{},
		&wecom.WecomUser{},
		&wecom.WecomMessage{},
		&wecom.WecomCursor{},
		&wecom.WecomBindCode{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate basic tables: %v", err)
	}

	// 单独迁移SOP相关表，按依赖顺序分步骤创建
	log.Infow("Migrating SOP tables...")

	// 第一步：创建模板表（无外键依赖）
	if err := db.AutoMigrate(&model.SopTemplate{}); err != nil {
		return fmt.Errorf("failed to migrate sop_template: %v", err)
	}

	// 第二步：创建节点表（依赖模板表）
	if err := db.AutoMigrate(&model.SopNode{}); err != nil {
		return fmt.Errorf("failed to migrate sop_node: %v", err)
	}

	// 第三步：创建执行记录表（依赖模板表和用户表）
	if err := db.AutoMigrate(&model.SopRun{}); err != nil {
		return fmt.Errorf("failed to migrate sop_run: %v", err)
	}

	// 第四步：创建节点执行记录表（依赖上面所有表）
	if err := db.AutoMigrate(&model.SopNodeRun{}); err != nil {
		return fmt.Errorf("failed to migrate sop_node_run: %v", err)
	}

	// 修复 sop_node_run 表的 input 和 output 字段类型（从 TEXT 改为 LONGTEXT）
	// 注意：GORM的AutoMigrate可能不会自动修改字段类型，需要手动修复
	if err := fixSopNodeRunTextFields(db); err != nil {
		log.Warnw("Failed to fix sop_node_run text fields, continuing", "error", err)
		// 不返回错误，继续执行，因为可能是字段已经是正确的类型
	} else {
		log.Infow("Sop_node_run text fields fixed successfully")
	}

	// 第五步：创建笔记表（依赖执行记录表）
	if err := db.AutoMigrate(&model.SopNote{}); err != nil {
		return fmt.Errorf("failed to migrate sop_note: %v", err)
	}

	// 第六步：创建文件表（依赖执行记录表和节点表）
	if err := db.AutoMigrate(&model.SopFile{}); err != nil {
		return fmt.Errorf("failed to migrate sop_file: %v", err)
	}

	// 修复 sop_file 表的 file_type 字段长度（从 50 增加到 255）
	if err := fixSopFileFields(db); err != nil {
		log.Warnw("Failed to fix sop_file fields, continuing", "error", err)
	} else {
		log.Infow("Sop_file fields fixed successfully")
	}

	// 第七步:创建对话消息表（依赖执行记录表和用户表）
	if err := db.AutoMigrate(&model.SopChatMsg{}); err != nil {
		return fmt.Errorf("failed to migrate sop_chat_message: %v", err)
	}

	// 第八步:创建用户模板权限表（依赖用户表和模板表）
	// if err := db.AutoMigrate(&model.UserTemplatePermission{}); err != nil {
	// 	return fmt.Errorf("failed to migrate user_template_permission: %v", err)
	// }

	log.Infow("SOP tables migration completed")

	log.Infow("All database schema migration completed")

	// 3. 迁移后验证字符集
	// 关键：必须在 AutoMigrate 之后执行，确保新创建的表也能被正确修复
	log.Infow("Post-migration charset verification...")
	if err := ensureDatabaseCharset(db, charsetConfig); err != nil {
		log.Warnw("Failed to ensure database charset after migration", "error", err)
	} else {
		log.Infow("Post-migration charset verification completed")
	}

	log.Infow("Database migration and charset repair completed successfully")
	return nil
}

// forceEnsureDatabaseCharset 强制确保数据库使用正确的字符集
func forceEnsureDatabaseCharset(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	log.Infow("Force ensuring database charset...",
		"target_charset", charsetConfig.TargetCharset,
		"target_collation", charsetConfig.TargetCollation)

	// 强制设置连接的字符集（每次操作前都设置）
	log.Infow("Setting connection charset...")
	if err := db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Warnw("Failed to set connection charset", "error", err)
	} else {
		log.Infow("Connection charset set successfully")
	}

	// 强制修复数据库字符集
	log.Infow("Force updating database charset...")
	alterSQL := charsetConfig.GetAlterDatabaseSQL()
	if err := db.Exec(alterSQL).Error; err != nil {
		log.Warnw("Failed to force update database charset", "error", err)
	} else {
		log.Infow("Database charset force updated successfully")
	}

	// 强制修复所有关键表的字符集
	for _, tableName := range charsetConfig.CriticalTables {
		if err := forceFixTableCharset(db, tableName, charsetConfig); err != nil {
			log.Warnw("Failed to force fix table charset", "table", tableName, "error", err)
			continue
		}
	}

	return nil
}

// forceFixTableCharset 强制修复表字符集
func forceFixTableCharset(db *gorm.DB, tableName string, charsetConfig *config.DatabaseCharsetConfig) error {
	// 检查表是否存在
	var count int64
	err := db.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", tableName).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("failed to check table existence: %v", err)
	}

	if count == 0 {
		log.Infow("Table does not exist, skipping charset fix", "table", tableName)
		return nil
	}

	// 强制设置连接的字符集（每次操作前都设置）
	if err := db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Warnw("Failed to set connection charset for table operation", "table", tableName, "error", err)
	}

	log.Infow("Force fixing table charset", "table", tableName)

	// 强制修复表字符集
	alterSQL := charsetConfig.GetAlterTableSQL(tableName)
	if err := db.Exec(alterSQL).Error; err != nil {
		return fmt.Errorf("failed to force update table charset: %v", err)
	}

	log.Infow("Table charset force updated successfully", "table", tableName)

	// 特别处理chat_message表的content字段
	if tableName == "chat_message" {
		if err := forceFixContentField(db, charsetConfig); err != nil {
			log.Warnw("Failed to force fix content field", "error", err)
		}
	}

	return nil
}

// forceFixContentField 强制修复content字段字符集
func forceFixContentField(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	log.Infow("Force fixing content field charset...")

	// 强制设置连接的字符集（每次操作前都设置）
	if err := db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Warnw("Failed to set connection charset for content field operation", "error", err)
	}

	// 强制修复字段字符集
	alterSQL := charsetConfig.GetAlterColumnSQL("chat_message", "content", "TEXT")
	if err := db.Exec(alterSQL).Error; err != nil {
		return fmt.Errorf("failed to force update content field charset: %v", err)
	}

	log.Infow("Content field charset force updated successfully")
	return nil
}

// forceFixChatMessageTable 特别强制修复chat_message表
func forceFixChatMessageTable(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	log.Infow("Force fixing chat_message table with multiple approaches...")

	// 强制设置连接的字符集（每次操作前都设置）
	if err := db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Warnw("Failed to set connection charset for chat_message table operation", "error", err)
	}

	// 方法1: 强制转换表字符集
	alterTableSQL := charsetConfig.GetAlterTableSQL("chat_message")
	if err := db.Exec(alterTableSQL).Error; err != nil {
		log.Warnw("Method 1 failed: force alter table", "error", err)
	} else {
		log.Infow("Method 1 completed: table charset updated")
	}

	// 方法2: 强制修改content字段
	alterColumnSQL := charsetConfig.GetAlterColumnSQL("chat_message", "content", "TEXT")
	if err := db.Exec(alterColumnSQL).Error; err != nil {
		log.Warnw("Method 2 failed: force alter column", "error", err)
	} else {
		log.Infow("Method 2 completed: content column charset updated")
	}

	// 方法3: 强制修改所有TEXT字段
	textFields := []string{"content", "title", "description", "tags"}
	for _, field := range textFields {
		alterFieldSQL := charsetConfig.GetAlterColumnSQL("chat_message", field, "TEXT")
		if err := db.Exec(alterFieldSQL).Error; err != nil {
			log.Debugw("Field charset update skipped", "field", field, "error", err)
		} else {
			log.Infow("Field charset updated", "field", field)
		}
	}

	return nil
}

// fixSopNodeRunTextFields 修复 sop_node_run 表的 input 和 output 字段类型
// 将 TEXT 类型改为 LONGTEXT 以支持超长文本
func fixSopNodeRunTextFields(db *gorm.DB) error {
	tableName := "sop_node_run"

	// 检查表是否存在
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) 
		FROM information_schema.TABLES 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ?
	`, tableName).Scan(&count).Error

	if err != nil {
		return fmt.Errorf("failed to check table existence: %v", err)
	}

	if count == 0 {
		log.Infow("Table does not exist, skipping text fields fix", "table", tableName)
		return nil
	}

	// 检查字段当前类型
	var inputType, outputType, thinkingType string
	err = db.Raw(`
		SELECT DATA_TYPE 
		FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ? 
			AND COLUMN_NAME = 'input'
	`, tableName).Scan(&inputType).Error

	if err != nil {
		return fmt.Errorf("failed to check input field type: %v", err)
	}

	err = db.Raw(`
		SELECT DATA_TYPE 
		FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ? 
			AND COLUMN_NAME = 'output'
	`, tableName).Scan(&outputType).Error

	if err != nil {
		return fmt.Errorf("failed to check output field type: %v", err)
	}

	// 检查 thinking 字段是否存在
	var thinkingExists int64
	err = db.Raw(`
		SELECT COUNT(*) 
		FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ? 
			AND COLUMN_NAME = 'thinking'
	`, tableName).Scan(&thinkingExists).Error

	if err == nil && thinkingExists > 0 {
		err = db.Raw(`
			SELECT DATA_TYPE 
			FROM information_schema.COLUMNS 
			WHERE TABLE_SCHEMA = DATABASE() 
				AND TABLE_NAME = ? 
				AND COLUMN_NAME = 'thinking'
		`, tableName).Scan(&thinkingType).Error
		if err != nil {
			return fmt.Errorf("failed to check thinking field type: %v", err)
		}
	}

	// 如果字段类型不是 LONGTEXT，则修改
	if inputType != "longtext" {
		log.Infow("Fixing input field type", "from", inputType, "to", "longtext")
		if err := db.Exec(`
			ALTER TABLE ` + tableName + ` 
			MODIFY COLUMN input LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci
		`).Error; err != nil {
			return fmt.Errorf("failed to modify input field: %v", err)
		}
		log.Infow("Input field type fixed successfully")
	}

	if outputType != "longtext" {
		log.Infow("Fixing output field type", "from", outputType, "to", "longtext")
		if err := db.Exec(`
			ALTER TABLE ` + tableName + ` 
			MODIFY COLUMN output LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci
		`).Error; err != nil {
			return fmt.Errorf("failed to modify output field: %v", err)
		}
		log.Infow("Output field type fixed successfully")
	}

	// 如果 thinking 字段存在但类型不是 LONGTEXT，则修改
	if thinkingExists > 0 && thinkingType != "longtext" {
		log.Infow("Fixing thinking field type", "from", thinkingType, "to", "longtext")
		if err := db.Exec(`
			ALTER TABLE ` + tableName + ` 
			MODIFY COLUMN thinking LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci
		`).Error; err != nil {
			return fmt.Errorf("failed to modify thinking field: %v", err)
		}
		log.Infow("Thinking field type fixed successfully")
	}

	return nil
}

// fixSopFileFields 修复 sop_file 表的字段长度
func fixSopFileFields(db *gorm.DB) error {
	tableName := "sop_file"

	// 检查表是否存在
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) 
		FROM information_schema.TABLES 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ?
	`, tableName).Scan(&count).Error

	if err != nil {
		return fmt.Errorf("failed to check table existence: %v", err)
	}

	if count == 0 {
		return nil
	}

	// 强制修改 file_type 字段长度为 255
	log.Infow("Ensuring file_type column length is 255", "table", tableName)
	if err := db.Exec(`
		ALTER TABLE ` + tableName + ` 
		MODIFY COLUMN file_type VARCHAR(255)
	`).Error; err != nil {
		return fmt.Errorf("failed to modify file_type field: %v", err)
	}

	return nil
}

// verifyCharsetRepair 验证字符集修复结果
func verifyCharsetRepair(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	log.Infow("Verifying charset repair results...")

	// 验证数据库字符集
	dbCharset, dbCollation, err := config.GetDatabaseCharsetInfo(db)
	if err != nil {
		return fmt.Errorf("failed to verify database charset: %v", err)
	}

	log.Infow("Database charset verification result",
		"current_charset", dbCharset,
		"current_collation", dbCollation,
		"target_charset", charsetConfig.TargetCharset)

	// 验证chat_message表字符集
	tableCharset, tableCollation, err := config.GetTableCharsetInfo(db, "chat_message")
	if err != nil {
		log.Warnw("Failed to verify chat_message table charset", "error", err)
	} else {
		log.Infow("Chat_message table charset verification result",
			"current_charset", tableCharset,
			"current_collation", tableCollation)
	}

	// 验证content字段字符集
	fieldCharset, fieldCollation, err := config.GetColumnCharsetInfo(db, "chat_message", "content")
	if err != nil {
		log.Warnw("Failed to verify content field charset", "error", err)
	} else {
		log.Infow("Content field charset verification result",
			"current_charset", fieldCharset,
			"current_collation", fieldCollation)
	}

	return nil
}

// getDatabaseCharsetConfig 获取数据库字符集配置
func getDatabaseCharsetConfig() *config.DatabaseCharsetConfig {
	// 从配置文件读取配置，如果没有则使用默认配置
	charsetConfig := config.DefaultDatabaseCharsetConfig()

	// 从viper读取配置
	if viper.IsSet("database.charset.target_charset") {
		charsetConfig.TargetCharset = viper.GetString("database.charset.target_charset")
	}

	if viper.IsSet("database.charset.target_collation") {
		charsetConfig.TargetCollation = viper.GetString("database.charset.target_collation")
	}

	if viper.IsSet("database.charset.auto_fix") {
		charsetConfig.AutoFix = viper.GetBool("database.charset.auto_fix")
	}

	if viper.IsSet("database.charset.check_on_startup") {
		charsetConfig.CheckOnStartup = viper.GetBool("database.charset.check_on_startup")
	}

	if viper.IsSet("database.charset.check_after_migration") {
		charsetConfig.CheckAfterMigration = viper.GetBool("database.charset.check_after_migration")
	}

	if viper.IsSet("database.charset.critical_tables") {
		charsetConfig.CriticalTables = viper.GetStringSlice("database.charset.critical_tables")
	}

	// 验证配置
	if err := charsetConfig.Validate(); err != nil {
		log.Warnw("Invalid charset config, using defaults", "error", err)
		return config.DefaultDatabaseCharsetConfig()
	}

	return charsetConfig
}

// ensureDatabaseCharset 确保数据库使用正确的字符集
func ensureDatabaseCharset(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	log.Infow("Ensuring database charset...",
		"target_charset", charsetConfig.TargetCharset,
		"target_collation", charsetConfig.TargetCollation)

	// 检查数据库字符集
	currentCharset, currentCollation, err := config.GetDatabaseCharsetInfo(db)
	if err != nil {
		return fmt.Errorf("failed to check database charset: %v", err)
	}

	log.Infow("Current database charset",
		"charset", currentCharset,
		"collation", currentCollation)

	// 如果字符集不是目标字符集，则修复
	if currentCharset != charsetConfig.TargetCharset {
		log.Infow("Database charset needs to be updated",
			"from", currentCharset,
			"to", charsetConfig.TargetCharset)

		// 修复数据库字符集
		alterSQL := charsetConfig.GetAlterDatabaseSQL()
		if err := db.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("failed to update database charset: %v", err)
		}

		log.Infow("Database charset updated successfully")
	}

	// 检查并修复关键表的字符集
	for _, tableName := range charsetConfig.CriticalTables {
		if err := ensureTableCharset(db, tableName, charsetConfig); err != nil {
			log.Warnw("Failed to ensure table charset", "table", tableName, "error", err)
			continue
		}
	}

	return nil
}

// ensureTableCharset 确保表使用正确的字符集
func ensureTableCharset(db *gorm.DB, tableName string, charsetConfig *config.DatabaseCharsetConfig) error {
	// 检查表是否存在
	var count int64
	err := db.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", tableName).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("failed to check table existence: %v", err)
	}

	if count == 0 {
		log.Infow("Table does not exist, skipping charset check", "table", tableName)
		return nil
	}

	// 检查表字符集
	currentCharset, currentCollation, err := config.GetTableCharsetInfo(db, tableName)
	if err != nil {
		return fmt.Errorf("failed to check table charset: %v", err)
	}

	log.Infow("Table charset info",
		"table", tableName,
		"charset", currentCharset,
		"collation", currentCollation)

	// 如果表字符集不是目标字符集，则修复
	if currentCharset != charsetConfig.TargetCharset {
		log.Infow("Updating table charset",
			"table", tableName,
			"from", currentCharset,
			"to", charsetConfig.TargetCharset)

		// 修复表字符集
		alterSQL := charsetConfig.GetAlterTableSQL(tableName)
		if err := db.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("failed to update table charset: %v", err)
		}

		log.Infow("Table charset updated successfully", "table", tableName)
	}

	// 特别处理chat_message表的content字段
	if tableName == "chat_message" {
		if err := ensureContentFieldCharset(db, charsetConfig); err != nil {
			log.Warnw("Failed to ensure content field charset", "error", err)
		}
	}

	return nil
}

// ensureContentFieldCharset 确保content字段使用正确的字符集
func ensureContentFieldCharset(db *gorm.DB, charsetConfig *config.DatabaseCharsetConfig) error {
	// 检查content字段是否存在
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) 
		FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = 'chat_message' 
			AND COLUMN_NAME = 'content'
	`).Scan(&count).Error

	if err != nil {
		return fmt.Errorf("failed to check content field existence: %v", err)
	}

	if count == 0 {
		log.Infow("Content field does not exist, skipping charset check")
		return nil
	}

	// 检查content字段字符集
	currentCharset, currentCollation, err := config.GetColumnCharsetInfo(db, "chat_message", "content")
	if err != nil {
		return fmt.Errorf("failed to check content field charset: %v", err)
	}

	log.Infow("Content field charset info",
		"charset", currentCharset,
		"collation", currentCollation)

	// 如果字段字符集不是目标字符集，则修复
	if currentCharset != charsetConfig.TargetCharset {
		log.Infow("Updating content field charset",
			"from", currentCharset,
			"to", charsetConfig.TargetCharset)

		// 修复字段字符集
		alterSQL := charsetConfig.GetAlterColumnSQL("chat_message", "content", "TEXT")
		if err := db.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("failed to update content field charset: %v", err)
		}

		log.Infow("Content field charset updated successfully")
	}

	return nil
}

// initUploadDirectories 初始化上传目录
func initUploadDirectories() error {
	imagePath := viper.GetString("resource.image_path")
	if imagePath == "" {
		imagePath = "/opt/numind/image/upload" // 默认路径
	}

	// 创建图片上传目录
	uploadDirs := []string{
		imagePath,
		filepath.Join(imagePath, "avatars"),
		filepath.Join(imagePath, "card"),
		filepath.Join(imagePath, "book"),
	}

	for _, dir := range uploadDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Errorw("Failed to create upload directory", "dir", dir, "error", err.Error())
			return fmt.Errorf("failed to create upload directory %s: %v", dir, err)
		}
		log.Infow("Created upload directory", "dir", dir)
	}

	return nil
}

// InitCOS prints COS status on startup for visibility
func InitCOS() {
	if util.IsCOSEnabled() {
		log.Infow("Tencent COS enabled", "bucket", viper.GetString("cos.bucket"), "region", viper.GetString("cos.region"))
	} else {
		log.Infow("Tencent COS disabled or not configured")
	}
}

// migrateWecomUsersTable 处理 wecom_users 表的字段重命名 (numind_user_id -> user_id)
// 这是一个自定义的迁移函数，因为 GORM's AutoMigrate 不支持重命名列。
func migrateWecomUsersTable(db *gorm.DB) error {
	tableName := "wecom_users"

	// 1. 检查表是否存在
	if !db.Migrator().HasTable(tableName) {
		log.Infow("wecom_users table does not exist, skipping rename logic", "table", tableName)
		return nil
	}

	// 2. 检查旧字段 numind_user_id 是否存在
	if db.Migrator().HasColumn(tableName, "numind_user_id") {
		// 3. 检查新字段 user_id 是否已存在 (如果已存在，可能已经更名过了)
		if !db.Migrator().HasColumn(tableName, "user_id") {
			log.Infow("Renaming numind_user_id to user_id in wecom_users table...")
			// 执行重命名 SQL
			// 注意: MySQL 8.0+ 支持 RENAME COLUMN，但为了兼容性，使用 CHANGE
			err := db.Exec(fmt.Sprintf("ALTER TABLE %s CHANGE numind_user_id user_id bigint(20)", tableName)).Error
			if err != nil {
				return fmt.Errorf("failed to rename column: %v", err)
			}
			log.Infow("Successfully renamed numind_user_id to user_id")
		} else {
			log.Infow("Both numind_user_id and user_id exist, skipping rename to avoid conflict")
		}
	} else {
		log.Debugw("numind_user_id not found in wecom_users, maybe already renamed")
	}

	return nil
}
