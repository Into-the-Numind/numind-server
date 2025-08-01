package wechat

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
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
	// 验证证书
	certInfo, err := ValidateCertificate(cfg["wechatpay_cert_path"], cfg["mch_cert_serial_no"])
	if err != nil {
		return nil, fmt.Errorf("证书验证失败: %v", err)
	}

	// 检查证书是否即将过期（提前6个月警告）
	if certInfo.DaysToExpire <= 180 {
		fmt.Printf("警告: 微信支付证书将在 %d 天后过期，建议及时更新\n", certInfo.DaysToExpire)
	}

	// 检查证书是否已过期
	if certInfo.IsExpired {
		return nil, fmt.Errorf("微信支付证书已过期，过期时间: %s", certInfo.ValidTo.Format("2006-01-02"))
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
			cfg["mch_cert_serial_no"],
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

// 支付回调业务
func ParsePayNotify(cfg map[string]string, ctx context.Context, req *http.Request) (*payments.Transaction, error) {
	handler := notify.NewNotifyHandler(cfg["mch_api_v3_key"], nil)
	transaction := &payments.Transaction{}
	_, err := handler.ParseNotifyRequest(ctx, req, transaction)
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
