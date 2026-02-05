package config

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// DatabaseCharsetConfig 数据库字符集配置
type DatabaseCharsetConfig struct {
	// 目标字符集
	TargetCharset string `yaml:"target_charset" default:"utf8mb4"`
	// 目标排序规则
	TargetCollation string `yaml:"target_collation" default:"utf8mb4_unicode_ci"`
	// 是否自动修复字符集
	AutoFix bool `yaml:"auto_fix" default:"true"`
	// 关键表列表
	CriticalTables []string `yaml:"critical_tables"`
	// 是否在启动时检查
	CheckOnStartup bool `yaml:"check_on_startup" default:"true"`
	// 是否在迁移后检查
	CheckAfterMigration bool `yaml:"check_after_migration" default:"true"`
}

// DefaultDatabaseCharsetConfig 返回默认的数据库字符集配置
func DefaultDatabaseCharsetConfig() *DatabaseCharsetConfig {
	return &DatabaseCharsetConfig{
		TargetCharset:       "utf8mb4",
		TargetCollation:     "utf8mb4_unicode_ci",
		AutoFix:             true,
		CheckOnStartup:      true,
		CheckAfterMigration: true,
		CriticalTables: []string{
			"chat_message",
			"chat_session",
			"book",
			"user",
			"card",
			"category",
			"image",
			"template",
			"feedback",
			"article",
			"admin",
			"account_record",
			"knowledge_document",
			"knowledge_chunk",
			"sales_session",
			"sales_message",
			"language_style",
			"wecom_user",
			"wecom_message",
		},
	}
}

// Validate 验证配置
func (c *DatabaseCharsetConfig) Validate() error {
	if c.TargetCharset == "" {
		return fmt.Errorf("target charset cannot be empty")
	}

	if c.TargetCollation == "" {
		return fmt.Errorf("target collation cannot be empty")
	}

	// 验证字符集和排序规则的匹配性
	if !strings.HasPrefix(c.TargetCollation, c.TargetCharset) {
		return fmt.Errorf("collation %s does not match charset %s", c.TargetCollation, c.TargetCharset)
	}

	return nil
}

// GetCharsetSQL 获取字符集SQL语句
func (c *DatabaseCharsetConfig) GetCharsetSQL() string {
	return fmt.Sprintf("CHARACTER SET %s COLLATE %s", c.TargetCharset, c.TargetCollation)
}

// GetAlterDatabaseSQL 获取修改数据库字符集的SQL
func (c *DatabaseCharsetConfig) GetAlterDatabaseSQL() string {
	return fmt.Sprintf("ALTER DATABASE CHARACTER SET %s COLLATE %s", c.TargetCharset, c.TargetCollation)
}

// GetAlterTableSQL 获取修改表字符集的SQL
func (c *DatabaseCharsetConfig) GetAlterTableSQL(tableName string) string {
	return fmt.Sprintf("ALTER TABLE %s CONVERT TO CHARACTER SET %s COLLATE %s",
		tableName, c.TargetCharset, c.TargetCollation)
}

// GetAlterColumnSQL 获取修改列字符集的SQL
func (c *DatabaseCharsetConfig) GetAlterColumnSQL(tableName, columnName, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s CHARACTER SET %s COLLATE %s",
		tableName, columnName, dataType, c.TargetCharset, c.TargetCollation)
}

// IsCriticalTable 检查是否为关键表
func (c *DatabaseCharsetConfig) IsCriticalTable(tableName string) bool {
	for _, table := range c.CriticalTables {
		if table == tableName {
			return true
		}
	}
	return false
}

// GetTableCharsetInfo 获取表字符集信息
func GetTableCharsetInfo(db *gorm.DB, tableName string) (charset, collation string, err error) {
	var result struct {
		TableCollation string `gorm:"column:TABLE_COLLATION"`
	}

	err = db.Raw(`
		SELECT TABLE_COLLATION
		FROM information_schema.TABLES 
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
	`, tableName).Scan(&result).Error

	if err != nil {
		return "", "", err
	}

	// 从排序规则中提取字符集
	if strings.Contains(result.TableCollation, "_") {
		parts := strings.Split(result.TableCollation, "_")
		charset = parts[0]
	} else {
		charset = result.TableCollation
	}

	return charset, result.TableCollation, nil
}

// GetColumnCharsetInfo 获取列字符集信息
func GetColumnCharsetInfo(db *gorm.DB, tableName, columnName string) (charset, collation string, err error) {
	var result struct {
		CharacterSetName string `gorm:"column:CHARACTER_SET_NAME"`
		CollationName    string `gorm:"column:COLLATION_NAME"`
	}

	err = db.Raw(`
		SELECT CHARACTER_SET_NAME, COLLATION_NAME
		FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
			AND TABLE_NAME = ? 
			AND COLUMN_NAME = ?
	`, tableName, columnName).Scan(&result).Error

	if err != nil {
		return "", "", err
	}

	return result.CharacterSetName, result.CollationName, nil
}

// GetDatabaseCharsetInfo 获取数据库字符集信息
func GetDatabaseCharsetInfo(db *gorm.DB) (charset, collation string, err error) {
	var result struct {
		DefaultCharacterSetName string `gorm:"column:DEFAULT_CHARACTER_SET_NAME"`
		DefaultCollationName    string `gorm:"column:DEFAULT_COLLATION_NAME"`
	}

	err = db.Raw(`
		SELECT 
			DEFAULT_CHARACTER_SET_NAME,
			DEFAULT_COLLATION_NAME
		FROM information_schema.SCHEMATA 
		WHERE SCHEMA_NAME = DATABASE()
	`).Scan(&result).Error

	if err != nil {
		return "", "", err
	}

	return result.DefaultCharacterSetName, result.DefaultCollationName, nil
}
