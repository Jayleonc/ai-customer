package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client 封装企微平台 API 调用
type Client struct {
	host       string
	appKey     string
	appSecret  string
	httpClient *http.Client

	mu         sync.RWMutex
	token      string
	tokenExpAt time.Time
}

func NewClient(host, appKey, appSecret string) *Client {
	return &Client{
		host:       strings.TrimRight(host, "/"),
		appKey:     appKey,
		appSecret:  appSecret,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetToken 获取/缓存 access_token
func (c *Client) GetToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.token != "" && time.Now().Before(c.tokenExpAt) {
		tk := c.token
		c.mu.RUnlock()
		return tk, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpAt) {
		return c.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_key":    c.appKey,
		"app_secret": c.appSecret,
	})
	resp, err := c.doPost(ctx, "/gateway/jzopen/GetAccessToken", "", body)
	if err != nil {
		return "", err
	}

	var out struct {
		Data struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"data"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	resp.Body.Close()

	if out.ErrCode != 0 {
		return "", fmt.Errorf("wecom GetAccessToken error: %d %s", out.ErrCode, out.ErrMsg)
	}

	ttl := out.Data.ExpiresIn - 120
	if ttl <= 0 {
		ttl = out.Data.ExpiresIn
	}
	c.token = out.Data.AccessToken
	c.tokenExpAt = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.token, nil
}

// SendGroupMsg 发送群消息
func (c *Client) SendGroupMsg(ctx context.Context, payload interface{}) error {
	tk, err := c.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	body, _ := json.Marshal(payload)
	resp, err := c.doPost(ctx, "/gateway/jzopen/SendGroupMsg", tk, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("SendGroupMsg error: %d %s", out.ErrCode, out.ErrMsg)
	}
	return nil
}

// GetRemoteGroup 触发异步获取群信息（name, owner_id 等）
// 实际数据通过 get.group 回调事件返回
func (c *Client) GetRemoteGroup(ctx context.Context, robotID, groupID, uniqSN string) error {
	tk, err := c.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}
	body, _ := json.Marshal(map[string]string{
		"robot_id": robotID,
		"group_id": groupID,
		"uniq_sn":  uniqSN,
	})
	resp, err := c.doPost(ctx, "/gateway/jzopen/GetRemoteGroup", tk, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("GetRemoteGroup error: %d %s", out.ErrCode, out.ErrMsg)
	}
	return nil
}

// GetGroupMemberList 触发异步获取群成员列表
// 实际数据通过 get.group.member.list 回调事件返回
func (c *Client) GetGroupMemberList(ctx context.Context, robotID, groupID, uniqSN string) error {
	tk, err := c.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}
	body, _ := json.Marshal(map[string]interface{}{
		"robot_id":   robotID,
		"group_id":   groupID,
		"uniq_sn":    uniqSN,
		"is_refresh": false,
	})
	resp, err := c.doPost(ctx, "/gateway/jzopen/GetGroupMemberList", tk, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("GetGroupMemberList error: %d %s", out.ErrCode, out.ErrMsg)
	}
	return nil
}

// RobotSnapshot 同步接口返回的机器人信息
type RobotSnapshot struct {
	RobotID     string `json:"robot_id"`
	Name        string `json:"name"`
	Avatar      string `json:"avatar"`
	LoginStatus int    `json:"login_status"` // 1=online, 2=offline, 3=initializing, 4=expired
	Status      int    `json:"status"`
	NickName    string `json:"nick_name"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
}

// SyncGetRobotList 同步拉取机器人列表（支持传 robotIDList 精准查询）
func (c *Client) SyncGetRobotList(ctx context.Context, robotIDList []string) ([]RobotSnapshot, error) {
	tk, err := c.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	reqBody := map[string]any{}
	if len(robotIDList) > 0 {
		reqBody["robot_id_list"] = robotIDList
	}
	body, _ := json.Marshal(reqBody)
	resp, err := c.doPost(ctx, "/gateway/jzopen/SyncGetRobotList", tk, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 兼容两种响应形态：
	// 1) {"errcode":0,"data":{"robot_list":[...]}}
	// 2) {"errcode":0,"robot_list":[...]}
	var wrap1 struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Data    struct {
			RobotList []RobotSnapshot `json:"robot_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap1); err == nil && wrap1.ErrCode == 0 {
		if wrap1.Data.RobotList != nil {
			return wrap1.Data.RobotList, nil
		}
	}

	var wrap2 struct {
		ErrCode   int             `json:"errcode"`
		ErrMsg    string          `json:"errmsg"`
		RobotList []RobotSnapshot `json:"robot_list"`
	}
	if err := json.Unmarshal(raw, &wrap2); err == nil {
		if wrap2.ErrCode != 0 {
			return nil, fmt.Errorf("SyncGetRobotList error: %d %s", wrap2.ErrCode, wrap2.ErrMsg)
		}
		return wrap2.RobotList, nil
	}

	return nil, fmt.Errorf("SyncGetRobotList decode failed: %s", string(raw))
}

func (c *Client) doPost(ctx context.Context, path, token string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	if token != "" {
		req.Header.Set("Token", token)
	}
	return c.httpClient.Do(req)
}
