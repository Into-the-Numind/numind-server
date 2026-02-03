//go:build !linux
// +build !linux

package wecom

import (
	"errors"
)

// Client 企业微信会话存档 SDK 存根（非 Linux 环境下不可用）
type Client struct {
}

// NewClient 非 Linux 环境下直接返回错误
func NewClient(corpID, secret, privateKeyPath string) (*Client, error) {
	return nil, errors.New("WeWork Finance SDK is only supported on Linux (x86_64)")
}

func (c *Client) FetchData(seq uint64, limit int) (*ChatDataResponse, error) {
	return nil, errors.New("not implemented")
}

func (c *Client) Decrypt(encryptRandomKey, encryptChatMsg string) (string, error) {
	return "", errors.New("not implemented")
}

func (c *Client) Close() {
}
