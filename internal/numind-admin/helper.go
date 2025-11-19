package numindadmin

import (
	"context"
	"errors"
	"fmt"
	"numind-server/internal/numind/biz"
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

// initSystemConfigs 从 yaml 文件读取配置，与数据库对比，如果改变了就更新数据库
func initSystemConfigs() error {
	ctx := context.Background()
	configBiz := biz.NewBiz(store.S).Configs()

	// 1. 先读取 yaml 文件中的所有配置（已经在 initConfig 中完成）
	log.Infow("Comparing yaml config with database config...")

	// 2. 获取数据库中的所有配置
	dbConfigs, err := configBiz.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to get configs from database: %w", err)
	}

	// 3. 将数据库配置转换为 map，方便查找
	dbConfigMap := make(map[string]*model.SystemConfigM)
	for i := range dbConfigs {
		dbConfigMap[dbConfigs[i].Key] = dbConfigs[i]
	}

	// 4. 获取 yaml 文件中的所有配置键
	yamlKeys := viper.AllKeys()
	updatedCount := 0
	createdCount := 0

	// 5. 对比 yaml 和数据库配置
	for _, key := range yamlKeys {
		// 跳过敏感信息和复杂结构
		if shouldSkipConfig(key) {
			continue
		}

		yamlValue := viper.Get(key)
		if !isSimpleValue(yamlValue) {
			continue
		}

		yamlValueStr := fmt.Sprintf("%v", yamlValue)

		// 检查数据库中是否存在该配置
		dbConfig, exists := dbConfigMap[key]
		if !exists {
			// 数据库中没有，创建新配置
			_, err := configBiz.Create(ctx, key, "", yamlValueStr, fmt.Sprintf("Loaded from yaml file: %s", viper.ConfigFileUsed()))
			if err != nil {
				log.Warnw("Failed to create config in database", "key", key, "error", err)
				continue
			}
			createdCount++
			log.Infow("Created new config in database", "key", key)
		} else {
			// 数据库中存在，对比值是否改变
			if dbConfig.Value != yamlValueStr {
				// 值改变了，更新数据库
				_, err := configBiz.Update(ctx, key, yamlValueStr, fmt.Sprintf("Updated from yaml file: %s", viper.ConfigFileUsed()))
				if err != nil {
					log.Warnw("Failed to update config in database", "key", key, "error", err)
					continue
				}
				updatedCount++
				log.Infow("Updated config in database", "key", key, "old_value", dbConfig.Value, "new_value", yamlValueStr)
			}
		}
	}

	log.Infow("Config sync completed", "created", createdCount, "updated", updatedCount, "total_in_db", len(dbConfigs))

	// 6. 重新从数据库加载所有配置到 viper（确保使用数据库中的最新值）
	finalConfigs, err := configBiz.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("failed to reload configs from database: %w", err)
	}

	log.Infow("Loading configs from database to viper...")
	for _, cfg := range finalConfigs {
		viper.Set(cfg.Key, cfg.Value)
		log.Debugw("Loaded config from database", "key", cfg.Key)
	}

	return nil
}

// shouldSkipConfig 判断是否应该跳过某个配置（敏感信息不保存到数据库）
func shouldSkipConfig(key string) bool {
	skipKeys := []string{
		"db.password",
		"jwt.secret",
		"wechat.app_secret",
		"wechat.mch_api_v3_key",
		"cos.secret_key",
		"baidu.secret_key",
		"ali.api_key",
		"ali.image.api_key",
	}

	for _, skipKey := range skipKeys {
		if strings.Contains(key, skipKey) {
			return true
		}
	}
	return false
}

// isSimpleValue 判断是否为简单值（字符串、数字、布尔值）
func isSimpleValue(value interface{}) bool {
	switch value.(type) {
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return true
	default:
		return false
	}
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
