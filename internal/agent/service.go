package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/khclient"
	"git.pinquest.cn/ai-customer/internal/model"
	"git.pinquest.cn/ai-customer/internal/store"
	"github.com/Jayleonc/turnmesh"
)

// Service 是客服 Agent 业务壳，底层 turn/tool loop 交给 turnmesh runtime。
type Service struct {
	cfg          config.AgentConfig
	toolExecutor *ToolExecutor
	msgStore     *store.MessageStore
	httpClient   *http.Client
}

func NewService(cfg config.AgentConfig, executor *ToolExecutor, msgStore *store.MessageStore) *Service {
	return &Service{
		cfg:          cfg,
		toolExecutor: executor,
		msgStore:     msgStore,
		httpClient:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Request 是 Agent 执行请求
type Request struct {
	GroupID        string
	ConversationID string
	SenderID       string
	SenderName     string
	UserQuery      string
	SystemPrompt   string
}

// Execute 执行 Agent 循环，返回最终回复文本
func (s *Service) Execute(ctx context.Context, req *Request) (string, error) {
	messages := s.buildMessages(ctx, req)

	// Token 预算：裁剪历史消息，防止输入超模型上下文限制
	messages = trimMessagesToBudget(messages, s.cfg.TokenBudget)
	slog.Info("[agent] token budget check",
		"estimated_tokens", estimateMessagesTokens(messages),
		"budget", s.cfg.TokenBudget)

	runtime, err := turnmesh.New(ctx, turnmesh.Config{
		Provider:    "openai-chatcompat",
		Model:       s.cfg.Model,
		BaseURL:     s.cfg.BaseURL,
		APIKey:      s.cfg.APIKey,
		Temperature: floatPtr(s.cfg.Temperature),
		Tools:       s.buildRuntimeTools(req.GroupID),
		HTTPClient:  s.httpClient,
	})
	if err != nil {
		return "", fmt.Errorf("turnmesh init failed: %w", err)
	}
	defer runtime.Close()

	result, err := runtime.RunTurn(ctx, turnmesh.TurnRequest{
		SessionID: req.ConversationID,
		Messages:  runtimeMessages(messages),
		Metadata: map[string]string{
			"group_id":        req.GroupID,
			"conversation_id": req.ConversationID,
			"sender_id":       req.SenderID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("turnmesh run failed: %w", err)
	}

	if clarification := extractClarification(result.ToolResults); clarification != "" {
		return clarification, nil
	}

	answer := strings.TrimSpace(result.Text)
	if answer == "" {
		return "抱歉，我处理这个问题花了太长时间。请尝试更具体地描述您的问题。", nil
	}

	return s.checkReplyQuality(answer), nil
}

func compactNonEmptyStrings(values []string) []string {
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

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trimTextByRunes(text string, maxRunes int, suffix string) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	suffixRunes := utf8.RuneCountInString(suffix)
	if suffixRunes >= maxRunes {
		return string([]rune(text)[:maxRunes])
	}
	return string([]rune(text)[:maxRunes-suffixRunes]) + suffix
}

type retrievalFormatOptions struct {
	Query            string
	Keywords         []string
	Candidates       []string
	MaxEvidence      int
	MaxEvidenceChars int
	ContextBudget    int
}

func retrievalFormatOptionsFromConfig(cfg config.AgentConfig, query string, keywords []string, candidates []string) retrievalFormatOptions {
	return retrievalFormatOptions{
		Query:            strings.TrimSpace(query),
		Keywords:         compactNonEmptyStrings(keywords),
		Candidates:       compactNonEmptyStrings(candidates),
		MaxEvidence:      normalizedRetrievalMaxEvidence(cfg),
		MaxEvidenceChars: cfg.RetrievalEvidenceMaxChars,
		ContextBudget:    normalizedRetrievalContextBudget(cfg),
	}
}

func normalizedRetrievalMaxEvidence(cfg config.AgentConfig) int {
	if cfg.RetrievalMaxEvidence > 0 {
		return cfg.RetrievalMaxEvidence
	}
	return 8
}

func normalizedRetrievalContextBudget(cfg config.AgentConfig) int {
	if cfg.RetrievalContextBudget > 0 {
		return cfg.RetrievalContextBudget
	}
	return 6000
}

func normalizedReadDocumentMaxChars(cfg config.AgentConfig) int {
	if cfg.ReadDocumentMaxChars > 0 {
		return cfg.ReadDocumentMaxChars
	}
	return 10000
}

func formatRetrieveResultsForPrompt(resp *khclient.RetrieveResponse, opts retrievalFormatOptions) string {
	if resp == nil || len(resp.Results) == 0 {
		return ""
	}

	results := append([]khclient.RetrieveResult(nil), resp.Results...)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if opts.MaxEvidence > 0 && len(results) > opts.MaxEvidence {
		results = results[:opts.MaxEvidence]
	}

	var sb strings.Builder
	if opts.Query != "" {
		fmt.Fprintf(&sb, "检索 query: %s\n", opts.Query)
	}
	if resp.Retrieval != nil {
		fmt.Fprintf(&sb, "检索策略: %s, top_k=%d, candidate_top_k=%d, fallback=%t\n",
			resp.Retrieval.Strategy, resp.Retrieval.TopK, resp.Retrieval.CandidateTopK, resp.Retrieval.FallbackUsed)
	}
	if len(opts.Candidates) > 0 {
		fmt.Fprintf(&sb, "候选术语（用于消歧）: %s\n", strings.Join(opts.Candidates, " / "))
	}
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}

	for i, r := range results {
		content := strings.TrimSpace(firstNonEmptyText(r.Content, r.Snippet))
		if opts.MaxEvidenceChars > 0 {
			content = focusSnippetForQuery(content, opts.Query, opts.Keywords, opts.MaxEvidenceChars)
		}
		if content == "" {
			content = "(该证据没有可展示正文，请根据来源和 doc_id 调用 read_document 精读)"
		}

		var block strings.Builder
		fmt.Fprintf(&block, "证据 %d (相关度: %.2f", i+1, r.Score)
		if r.EvidenceType != "" {
			fmt.Fprintf(&block, ", 类型: %s", r.EvidenceType)
		}
		block.WriteString("):\n")
		if r.DocumentName != "" {
			fmt.Fprintf(&block, "来源: %s\n", r.DocumentName)
		}
		if r.VfsPath != "" {
			fmt.Fprintf(&block, "路径: %s\n", r.VfsPath)
		}
		if r.StructurePath != "" && r.StructurePath != r.VfsPath {
			fmt.Fprintf(&block, "结构路径: %s\n", r.StructurePath)
		}
		if r.DocumentID != "" {
			fmt.Fprintf(&block, "doc_id=%s\n", r.DocumentID)
		}
		if r.AssetID != "" {
			fmt.Fprintf(&block, "asset_id=%s\n", r.AssetID)
		}
		if r.PreviewURL != "" {
			fmt.Fprintf(&block, "图片预览=%s\n", r.PreviewURL)
		}
		block.WriteString(content)
		block.WriteString("\n\n")

		next := block.String()
		if opts.ContextBudget > 0 {
			remaining := opts.ContextBudget - utf8.RuneCountInString(sb.String())
			if remaining <= 160 {
				break
			}
			if utf8.RuneCountInString(next) > remaining {
				next = trimTextByRunes(next, remaining, "\n...(证据已按上下文预算截断，可调用 read_document 精读全文)")
				sb.WriteString(next)
				break
			}
		}
		sb.WriteString(next)
	}

	return strings.TrimSpace(sb.String())
}

func extractRetrieveKeywords(query string) []string {
	normalized := strings.TrimSpace(query)
	noiseWords := []string{
		"请问", "麻烦", "帮我", "我想问", "想问下", "就是", "一下", "一下子",
		"同时", "一起", "多少", "几个", "怎么", "如何", "为什么",
		"咋回事", "什么情况", "咋办", "帮看看", "看看",
		"吗", "呢", "呀", "啊", "吧", "里的", "里面",
	}
	for _, w := range noiseWords {
		normalized = strings.ReplaceAll(normalized, w, " ")
	}

	separators := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '，', '.', '。', '?', '？', '!', '！', ';', '；', ':', '：', '、', '"', '\'', '“', '”', '(', ')', '（', '）', '[', ']', '【', '】':
			return true
		default:
			return false
		}
	}
	parts := strings.FieldsFunc(normalized, separators)
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if utf8.RuneCountInString(p) < 2 {
			continue
		}
		if utf8.RuneCountInString(p) > 12 {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		out = append(out, strings.TrimSpace(query))
	}
	return out
}

func isQuestionLike(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if strings.Contains(t, "?") || strings.Contains(t, "？") {
		return true
	}
	questionWords := []string{"多少", "几个", "怎么", "如何", "为什么", "什么", "是否", "能否", "可不可以", "有没有", "吗", "呢"}
	for _, w := range questionWords {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func focusSnippetForQuery(content string, query string, keywords []string, maxSnippetLen int) string {
	if maxSnippetLen <= 0 {
		return content
	}
	runes := []rune(content)
	if len(runes) <= maxSnippetLen {
		return content
	}

	hints := make([]string, 0, len(keywords)+1)
	seen := map[string]struct{}{}
	addHint := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || utf8.RuneCountInString(h) < 2 {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		hints = append(hints, h)
	}
	addHint(strings.TrimSpace(query))
	for _, kw := range keywords {
		addHint(kw)
	}
	for _, t := range extractRetrieveKeywords(query) {
		addHint(t)
	}

	start := 0
	for _, hint := range hints {
		byteIdx := strings.Index(content, hint)
		if byteIdx < 0 {
			continue
		}
		runeIdx := utf8.RuneCountInString(content[:byteIdx])
		start = runeIdx - maxSnippetLen/3
		if start < 0 {
			start = 0
		}
		break
	}

	end := start + maxSnippetLen
	if end > len(runes) {
		end = len(runes)
		if end-maxSnippetLen > 0 {
			start = end - maxSnippetLen
		}
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "...(前文省略)" + snippet
	}
	if end < len(runes) {
		snippet = snippet + "...(调用 read_document 查看全文)"
	}
	return snippet
}

// buildMessages 构建 LLM 消息列表
// 历史消息作为独立的 user/assistant message 注入，保留发言人身份
func (s *Service) buildMessages(ctx context.Context, req *Request) []chatMessage {
	var messages []chatMessage

	// System prompt
	messages = append(messages, chatMessage{
		Role:    "system",
		Content: req.SystemPrompt,
	})

	// 注入历史消息（保留完整的多轮对话结构）
	history, err := s.msgStore.ListRecent(ctx, req.ConversationID, s.cfg.HistoryLimit)
	if err != nil {
		slog.Warn("[agent] load history failed", "error", err)
	}
	slog.Info("[agent] history loaded", "conv_id", req.ConversationID, "count", len(history))

	history = dropCurrentRequestFromHistory(history, req)
	filteredHistory := filterResolvedHistoryForCurrentTurn(history, 6)
	if len(filteredHistory) != len(history) {
		slog.Info("[agent] resolved history pruned", "original", len(history), "kept", len(filteredHistory))
	}

	// 转换为 chatMessage 并应用距离衰减裁剪
	var historyMsgs []chatMessage
	for _, msg := range filteredHistory {
		content := msg.Content
		// 群聊场景：user 消息注入发言人标识，让 LLM 知道是谁在说话
		if msg.Role == "user" && msg.SenderName != "" {
			content = fmt.Sprintf("[%s]: %s", msg.SenderName, content)
		} else if msg.Role == "user" && msg.SenderID != "" {
			content = fmt.Sprintf("[用户 %s]: %s", shortID(msg.SenderID), content)
		}
		historyMsgs = append(historyMsgs, chatMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	// 距离衰减：最近 3 条完整保留，4-10 条 assistant 截断，更远的只保留问题型 user 消息
	historyMsgs = trimHistoryByDecay(historyMsgs, 3, 7)
	slog.Info("[agent] history after decay", "original", len(history), "trimmed", len(historyMsgs))
	messages = append(messages, historyMsgs...)

	// 当前用户消息（带发言人标识）
	userContent := req.UserQuery
	if req.SenderName != "" {
		userContent = fmt.Sprintf("[%s]: %s", req.SenderName, userContent)
	} else if req.SenderID != "" {
		userContent = fmt.Sprintf("[用户 %s]: %s", shortID(req.SenderID), userContent)
	}
	slog.Info("[agent] current query", "content", userContent)
	messages = append(messages, chatMessage{
		Role:    "user",
		Content: userContent,
	})

	return messages
}

func dropCurrentRequestFromHistory(history []model.Message, req *Request) []model.Message {
	if len(history) == 0 || req == nil {
		return history
	}
	last := history[len(history)-1]
	if last.Role != "user" {
		return history
	}
	if strings.TrimSpace(last.Content) != strings.TrimSpace(req.UserQuery) {
		return history
	}
	if strings.TrimSpace(req.SenderID) != "" && strings.TrimSpace(last.SenderID) != strings.TrimSpace(req.SenderID) {
		return history
	}
	return history[:len(history)-1]
}

func filterResolvedHistoryForCurrentTurn(history []model.Message, preserveTail int) []model.Message {
	if preserveTail < 0 {
		preserveTail = 0
	}
	cutoff := len(history) - preserveTail
	if cutoff <= 0 {
		return history
	}

	drop := make([]bool, len(history))
	for i := 0; i < cutoff; {
		if history[i].Role != "user" {
			i++
			continue
		}

		start := i
		for i < cutoff && history[i].Role == "user" {
			i++
		}

		hasAssistantReply := false
		for i < cutoff && history[i].Role != "user" {
			if history[i].Role == "assistant" {
				hasAssistantReply = true
			}
			i++
		}
		if !hasAssistantReply {
			continue
		}
		for j := start; j < i; j++ {
			drop[j] = true
		}
	}

	filtered := make([]model.Message, 0, len(history))
	for i, msg := range history {
		if !drop[i] {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

func (s *Service) buildRuntimeTools(groupID string) []turnmesh.Tool {
	defined := DefinedTools()
	tools := make([]turnmesh.Tool, 0, len(defined))
	for _, definedTool := range defined {
		toolSchema := definedTool.Parameters
		tools = append(tools, turnmesh.Tool{
			Name:        definedTool.Name,
			Description: definedTool.Description,
			InputSchema: toolSchema,
			Handler: func(ctx context.Context, call turnmesh.ToolCall) (turnmesh.ToolOutcome, error) {
				args := string(firstNonEmptyRaw(call.Arguments, call.Input))
				slog.Info("[agent] turnmesh tool call", "tool", call.Name, "args", args)
				result := s.executeToolSafe(ctx, ToolCall{
					ID:   call.ID,
					Name: call.Name,
					Args: args,
				}, groupID)
				return turnmesh.ToolOutcome{
					Output: result,
					Status: turnmesh.ToolSucceeded,
				}, nil
			},
		})
	}
	return tools
}

func runtimeMessages(messages []chatMessage) []turnmesh.Message {
	out := make([]turnmesh.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, turnmesh.Message{
			Role:    turnmesh.MessageRole(message.Role),
			Content: message.Content,
		})
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func extractClarification(results []turnmesh.ToolResult) string {
	for _, result := range results {
		if result.Tool != "ask_clarification" {
			continue
		}
		if !strings.HasPrefix(result.Output, "[CLARIFICATION]") {
			continue
		}
		return strings.TrimPrefix(result.Output, "[CLARIFICATION]")
	}
	return ""
}

func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func floatPtr(value float64) *float64 {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func turnmeshErrorLogAttrs(err error) []any {
	attrs := []any{"error", err}
	tmErr, ok := turnmesh.AsError(err)
	if !ok {
		return attrs
	}
	if tmErr.Code != "" {
		attrs = append(attrs, "turnmesh_code", tmErr.Code)
	}
	if tmErr.Cause != "" {
		attrs = append(attrs, "turnmesh_cause", tmErr.Cause)
	}
	if len(tmErr.Details) > 0 {
		attrs = append(attrs, "turnmesh_details", tmErr.Details)
	}
	return attrs
}

func replyLengthInstruction(maxLen int) string {
	if maxLen > 0 {
		return fmt.Sprintf("简洁明了，3-5 句话，字数尽量不超过 %d 字；但不要为了压缩而省略关键步骤或必要条件", maxLen)
	}
	return "简洁明了，优先完整准确；简单问题 3-5 句话，涉及步骤、条件或排障时允许适度展开，不要为了压缩而省略关键信息"
}

// executeToolSafe 带超时和 panic 恢复的工具执行
// 防止单个工具调用阻塞整个 Agent 循环或因 panic 导致进程崩溃
func (s *Service) executeToolSafe(ctx context.Context, call ToolCall, groupID string) string {
	timeout := time.Duration(s.cfg.ToolTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[agent] tool panic recovered", "tool", call.Name, "panic", fmt.Sprintf("%v", r))
				ch <- fmt.Sprintf("工具 %s 执行异常，请忽略此工具结果继续回答", call.Name)
			}
		}()
		ch <- s.toolExecutor.Execute(toolCtx, call, groupID)
	}()

	select {
	case result := <-ch:
		return result
	case <-toolCtx.Done():
		slog.Warn("[agent] tool timeout", "tool", call.Name, "timeout", timeout)
		return fmt.Sprintf("工具 %s 执行超时，请忽略此工具结果继续回答", call.Name)
	}
}

// checkReplyQuality 检查 LLM 回复质量：超长回复在句末截断
func (s *Service) checkReplyQuality(reply string) string {
	if reply == "" {
		return reply
	}

	maxLen := s.cfg.ReplyMaxLength
	if maxLen <= 0 {
		return reply
	}

	runes := []rune(reply)
	if len(runes) <= maxLen {
		return reply
	}

	// 尝试在句末截断，避免断在字中间
	cutoff := maxLen
	for i := maxLen; i > maxLen-50 && i > 0; i-- {
		r := runes[i-1]
		if r == '。' || r == '.' || r == '！' || r == '？' || r == '\n' {
			cutoff = i
			break
		}
	}
	slog.Info("[agent] reply truncated", "original_len", len(runes), "truncated_to", cutoff)
	return string(runes[:cutoff])
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
}

// BuildSystemPrompt 构建群聊客服的 system prompt
func BuildSystemPrompt(group *model.EnterpriseGroup, agentCfg config.AgentConfig) string {
	if group.SystemPrompt != "" {
		return group.SystemPrompt
	}

	return fmt.Sprintf(`你是企业微信客户运营群内的 AI 助手。

## 身份与场景
你所在的群是一个**客户运营群**，群内有以下角色：
- **客户**：使用我们产品的外部用户，遇到问题时直接在群里提问，提问通常很模糊。
- **商务/行业顾问/技术支持**：我们的内部同事。
- **你（AI 助手）**：当被 @时，基于知识库帮助回答产品使用问题。

当前群对应的客户是：**%s**
该客户的特性标签（已开通的功能）：%s

## 核心规则：基于知识库回答（不可绕过）

回答产品、功能、配置、接口、报错、操作步骤等知识库问题前，必须先调用 search_knowledge 检索知识库。严禁跳过检索直接用自己的训练知识编造答案。

### 工作流
1. **理解并改写问题**：当前最后一条 user 消息是唯一需要回答的问题。结合会话历史理解客户真正想问什么，消息格式为 [用户名]: 内容；如果历史里出现 [图片观察]，它表示客户图片/截图的视觉摘要和 OCR 线索。不要把当前消息原文直接检索；先在心里把“这个/那个/上面说的/不对啊”等上下文依赖表达改写成完整问题。
2. **主动检索**：用 search_knowledge 提交改写后的自包含 query；keywords 只放错误码、接口名、产品名等硬关键词。如果是内部同事 @你代答客户问题，query 应围绕客户原问题，而不是内部同事的转述动作。
3. **精读文档**：如果检索证据不完整、步骤被截断或需要确认上下文，调用 read_document 读取全文。
4. **补充检索**：如果第一次检索不够精准，改写 query 或删减 keywords 后再检索。
5. **基于检索结果回答**：只用检索到的内容作答，严禁编造。
6. **无相关结果时**：不要输出 [NO_ANSWER]。请明确告知“当前知识库未检索到明确答案”，并追问一个关键补充信息（功能页面、接口名、报错关键词三选一）。如果检索里出现了多个候选术语，优先让用户在 2-3 个候选中确认，而不是泛泛追问。

## 群聊上下文理解
- 客户不会每个问题都 @你，他们可能聊了几句之后才 @你。你必须结合会话历史理解完整上下文。
- 当内部同事 @你时，是希望你帮忙回答客户之前提出的问题，回溯上文找到真正的问题。
- 客户提问通常很模糊（"这个怎么弄"、"不对啊"），结合上下文推断具体功能和操作。
- 历史中已经被 assistant 回复过的旧问题只作为背景，不得再次当作当前要回答的问题；除非当前最后一条 user 消息明确引用、复述或要求重答那个旧问题。
- 如果当前消息只是“重新理解我的问题”“我没发图片”“不是这个”等纠偏表达，优先回到最近一轮未解决的明确问题；不要跳到更早、已经回复过的历史问题。
- 客户发图片时，[图片观察] 只是理解截图的辅助线索，不是知识库答案。必须把图片里的页面名、错误码、按钮名、可见文字等改写成 search_knowledge query，再基于检索结果回答。

## 回复要求
- %s
- 只回答用户问的问题，不要主动扩展、不要提供额外建议
- 如果回答基于知识库，必须在正文开头或合适位置说明“根据知识库显示”或“系统查询显示”等引导语
- 严禁在回复末尾加引导语（如"如果还有问题随时问我"、"如果需要可以说下具体场景"等）
- 语气像一个熟悉产品的同事在帮忙，专业但不生硬
- 能定位到具体操作步骤就直接给步骤，不要说"请参考文档"
- 客户问了未开通功能时，调用 check_feature_tag 确认后告知

## 格式要求（严格遵守）
- 回复内容不要使用任何 Markdown 格式符号，包括但不限于：**加粗**、*斜体*、# 标题、- 列表符号、代码块等
- 使用纯文本回复，用换行和数字序号来组织内容（如 1. 2. 3.）
- 不要原封不动地复制知识库原文，要用自己的话重新组织和润色，让回复自然流畅，像真人在对话`, group.CustomerName, group.FeatureTag, replyLengthInstruction(agentCfg.ReplyMaxLength))
}
