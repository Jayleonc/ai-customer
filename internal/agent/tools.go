package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/khclient"
	"git.pinquest.cn/ai-customer/internal/store"
)

// Tool 定义 Agent 可调用的工具
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall 表示 LLM 返回的工具调用请求
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"arguments"`
}

// ToolExecutor 负责实际执行工具调用
type ToolExecutor struct {
	cfg        config.AgentConfig
	khClient   *khclient.Client
	groupStore *store.GroupStore
}

func NewToolExecutor(cfg *config.Config, kh *khclient.Client, gs *store.GroupStore) *ToolExecutor {
	return &ToolExecutor{cfg: cfg.Agent, khClient: kh, groupStore: gs}
}

// Execute 执行工具调用，返回结果文本
func (e *ToolExecutor) Execute(ctx context.Context, call ToolCall, groupID string) string {
	switch call.Name {
	case "search_knowledge":
		return e.searchKnowledge(ctx, call.Args, groupID)
	case "read_document":
		return e.readDocument(ctx, call.Args)
	case "check_feature_tag":
		return e.checkFeatureTag(ctx, groupID)
	case "ask_clarification":
		return e.askClarification(call.Args)
	default:
		return fmt.Sprintf("unknown tool: %s", call.Name)
	}
}

func (e *ToolExecutor) searchKnowledge(ctx context.Context, argsJSON string, groupID string) string {
	var args struct {
		Query          string   `json:"query"`
		SemanticQuery  string   `json:"semantic_query"`
		Keywords       []string `json:"keywords"`
		Strategy       string   `json:"strategy"`
		TopK           *int     `json:"top_k"`
		Threshold      *float64 `json:"threshold"`
		ScoreThreshold *float64 `json:"score_threshold"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "参数解析错误: " + err.Error()
	}

	query := strings.TrimSpace(args.Query)
	if query == "" {
		query = strings.TrimSpace(args.SemanticQuery)
	}
	if query == "" {
		return "参数错误: query 不能为空"
	}

	// 获取群组关联的 dataset_ids
	var datasetIDs []string
	if group, err := e.groupStore.GetByGroupID(ctx, groupID); err == nil && len(group.DatasetIDs) > 0 {
		datasetIDs = group.DatasetIDs
	}

	retrieveReq := &khclient.RetrieveRequest{
		Query:      query,
		DatasetIDs: datasetIDs,
		Keywords:   compactNonEmptyStrings(args.Keywords),
	}
	if args.Strategy != "" {
		retrieveReq.SearchStrategy = args.Strategy
	}
	if args.TopK != nil && *args.TopK > 0 {
		retrieveReq.TopK = *args.TopK
	}
	threshold := args.Threshold
	if threshold == nil {
		threshold = args.ScoreThreshold
	}
	if threshold != nil && *threshold > 0 {
		retrieveReq.ScoreThreshold = *threshold
	}

	resp, err := e.khClient.Retrieve(ctx, retrieveReq)
	if err != nil {
		return "知识库检索失败: " + err.Error()
	}

	if len(resp.Results) == 0 {
		return "未找到相关结果。请尝试改写 query 或删减 keywords 后重试。"
	}

	return formatRetrieveResultsForPrompt(resp, retrievalFormatOptionsFromConfig(e.cfg, query, compactNonEmptyStrings(args.Keywords), nil))
}

func (e *ToolExecutor) readDocument(ctx context.Context, argsJSON string) string {
	var args struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "参数解析错误: " + err.Error()
	}

	doc, err := e.khClient.GetDocumentDetail(ctx, args.DocumentID)
	if err != nil {
		return "文档读取失败: " + err.Error()
	}

	content := trimTextByRunes(doc.Content, normalizedReadDocumentMaxChars(e.cfg), "\n...(文档内容已按工具预算截断；如仍缺关键信息，请结合检索结果继续缩小问题范围)")
	return fmt.Sprintf("文档: %s\n\n%s", doc.Name, content)
}

