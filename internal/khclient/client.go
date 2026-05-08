package khclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client 封装 knowledge-hub API 调用
type Client struct {
	host       string
	apiKey     string
	httpClient *http.Client
}

func NewClient(host, apiKey string, timeoutSec int) *Client {
	return &Client{
		host:       strings.TrimRight(host, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
	}
}

// ---- RAG 检索 ----

type RetrieveRequest struct {
	Query          string   `json:"query"`
	DatasetIDs     []string `json:"dataset_ids,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`
	TopK           int      `json:"top_k,omitempty"`
	ScoreThreshold float64  `json:"score_threshold,omitempty"`
	SearchStrategy string   `json:"search_strategy,omitempty"` // semantic / keyword / hybrid / auto
	ReRank         bool     `json:"re_rank,omitempty"`         // 是否对结果做二次重排序
}

type RetrieveResult struct {
	ID             string   `json:"id"`
	EvidenceType   string   `json:"evidence_type"`
	EvidenceSource string   `json:"evidence_source"`
	DocumentID     string   `json:"document_id"`
	DocumentName   string   `json:"document_name"`
	DatasetID      string   `json:"dataset_id"`
	ProjectID      string   `json:"project_id"`
	AssetID        string   `json:"asset_id,omitempty"`
	PreviewURL     string   `json:"preview_url,omitempty"`
	Content        string   `json:"content"`
	Snippet        string   `json:"snippet"`
	Score          float64  `json:"score"`
	HeaderPath     []string `json:"header_path"`
	VfsPath        string   `json:"vfs_path"`
	StructurePath  string   `json:"structure_path"`
}

type RetrieveEvidence struct {
	ID            string                  `json:"id"`
	Type          string                  `json:"type"`
	Source        string                  `json:"source"`
	Score         float64                 `json:"score,omitempty"`
	DocumentID    string                  `json:"document_id,omitempty"`
	DocumentName  string                  `json:"document_name,omitempty"`
	DatasetID     string                  `json:"dataset_id,omitempty"`
	ProjectID     string                  `json:"project_id,omitempty"`
	ProjectNodeID string                  `json:"project_node_id,omitempty"`
	VfsPath       string                  `json:"vfs_path,omitempty"`
	StructurePath string                  `json:"structure_path,omitempty"`
	Text          *RetrieveTextEvidence   `json:"text,omitempty"`
	Visual        *RetrieveVisualEvidence `json:"visual,omitempty"`
}

type RetrieveTextEvidence struct {
	SegmentID string `json:"segment_id,omitempty"`
	Content   string `json:"content,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
}

type RetrieveVisualEvidence struct {
	AssetID        string `json:"asset_id,omitempty"`
	PreviewURL     string `json:"preview_url,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	PositionType   string `json:"position_type,omitempty"`
	PositionLabel  string `json:"position_label,omitempty"`
	Page           *int   `json:"page,omitempty"`
	ParagraphIndex *int   `json:"paragraph_index,omitempty"`
	Description    string `json:"description,omitempty"`
	EvidenceReason string `json:"evidence_reason,omitempty"`
}

type RetrieveMeta struct {
	Strategy      string   `json:"strategy"`
	TopK          int      `json:"top_k"`
	CandidateTopK int      `json:"candidate_top_k"`
	Keywords      []string `json:"keywords,omitempty"`
	FallbackUsed  bool     `json:"fallback_used,omitempty"`
}

type RetrieveResponse struct {
	Results   []RetrieveResult   `json:"-"`
	Evidence  []RetrieveEvidence `json:"evidence,omitempty"`
	Retrieval *RetrieveMeta      `json:"retrieval,omitempty"`
}

func (c *Client) Retrieve(ctx context.Context, req *RetrieveRequest) (*RetrieveResponse, error) {
	var wrapper struct {
		Data RetrieveResponse `json:"data"`
	}
	if err := c.doPost(ctx, "/api/rag/retrieve", req, &wrapper); err != nil {
		return nil, fmt.Errorf("kh retrieve: %w", err)
	}
	wrapper.Data.normalizeResultsFromEvidence()
	return &wrapper.Data, nil
}

func (r *RetrieveResponse) normalizeResultsFromEvidence() {
	if len(r.Evidence) == 0 {
		return
	}

	r.Results = make([]RetrieveResult, 0, len(r.Evidence))
	for _, ev := range r.Evidence {
		item := RetrieveResult{
			ID:             ev.ID,
			EvidenceType:   ev.Type,
			EvidenceSource: ev.Source,
			DocumentID:     ev.DocumentID,
			DocumentName:   ev.DocumentName,
			DatasetID:      ev.DatasetID,
			ProjectID:      ev.ProjectID,
			Score:          ev.Score,
			VfsPath:        ev.VfsPath,
			StructurePath:  ev.StructurePath,
		}
		if ev.Text != nil {
			if ev.Text.SegmentID != "" {
				item.ID = ev.Text.SegmentID
			}
			item.Content = ev.Text.Content
			item.Snippet = ev.Text.Snippet
		} else if ev.Visual != nil {
			item.AssetID = ev.Visual.AssetID
			item.PreviewURL = ev.Visual.PreviewURL
			item.Content = firstNonEmptyString(ev.Visual.EvidenceReason, ev.Visual.Description)
			item.Snippet = item.Content
		}
		if item.Content == "" {
			item.Content = item.Snippet
		}
		r.Results = append(r.Results, item)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ---- 文档精读 ----

type DocumentDetailRequest struct {
	ID string `json:"id"`
}

type DocumentDetailResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (c *Client) GetDocumentDetail(ctx context.Context, docID string) (*DocumentDetailResponse, error) {
	var wrapper struct {
		Data DocumentDetailResponse `json:"data"`
	}
	if err := c.doPost(ctx, "/api/document/detail", &DocumentDetailRequest{ID: docID}, &wrapper); err != nil {
		return nil, fmt.Errorf("kh document detail: %w", err)
	}
	return &wrapper.Data, nil
}

// ---- 数据集列表 ----

type DatasetItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListDatasetsRequest struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// ListDatasets 拉取可访问的数据集列表（knowledge-hub: POST /api/dataset/list）
func (c *Client) ListDatasets(ctx context.Context) ([]DatasetItem, error) {
	const maxPageSize = 100
	page := 1
	all := make([]DatasetItem, 0, maxPageSize)

	for {
		var wrapper struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				List     []DatasetItem `json:"list"`
				Total    int64         `json:"total"`
				Page     int           `json:"page"`
				PageSize int           `json:"page_size"`
			} `json:"data"`
		}

		req := &ListDatasetsRequest{Page: page, PageSize: maxPageSize}
		if err := c.doPost(ctx, "/api/dataset/list", req, &wrapper); err != nil {
			return nil, fmt.Errorf("kh list datasets: %w", err)
		}
		if wrapper.Code != 0 {
			return nil, fmt.Errorf("kh list datasets failed: code=%d, message=%s", wrapper.Code, wrapper.Message)
		}

		all = append(all, wrapper.Data.List...)
		if len(wrapper.Data.List) == 0 {
			break
		}
		if int64(len(all)) >= wrapper.Data.Total {
			break
		}
		page++
	}

	return all, nil
}

func (c *Client) doPost(ctx context.Context, path string, body interface{}, out interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+path, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kh API %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
