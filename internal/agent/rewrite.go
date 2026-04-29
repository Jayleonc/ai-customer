package agent

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"git.pinquest.cn/ai-customer/internal/model"
	"github.com/Jayleonc/turnmesh"
)

const rewriteSystemPrompt = `你是一个查询重写助手。你的任务是根据群聊的对话历史，判断用户最新的一句话到底在问什么，然后输出一个适合知识库检索的 query。

规则：
1. 只输出最终的检索 query，不要任何解释、不要引号
2. 仔细阅读所有对话历史，理解完整的上下文和讨论主题
3. 如果用户的问题包含代词（"这个"、"那个"、"它"等），必须根据上下文替换为具体的功能名或操作名
4. 如果用户问题已经足够明确完整，直接原样返回即可
5. 保留专有名词、产品名、错误码等关键术语
6. 输出控制在 50 字以内`

// rewriteQueryWithLLM 每次都调用 LLM，传入完整对话历史
// 让 AI 自己判断用户到底在问什么，输出适合检索的 query
// 任何环节失败或超时都返回原始 query，绝不阻塞主流程
func (s *Service) rewriteQueryWithLLM(ctx context.Context, query string, history []model.Message) string {
	// 独立超时：rewrite 不值得等太久，超时直接用原始 query
	rewriteCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// 构建 messages：完整历史 + 当前 rewrite 请求
	var messages []chatMessage

	// 注入完整的对话历史，让 AI 理解完整上下文
	for _, msg := range history {
		if msg.Role == "user" || msg.Role == "assistant" {
			content := msg.Content
			if msg.Role == "user" && msg.SenderName != "" {
				content = fmt.Sprintf("[%s]: %s", msg.SenderName, content)
			}
			messages = append(messages, chatMessage{Role: msg.Role, Content: content})
		}
	}

	messages = append(messages, chatMessage{
		Role:    "user",
		Content: "请根据上面的对话历史，判断以下这句话的真正意图，输出适合知识库检索的 query：\n" + query,
	})

	// 选择模型：优先用配置的 rewrite 专用模型，否则复用主模型
	rewriteModel := s.cfg.QueryRewriteModel
	if rewriteModel == "" {
		rewriteModel = s.cfg.Model
	}

	result, err := turnmesh.RunOneShot(rewriteCtx, turnmesh.Config{
		Provider:        "openai-chatcompat",
		Model:           rewriteModel,
		BaseURL:         s.cfg.BaseURL,
		APIKey:          s.cfg.APIKey,
		Temperature:     floatPtr(0.1),
		MaxOutputTokens: intPtr(100),
		HTTPClient:      s.httpClient,
	}, turnmesh.OneShotRequest{
		SystemPrompt: rewriteSystemPrompt,
		Messages:     runtimeMessages(messages),
		Metadata: map[string]string{
			"purpose": "query_rewrite",
		},
	})
	if err != nil {
		slog.Warn("[agent] rewrite one-shot failed, using original query", turnmeshErrorLogAttrs(err)...)
		return fallbackRewriteQuery(query)
	}

	rewritten := strings.TrimSpace(result.Text)
	if rewritten == "" {
		return fallbackRewriteQuery(query)
	}

	return rewritten
}

var rewriteSplitPattern = regexp.MustCompile(`[，,。！？!?；;、\n\r\t]+`)

func fallbackRewriteQuery(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return query
	}

	parts := rewriteSplitPattern.Split(trimmed, -1)
	best := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) > utf8.RuneCountInString(best) {
			best = p
		}
	}
	if best == "" {
		best = trimmed
	}

	leadingNoise := []string{
		"请问", "麻烦", "帮我", "我想问", "想问下", "想咨询", "就是", "这个", "那个",
	}
	for _, prefix := range leadingNoise {
		for strings.HasPrefix(best, prefix) {
			best = strings.TrimSpace(strings.TrimPrefix(best, prefix))
		}
	}

	trailingParticles := []string{"吗", "呢", "呀", "啊", "吧"}
	for _, suffix := range trailingParticles {
		for strings.HasSuffix(best, suffix) {
			best = strings.TrimSpace(strings.TrimSuffix(best, suffix))
		}
	}

	if best == "" {
		return trimmed
	}
	return best
}
