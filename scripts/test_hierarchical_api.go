package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

const (
	baseURL = "http://49.233.219.254:9091"
)

// Data Structures

type LoginData struct {
	AccessToken string `json:"access_token"`
	User        struct {
		ID       uint   `json:"id"`
		Nickname string `json:"nickname"`
	} `json:"user"`
}

type LoginResponse struct {
	Code    int       `json:"code"`
	Data    LoginData `json:"data"`
	Message string    `json:"message"`
}

type SubUserInfo struct {
	UserID   uint   `json:"user_id"`
	Nickname string `json:"nickname"`
}

type SubUserListResponse struct {
	Code int `json:"code"`
	Data struct {
		SubUsers   []SubUserInfo `json:"sub_users"`
		TotalCount int64         `json:"total_count"`
	} `json:"data"`
	Message string `json:"message"`
}

type StatisticsData struct {
	TotalSubUsers       int `json:"total_sub_users"`
	ActiveSubUsers      int `json:"active_sub_users"`
	TotalTemplatesCount int `json:"total_templates_count"`
	MyTotalSopRuns      int `json:"my_total_sop_runs"`
	MyMonthlySopRuns    int `json:"my_monthly_sop_runs"`
}

type StatisticsResponse struct {
	Code    int            `json:"code"`
	Data    StatisticsData `json:"data"`
	Message string         `json:"message"`
}

type SopTemplateInfo struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type AuthorizedTemplateInfo struct {
	TemplateID uint   `json:"template_id"`
	Name       string `json:"name"`
}

type SopTemplateListResponse struct {
	Code int `json:"code"`
	Data struct {
		Templates []SopTemplateInfo `json:"templates"`
		Total     int64             `json:"total"`
	} `json:"data"`
	Message string `json:"message"`
}

type AuthorizedTemplateListResponse struct {
	Code int `json:"code"`
	Data struct {
		Templates []AuthorizedTemplateInfo `json:"templates"`
		Total     int64                    `json:"total"`
	} `json:"data"`
	Message string `json:"message"`
}

func main() {
	fmt.Println("Starting Hierarchical User API Test...")

	// 1. Login as Primary User ('98')
	token, userID, err := login("98")
	if err != nil {
		panic(fmt.Sprintf("Login failed: %v", err))
	}
	fmt.Printf("✅ Login successful. UserID: %d, Token: %s...\n", userID, token[:10])

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// 2. Get Statistics
	stats, err := getStatistics(client, token)
	if err != nil {
		fmt.Printf("❌ GetStatistics failed: %v\n", err)
	} else {
		fmt.Printf("✅ GetStatistics success: SubUsers=%d, MyRuns=%d\n", stats.TotalSubUsers, stats.MyTotalSopRuns)
	}

	// 3. List Sub-users
	subUsers, err := listSubUsers(client, token)
	if err != nil {
		fmt.Printf("❌ ListSubUsers failed: %v\n", err)
	} else {
		fmt.Printf("✅ ListSubUsers success: Count=%d\n", len(subUsers))
	}

	if len(subUsers) == 0 {
		fmt.Println("⚠️ No sub-users found. Skipping permission tests.")
		return
	}

	targetSubUser := subUsers[0]
	fmt.Printf("➡️ Testing permissions for SubUser ID: %d (%s)\n", targetSubUser.UserID, targetSubUser.Nickname)

	// 4. List Templates (to pick one)
	templates, err := listTemplates(client, token)
	if err != nil {
		fmt.Printf("❌ ListTemplates failed: %v\n", err)
		return
	}
	if len(templates) == 0 {
		fmt.Println("⚠️ No SOP templates found. Skipping permission tests.")
		return
	}
	targetTemplate := templates[0]
	fmt.Printf("➡️ Selected Template ID: %d (%s)\n", targetTemplate.ID, targetTemplate.Name)

	// 5. Grant Permission
	err = grantPermission(client, token, targetSubUser.UserID, []uint{targetTemplate.ID})
	if err != nil {
		fmt.Printf("❌ GrantPermission failed: %v\n", err)
	} else {
		fmt.Printf("✅ GrantPermission success\n")
	}

	// 6. Verify Permission (Get SubUser Templates)
	granted, err := listSubUserTemplates(client, token, targetSubUser.UserID)
	if err != nil {
		fmt.Printf("❌ ListSubUserTemplates failed: %v\n", err)
	} else {
		found := false
		for _, t := range granted {
			if t.TemplateID == targetTemplate.ID {
				found = true
				break
			}
		}
		if found {
			fmt.Printf("✅ Verified granted template is in list\n")
		} else {
			fmt.Printf("❌ Verified granted template is NOT in list\n")
		}
	}

	// 7. Revoke Permission
	err = revokePermission(client, token, targetSubUser.UserID, []uint{targetTemplate.ID})
	if err != nil {
		fmt.Printf("❌ RevokePermission failed: %v\n", err)
	} else {
		fmt.Printf("✅ RevokePermission success\n")
	}

	// 8. Verify Revocation
	grantedAfter, err := listSubUserTemplates(client, token, targetSubUser.UserID)
	if err != nil {
		fmt.Printf("❌ ListSubUserTemplates (after revoke) failed: %v\n", err)
	} else {
		found := false
		for _, t := range grantedAfter {
			if t.TemplateID == targetTemplate.ID {
				found = true
				break
			}
		}
		if !found {
			fmt.Printf("✅ Verified template is revoked\n")
		} else {
			fmt.Printf("❌ Template still exists after revocation\n")
		}
	}

	fmt.Println("🎉 All tests completed.")
}