func (e *ToolExecutor) checkFeatureTag(ctx context.Context, groupID string) string {
	group, err := e.groupStore.GetByGroupID(ctx, groupID)
	if err != nil {
		return "群组信息查询失败: " + err.Error()
	}

	return fmt.Sprintf("客户: %s\n特性标签: %s", group.CustomerName, group.FeatureTag)
}

func (e *ToolExecutor) askClarification(argsJSON string) string {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return args.Question
	}
	// 返回追问文本，Agent 会将其作为最终回复
	return "[CLARIFICATION]" + args.Question
}

// DefinedTools 返回 Agent 可用的工具定义（OpenAI function calling 格式）
func DefinedTools() []Tool {
	return []Tool{
		{
			Name: "search_knowledge",
			Description: `在企业知识库中检索相关文档片段。默认把 query 交给 Knowledge Hub RAG 自动选择召回策略，不要主动指定 strategy、top_k 或 threshold，除非用户明确要求限制检索方式、数量或阈值。
使用此工具前，你必须仔细分析用户的原始输入：
1. 不要把用户的原话直接作为查询条件。
2. 必须结合聊天历史判断客户真正想问什么，把“这个/那个/上面说的/不对啊”等上下文依赖表达改写成自包含 query。
3. 如果是内部同事 @你代答客户问题，query 应围绕客户原问题，而不是内部同事的转述动作。
4. 已经被 assistant 回复过的旧问题只作为背景，不能当作当前检索 query；除非当前最后一条 user 消息明确引用、复述或要求重答那个旧问题。
5. 如果历史里出现 [图片观察]，把图片中的页面名、错误码、按钮名、可见文字、可能问题转成 query 和 keywords，不要把“图片观察”四个字本身拿去检索。
6. keywords 只填写用户明确提到且必须精确匹配的词。
7. 如果第一次搜索无结果，严禁直接回复找不到。你必须删减 keywords 或改写 query，至少重试 2 次后才能放弃。`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "传给 Knowledge Hub RAG 的自包含查询。这是你结合聊天历史重写后的真实问题，不是用户原话；必须剥离寒暄、@人、转述语，并补全上下文代词。",
					},
					"keywords": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
						"description": "从问题中提取的硬性专有名词、英文缩写、错误码或特定产品名（如 'Token', 'VIP', 'HTTP 500'）。" +
							"如果你要查找错误码、UUID、版本号、特定人名或专有英文词汇，必须将它们填入 keywords 数组。",
					},
					"strategy": map[string]any{
						"type":        "string",
						"enum":        []string{"semantic", "keyword", "hybrid", "auto"},
						"description": "可选检索策略。默认不要填写，交给 Knowledge Hub RAG 自动选择；仅当用户明确要求某种检索方式时填写。",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "可选返回数量。默认不要填写，交给 Knowledge Hub RAG 决定；仅当用户明确要求数量时填写。",
					},
					"threshold": map[string]any{
						"type":        "number",
						"description": "可选分数阈值。默认不要填写，交给 Knowledge Hub RAG 决定；仅当用户明确要求阈值时填写。",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "read_document",
			Description: "根据 doc_id 精读完整文档内容。search_knowledge 返回的是局部片段，如果关键信息被截断或不足以得出确定结论，必须调用此工具读取全文。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"document_id": map[string]any{"type": "string", "description": "从检索结果中获取的 doc_id"},
				},
				"required": []string{"document_id"},
			},
		},
		{
			Name:        "check_feature_tag",
			Description: "查询当前群对应客户的特性标签（已开通的产品和功能）。在回答客户关于功能开通/权限/配额的问题前必须调用。",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "ask_clarification",
			Description: "当用户的问题过于模糊、缺少关键信息时，生成追问让用户补充。不要猜测，要追问。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "需要用户回答的追问内容"},
				},
				"required": []string{"question"},
			},
		},
	}
}
