package main

import (
	"fmt"
	"log"
	"os"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// User 用户模型（简化版）
type User struct {
	ID               uint   `gorm:"primaryKey"`
	MembershipType   string `gorm:"size:20"`
	SubscriptionType string `gorm:"size:20"`
}

func main() {
	// 从环境变量获取数据库连接信息
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		// 默认连接字符串，请根据实际情况修改
		dsn = "root:password@tcp(localhost:3306)/numind?charset=utf8mb4&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 检查迁移前的数据
	fmt.Println("=== 迁移前的数据统计 ===")
	var beforeStats []struct {
		MembershipType   string
		SubscriptionType string
		Count            int64
	}
	db.Table("user").Select("membership_type, subscription_type, COUNT(*) as count").
		Group("membership_type, subscription_type").
		Order("membership_type, subscription_type").
		Scan(&beforeStats)

	for _, stat := range beforeStats {
		fmt.Printf("membership_type: %s, subscription_type: %s, count: %d\n",
			stat.MembershipType, stat.SubscriptionType, stat.Count)
	}

	// 执行迁移
	fmt.Println("\n=== 开始数据迁移 ===")

	// 迁移 monthly 类型
	result1 := db.Model(&User{}).Where("membership_type = ?", "monthly").
		Updates(map[string]interface{}{
			"membership_type":   "subscription",
			"subscription_type": "monthly",
		})
	if result1.Error != nil {
		log.Printf("Error migrating monthly users: %v", result1.Error)
	} else {
		fmt.Printf("迁移 monthly 用户: %d 条记录\n", result1.RowsAffected)
	}

	// 迁移 yearly 类型
	result2 := db.Model(&User{}).Where("membership_type = ?", "yearly").
		Updates(map[string]interface{}{
			"membership_type":   "subscription",
			"subscription_type": "yearly",
		})
	if result2.Error != nil {
		log.Printf("Error migrating yearly users: %v", result2.Error)
	} else {
		fmt.Printf("迁移 yearly 用户: %d 条记录\n", result2.RowsAffected)
	}

	// 检查迁移后的数据
	fmt.Println("\n=== 迁移后的数据统计 ===")
	var afterStats []struct {
		MembershipType   string
		SubscriptionType string
		Count            int64
	}
	db.Table("user").Select("membership_type, subscription_type, COUNT(*) as count").
		Group("membership_type, subscription_type").
		Order("membership_type, subscription_type").
		Scan(&afterStats)

	for _, stat := range afterStats {
		fmt.Printf("membership_type: %s, subscription_type: %s, count: %d\n",
			stat.MembershipType, stat.SubscriptionType, stat.Count)
	}

	// 检查是否还有遗漏的数据
	var remaining []User
	db.Where("membership_type IN ?", []string{"monthly", "yearly"}).Find(&remaining)
	if len(remaining) > 0 {
		fmt.Printf("\n警告: 还有 %d 条记录未迁移:\n", len(remaining))
		for _, user := range remaining {
			fmt.Printf("ID: %d, membership_type: %s\n", user.ID, user.MembershipType)
		}
	} else {
		fmt.Println("\n✅ 所有数据迁移完成!")
	}
}
