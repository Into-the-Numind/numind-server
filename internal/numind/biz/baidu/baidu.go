package baidu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type BaiduBiz interface {
	GetAccessToken() (string, error)
	OCRImage(imageData []byte) (string, error)
}

type baiduBiz struct {
	apiKey    string
	secretKey string
	token     string
	tokenLock sync.Mutex
	tokenTime time.Time
}

func New(apiKey, secretKey string) BaiduBiz {
	return &baiduBiz{
		apiKey:    apiKey,
		secretKey: secretKey,
	}
}

// 获取access_token，自动缓存
func (b *baiduBiz) GetAccessToken() (string, error) {
	b.tokenLock.Lock()
	defer b.tokenLock.Unlock()
	if b.token != "" && time.Since(b.tokenTime) < time.Hour {
		return b.token, nil
	}
	url := "https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=" + b.apiKey + "&client_secret=" + b.secretKey
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", errors.New("get access_token failed: " + string(body))
	}
	b.token = result.AccessToken
	b.tokenTime = time.Now()
	return b.token, nil
}

// OCR图片识别
func (b *baiduBiz) OCRImage(imageData []byte) (string, error) {
	token, err := b.GetAccessToken()
	if err != nil {
		return "", err
	}
	urlStr := "https://aip.baidubce.com/rest/2.0/ocr/v1/general_basic?access_token=" + token
	imgBase64 := base64.StdEncoding.EncodeToString(imageData)
	imgEncoded := url.QueryEscape(imgBase64)
	data := "image=" + imgEncoded
	req, _ := http.NewRequest("POST", urlStr, bytes.NewBufferString(data))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}
