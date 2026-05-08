package agent

import (
	"strings"
	"testing"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/model"
)

func TestSystemPromptRequiresContextAwareQueryRewrite(t *testing.T) {
	prompt := BuildSystemPrompt(&model.EnterpriseGroup{
		CustomerName: "客户 A",
		FeatureTag:   "{}",
	}, config.AgentConfig{})

	for _, want := range []string{
		"理解并改写问题",
		"改写成完整问题",
		"search_knowledge 提交改写后的自包含 query",
		"query 应围绕客户原问题",
		"[图片观察]",
		"图片里的页面名、错误码、按钮名",
		"已经被 assistant 回复过的旧问题只作为背景",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestFilterResolvedHistoryForCurrentTurnDropsStaleAnsweredTurns(t *testing.T) {
	history := []model.Message{
		{Role: "user", Content: "旧问题怎么处理？"},
		{Role: "assistant", Content: "旧答案。"},
		{Role: "user", Content: "企微员工账号怎么接入？"},
		{Role: "assistant", Content: "请确认 1/2/3。"},
		{Role: "user", Content: "1"},
	}

	filtered := filterResolvedHistoryForCurrentTurn(history, 3)
	if len(filtered) != 3 {
		t.Fatalf("filtered len = %d, want 3: %#v", len(filtered), filtered)
	}
	if filtered[0].Content != "企微员工账号怎么接入？" || filtered[2].Content != "1" {
		t.Fatalf("unexpected filtered history: %#v", filtered)
	}
}

func TestSearchKnowledgeToolRequiresSelfContainedQuery(t *testing.T) {
	var searchTool *Tool
	tools := DefinedTools()
	for i := range tools {
		if tools[i].Name == "search_knowledge" {
			searchTool = &tools[i]
			break
		}
	}
	if searchTool == nil {
		t.Fatal("search_knowledge tool not found")
	}

	for _, want := range []string{
		"结合聊天历史判断客户真正想问什么",
		"改写成自包含 query",
		"围绕客户原问题",
		"[图片观察]",
		"已经被 assistant 回复过的旧问题只作为背景",
	} {
		if !strings.Contains(searchTool.Description, want) {
			t.Fatalf("expected tool description to contain %q, got:\n%s", want, searchTool.Description)
		}
	}
}
