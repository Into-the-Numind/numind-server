package wechat

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// CertificateManager 证书管理器
type CertificateManager struct {
	CertPath         string
	PrivateKeyPath   string
	ExpectedSerialNo string
	BackupDir        string
}

// NewCertificateManager 创建证书管理器
func NewCertificateManager(certPath, privateKeyPath, expectedSerialNo string) *CertificateManager {
	return &CertificateManager{
		CertPath:         certPath,
		PrivateKeyPath:   privateKeyPath,
		ExpectedSerialNo: expectedSerialNo,
		BackupDir:        filepath.Join(filepath.Dir(certPath), "backup"),
	}
}

// CheckCertificateHealth 检查证书健康状态
func (cm *CertificateManager) CheckCertificateHealth() (*CertificateInfo, error) {
	return ValidateCertificate(cm.CertPath, cm.ExpectedSerialNo)
}

// BackupCertificate 备份当前证书
func (cm *CertificateManager) BackupCertificate() error {
	// 创建备份目录
	if err := os.MkdirAll(cm.BackupDir, 0755); err != nil {
		return fmt.Errorf("创建备份目录失败: %v", err)
	}

	// 备份证书文件
	timestamp := time.Now().Format("20060102_150405")
	certBackupPath := filepath.Join(cm.BackupDir, fmt.Sprintf("cert_%s.pem", timestamp))

	if err := copyFile(cm.CertPath, certBackupPath); err != nil {
		return fmt.Errorf("备份证书失败: %v", err)
	}

	// 备份私钥文件
	keyBackupPath := filepath.Join(cm.BackupDir, fmt.Sprintf("key_%s.pem", timestamp))
	if err := copyFile(cm.PrivateKeyPath, keyBackupPath); err != nil {
		return fmt.Errorf("备份私钥失败: %v", err)
	}

	log.Printf("证书已备份到: %s", cm.BackupDir)
	return nil
}

// UpdateCertificate 更新证书
func (cm *CertificateManager) UpdateCertificate(newCertPath, newKeyPath string) error {
	// 备份当前证书
	if err := cm.BackupCertificate(); err != nil {
		return fmt.Errorf("备份当前证书失败: %v", err)
	}

	// 验证新证书
	newCertInfo, err := ValidateCertificate(newCertPath, cm.ExpectedSerialNo)
	if err != nil {
		return fmt.Errorf("新证书验证失败: %v", err)
	}

	// 检查新证书是否即将过期
	if newCertInfo.DaysToExpire <= 180 {
		log.Printf("警告: 新证书将在 %d 天后过期", newCertInfo.DaysToExpire)
	}

	// 更新证书文件
	if err := copyFile(newCertPath, cm.CertPath); err != nil {
		return fmt.Errorf("更新证书文件失败: %v", err)
	}

	// 更新私钥文件
	if err := copyFile(newKeyPath, cm.PrivateKeyPath); err != nil {
		return fmt.Errorf("更新私钥文件失败: %v", err)
	}

	// 设置正确的文件权限
	if err := os.Chmod(cm.PrivateKeyPath, 0600); err != nil {
		return fmt.Errorf("设置私钥权限失败: %v", err)
	}
	if err := os.Chmod(cm.CertPath, 0644); err != nil {
		return fmt.Errorf("设置证书权限失败: %v", err)
	}

	log.Printf("证书更新成功，新证书序列号: %s", newCertInfo.SerialNumber)
	return nil
}

// MonitorCertificate 监控证书状态
func (cm *CertificateManager) MonitorCertificate(ctx context.Context, checkInterval time.Duration) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			certInfo, err := cm.CheckCertificateHealth()
			if err != nil {
				log.Printf("证书健康检查失败: %v", err)
				continue
			}

			// 检查证书是否即将过期
			if certInfo.DaysToExpire <= 30 {
				log.Printf("紧急警告: 证书将在 %d 天后过期，请立即更新", certInfo.DaysToExpire)
			} else if certInfo.DaysToExpire <= 90 {
				log.Printf("警告: 证书将在 %d 天后过期，建议及时更新", certInfo.DaysToExpire)
			} else if certInfo.DaysToExpire <= 180 {
				log.Printf("提醒: 证书将在 %d 天后过期，建议在到期前6个月进行更新", certInfo.DaysToExpire)
			}
		}
	}
}

// GetCertificateStatus 获取证书状态报告
func (cm *CertificateManager) GetCertificateStatus() (string, error) {
	certInfo, err := cm.CheckCertificateHealth()
	if err != nil {
		return "", err
	}

	status := "证书状态报告:\n"
	status += fmt.Sprintf("- 序列号: %s\n", certInfo.SerialNumber)
	status += fmt.Sprintf("- 有效期: %s 至 %s\n",
		certInfo.ValidFrom.Format("2006-01-02"),
		certInfo.ValidTo.Format("2006-01-02"))
	status += fmt.Sprintf("- 剩余天数: %d\n", certInfo.DaysToExpire)

	if certInfo.IsExpired {
		status += "- 状态: 已过期\n"
	} else if certInfo.DaysToExpire <= 30 {
		status += "- 状态: 即将过期（紧急）\n"
	} else if certInfo.DaysToExpire <= 90 {
		status += "- 状态: 即将过期（警告）\n"
	} else if certInfo.DaysToExpire <= 180 {
		status += "- 状态: 即将过期（提醒）\n"
	} else {
		status += "- 状态: 正常\n"
	}

	return status, nil
}

// copyFile 复制文件
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	err = os.WriteFile(dst, input, 0644)
	if err != nil {
		return err
	}

	return nil
}
