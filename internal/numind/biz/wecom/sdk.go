package wecom

/*
// ===========================================
// CGO 封装：企业微信会话存档 C++ SDK
// ===========================================
//
// 注意：此文件需要在安装了 SDK 的环境下编译
// 编译前请确保：
// 1. 将 libWeWorkFinanceSdk.so 放入 /usr/lib 或 lib/wecom-sdk/
// 2. 将 WeWorkFinanceSdk_C.h 放入 lib/wecom-sdk/
//
// 编译命令：
// CGO_ENABLED=1 go build -o wecom-agent ./cmd/wecom-agent
//
// ===========================================

#cgo CFLAGS: -I${SRCDIR}/../../../../lib/wecom-sdk
#cgo LDFLAGS: -L/usr/lib -lWeWorkFinanceSdk

#include <stdlib.h>

// 前向声明 SDK 类型和函数
// 注意：实际头文件中的定义可能略有不同，请根据官方头文件调整

typedef struct WeWorkFinanceSdk WeWorkFinanceSdk_t;

typedef struct Slice {
    char* buf;
    int len;
} Slice_t;

// SDK 函数声明
extern WeWorkFinanceSdk_t* NewSdk();
extern int Init(WeWorkFinanceSdk_t* sdk, const char* corpid, const char* secret);
extern int GetChatData(WeWorkFinanceSdk_t* sdk, unsigned long long seq, unsigned int limit,
                       const char* proxy, const char* passwd, int timeout, Slice_t* chatData);
extern int DecryptData(const char* encrypt_key, const char* encrypt_msg, Slice_t* msg);
extern int GetMediaData(WeWorkFinanceSdk_t* sdk, const char* sdkfileid, const char* proxy,
                        const char* passwd, int timeout, const char* savefile);
extern void DestroySdk(WeWorkFinanceSdk_t* sdk);
extern void FreeSlice(Slice_t* slice);
*/
import "C"

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"unsafe"
)

// Client 企业微信会话存档 SDK 客户端
type Client struct {
	sdk        *C.WeWorkFinanceSdk_t
	privateKey *rsa.PrivateKey
}

// NewClient 创建 SDK 客户端
// corpID: 企业ID
// secret: 会话存档 Secret
// privateKeyPath: RSA 私钥文件路径
func NewClient(corpID, secret, privateKeyPath string) (*Client, error) {
	// 初始化 SDK
	sdk := C.NewSdk()
	if sdk == nil {
		return nil, fmt.Errorf("failed to create SDK instance")
	}

	cCorpID := C.CString(corpID)
	cSecret := C.CString(secret)
	defer C.free(unsafe.Pointer(cCorpID))
	defer C.free(unsafe.Pointer(cSecret))

	ret := C.Init(sdk, cCorpID, cSecret)
	if ret != 0 {
		return nil, fmt.Errorf("SDK init failed with code: %d", ret)
	}

	// 加载私钥
	privateKey, err := loadPrivateKey(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load private key failed: %w", err)
	}

	return &Client{
		sdk:        sdk,
		privateKey: privateKey,
	}, nil
}

// FetchData 拉取会话记录
// seq: 起始序列号（返回 seq+1 开始的消息）
// limit: 拉取数量，最大 1000
func (c *Client) FetchData(seq uint64, limit int) (*ChatDataResponse, error) {
	var chatData C.Slice_t

	cProxy := C.CString("")
	cPasswd := C.CString("")
	defer C.free(unsafe.Pointer(cProxy))
	defer C.free(unsafe.Pointer(cPasswd))

	ret := C.GetChatData(c.sdk, C.ulonglong(seq), C.uint(limit), cProxy, cPasswd, 60, &chatData)
	if ret != 0 {
		return nil, fmt.Errorf("GetChatData failed with code: %d", ret)
	}
	defer C.FreeSlice(&chatData)

	// 解析 JSON 响应
	jsonStr := C.GoStringN(chatData.buf, chatData.len)
	var response ChatDataResponse
	if err := json.Unmarshal([]byte(jsonStr), &response); err != nil {
		return nil, fmt.Errorf("parse chat data response failed: %w", err)
	}

	if response.ErrCode != 0 {
		return nil, fmt.Errorf("API error: %d - %s", response.ErrCode, response.ErrMsg)
	}

	return &response, nil
}

// Decrypt 解密消息内容
// encryptRandomKey: 加密的随机密钥（需先用 RSA 解密）
// encryptChatMsg: 加密的消息内容
func (c *Client) Decrypt(encryptRandomKey, encryptChatMsg string) (string, error) {
	// Step 1: Base64 解码 encryptRandomKey
	encryptedKey, err := base64.StdEncoding.DecodeString(encryptRandomKey)
	if err != nil {
		return "", fmt.Errorf("base64 decode encrypt_random_key failed: %w", err)
	}

	// Step 2: RSA PKCS1 解密
	randomKey, err := rsa.DecryptPKCS1v15(rand.Reader, c.privateKey, encryptedKey)
	if err != nil {
		return "", fmt.Errorf("RSA decrypt failed: %w", err)
	}

	// Step 3: 调用 SDK 的 DecryptData
	cEncryptKey := C.CString(string(randomKey))
	cEncryptMsg := C.CString(encryptChatMsg)
	defer C.free(unsafe.Pointer(cEncryptKey))
	defer C.free(unsafe.Pointer(cEncryptMsg))

	var msg C.Slice_t
	ret := C.DecryptData(cEncryptKey, cEncryptMsg, &msg)
	if ret != 0 {
		return "", fmt.Errorf("DecryptData failed with code: %d", ret)
	}
	defer C.FreeSlice(&msg)

	return C.GoStringN(msg.buf, msg.len), nil
}

// DownloadMedia 下载媒体文件
// sdkFileID: 媒体文件 ID
// savePath: 保存路径
func (c *Client) DownloadMedia(sdkFileID, savePath string) error {
	cSDKFileID := C.CString(sdkFileID)
	cProxy := C.CString("")
	cPasswd := C.CString("")
	cSavePath := C.CString(savePath)
	defer C.free(unsafe.Pointer(cSDKFileID))
	defer C.free(unsafe.Pointer(cProxy))
	defer C.free(unsafe.Pointer(cPasswd))
	defer C.free(unsafe.Pointer(cSavePath))

	ret := C.GetMediaData(c.sdk, cSDKFileID, cProxy, cPasswd, 60, cSavePath)
	if ret != 0 {
		return fmt.Errorf("GetMediaData failed with code: %d", ret)
	}

	return nil
}

// Close 释放 SDK 资源
func (c *Client) Close() {
	if c.sdk != nil {
		C.DestroySdk(c.sdk)
		c.sdk = nil
	}
}

// loadPrivateKey 从文件加载 RSA 私钥
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	keyData, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// 尝试 PKCS8 格式
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
		var ok bool
		privateKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("not an RSA private key")
		}
	}

	return privateKey, nil
}
