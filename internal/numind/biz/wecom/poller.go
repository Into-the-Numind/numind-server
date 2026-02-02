package wecom

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

// Poller 消息拉取服务
type Poller struct {
	db             *gorm.DB
	client         *Client // SDK 客户端 (可选，审核通过后启用)
	privateKeyPath string
	interval       time.Duration
}

// NewPoller 创建 Poller 实例
func NewPoller(db *gorm.DB, client *Client, privateKeyPath string, interval time.Duration) *Poller {
	return &Poller{
		db:             db,
		client:         client,
		privateKeyPath: privateKeyPath,
		interval:       interval,
	}
}

// Start 启动轮询循环
func (p *Poller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Poller stopped")
			return
		case <-ticker.C:
			if err := p.poll(); err != nil {
				log.Printf("Poll error: %v", err)
			}
		}
	}
}

// poll 执行一次拉取
func (p *Poller) poll() error {
	// Step 1: 获取当前游标
	var cursor WecomCursor
	if err := p.db.First(&cursor, "id = ?", "global_seq").Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			cursor = WecomCursor{ID: "global_seq", Seq: 0}
		} else {
			return err
		}
	}

	// Step 2: 检查 SDK 是否可用
	if p.client == nil {
		// SDK 未初始化，跳过本次拉取（等待审核通过）
		log.Println("⏳ SDK not initialized, waiting for approval...")
		return nil
	}

	// Step 3: 调用 SDK 拉取数据
	// chatDataList, err := p.client.FetchData(cursor.Seq, 1000)
	// if err != nil {
	// 	return err
	// }

	// Step 4: 解密并解析消息
	// for _, data := range chatDataList {
	// 	plainText, err := p.client.Decrypt(data.EncryptRandomKey, data.EncryptChatMsg)
	// 	if err != nil {
	// 		log.Printf("Decrypt error for msg %s: %v", data.MsgID, err)
	// 		continue
	// 	}
	//
	// 	msg, err := ParseMessage(plainText)
	// 	if err != nil {
	// 		log.Printf("Parse error for msg %s: %v", data.MsgID, err)
	// 		continue
	// 	}
	//
	// 	// 保存消息
	// 	if err := p.saveMessage(msg); err != nil {
	// 		log.Printf("Save error for msg %s: %v", data.MsgID, err)
	// 		continue
	// 	}
	//
	// 	// 更新游标
	// 	if data.Seq > cursor.Seq {
	// 		cursor.Seq = data.Seq
	// 	}
	// }

	// Step 5: 保存新游标
	// if err := p.db.Save(&cursor).Error; err != nil {
	// 	return err
	// }

	return nil
}

// saveMessage 保存消息到数据库
func (p *Poller) saveMessage(msg *WecomMessage) error {
	// 使用 UPSERT 避免重复插入
	return p.db.Save(msg).Error
}

// ensureUserExists 确保外部用户记录存在
func (p *Poller) ensureUserExists(externalUserID string) error {
	var user WecomUser
	err := p.db.First(&user, "id = ?", externalUserID).Error
	if err == gorm.ErrRecordNotFound {
		// 创建新用户记录（未绑定状态）
		user = WecomUser{
			ID:   externalUserID,
			Name: "", // 稍后通过 API 获取
		}
		return p.db.Create(&user).Error
	}
	return err
}
