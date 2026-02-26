package wechat

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/certificates"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/jsapi"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type PayClient struct {
	Client *core.Client
}

// CertificateInfo 证书信息
type CertificateInfo struct {
	SerialNumber string
	ValidFrom    time.Time
	ValidTo      time.Time
	IsExpired    bool
	DaysToExpire int
}

// ValidateCertificate 验证证书信息
func ValidateCertificate(certPath, expectedSerialNo string) (*CertificateInfo, error) {
	// 检查证书文件是否存在
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("证书文件不存在: %s", certPath)
	}

	// 读取证书
	cert, err := utils.LoadCertificateWithPath(certPath)
	if err != nil {
		return nil, fmt.Errorf("加载证书失败: %v", err)
	}

	// 获取证书序列号
	serialNo := strings.ToUpper(fmt.Sprintf("%X", cert.SerialNumber))

	// 获取证书有效期
	validFrom := cert.NotBefore
	validTo := cert.NotAfter

	// 检查是否过期
	now := time.Now()
	isExpired := now.After(validTo)
	daysToExpire := int(validTo.Sub(now).Hours() / 24)

	info := &CertificateInfo{
		SerialNumber: serialNo,
		ValidFrom:    validFrom,
		ValidTo:      validTo,
		IsExpired:    isExpired,
		DaysToExpire: daysToExpire,
	}

	// 验证序列号是否匹配
	if expectedSerialNo != "" && serialNo != expectedSerialNo {
		return info, fmt.Errorf("证书序列号不匹配: 期望=%s, 实际=%s", expectedSerialNo, serialNo)
	}

	return info, nil
}

// NewPayClientFromMap 支持从map[string]string初始化PayClient
// 支持平台证书模式和微信支付公钥模式
func NewPayClientFromMap(cfg map[string]string) (*PayClient, error) {
	// 检查是否使用微信支付公钥模式
	if cfg["use_wechatpay_public_key"] == "true" {
		return newPayClientWithPublicKey(cfg)
	}

	// 使用平台证书模式（兼容旧版本）
	return newPayClientWithCertificate(cfg)
}

// newPayClientWithPublicKey 使用微信支付公钥模式初始化客户端
func newPayClientWithPublicKey(cfg map[string]string) (*PayClient, error) {
	mchPrivateKey, err := utils.LoadPrivateKeyWithPath(cfg["mch_private_key_path"])
	if err != nil {
		return nil, fmt.Errorf("加载商户私钥失败: %v", err)
	}

	// 使用微信支付公钥模式，不需要平台证书
	client, err := core.NewClient(
		context.Background(),
		option.WithWechatPayAutoAuthCipher(
			cfg["mch_id"],
			cfg["mch_cert_serial_no"],
			mchPrivateKey,
			cfg["mch_api_v3_key"],
		),
		// 不添加平台证书，使用微信支付公钥验签
	)
	if err != nil {
		return nil, fmt.Errorf("创建微信支付公钥模式客户端失败: %v", err)
	}

	return &PayClient{Client: client}, nil
}

// newPayClientWithCertificate 使用平台证书模式初始化客户端
func newPayClientWithCertificate(cfg map[string]string) (*PayClient, error) {
	// 注意：wechatpay_cert_path 是微信支付平台证书，不是商户证书
	// 平台证书不需要验证序列号，因为可能有多个有效证书
	certPath := cfg["wechatpay_cert_path"]
	if certPath == "" {
		return nil, fmt.Errorf("微信支付平台证书路径未配置")
	}

	// 检查证书文件是否存在
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("微信支付平台证书文件不存在: %s", certPath)
	}

	// 验证平台证书（不验证序列号，因为可能有多个有效证书）
	certInfo, err := ValidateCertificate(certPath, "")
	if err != nil {
		return nil, fmt.Errorf("平台证书验证失败: %v", err)
	}

	// 检查证书是否即将过期（提前6个月警告）
	if certInfo.DaysToExpire <= 180 {
		fmt.Printf("警告: 微信支付平台证书将在 %d 天后过期，建议及时更新\n", certInfo.DaysToExpire)
	}

	// 检查证书是否已过期
	if certInfo.IsExpired {
		return nil, fmt.Errorf("微信支付平台证书已过期，过期时间: %s", certInfo.ValidTo.Format("2006-01-02"))
	}

	mchPrivateKey, err := utils.LoadPrivateKeyWithPath(cfg["mch_private_key_path"])
	if err != nil {
		return nil, err
	}
	wechatPayCert, err := utils.LoadCertificateWithPath(cfg["wechatpay_cert_path"])
	if err != nil {
		return nil, err
	}
	client, err := core.NewClient(
		context.Background(),
		option.WithWechatPayAutoAuthCipher(
			cfg["mch_id"],
			cfg["mch_cert_serial_no"], // 这是商户证书序列号，用于商户身份认证
			mchPrivateKey,
			cfg["mch_api_v3_key"],
		),
		option.WithWechatPayCertificate([]*x509.Certificate{wechatPayCert}),
	)
	if err != nil {
		return nil, err
	}
	return &PayClient{Client: client}, nil
}

