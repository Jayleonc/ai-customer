package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const downloadChatFileTimeout = 20 * time.Second

// Client 封装企微平台 API 调用
type Client struct {
	host       string
	appKey     string
	appSecret  string
	httpClient *http.Client

	mu              sync.RWMutex
	token           string
	tokenExpAt      time.Time
	downloadMu      sync.Mutex
	downloadWaiters map[string]chan DownloadChatFileResult
}

func NewClient(host, appKey, appSecret string) *Client {
	return &Client{
		host:            strings.TrimRight(host, "/"),
		appKey:          appKey,
		appSecret:       appSecret,
		httpClient:      &http.Client{Timeout: 10 * time.Second},
		downloadWaiters: make(map[string]chan DownloadChatFileResult),
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

// DownloadChatFileResult 是 DownloadChatFile 异步回调返回的文件下载信息。
type DownloadChatFileResult struct {
	FileURL  string
	FileName string
	ErrCode  int
	ErrMsg   string
}

// ResolveChatFileURL 把消息里的媒体引用解析成可下载 URL。
// 平台文档把 receive.group.msg 的 image.url 描述为资源 url，但实际可能是 file_md5。
func (c *Client) ResolveChatFileURL(ctx context.Context, robotID, mediaRef string) (DownloadChatFileResult, error) {
	mediaRef = strings.TrimSpace(mediaRef)
	if mediaRef == "" {
		return DownloadChatFileResult{}, fmt.Errorf("media ref is empty")
	}
	if isHTTPURL(mediaRef) {
		return DownloadChatFileResult{FileURL: mediaRef}, nil
	}
	return c.DownloadChatFileURL(ctx, robotID, mediaRef)
}

// DownloadChatFileURL 触发 DownloadChatFile，并等待 download.chat.file 异步回调。
func (c *Client) DownloadChatFileURL(ctx context.Context, robotID, fileMD5 string) (DownloadChatFileResult, error) {
	robotID = strings.TrimSpace(robotID)
	fileMD5 = strings.TrimSpace(fileMD5)
	if robotID == "" {
		return DownloadChatFileResult{}, fmt.Errorf("robot id is empty")
	}
	if fileMD5 == "" {
		return DownloadChatFileResult{}, fmt.Errorf("file md5 is empty")
	}

	waitCtx, cancel := context.WithTimeout(ctx, downloadChatFileTimeout)
	defer cancel()

	uniqSN := uuid.NewString()
	ch := c.registerDownloadWaiter(uniqSN)
	defer c.unregisterDownloadWaiter(uniqSN)

	tk, err := c.GetToken(waitCtx)
	if err != nil {
		return DownloadChatFileResult{}, fmt.Errorf("get token: %w", err)
	}

	body, _ := json.Marshal(map[string]string{
		"robot_id": robotID,
		"file_md5": fileMD5,
		"uniq_sn":  uniqSN,
	})
	resp, err := c.doPost(waitCtx, "/gateway/jzopen/DownloadChatFile", tk, body)
	if err != nil {
		return DownloadChatFileResult{}, fmt.Errorf("request DownloadChatFile: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DownloadChatFileResult{}, fmt.Errorf("decode DownloadChatFile response: %w", err)
	}
	if out.ErrCode != 0 {
		return DownloadChatFileResult{}, fmt.Errorf("DownloadChatFile error: %d %s", out.ErrCode, out.ErrMsg)
	}

	select {
	case result := <-ch:
		if result.ErrCode != 0 {
			return DownloadChatFileResult{}, fmt.Errorf("DownloadChatFile callback error: %d %s", result.ErrCode, result.ErrMsg)
		}
		if strings.TrimSpace(result.FileURL) == "" {
			return DownloadChatFileResult{}, fmt.Errorf("DownloadChatFile callback missing file_url")
		}
		return result, nil
	case <-waitCtx.Done():
		return DownloadChatFileResult{}, fmt.Errorf("wait DownloadChatFile callback: %w", waitCtx.Err())
	}
}

// CompleteDownloadChatFile 由 dispatcher 在收到 download.chat.file 回调时调用。
func (c *Client) CompleteDownloadChatFile(uniqSN string, result DownloadChatFileResult) bool {
	uniqSN = strings.TrimSpace(uniqSN)
	if uniqSN == "" {
		return false
	}
	c.downloadMu.Lock()
	defer c.downloadMu.Unlock()
	ch, ok := c.downloadWaiters[uniqSN]
	if !ok {
		return false
	}
	delete(c.downloadWaiters, uniqSN)
	ch <- result
	close(ch)
	return true
}

func (c *Client) registerDownloadWaiter(uniqSN string) chan DownloadChatFileResult {
	ch := make(chan DownloadChatFileResult, 1)
	c.downloadMu.Lock()
	c.downloadWaiters[uniqSN] = ch
	c.downloadMu.Unlock()
	return ch
}

func (c *Client) unregisterDownloadWaiter(uniqSN string) {
	c.downloadMu.Lock()
	delete(c.downloadWaiters, uniqSN)
	c.downloadMu.Unlock()
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
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
