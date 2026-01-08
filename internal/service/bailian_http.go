package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BailianHTTPClient 阿里百炼纯 HTTP 客户端
type BailianHTTPClient struct {
	AccessKey   string
	SecretKey   string
	WorkspaceId string
	Endpoint    string
	HTTPClient  *http.Client
}

// NewBailianHTTPClient 创建百炼客户端，配置连接池
func NewBailianHTTPClient(accessKey, secretKey, workspaceId string) *BailianHTTPClient {
	return &BailianHTTPClient{
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		WorkspaceId: workspaceId,
		Endpoint:    "bailian.cn-hangzhou.aliyuncs.com",
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
				MaxIdleConnsPerHost: 20,
			},
			Timeout: 60 * time.Second,
		},
	}
}

// GenerateV3Signature 实现阿里云 V3 签名算法 (ACS3-HMAC-SHA256)
// 参考文档: https://help.aliyun.com/zh/sdk/product-overview/v3-signature
func (c *BailianHTTPClient) GenerateV3Signature(method, action, version string, body []byte) (map[string]string, error) {
	now := time.Now().UTC()
	// x-acs-date 必须为 ISO8601 UTC 格式
	x_acs_date := now.Format("2006-01-02T15:04:05Z")
	nonce := uuid.New().String()

	// 1. 计算 Payload 的 SHA256 摘要
	h := sha256.New()
	h.Write(body)
	contentSha256 := hex.EncodeToString(h.Sum(nil))

	// 准备参与签名的基础 Header
	headers := map[string]string{
		"host":                  c.Endpoint,
		"x-acs-action":          action,
		"x-acs-version":         version,
		"x-acs-date":            x_acs_date,
		"x-acs-signature-nonce": nonce,
		"x-acs-content-sha256":  contentSha256,
		"content-type":          "application/json; charset=utf-8",
	}

	// 2. 构建 Canonical Headers (按字母序排列，包含换行符)
	var signedHeaders []string
	for k := range headers {
		signedHeaders = append(signedHeaders, strings.ToLower(k))
	}
	sort.Strings(signedHeaders)

	var canonicalHeaders strings.Builder
	for _, k := range signedHeaders {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[k]))
		canonicalHeaders.WriteString("\n")
	}

	signedHeadersStr := strings.Join(signedHeaders, ";")

	// 3. 构建 Canonical Request
	// 格式: Method + "\n" + CanonicalURI + "\n" + CanonicalQueryString + "\n" + CanonicalHeaders + "\n" + SignedHeaders + "\n" + HashedPayload
	canonicalURI := "/"
	canonicalQuery := "" // 百炼 RPC 接口通常不带 Query 参数

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeadersStr,
		contentSha256,
	}, "\n")

	// 4. 计算 StringToSign
	h2 := sha256.New()
	h2.Write([]byte(canonicalRequest))
	hashedCanonicalRequest := hex.EncodeToString(h2.Sum(nil))

	stringToSign := "ACS3-HMAC-SHA256\n" + hashedCanonicalRequest

	// 5. 计算 HMAC-SHA256 签名
	mac := hmac.New(sha256.New, []byte(c.SecretKey))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	// 6. 生成 Authorization 头
	authHeader := fmt.Sprintf("ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		c.AccessKey, signedHeadersStr, signature)

	headers["Authorization"] = authHeader
	return headers, nil
}

// GetLease 申请文件上传租约 (ApplyFileUploadLease)
func (c *BailianHTTPClient) GetLease(fileName string) (string, map[string]string, string, error) {
	action := "ApplyFileUploadLease"
	version := "2023-12-29"

	bodyMap := map[string]interface{}{
		"FileName":     fileName,
		"CategoryType": "SESSION_FILE",
		"WorkspaceId":  c.WorkspaceId,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	headers, err := c.GenerateV3Signature("POST", action, version, bodyBytes)
	if err != nil {
		return "", nil, "", err
	}

	url := fmt.Sprintf("https://%s/", c.Endpoint)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", nil, "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, "", fmt.Errorf("GetLease failed: %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data struct {
			FileUploadLeaseId string `json:"FileUploadLeaseId"`
			Param             struct {
				Url     string            `json:"Url"`
				Headers map[string]string `json:"Headers"`
			} `json:"Param"`
		} `json:"Data"`
		Message string `json:"Message"`
		Code    string `json:"Code"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, "", err
	}

	if result.Code != "" && result.Code != "200" && result.Code != "Success" {
		return "", nil, "", fmt.Errorf("API Error: %s - %s", result.Code, result.Message)
	}

	return result.Data.Param.Url, result.Data.Param.Headers, result.Data.FileUploadLeaseId, nil
}

// ConfirmFile 确认文件上传完成 (AddFile)
func (c *BailianHTTPClient) ConfirmFile(leaseId string) (string, error) {
	action := "AddFile"
	version := "2023-12-29"

	bodyMap := map[string]interface{}{
		"FileUploadLeaseId": leaseId,
		"CategoryType":      "SESSION_FILE",
		"WorkspaceId":       c.WorkspaceId,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	headers, err := c.GenerateV3Signature("POST", action, version, bodyBytes)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://%s/", c.Endpoint)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ConfirmFile failed: %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data struct {
			FileId string `json:"FileId"`
		} `json:"Data"`
		Message string `json:"Message"`
		Code    string `json:"Code"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}

	if result.Code != "" && result.Code != "200" && result.Code != "Success" {
		return "", fmt.Errorf("API Error: %s - %s", result.Code, result.Message)
	}

	return result.Data.FileId, nil
}

/*
前端 JavaScript 示例 (使用 Axios):

async function uploadFileToBailian(file) {
    // 1. 从后端获取上传参数 (Url, Headers, LeaseId)
    // 假设后端接口为 /api/v1/bailian/lease
    const { url, headers, leaseId } = await axios.post('/api/v1/bailian/lease', { fileName: file.name });

    // 2. 直接上传二进制流到 OSS (必须使用 PUT 方法)
    await axios.put(url, file, {
        headers: {
            ...headers,
            'Content-Type': file.type || 'application/octet-stream'
        }
    });

    // 3. 通知后端确认导入
    // 假设后端接口为 /api/v1/bailian/confirm
    const { fileId } = await axios.post('/api/v1/bailian/confirm', { leaseId });
    console.log('文件上传成功，FileID:', fileId);
    return fileId;
}

前端 JavaScript 示例 (使用 Fetch):

async function uploadFileWithFetch(file) {
    const leaseData = await fetch('/api/v1/bailian/lease', {
        method: 'POST',
        body: JSON.stringify({ fileName: file.name })
    }).then(res => res.json());

    const { url, headers, leaseId } = leaseData;

    await fetch(url, {
        method: 'PUT',
        headers: {
            ...headers,
            'Content-Type': file.type || 'application/octet-stream'
        },
        body: file // 直接传入 File 对象
    });

    const confirmData = await fetch('/api/v1/bailian/confirm', {
        method: 'POST',
        body: JSON.stringify({ leaseId })
    }).then(res => res.json());

    return confirmData.fileId;
}
*/

