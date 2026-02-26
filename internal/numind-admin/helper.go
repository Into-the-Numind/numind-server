package numindadmin

import (
	"context"
	"errors"
	"fmt"
	configbiz "numind-server/internal/numind/biz/config"
	dbconfig "numind-server/internal/numind/config"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/redis"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/pkg/auth"
	"numind-server/pkg/db"
)

const (
	recommendedHomeDir = ".numind"
	defaultConfigName  = "config.yaml"
)

// initConfig 设置需要读取的配置文件名、环境变量，并读取配置文件内容到 viper 中
func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(filepath.Join(home, recommendedHomeDir))
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(defaultConfigName)
	}

	viper.AutomaticEnv()
	viper.SetEnvPrefix("NUMIND")
	replacer := strings.NewReplacer(".", "_")
	viper.SetEnvKeyReplacer(replacer)

	if err := viper.ReadInConfig(); err != nil {
		log.Errorw("Failed to read viper configuration file", "err", err)
	}

	log.Debugw("Using config file", "file", viper.ConfigFileUsed())
}

// logOptions 从 viper 中读取日志配置
func logOptions() *log.Options {
	return &log.Options{
		DisableCaller:     viper.GetBool("log.disable-caller"),
		DisableStacktrace: viper.GetBool("log.disable-stacktrace"),
		Level:             viper.GetString("log.level"),
		Format:            viper.GetString("log.format"),
		OutputPaths:       viper.GetStringSlice("log.output-paths"),
	}
}

// initStore 读取 db 配置，创建 gorm.DB 实例，并初始化 store 层
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

	// 初始化Redis（后台管理系统也需要Redis来更新配置）
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

	charsetConfig := getDatabaseCharsetConfig()

	log.Infow("Starting database charset verification and repair...")
	if err := forceEnsureDatabaseCharset(db, charsetConfig); err != nil {
		log.Warnw("Failed to ensure database charset, continuing with migration", "error", err)
	} else {
		log.Infow("Database charset verification and repair completed")
	}

	// 检查并删除 system_config 表（如果存在），以便统一字段类型
	if err := checkAndDropSystemConfigTable(db); err != nil {
		log.Warnw("Failed to check/drop system_config table, continuing with migration", "error", err)
	}

	log.Infow("Starting database schema migration...")
	err := db.AutoMigrate(
		&model.User{},
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
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %v", err)
	}
	log.Infow("Database schema migration completed")

	// 创建默认管理员账户
	if err := initDefaultAdmin(db); err != nil {
		log.Warnw("Failed to init default admin", "error", err)
	}

	return nil
}

// checkAndDropSystemConfigTable 检查 system_config 表是否存在，如果存在则删除
func checkAndDropSystemConfigTable(db *gorm.DB) error {
	tableName := "system_config"

	// 检查表是否存在
	var count int64
	err := db.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", tableName).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("failed to check table existence: %w", err)
	}

	if count == 0 {
		log.Infow("system_config table does not exist, skipping drop", "table", tableName)
		return nil
	}

	// 表存在，删除它
	log.Infow("system_config table exists, dropping it to unify field types", "table", tableName)
	if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName)).Error; err != nil {
		return fmt.Errorf("failed to drop system_config table: %w", err)
	}

	log.Infow("system_config table dropped successfully", "table", tableName)
	return nil
}

// initDefaultAdmin 初始化默认管理员账户
func initDefaultAdmin(db *gorm.DB) error {
	ctx := context.Background()
	adminStore := store.NewAdminAccountStore(db)

	// 检查是否已存在 admin 用户
	_, err := adminStore.GetByUsername(ctx, "admin")
	if err == nil {
		// 已存在，不需要创建
		log.Infow("Default admin account already exists")
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to check admin account: %w", err)
	}

	// 创建默认管理员账户
	hashedPassword, err := auth.Encrypt("admin123456")
	if err != nil {
		return fmt.Errorf("failed to encrypt password: %w", err)
	}

	defaultAdmin := &model.Admin{
		Username: "admin",
		Password: hashedPassword,
		Nickname: "系统管理员",
		Status:   model.AdminStatusEnabled,
		Remark:   "默认管理员账户",
	}

	if err := adminStore.Create(ctx, defaultAdmin); err != nil {
		return fmt.Errorf("failed to create default admin: %w", err)
	}

	log.Infow("Default admin account created successfully", "username", "admin")
	return nil
}


// forceEnsureDatabaseCharset 强制确保数据库使用正确的字符集
func forceEnsureDatabaseCharset(db *gorm.DB, charsetConfig *dbconfig.DatabaseCharsetConfig) error {
	if err := db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Warnw("Failed to set connection charset", "error", err)
	}

	alterSQL := charsetConfig.GetAlterDatabaseSQL()
	if err := db.Exec(alterSQL).Error; err != nil {
		log.Warnw("Failed to update database charset", "error", err)
	}

	return nil
}

// getDatabaseCharsetConfig 获取数据库字符集配置
func getDatabaseCharsetConfig() *dbconfig.DatabaseCharsetConfig {
	charsetConfig := dbconfig.DefaultDatabaseCharsetConfig()

	if viper.IsSet("database.charset.target_charset") {
		charsetConfig.TargetCharset = viper.GetString("database.charset.target_charset")
	}

	if viper.IsSet("database.charset.target_collation") {
		charsetConfig.TargetCollation = viper.GetString("database.charset.target_collation")
	}

	return charsetConfig
}

// InitCOS 初始化 COS
func InitCOS() {
	// 后台管理系统可能不需要 COS，这里简化处理
	log.Infow("COS initialization skipped for admin system")
}
