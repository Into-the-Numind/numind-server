package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 应用配置结构
type Config struct {
	App         AppConfig        `mapstructure:"app"`
	Server      ServerConfig     `mapstructure:"server"`
	Database    DatabaseConfig   `mapstructure:"database"`
	Redis       RedisConfig      `mapstructure:"redis"`
	JWT         JWTConfig        `mapstructure:"jwt"`
	Wechat      WechatConfig     `mapstructure:"wechat"`
	OSS         OSSConfig        `mapstructure:"oss"`
	OpenAI      OpenAIConfig     `mapstructure:"openai"`
	CORS        CORSConfig       `mapstructure:"cors"`
	Security    SecurityConfig   `mapstructure:"security"`
	Logging     LoggingConfig    `mapstructure:"logging"`
	Monitoring  MonitoringConfig `mapstructure:"monitoring"`
	Environment string           `mapstructure:"environment"`
}

// AppConfig 应用基本信息配置
type AppConfig struct {
	Name        string `mapstructure:"name"`
	Version     string `mapstructure:"version"`
	Description string `mapstructure:"description"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port           string        `mapstructure:"port"`
	ReadTimeout    time.Duration `mapstructure:"read_timeout"`
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`
	MaxHeaderBytes int           `mapstructure:"max_header_bytes"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Name            string        `mapstructure:"name"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
}

// RedisConfig Redis 配置
type RedisConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	Password     string `mapstructure:"password"`
	DB           int    `mapstructure:"db"`
	PoolSize     int    `mapstructure:"pool_size"`
	MinIdleConns int    `mapstructure:"min_idle_conns"`
}

// JWTConfig JWT 配置
type JWTConfig struct {
	Secret string `mapstructure:"secret"`
}

// WechatConfig 微信小程序配置
type WechatConfig struct {
	AppID     string `mapstructure:"app_id"`
	AppSecret string `mapstructure:"app_secret"`
}

// OSSConfig 对象存储配置
type OSSConfig struct {
	Endpoint        string `mapstructure:"endpoint"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	AccessKeySecret string `mapstructure:"access_key_secret"`
	BucketName      string `mapstructure:"bucket_name"`
}

// OpenAIConfig OpenAI 配置
type OpenAIConfig struct {
	APIKey     string        `mapstructure:"api_key"`
	BaseURL    string        `mapstructure:"base_url"`
	Timeout    time.Duration `mapstructure:"timeout"`
	MaxRetries int           `mapstructure:"max_retries"`
}

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowedOrigins   []string      `mapstructure:"allowed_origins"`
	AllowedMethods   []string      `mapstructure:"allowed_methods"`
	AllowedHeaders   []string      `mapstructure:"allowed_headers"`
	AllowCredentials bool          `mapstructure:"allow_credentials"`
	MaxAge           time.Duration `mapstructure:"max_age"`
}

// SecurityConfig 安全配置
type SecurityConfig struct {
	RateLimitRequests int           `mapstructure:"rate_limit_requests"`
	RateLimitWindow   time.Duration `mapstructure:"rate_limit_window"`
	JWTExpireHours    int           `mapstructure:"jwt_expire_hours"`
}

// LoggingConfig 日志配置
type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	Output     string `mapstructure:"output"`
	FilePath   string `mapstructure:"file_path"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxAge     int    `mapstructure:"max_age"`
	MaxBackups int    `mapstructure:"max_backups"`
	Compress   bool   `mapstructure:"compress"`
}

// MonitoringConfig 监控配置
type MonitoringConfig struct {
	Enabled             bool          `mapstructure:"enabled"`
	MetricsPort         int           `mapstructure:"metrics_port"`
	HealthCheckInterval time.Duration `mapstructure:"health_check_interval"`
	HealthCheckTimeout  time.Duration `mapstructure:"health_check_timeout"`
}

// Load 加载配置
func Load() *Config {
	// 获取环境变量
	env := getEnvironment()

	// 设置 Viper 配置
	v := viper.New()

	// 设置配置文件路径
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")

	// 设置环境变量前缀
	v.SetEnvPrefix("NUMIND")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		panic(fmt.Errorf("failed to read config file: %w", err))
	}

	// 合并公共配置和环境特定配置
	config := &Config{}

	// 首先加载公共配置
	if err := v.UnmarshalKey("common", config); err != nil {
		panic(fmt.Errorf("failed to unmarshal common config: %w", err))
	}

	// 然后加载环境特定配置并覆盖公共配置
	if err := v.UnmarshalKey(env, config); err != nil {
		panic(fmt.Errorf("failed to unmarshal %s config: %w", env, err))
	}

	// 设置环境标识
	config.Environment = env

	// 验证配置
	if err := validateConfig(config); err != nil {
		panic(fmt.Errorf("config validation failed: %w", err))
	}

	return config
}

// getEnvironment 获取当前环境
func getEnvironment() string {
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "development"
	}

	// 支持的环境列表
	validEnvs := []string{"development", "qa", "production"}
	for _, validEnv := range validEnvs {
		if env == validEnv {
			return env
		}
	}

	// 如果环境无效，默认使用 development
	return "development"
}

// validateConfig 验证配置
func validateConfig(config *Config) error {
	// 验证服务器配置
	if config.Server.Port == "" {
		return fmt.Errorf("server port is required")
	}

	// 验证数据库配置
	if config.Database.Host == "" {
		return fmt.Errorf("database host is required")
	}
	if config.Database.User == "" {
		return fmt.Errorf("database user is required")
	}
	if config.Database.Name == "" {
		return fmt.Errorf("database name is required")
	}

	// 验证 JWT 配置
	if config.JWT.Secret == "" {
		return fmt.Errorf("JWT secret is required")
	}

	// 验证 Redis 配置
	if config.Redis.Host == "" {
		return fmt.Errorf("Redis host is required")
	}

	return nil
}

// GetDSN 获取数据库连接字符串
func (c *Config) GetDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		c.Database.User,
		c.Database.Password,
		c.Database.Host,
		c.Database.Port,
		c.Database.Name,
	)
}

// GetRedisAddr 获取 Redis 地址
func (c *Config) GetRedisAddr() string {
	return fmt.Sprintf("%s:%d", c.Redis.Host, c.Redis.Port)
}

// IsDevelopment 是否为开发环境
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development"
}

// IsProduction 是否为生产环境
func (c *Config) IsProduction() bool {
	return c.Environment == "production"
}

// IsQA 是否为 QA 环境
func (c *Config) IsQA() bool {
	return c.Environment == "qa"
}

// GetLogLevel 获取日志级别
func (c *Config) GetLogLevel() string {
	if c.Logging.Level == "" {
		return "info"
	}
	return c.Logging.Level
}

// GetCORSAllowedOrigins 获取 CORS 允许的源
func (c *Config) GetCORSAllowedOrigins() []string {
	if len(c.CORS.AllowedOrigins) == 0 {
		return []string{"*"}
	}
	return c.CORS.AllowedOrigins
}

// GetCORSAllowedMethods 获取 CORS 允许的方法
func (c *Config) GetCORSAllowedMethods() []string {
	if len(c.CORS.AllowedMethods) == 0 {
		return []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	}
	return c.CORS.AllowedMethods
}

// GetCORSAllowedHeaders 获取 CORS 允许的头部
func (c *Config) GetCORSAllowedHeaders() []string {
	if len(c.CORS.AllowedHeaders) == 0 {
		return []string{"Origin", "Content-Type", "Accept", "Authorization"}
	}
	return c.CORS.AllowedHeaders
}
