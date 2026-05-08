package imagectx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"git.pinquest.cn/ai-customer/internal/config"
	"github.com/Jayleonc/turnmesh"
)

type Observer interface {
	Observe(ctx context.Context, input Input) (*Observation, error)
}

type Input struct {
	URL        string
	Name       string
	SenderID   string
	SenderName string
}

type Observation struct {
	ImageType          string   `json:"image_type,omitempty"`
	Surface            string   `json:"surface,omitempty"`
	VisibleText        []string `json:"visible_text,omitempty"`
	ErrorCodes         []string `json:"error_codes,omitempty"`
	Operation          string   `json:"operation,omitempty"`
	LikelyProblem      string   `json:"likely_problem,omitempty"`
	QueryHints         []string `json:"query_hints,omitempty"`
	Redactions         []string `json:"redactions,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
	NeedsClarification bool     `json:"needs_clarification,omitempty"`
	Summary            string   `json:"summary,omitempty"`
}

func (o *Observation) HistoryText(senderName string) string {
	if o == nil {
		return ""
	}
	var lines []string
	lines = append(lines, "[图片观察]")
	if senderName = strings.TrimSpace(senderName); senderName != "" {
		lines = append(lines, "发送者: "+senderName)
	}
	appendValue := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	appendList := func(label string, values []string) {
		values = compactStrings(values)
		if len(values) > 0 {
			lines = append(lines, label+": "+strings.Join(values, " / "))
		}
	}

	appendValue("摘要", o.Summary)
	appendValue("画面类型", o.ImageType)
	appendValue("产品界面", o.Surface)
	appendValue("正在操作", o.Operation)
	appendValue("可能问题", o.LikelyProblem)
	appendList("可见文字", o.VisibleText)
	appendList("错误码", o.ErrorCodes)
	appendList("检索提示词", o.QueryHints)
	appendList("隐私处理", o.Redactions)
	appendValue("置信度", o.Confidence)
	if o.NeedsClarification {
		lines = append(lines, "需要追问: 是")
	}
	return strings.Join(lines, "\n")
}

type noopObserver struct{}

func NewObserver(cfg config.AgentConfig) Observer {
	if strings.EqualFold(strings.TrimSpace(cfg.ImageUnderstandingMode), "disabled") {
		return noopObserver{}
	}
	timeout := time.Duration(cfg.VisionTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &turnmeshObserver{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (noopObserver) Observe(_ context.Context, input Input) (*Observation, error) {
	return FallbackObservation(input, errors.New("image understanding disabled")), nil
}

type turnmeshObserver struct {
	cfg        config.AgentConfig
	httpClient *http.Client
}

func (o *turnmeshObserver) Observe(ctx context.Context, input Input) (*Observation, error) {
	imageData, mimeType, err := o.downloadImage(ctx, input)
	if err != nil {
		return nil, err
	}

	maxTokens := 900
	temperature := 0.0
	result, err := turnmesh.RunOneShot(ctx, turnmesh.Config{
		Provider:        firstNonEmpty(o.cfg.VisionProvider, "openai"),
		Model:           firstNonEmpty(o.cfg.VisionModel, o.cfg.Model),
		BaseURL:         firstNonEmpty(o.cfg.VisionBaseURL, o.cfg.BaseURL),
		APIKey:          firstNonEmpty(o.cfg.VisionAPIKey, o.cfg.APIKey),
		Temperature:     &temperature,
		MaxOutputTokens: &maxTokens,
		MaxMediaBytes:   o.maxImageBytes(),
		HTTPClient:      o.httpClient,
	}, turnmesh.OneShotRequest{
		SystemPrompt: observerSystemPrompt,
		Messages: []turnmesh.Message{
			turnmesh.UserParts(
				turnmesh.TextPart(observerUserPrompt(input)),
				turnmesh.ImageBytesPart(
					mimeType,
					imageData,
					turnmesh.WithPartName(input.Name),
					turnmesh.WithPartDetail(firstNonEmpty(o.cfg.VisionDetail, "low")),
					turnmesh.WithPartMetadata(map[string]string{
						"sender_id": input.SenderID,
						"url":       input.URL,
					}),
				),
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("observe image with turnmesh: %w", err)
	}

	observation, err := ParseObservation(result.Text)
	if err != nil {
		return &Observation{
			Summary:            trimRunes(strings.TrimSpace(result.Text), 600),
			Confidence:         "low",
			NeedsClarification: true,
		}, nil
	}
	observation.normalize()
	return observation, nil
}

func (o *turnmeshObserver) downloadImage(ctx context.Context, input Input) ([]byte, string, error) {
	imageURL := strings.TrimSpace(input.URL)
	if imageURL == "" {
		return nil, "", errors.New("image url is empty")
	}
	parsed, err := url.Parse(imageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, "", fmt.Errorf("image url must be http(s), got %q", imageURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build image request: %w", err)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download image status %d", resp.StatusCode)
	}

	maxBytes := o.maxImageBytes()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("image size %d exceeds limit %d", len(data), maxBytes)
	}

	mimeType := imageMIMEType(resp.Header.Get("Content-Type"), input, data)
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return nil, "", fmt.Errorf("unsupported image content type %q", mimeType)
	}
	return data, mimeType, nil
}

func (o *turnmeshObserver) maxImageBytes() int64 {
	if o.cfg.VisionImageMaxBytes > 0 {
		return o.cfg.VisionImageMaxBytes
	}
	return 5 * 1024 * 1024
}

func ParseObservation(text string) (*Observation, error) {
	object, err := extractJSONObject(text)
	if err != nil {
		return nil, err
	}
	var observation Observation
	if err := json.Unmarshal([]byte(object), &observation); err != nil {
		return nil, fmt.Errorf("parse observation json: %w", err)
	}
	return &observation, nil
}

func FallbackObservation(input Input, cause error) *Observation {
	summary := "客户发送了一张图片"
	if input.Name != "" {
		summary += "：" + input.Name
	}
	if cause != nil {
		summary += "；系统暂时没有成功识别图片内容"
	}
	return &Observation{
		ImageType:          "unknown",
		Summary:            trimRunes(summary, 500),
		LikelyProblem:      "图片内容未识别，需要客户补充页面、报错或关键文字",
		Confidence:         "low",
		NeedsClarification: true,
	}
}

func imageMIMEType(header string, input Input, data []byte) string {
	if header != "" {
		if mediaType, _, err := mime.ParseMediaType(header); err == nil && strings.HasPrefix(strings.ToLower(mediaType), "image/") {
			return mediaType
		}
	}
	if detected := turnmesh.DetectMIMEType(data); strings.HasPrefix(strings.ToLower(detected), "image/") {
		return detected
	}
	if ext := imageExtension(input); ext != "" {
		if mediaType := mime.TypeByExtension(ext); strings.HasPrefix(strings.ToLower(mediaType), "image/") {
			return mediaType
		}
	}
	return ""
}

func imageExtension(input Input) string {
	if ext := filepath.Ext(strings.TrimSpace(input.Name)); ext != "" {
		return ext
	}
	parsed, err := url.Parse(strings.TrimSpace(input.URL))
	if err != nil {
		return ""
	}
	return filepath.Ext(parsed.Path)
}

func extractJSONObject(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty observation")
	}
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
		text = strings.TrimSpace(text)
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return "", errors.New("json object not found")
	}
	return text[start : end+1], nil
}

func observerUserPrompt(input Input) string {
	var lines []string
	lines = append(lines, "请观察客户刚发来的图片，提取客服回答和知识库检索会用到的信息。")
	if input.Name != "" {
		lines = append(lines, "图片名: "+input.Name)
	}
	if input.SenderName != "" {
		lines = append(lines, "发送者: "+input.SenderName)
	}
	lines = append(lines, "只输出 JSON。不要解释。")
	return strings.Join(lines, "\n")
}

const observerSystemPrompt = `你是企业微信客服场景的图片观察器。你的任务不是回答用户，而是把图片转成后续 RAG 检索和客服 Agent 可用的结构化上下文。

要求：
1. 识别图片类型，例如产品页面截图、报错弹窗、配置页面、订单/表格、聊天截图、二维码、照片等。
2. 提取可见文字、错误码、按钮名、页面名、产品名、操作对象和可能的问题。
3. 如果图片中有手机号、姓名、身份证、地址、token、密钥、订单号等敏感信息，只描述类型，不要原文输出。
4. 不要编造看不清的内容。看不清就标记 needs_clarification=true。
5. 只输出一个 JSON 对象，字段为 image_type、surface、visible_text、error_codes、operation、likely_problem、query_hints、redactions、confidence、needs_clarification、summary。`

func (o *Observation) normalize() {
	o.VisibleText = compactStrings(o.VisibleText)
	o.ErrorCodes = compactStrings(o.ErrorCodes)
	o.QueryHints = compactStrings(o.QueryHints)
	o.Redactions = compactStrings(o.Redactions)
	o.Summary = trimRunes(strings.TrimSpace(o.Summary), 600)
	o.ImageType = strings.TrimSpace(o.ImageType)
	o.Surface = strings.TrimSpace(o.Surface)
	o.Operation = strings.TrimSpace(o.Operation)
	o.LikelyProblem = strings.TrimSpace(o.LikelyProblem)
	o.Confidence = strings.TrimSpace(o.Confidence)
	if o.Confidence == "" {
		o.Confidence = "medium"
	}
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func trimRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	return string([]rune(text)[:maxRunes])
}
