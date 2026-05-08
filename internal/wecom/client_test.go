package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveChatFileURLReturnsHTTPURLDirectly(t *testing.T) {
	client := NewClient("http://example.test", "app", "secret")
	result, err := client.ResolveChatFileURL(context.Background(), "robot-1", "https://cdn.example.test/a.png")
	if err != nil {
		t.Fatalf("ResolveChatFileURL returned error: %v", err)
	}
	if result.FileURL != "https://cdn.example.test/a.png" {
		t.Fatalf("FileURL = %q, want direct URL", result.FileURL)
	}
}

func TestDownloadChatFileURLWaitsForCallback(t *testing.T) {
	var client *Client
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gateway/jzopen/GetAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode": 0,
				"data": map[string]any{
					"access_token": "token-1",
					"expires_in":   7200,
				},
			})
		case "/gateway/jzopen/DownloadChatFile":
			var req struct {
				RobotID string `json:"robot_id"`
				FileMD5 string `json:"file_md5"`
				UniqSN  string `json:"uniq_sn"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.RobotID != "robot-1" || req.FileMD5 != "md5-1" || req.UniqSN == "" {
				t.Errorf("unexpected request: %+v", req)
			}
			go func(uniqSN string) {
				time.Sleep(10 * time.Millisecond)
				client.CompleteDownloadChatFile(uniqSN, DownloadChatFileResult{
					FileURL:  "https://cdn.example.test/image.png",
					FileName: "image.png",
				})
			}(req.UniqSN)
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client = NewClient(server.URL, "app", "secret")
	result, err := client.DownloadChatFileURL(context.Background(), "robot-1", "md5-1")
	if err != nil {
		t.Fatalf("DownloadChatFileURL returned error: %v", err)
	}
	if result.FileURL != "https://cdn.example.test/image.png" {
		t.Fatalf("FileURL = %q", result.FileURL)
	}
	if result.FileName != "image.png" {
		t.Fatalf("FileName = %q", result.FileName)
	}
}
