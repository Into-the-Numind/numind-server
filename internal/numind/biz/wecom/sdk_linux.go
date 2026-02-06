//go:build linux && cgo
// +build linux,cgo

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
// ===========================================

#cgo CFLAGS: -I${SRCDIR}/../../../../lib/wecom-sdk
#cgo LDFLAGS: -L${SRCDIR}/../../../../lib/wecom-sdk -lWeWorkFinanceSdk -Wl,-rpath,${SRCDIR}/../../../../lib/wecom-sdk

#include <stdlib.h>
#include "WeWorkFinanceSdk_C.h"
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
	chatData := C.NewSlice()
	if chatData == nil {
		return nil, fmt.Errorf("failed to allocate Slice_t")
	}
	defer C.FreeSlice(chatData)

	cProxy := C.CString("")
	cPasswd := C.CString("")
	defer C.free(unsafe.Pointer(cProxy))
	defer C.free(unsafe.Pointer(cPasswd))

	ret := C.GetChatData(c.sdk, C.ulonglong(seq), C.uint(limit), cProxy, cPasswd, 60, chatData)
	if ret != 0 {
		return nil, fmt.Errorf("GetChatData failed with code: %d", ret)
	}

	// 解析 JSON 响应
	if chatData.buf == nil {
		return nil, fmt.Errorf("SDK returned null buffer with code %d", ret)
	}
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

	msg := C.NewSlice()
	if msg == nil {
		return "", fmt.Errorf("failed to allocate Slice_t for decrypt")
	}
	defer C.FreeSlice(msg)

	ret := C.DecryptData(cEncryptKey, cEncryptMsg, msg)
	if ret != 0 {
		return "", fmt.Errorf("DecryptData failed with code: %d", ret)
	}

	return C.GoStringN(msg.buf, msg.len), nil
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