func login(code string) (string, uint, error) {
	payload := map[string]string{"code": code}
	jsonData, _ := json.Marshal(payload)

	resp, err := http.Post(baseURL+"/v1/wechat/login", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res LoginResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return "", 0, err
	}
	if res.Code != 0 {
		return "", 0, fmt.Errorf("api error: %s", res.Message)
	}
	return res.Data.AccessToken, res.Data.User.ID, nil
}

func getStatistics(client *http.Client, token string) (*StatisticsData, error) {
	req, _ := http.NewRequest("GET", baseURL+"/v1/customers/statistics", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res StatisticsResponse
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Println("Stats raw:", string(body))
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf(res.Message)
	}
	return &res.Data, nil
}

func listSubUsers(client *http.Client, token string) ([]SubUserInfo, error) {
	req, _ := http.NewRequest("GET", baseURL+"/v1/customers/sub-users", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res SubUserListResponse
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Println("SubUsers raw:", string(body))
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf(res.Message)
	}
	return res.Data.SubUsers, nil
}

func listTemplates(client *http.Client, token string) ([]SopTemplateInfo, error) {
	req, _ := http.NewRequest("GET", baseURL+"/v1/sop/templates", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res SopTemplateListResponse
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Println("Templates raw:", string(body))
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf(res.Message)
	}
	return res.Data.Templates, nil
}

func grantPermission(client *http.Client, token string, subUserID uint, templateIDs []uint) error {
	payload := map[string]interface{}{"template_ids": templateIDs}
	jsonData, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/v1/customers/sub-users/%d/templates", baseURL, subUserID)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res map[string]interface{}
	json.Unmarshal(body, &res)
	if code, ok := res["code"].(float64); !ok || code != 0 {
		return fmt.Errorf("api error: %v", res["message"])
	}
	return nil
}

func listSubUserTemplates(client *http.Client, token string, subUserID uint) ([]AuthorizedTemplateInfo, error) {
	url := fmt.Sprintf("%s/v1/customers/sub-users/%d/templates", baseURL, subUserID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res AuthorizedTemplateListResponse
	if err := json.Unmarshal(body, &res); err != nil {
		fmt.Println("SubUserTemplates raw:", string(body))
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf(res.Message)
	}
	return res.Data.Templates, nil
}

func revokePermission(client *http.Client, token string, subUserID uint, templateIDs []uint) error {
	payload := map[string]interface{}{"template_ids": templateIDs}
	jsonData, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/v1/customers/sub-users/%d/templates", baseURL, subUserID)
	req, _ := http.NewRequest("DELETE", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var res map[string]interface{}
	json.Unmarshal(body, &res)
	if code, ok := res["code"].(float64); !ok || code != 0 {
		return fmt.Errorf("api error: %v", res["message"])
	}
	return nil
}
