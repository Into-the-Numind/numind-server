package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// xhsQRCreateResponse represents the response from xhs-service /xhs/qr/create
type xhsQRCreateResponse struct {
	QRID  string `json:"qr_id"`
	Code  string `json:"code"`
	QRURL string `json:"qr_url"`
}

// xhsQRStatusResponse represents the response from xhs-service /xhs/qr/status/{qr_id}
type xhsQRStatusResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// xhsQRCompleteResponse represents the response from xhs-service /xhs/qr/complete/{qr_id}
type xhsQRCompleteResponse struct {
	Cookies  map[string]string `json:"cookies"`
	UserID   string            `json:"user_id"`
	Nickname string            `json:"nickname"`
}

// CreateQRLogin calls xhs-service to start a QR login flow
func (mb *MonitorBiz) CreateQRLogin(ctx context.Context, userID uint) (qrID, code, qrURL string, err error) {
	client := newXhsHTTPClient()
	url := fmt.Sprintf("%s/xhs/qr/create", xhsServiceBaseURL())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("CreateQRLogin: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("CreateQRLogin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", "", errno.ErrXhsQRLoginFailed.SetMessage("xhs-service returned %d: %s", resp.StatusCode, string(body))
	}

	var result xhsQRCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", "", fmt.Errorf("CreateQRLogin: decode: %w", err)
	}

	return result.QRID, result.Code, result.QRURL, nil
}

// CheckQRStatus calls xhs-service to poll the QR scan status
func (mb *MonitorBiz) CheckQRStatus(ctx context.Context, userID uint, qrID string) (status int, message string, err error) {
	client := newXhsHTTPClient()
	url := fmt.Sprintf("%s/xhs/qr/status/%s", xhsServiceBaseURL(), qrID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("CheckQRStatus: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("CheckQRStatus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return 0, "", errno.ErrXhsQRSessionNotFound
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, "", errno.ErrXhsQRLoginFailed.SetMessage("check status failed: %s", string(body))
	}

	var result xhsQRStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, "", fmt.Errorf("CheckQRStatus: decode: %w", err)
	}

	return result.Status, result.Message, nil
}

// CompleteQRLogin calls xhs-service to complete the QR login and stores cookies in monitor_config
func (mb *MonitorBiz) CompleteQRLogin(ctx context.Context, userID uint, qrID string) error {
	client := newXhsHTTPClient()
	url := fmt.Sprintf("%s/xhs/qr/complete/%s", xhsServiceBaseURL(), qrID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("CompleteQRLogin: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("CompleteQRLogin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return errno.ErrXhsQRSessionNotFound
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errno.ErrXhsQRLoginFailed.SetMessage("complete login failed: %s", string(body))
	}

	var result xhsQRCompleteResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("CompleteQRLogin: decode: %w", err)
	}

	// Serialize cookies to JSON string for storage
	cookiesJSON, err := json.Marshal(result.Cookies)
	if err != nil {
		return fmt.Errorf("CompleteQRLogin: marshal cookies: %w", err)
	}

	// Get or create the user's monitor config, then update XHS fields
	config, err := mb.store.Monitor().GetConfig(ctx, userID)
	if err != nil {
		return fmt.Errorf("CompleteQRLogin: get config: %w", err)
	}

	config.UserID = userID
	config.XhsCookies = string(cookiesJSON)
	config.XhsNickname = result.Nickname
	config.XhsUserID = result.UserID

	if err := mb.store.Monitor().UpsertConfig(ctx, config); err != nil {
		return fmt.Errorf("CompleteQRLogin: upsert config: %w", err)
	}

	log.Infow("XHS account bound successfully", "userID", userID, "xhsUserID", result.UserID, "nickname", result.Nickname)
	return nil
}

// GetXhsBindStatus checks if the user has a bound XHS account
func (mb *MonitorBiz) GetXhsBindStatus(ctx context.Context, userID uint) (bound bool, nickname, xhsUserID string, err error) {
	config, err := mb.store.Monitor().GetConfig(ctx, userID)
	if err != nil {
		return false, "", "", fmt.Errorf("GetXhsBindStatus: %w", err)
	}

	if config.XhsCookies == "" || strings.TrimSpace(config.XhsCookies) == "" {
		return false, "", "", nil
	}

	return true, config.XhsNickname, config.XhsUserID, nil
}

// UnbindXhs removes the XHS account binding
func (mb *MonitorBiz) UnbindXhs(ctx context.Context, userID uint) error {
	config, err := mb.store.Monitor().GetConfig(ctx, userID)
	if err != nil {
		return fmt.Errorf("UnbindXhs: %w", err)
	}

	config.UserID = userID
	config.XhsCookies = ""
	config.XhsNickname = ""
	config.XhsUserID = ""

	if err := mb.store.Monitor().UpsertConfig(ctx, config); err != nil {
		return fmt.Errorf("UnbindXhs: upsert config: %w", err)
	}

	log.Infow("XHS account unbound", "userID", userID)
	return nil
}
