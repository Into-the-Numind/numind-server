package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// ConfigDefinition 配置项定义
type ConfigDefinition struct {
	Key          string // 配置键
	Title        string // 配置标题，用于后台管理系统显示
	DefaultValue string // 默认值（从配置文件读取）
	Description  string // 配置详细描述
}

// GetManagedConfigDefinitions 获取需要管理的配置项定义列表
// 这些配置项会在程序启动时与数据库同步
// 包含 volc、ali 节点下的 AI 工作台相关配置项
func GetManagedConfigDefinitions() []ConfigDefinition {
	return []ConfigDefinition{
		// 火山引擎配置
		{
			Key:          "volc.api_key",
			Title:        "火山引擎API密钥",
			DefaultValue: getDefaultValue("volc.api_key", ""),
			Description:  "火山引擎API访问密钥，用于身份验证。",
		},
		{
			Key:          "volc.base_url",
			Title:        "火山引擎API地址",
			DefaultValue: getDefaultValue("volc.base_url", "https://ark.cn-beijing.volces.com/api/v3"),
			Description:  "火山引擎API的基础URL地址。",
		},
		{
			Key:          "volc.model",
			Title:        "火山引擎模型",
			DefaultValue: getDefaultValue("volc.model", "doubao-seed-1-6-flash-250828"),
			Description:  "火山引擎文本生成模型名称。用于指定调用哪个AI模型来处理文本内容。",
		},
		{
			Key:          "volc.temperature",
			Title:        "火山引擎温度参数",
			DefaultValue: getDefaultValue("volc.temperature", "0.5"),
			Description:  "火山引擎模型温度参数，控制生成文本的随机性。取值范围：0-1，值越大随机性越高，值越小输出越确定。",
		},
		{
			Key:          "volc.tokens",
			Title:        "火山引擎Token数量",
			DefaultValue: getDefaultValue("volc.tokens", "2000"),
			Description:  "火山引擎模型最大Token数量。限制AI生成文本的最大长度，单位：Token。",
		},
		{
			Key:          "volc.timeout",
			Title:        "火山引擎超时时间",
			DefaultValue: getDefaultValue("volc.timeout", "180s"),
			Description:  "火山引擎API调用的超时时间。",
		},
		{
			Key:          "volc.max_retries",
			Title:        "火山引擎最大重试次数",
			DefaultValue: getDefaultValue("volc.max_retries", "3"),
			Description:  "火山引擎API调用失败时的最大重试次数。",
		},
		// 阿里云配置 - 通用配置
		{
			Key:          "ali.api_key",
			Title:        "阿里云API密钥",
			DefaultValue: getDefaultValue("ali.api_key", ""),
			Description:  "阿里云文本服务API密钥，用于身份验证。",
		},
		// 阿里云文本生成服务（火山引擎降级备选）
		{
			Key:          "ali.text.model",
			Title:        "阿里云文本模型",
			DefaultValue: getDefaultValue("ali.text.model", "qwen-turbo"),
			Description:  "阿里云文本生成模型名称。当火山引擎调用失败时，会降级使用此模型。",
		},
		{
			Key:          "ali.text.timeout",
			Title:        "阿里云文本生成超时时间",
			DefaultValue: getDefaultValue("ali.text.timeout", "180s"),
			Description:  "阿里云文本生成服务的超时时间。",
		},
	}
}

// getDefaultValue 从配置文件读取默认值，如果不存在则使用fallback
func getDefaultValue(key string, fallback string) string {
	if viper.IsSet(key) {
		// 根据类型获取值
		switch v := viper.Get(key).(type) {
		case string:
			return v
		case int:
			return fmt.Sprintf("%d", v)
		case float64:
			return fmt.Sprintf("%.2f", v)
		case bool:
			return fmt.Sprintf("%t", v)
		default:
			// 尝试作为字符串获取
			return viper.GetString(key)
		}
	}
	return fallback
}
