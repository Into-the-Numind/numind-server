package langfuse

import "github.com/spf13/viper"

// Config Langfuse 配置
type Config struct {
	Enabled   bool   `mapstructure:"enabled"`
	BaseURL   string `mapstructure:"base_url"`
	PublicKey string `mapstructure:"public_key"`
	SecretKey string `mapstructure:"secret_key"`
}

// LoadConfig 从 Viper 读取 langfuse 配置
func LoadConfig() *Config {
	return &Config{
		Enabled:   viper.GetBool("langfuse.enabled"),
		BaseURL:   viper.GetString("langfuse.base_url"),
		PublicKey: viper.GetString("langfuse.public_key"),
		SecretKey: viper.GetString("langfuse.secret_key"),
	}
}
