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
	khClient     *khclient.Client
	groupStore   *store.GroupStore
	msgStore     *store.MessageStore
	httpClient   *http.Client
}

func NewService(cfg config.AgentConfig, executor *ToolExecutor, kh *khclient.Client, gs *store.GroupStore, msgStore *store.MessageStore) *Service {
	return &Service{
		cfg:          cfg,
		toolExecutor: executor,
		khClient:     kh,
		groupStore:   gs,
		msgStore:     msgStore,
		httpClient:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Request 是 Agent 执行请求
type Request struct {
	GroupID          string
	ConversationID   string
	SenderID         string
	SenderName       string
	UserQuery        string
	SkipQueryRewrite bool
	SystemPrompt     string
}

// Execute 执行 Agent 循环，返回最终回复文本
func (s *Service) Execute(ctx context.Context, req *Request) (string, error) {
	messages := s.buildMessages(ctx, req)

	// TODO 不见得这是最好的，因为如果之前的聊天里已经回答过这个问题，又来问，那先RAG，绝对是浪费资源。所以不见得这个预检索是很好的，去掉似乎也可以。
	// 强制预检索：在 LLM 决策之前，先用用户问题搜索知识库，把结果注入上下文
	// 保存结果用于 LLM 不可用时的降级回复
	var preSearchResult string
	if req.UserQuery != "" {
		preSearchResult = s.preSearch(ctx, req.UserQuery, req.GroupID, req.ConversationID, req.SkipQueryRewrite)
		if preSearchResult != "" {
			slog.Info("[agent] pre-search injected", "result_length", len(preSearchResult))
			messages = append(messages, chatMessage{
				Role:    "system",
				Content: fmt.Sprintf("以下是知识库中与用户问题相关的检索结果，请基于这些内容回答：\n\n%s", preSearchResult),
			})
		} else {
			// 预检索无结果：提示 LLM 不要反复调用 search_knowledge 浪费 token
			// 引导其明确告知未检索到并追问关键上下文，而不是返回哨兵文本
			messages = append(messages, chatMessage{
				Role:    "system",
				Content: "系统已自动检索知识库，暂未找到与用户问题直接相关的内容。你最多可以再尝试一次 search_knowledge（换用不同关键词），如果仍无结果，请直接告诉用户“当前知识库未检索到明确答案”，并追问一个关键补充信息。若存在多个可能场景，请用一句话给出 2-3 个候选让用户二选一/三选一（例如：你说的是 A 还是 B）。不要输出 [NO_ANSWER]。",
			})
		}
	}

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
		if preSearchResult != "" {
			slog.Warn("[agent] turnmesh init failed, degrading to pre-search", turnmeshErrorLogAttrs(err)...)
			return s.degradeToPreSearch(preSearchResult), nil
		}
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
		if preSearchResult != "" {
			slog.Warn("[agent] turnmesh run failed, degrading to pre-search", turnmeshErrorLogAttrs(err)...)
			return s.degradeToPreSearch(preSearchResult), nil
		}
		return "", fmt.Errorf("turnmesh run failed: %w", err)
	}

	if clarification := extractClarification(result.ToolResults); clarification != "" {
		return clarification, nil
	}

	answer := strings.TrimSpace(result.Text)
	if answer == "" {
		if preSearchResult != "" {
			return s.degradeToPreSearch(preSearchResult), nil
		}
		return "抱歉，我处理这个问题花了太长时间。请尝试更具体地描述您的问题。", nil
	}

	return s.checkReplyQuality(answer), nil
}

// preSearch 在 LLM 调用前，结合对话历史重写 query，使用 hybrid 检索知识库
// 直接调用 khClient 而非复用 searchKnowledge 工具，使用独立的预检索参数
func (s *Service) preSearch(ctx context.Context, query string, groupID string, conversationID string, skipQueryRewrite bool) string {
	// Step 1: Query Rewrite — 全量历史丢给 AI，让 AI 判断用户真正意图
	rewrittenQuery := query
	history, err := s.msgStore.ListRecent(ctx, conversationID, s.cfg.HistoryLimit)
	if err != nil {
		slog.Warn("[agent] pre-search load history failed", "error", err)
	}

	if !skipQueryRewrite && s.cfg.QueryRewriteMode != "disabled" {
		rewrittenQuery = s.rewriteQueryWithLLM(ctx, query, history)
		rewrittenQuery = enhanceQueryWithHistory(rewrittenQuery, history)
	}
	if rewrittenQuery != query {
		slog.Info("[agent] query rewritten", "original", query, "rewritten", rewrittenQuery)
	}

	// Step 2: 获取群关联的 dataset_ids
	var datasetIDs []string
	if group, err := s.groupStore.GetByGroupID(ctx, groupID); err == nil && len(group.DatasetIDs) > 0 {
		datasetIDs = group.DatasetIDs
	}

	// Step 3: hybrid 检索（kh 内部并行执行语义 + PGroonga 全文，RRF 融合）
	strategy := s.cfg.PreSearchStrategy
	if strategy == "" {
		strategy = "hybrid"
	}
	topK := s.cfg.PreSearchTopK
	if topK <= 0 {
		topK = 10
	}
	keywords := enrichRetrieveKeywords(rewrittenQuery, extractRetrieveKeywords(rewrittenQuery))
	if strategy == "hybrid" && topK < 200 {
		topK = 200
	}
	scoreThreshold := s.cfg.PreSearchScoreThreshold
	if scoreThreshold <= 0 {
		scoreThreshold = 0.3
	}

	resp, err := s.khClient.Retrieve(ctx, &khclient.RetrieveRequest{
		Query:          rewrittenQuery,
		DatasetIDs:     datasetIDs,
		Keywords:       keywords,
		TopK:           topK,
		ScoreThreshold: scoreThreshold,
		SearchStrategy: strategy,
	})
	if err != nil {
		slog.Warn("[agent] pre-search retrieve failed", "error", err)
		return ""
	}
	if len(resp.List) == 0 {
		slog.Info("[agent] pre-search no results", "query", rewrittenQuery)
		return ""
	}

	// Step 3.5: 通用消歧增强
	// 从首轮结果中提取候选术语（如功能名/模块名），用于：
	// 1) 二次扩检索提升召回；
	// 2) 注入给 LLM 作为反问候选，避免泛泛追问。
	candidates := deriveDisambiguationCandidates(resp.List, rewrittenQuery, keywords, 6)
	snippetKeywords := keywords
	if len(candidates) > 0 {
		snippetKeywords = mergeKeywordList(keywords, candidates[:minInt(3, len(candidates))], 8)
	}
	if len(candidates) > 0 {
		topScore := resp.List[0].Score
		if topScore < 0.85 {
			refinedKeywords := snippetKeywords
			refinedQuery := strings.TrimSpace(rewrittenQuery + " " + strings.Join(refinedKeywords, " "))
			if refinedQuery != "" && refinedQuery != rewrittenQuery {
				refinedResp, refinedErr := s.khClient.Retrieve(ctx, &khclient.RetrieveRequest{
					Query:          refinedQuery,
					DatasetIDs:     datasetIDs,
					Keywords:       refinedKeywords,
					TopK:           topK,
					ScoreThreshold: scoreThreshold,
					SearchStrategy: strategy,
				})
				if refinedErr != nil {
					slog.Warn("[agent] pre-search refined retrieve failed", "error", refinedErr)
				} else if len(refinedResp.List) > 0 {
					originCount := len(resp.List)
					resp.List = mergeRetrieveResults(resp.List, refinedResp.List, topK)
					slog.Info("[agent] pre-search refined retrieve merged",
						"origin_count", originCount,
						"merged_count", len(resp.List),
						"candidate_count", len(candidates),
						"refined_query", refinedQuery)
				}
			}
		}
	}

	// Step 4: 格式化结果，top 5 片段 + 截断 300 字
	maxSnippets := s.cfg.PreSearchMaxSnippets
	if maxSnippets <= 0 {
		maxSnippets = 5
	}
	maxSnippetLen := s.cfg.PreSearchMaxSnippetLength
	if maxSnippetLen <= 0 {
		maxSnippetLen = 300
	}

	results := append([]khclient.RetrieveResult(nil), resp.List...)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	var sb strings.Builder
	if len(candidates) > 0 {
		fmt.Fprintf(&sb, "候选术语（用于消歧）: %s\n\n", strings.Join(candidates, " / "))
	}
	for i, r := range results {
		if i >= maxSnippets {
			break
		}
		content := focusSnippetForQuery(r.Content, rewrittenQuery, snippetKeywords, maxSnippetLen)
		fmt.Fprintf(&sb, "片段 %d (相关度: %.2f):\n[来源: %s]\ndoc_id=%s\n%s\n\n",
			i+1, r.Score, r.DocumentName, r.DocumentID, content)
	}
	return sb.String()
}

func deriveDisambiguationCandidates(results []khclient.RetrieveResult, query string, keywords []string, max int) []string {
	if len(results) == 0 || max <= 0 {
		return nil
	}

	queryTerms := map[string]struct{}{}
	for _, t := range extractRetrieveKeywords(query) {
		queryTerms[t] = struct{}{}
	}
	for _, t := range keywords {
		t = strings.TrimSpace(t)
		if t != "" {
			queryTerms[t] = struct{}{}
		}
	}

	blacklist := map[string]struct{}{
		"功能": {}, "规则": {}, "说明": {}, "操作": {}, "页面": {}, "系统": {}, "平台": {}, "客户": {}, "接口": {}, "参数": {}, "方式": {}, "问题": {},
		"文档": {}, "知识库": {}, "faq": {}, "xlsx": {}, "doc": {}, "docx": {}, "csv": {},
	}

	termScore := map[string]float64{}
	limit := minInt(len(results), 12)
	for i := 0; i < limit; i++ {
		r := results[i]
		// 候选词只从正文抽取，避免把文件名/路径噪音带入 refined query
		source := strings.TrimSpace(r.Content)
		if source == "" {
			continue
		}
		if utf8.RuneCountInString(source) > 300 {
			source = string([]rune(source)[:300])
		}
		terms := extractRetrieveKeywords(source)
		for _, term := range terms {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			if utf8.RuneCountInString(term) < 2 || utf8.RuneCountInString(term) > 10 {
				continue
			}
			if strings.ContainsAny(term, `/\._-`) {
				continue
			}
			if _, skip := queryTerms[term]; skip {
				continue
			}
			if _, skip := blacklist[term]; skip {
				continue
			}
			termScore[term] += r.Score
		}
	}

	if len(termScore) == 0 {
		return nil
	}

	type pair struct {
		term  string
		score float64
	}
	all := make([]pair, 0, len(termScore))
	for term, score := range termScore {
		all = append(all, pair{term: term, score: score})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score == all[j].score {
			return utf8.RuneCountInString(all[i].term) > utf8.RuneCountInString(all[j].term)
		}
		return all[i].score > all[j].score
	})

	out := make([]string, 0, minInt(max, len(all)))
	for _, it := range all {
		if len(out) >= max {
			break
		}
		out = append(out, it.term)
	}
	return out
}

