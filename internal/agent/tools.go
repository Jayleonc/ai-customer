package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	khClient   *khclient.Client
	groupStore *store.GroupStore
}

func NewToolExecutor(kh *khclient.Client, gs *store.GroupStore) *ToolExecutor {
	return &ToolExecutor{khClient: kh, groupStore: gs}
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
		SemanticQuery string   `json:"semantic_query"`
		Keywords      []string `json:"keywords"`
		Strategy      string   `json:"strategy"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "参数解析错误: " + err.Error()
	}

	query := strings.TrimSpace(args.SemanticQuery)
	if query == "" {
		return "参数错误: semantic_query 不能为空"
	}

	// 决定检索策略
	strategy := args.Strategy
	if strategy == "" {
		if len(args.Keywords) > 0 {
			strategy = "hybrid"
		} else {
			strategy = "semantic"
		}
	}

	// 获取群组关联的 dataset_ids
	var datasetIDs []string
	if group, err := e.groupStore.GetByGroupID(ctx, groupID); err == nil && len(group.DatasetIDs) > 0 {
		datasetIDs = group.DatasetIDs
	}

	// 构造查询：keyword 模式下关键词作为主查询，其他模式语义查询为主
	searchQuery := query
	if strategy == "keyword" && len(args.Keywords) > 0 {
		searchQuery = strings.Join(args.Keywords, " ")
	}
	keywords := enrichRetrieveKeywords(query, args.Keywords)

	topK := 20
	if strategy == "hybrid" {
		topK = 120
	}

	resp, err := e.khClient.Retrieve(ctx, &khclient.RetrieveRequest{
		Query:          searchQuery,
		DatasetIDs:     datasetIDs,
		Keywords:       keywords,
		TopK:           topK,
		ScoreThreshold: 0.2,
		SearchStrategy: strategy,
	})
	if err != nil {
		return "知识库检索失败: " + err.Error()
	}

	if len(resp.List) == 0 {
		return "未找到相关结果。请尝试改写 semantic_query 或删减 keywords 后重试。"
	}

	var sb strings.Builder
	for i, r := range resp.List {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&sb, "片段 %d (Score: %.4f):\n[来源: %s] %s\ndoc_id=%s\n%s\n\n",
			i+1, r.Score, r.DocumentName, r.VfsPath, r.DocumentID, r.Content)
	}
	return sb.String()
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

	content := doc.Content
	if len(content) > 3000 {
		content = content[:3000] + "\n...(内容已截断)"
	}
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
			Description: `在企业知识库中检索相关文档片段。
使用此工具前，你必须仔细分析用户的原始输入：
1. 不要把用户的原话直接作为查询条件。
2. 必须区分出哪些是需要语义理解的内容（填入 semantic_query），哪些是必须精确匹配的字眼（填入 keywords）。
3. 如果第一次搜索无结果，严禁直接回复找不到。你必须删减 keywords 或改写 semantic_query，至少重试 2 次后才能放弃。`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"semantic_query": map[string]any{
						"type":        "string",
						"description": "用于向量匹配的纯净语义查询。必须剥离用户输入中的无意义语气词（如'帮我查一下'），并补全上下文代词。",
					},
					"keywords": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
						"description": "从问题中提取的硬性专有名词、英文缩写、错误码或特定产品名（如 'Token', 'VIP', 'HTTP 500'）。" +
							"如果你要查找错误码、UUID、版本号、特定人名或专有英文词汇，必须将它们填入 keywords 数组。",
					},
					"strategy": map[string]any{
						"type":        "string",
						"enum":        []string{"semantic", "keyword", "hybrid"},
						"description": "检索策略。概念理解或模糊意图选 semantic；查具体错误码或特定名称选 keyword；不确定选 hybrid。",
					},
				},
				"required": []string{"semantic_query"},
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