// Native下单业务
func CreateNativeOrder(cfg map[string]string, outTradeNo, description string, amount int64) (interface{}, error) {
	payClient, err := NewPayClientFromMap(cfg)
	if err != nil {
		return nil, err
	}
	svc := native.NativeApiService{Client: payClient.Client}
	resp, _, err := svc.Prepay(context.Background(),
		native.PrepayRequest{
			Appid:       core.String(cfg["app_id"]),
			Mchid:       core.String(cfg["mch_id"]),
			Description: core.String(description),
			OutTradeNo:  core.String(outTradeNo),
			NotifyUrl:   core.String(cfg["notify_url"]),
			Amount: &native.Amount{
				Total: core.Int64(amount),
			},
		},
	)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// 小程序支付下单业务
func CreateMiniProgramOrder(cfg map[string]string, outTradeNo, description string, amount int64, openID string) (interface{}, error) {
	payClient, err := NewPayClientFromMap(cfg)
	if err != nil {
		return nil, err
	}

	// 使用 JSAPI 支付服务
	svc := jsapi.JsapiApiService{Client: payClient.Client}
	resp, _, err := svc.Prepay(context.Background(),
		jsapi.PrepayRequest{
			Appid:       core.String(cfg["app_id"]),
			Mchid:       core.String(cfg["mch_id"]),
			Description: core.String(description),
			OutTradeNo:  core.String(outTradeNo),
			NotifyUrl:   core.String(cfg["notify_url"]),
			Payer: &jsapi.Payer{
				Openid: core.String(openID),
			},
			Amount: &jsapi.Amount{
				Total: core.Int64(amount),
			},
		},
	)
	if err != nil {
		return nil, err
	}

	// 生成小程序支付参数
	prepayID := *resp.PrepayId
	appID := cfg["app_id"]
	timeStamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonceStr()
	packageStr := "prepay_id=" + prepayID
	signType := "RSA"

	// 生成支付签名
	message := appID + "\n" + timeStamp + "\n" + nonceStr + "\n" + packageStr + "\n"
	paySign, err := generatePaySign(message, cfg["mch_private_key_path"])
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"timeStamp": timeStamp,
		"nonceStr":  nonceStr,
		"package":   packageStr,
		"signType":  signType,
		"paySign":   paySign,
	}, nil
}

// decryptCertificate 解密单个证书
func decryptCertificate(encryptCert *certificates.EncryptCertificate, apiV3Key string) ([]byte, error) {
	if encryptCert.Algorithm == nil || *encryptCert.Algorithm != "AEAD_AES_256_GCM" {
		return nil, fmt.Errorf("不支持的加密算法: %v", encryptCert.Algorithm)
	}

	// 使用 API v3 Key 解密证书
	c, err := aes.NewCipher([]byte(apiV3Key))
	if err != nil {
		return nil, fmt.Errorf("创建 AES 密码失败: %v", err)
	}

	aesgcm, err := cipher.NewGCM(c)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %v", err)
	}

	// 解码密文
	ciphertext, err := base64.StdEncoding.DecodeString(*encryptCert.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("解码密文失败: %v", err)
	}

	// 准备解密参数
	nonce := []byte(*encryptCert.Nonce)
	associatedData := []byte{}
	if encryptCert.AssociatedData != nil {
		associatedData = []byte(*encryptCert.AssociatedData)
	}

	// 解密
	plaintext, err := aesgcm.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, fmt.Errorf("解密证书失败: %v", err)
	}

	return plaintext, nil
}