func mergeKeywordList(base []string, extra []string, max int) []string {
	out := make([]string, 0, len(base)+len(extra))
	seen := map[string]struct{}{}
	appendTerm := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range base {
		appendTerm(v)
	}
	for _, v := range extra {
		appendTerm(v)
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func mergeRetrieveResults(primary []khclient.RetrieveResult, secondary []khclient.RetrieveResult, max int) []khclient.RetrieveResult {
	merged := make([]khclient.RetrieveResult, 0, len(primary)+len(secondary))
	seen := map[string]int{}
	appendResult := func(r khclient.RetrieveResult) {
		key := strings.TrimSpace(r.DocumentID) + "|" + strings.TrimSpace(r.Content)
		if key == "|" {
			key = strings.TrimSpace(r.DocumentName) + "|" + strings.TrimSpace(r.VfsPath)
		}
		if idx, ok := seen[key]; ok {
			if r.Score > merged[idx].Score {
				merged[idx] = r
			}
			return
		}
		seen[key] = len(merged)
		merged = append(merged, r)
	}
	for _, r := range primary {
		appendResult(r)
	}
	for _, r := range secondary {
		appendResult(r)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if max > 0 && len(merged) > max {
		merged = merged[:max]
	}
	return merged
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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

func enrichRetrieveKeywords(query string, base []string) []string {
	out := make([]string, 0, len(base)+1)
	seen := map[string]struct{}{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	phrase := normalizeQueryPhrase(query)
	if phrase != "" {
		add(phrase)
	}
	for _, kw := range base {
		add(kw)
	}
	return out
}

func normalizeQueryPhrase(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"，", " ", ",", " ", "。", " ", ".", " ",
		"？", " ", "?", " ", "！", " ", "!", " ",
		"；", " ", ";", " ", "：", " ", ":", " ",
		"（", " ", "）", " ", "(", " ", ")", " ",
		"【", " ", "】", " ", "[", " ", "]", " ",
	)
	q = replacer.Replace(q)
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return ""
	}
	r := utf8.RuneCountInString(q)
	if r < 4 || r > 24 {
		return ""
	}
	return q
}

func enhanceQueryWithHistory(query string, history []model.Message) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" || isQuestionLike(trimmed) {
		return trimmed
	}

	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != "user" {
			continue
		}
		prev := strings.TrimSpace(msg.Content)
		if prev == "" || !isQuestionLike(prev) {
			continue
		}
		if strings.Contains(prev, trimmed) {
			return prev
		}
		return prev + " " + trimmed
	}

	return trimmed
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

	// 转换为 chatMessage 并应用距离衰减裁剪
	var historyMsgs []chatMessage
	for _, msg := range history {
		content := msg.Content
		// 群聊场景：user 消息注入发言人标识，让 LLM 知道是谁在说话
		if msg.Role == "user" && msg.SenderName != "" {
			content = fmt.Sprintf("[%s]: %s", msg.SenderName, content)
		} else if msg.Role == "user" && msg.SenderID != "" {
			content = fmt.Sprintf("[用户 %s]: %s", msg.SenderID[:8], content)
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
		userContent = fmt.Sprintf("[用户 %s]: %s", req.SenderID[:8], userContent)
	}
	slog.Info("[agent] current query", "content", userContent)
	messages = append(messages, chatMessage{
		Role:    "user",
		Content: userContent,
	})

	return messages
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

// degradeToPreSearch LLM 不可用时，基于预检索结果构造降级回复
func (s *Service) degradeToPreSearch(preSearchResult string) string {
	lines := strings.Split(preSearchResult, "\n")
	var contentParts []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 跳过元数据行，只保留正文内容
		if strings.HasPrefix(line, "片段 ") ||
			strings.HasPrefix(line, "[来源:") ||
			strings.HasPrefix(line, "doc_id=") ||
			strings.HasPrefix(line, "候选术语") {
			continue
		}
		contentParts = append(contentParts, line)
	}

	if len(contentParts) == 0 {
		return "系统暂时繁忙，请稍后再试。"
	}

	var sb strings.Builder
	sb.WriteString("系统查询显示，以下是知识库中相关的参考信息：\n\n")
	totalRunes := 0
	maxRunes := 500
	if s.cfg.ReplyMaxLength > 0 && s.cfg.ReplyMaxLength-50 > 0 {
		maxRunes = s.cfg.ReplyMaxLength - 50
	}

	for _, part := range contentParts {
		partLen := utf8.RuneCountInString(part)
		if totalRunes+partLen > maxRunes {
			break
		}
		sb.WriteString(part)
		sb.WriteString("\n")
		totalRunes += partLen
	}

	return strings.TrimSpace(sb.String())
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

系统已经自动用用户的问题检索了知识库，检索结果会附在消息末尾。你必须基于这些检索结果回答，严禁用自己的训练知识编造答案。

### 工作流
1. **理解问题**：结合会话历史理解用户真正在问什么。消息格式为 [用户名]: 内容。
2. **阅读检索结果**：系统已自动检索，结果在消息中。仔细阅读所有片段。
3. **精读文档**：如果检索到的片段信息不完整，调用 read_document 读取全文。
4. **补充检索**：如果自动检索的结果不够精准，可以调用 search_knowledge 用更精确的关键词重新检索。
5. **基于检索结果回答**：只用检索到的内容作答，严禁编造。
6. **无相关结果时**：不要输出 [NO_ANSWER]。请明确告知“当前知识库未检索到明确答案”，并追问一个关键补充信息（功能页面、接口名、报错关键词三选一）。如果检索里出现了多个候选术语，优先让用户在 2-3 个候选中确认，而不是泛泛追问。

## 群聊上下文理解
- 客户不会每个问题都 @你，他们可能聊了几句之后才 @你。你必须结合会话历史理解完整上下文。
- 当内部同事 @你时，是希望你帮忙回答客户之前提出的问题，回溯上文找到真正的问题。
- 客户提问通常很模糊（"这个怎么弄"、"不对啊"），结合上下文推断具体功能和操作。

## 回复要求
- 简洁明了，3-5 句话，字数绝对不超过 %d 字
- 只回答用户问的问题，不要主动扩展、不要提供额外建议
- 如果回答基于知识库，必须在正文开头或合适位置说明“根据知识库显示”或“系统查询显示”等引导语
- 严禁在回复末尾加引导语（如"如果还有问题随时问我"、"如果需要可以说下具体场景"等）
- 语气像一个熟悉产品的同事在帮忙，专业但不生硬
- 能定位到具体操作步骤就直接给步骤，不要说"请参考文档"
- 客户问了未开通功能时，调用 check_feature_tag 确认后告知

## 格式要求（严格遵守）
- 回复内容不要使用任何 Markdown 格式符号，包括但不限于：**加粗**、*斜体*、# 标题、- 列表符号、代码块等
- 使用纯文本回复，用换行和数字序号来组织内容（如 1. 2. 3.）
- 不要原封不动地复制知识库原文，要用自己的话重新组织和润色，让回复自然流畅，像真人在对话`, group.CustomerName, group.FeatureTag, agentCfg.ReplyMaxLength)
}