// downloadWechatPayCertificates 从微信支付 API 下载所有平台证书
// 重要：微信支付可能同时有多个有效证书（新旧证书切换期间），需要下载所有证书
func downloadWechatPayCertificates(cfg map[string]string) ([]*x509.Certificate, error) {
	// 创建 PayClient（使用公钥模式，不需要平台证书）
	payClient, err := newPayClientWithPublicKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建支付客户端失败: %v", err)
	}

	// 使用证书下载服务
	svc := certificates.CertificatesApiService{Client: payClient.Client}
	resp, _, err := svc.DownloadCertificates(context.Background())
	if err != nil {
		return nil, fmt.Errorf("下载平台证书失败: %v", err)
	}

	if resp == nil || len(resp.Data) == 0 {
		return nil, fmt.Errorf("未获取到平台证书")
	}

	apiV3Key := cfg["mch_api_v3_key"]
	var certs []*x509.Certificate
	var latestCertPlaintext []byte

	// 解密所有证书（微信支付可能同时有多个有效证书）
	for i, certData := range resp.Data {
		if certData.EncryptCertificate == nil {
			continue
		}

		// 解密证书
		plaintext, err := decryptCertificate(certData.EncryptCertificate, apiV3Key)
		if err != nil {
			// 记录错误但继续处理其他证书
			fmt.Printf("警告: 解密证书 %d 失败: %v\n", i, err)
			continue
		}

		// 解析证书
		cert, err := utils.LoadCertificate(string(plaintext))
		if err != nil {
			fmt.Printf("警告: 解析证书 %d 失败: %v\n", i, err)
			continue
		}

		certs = append(certs, cert)

		// 保存第一个（最新的）证书到文件
		if i == 0 && latestCertPlaintext == nil {
			latestCertPlaintext = plaintext
		}
	}

	if len(certs) == 0 {
		return nil, fmt.Errorf("未能成功解密任何证书")
	}

	// 尝试保存最新的证书到文件（如果路径存在）
	if latestCertPlaintext != nil {
		certPath := cfg["wechatpay_cert_path"]
		if certPath != "" {
			// 确保目录存在
			dir := filepath.Dir(certPath)
			if err := os.MkdirAll(dir, 0755); err == nil {
				// 保存证书（忽略错误，因为可能没有写入权限）
				_ = os.WriteFile(certPath, latestCertPlaintext, 0644)
			}
		}
	}

	return certs, nil
}

// loadWechatPayCertificates 加载微信支付平台证书列表，如果文件不存在则尝试下载所有证书
// 重要：必须加载所有证书，因为微信支付可能同时有多个有效证书（新旧证书切换期间）
func loadWechatPayCertificates(cfg map[string]string) ([]*x509.Certificate, error) {
	certPath := cfg["wechatpay_cert_path"]

	// 首先尝试从文件加载（兼容旧版本，只加载单个证书文件）
	if certPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			cert, err := utils.LoadCertificateWithPath(certPath)
			if err == nil {
				// 如果文件存在，也尝试下载所有证书以确保完整性
				// 但先返回文件中的证书，避免阻塞
				go func() {
					// 后台下载所有证书并更新
					downloadedCerts, err := downloadWechatPayCertificates(cfg)
					_ = err
					_ = downloadedCerts
				}()
				return []*x509.Certificate{cert}, nil
			}
		}
	}

	// 文件不存在或加载失败，从 API 下载所有证书
	return downloadWechatPayCertificates(cfg)
}

// 支付回调业务
func ParsePayNotify(cfg map[string]string, ctx context.Context, req *http.Request) (*payments.Transaction, error) {
	// 加载所有平台证书（如果文件不存在则自动下载）
	// 注意：即使使用公钥模式，验证回调签名时仍需要平台证书
	// 重要：微信支付可能同时有多个有效证书（新旧证书切换期间），必须加载所有证书
	// 回调请求头中的 Wechatpay-Serial 字段指定了使用的证书序列号，验证器必须包含该证书
	wechatPayCerts, err := loadWechatPayCertificates(cfg)
	if err != nil {
		return nil, fmt.Errorf("加载微信支付平台证书失败: %v。提示：请确保证书文件存在，或检查网络连接以自动下载证书", err)
	}

	if len(wechatPayCerts) == 0 {
		return nil, fmt.Errorf("未加载到任何有效的平台证书")
	}

	// 使用所有证书创建验证器，确保能验证不同序列号的证书
	// 这是关键：回调可能使用任意一个有效证书签名，所以验证器必须包含所有证书
	verifier := verifiers.NewSHA256WithRSAVerifier(core.NewCertificateMapWithList(wechatPayCerts))

	// 使用 Verifier 创建通知处理器
	handler := notify.NewNotifyHandler(cfg["mch_api_v3_key"], verifier)
	transaction := &payments.Transaction{}
	_, err = handler.ParseNotifyRequest(ctx, req, transaction)
	if err != nil {
		return nil, err
	}
	return transaction, nil
}

// generateNonceStr 生成随机字符串
func generateNonceStr() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[n.Int64()]
	}
	return string(b)
}

// generatePaySign 生成支付签名
func generatePaySign(message, privateKeyPath string) (string, error) {
	// 读取私钥
	privateKey, err := utils.LoadPrivateKeyWithPath(privateKeyPath)
	if err != nil {
		return "", fmt.Errorf("加载私钥失败: %v", err)
	}

	// 计算消息的 SHA256 哈希
	hash := sha256.Sum256([]byte(message))

	// 使用私钥签名
	signature, err := rsa.SignPKCS1v15(nil, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("签名失败: %v", err)
	}

	// 返回 base64 编码的签名
	return base64.StdEncoding.EncodeToString(signature), nil
}
