package agent

import (
	"strings"
	"testing"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/khclient"
)

func TestFormatRetrieveResultsKeepsFullEvidenceWhenPerItemLimitDisabled(t *testing.T) {
	resp := &khclient.RetrieveResponse{
		Results: []khclient.RetrieveResult{
			{
				ID:           "seg-1",
				EvidenceType: "text",
				DocumentID:   "doc-1",
				DocumentName: "Doc",
				Content:      "开头内容。中间关键步骤。最后的关键结论必须保留。",
				Score:        0.9,
			},
		},
	}

	out := formatRetrieveResultsForPrompt(resp, retrievalFormatOptionsFromConfig(config.AgentConfig{
		RetrievalMaxEvidence:      8,
		RetrievalEvidenceMaxChars: 0,
		RetrievalContextBudget:    1000,
	}, "怎么处理", nil, nil))

	if !strings.Contains(out, "最后的关键结论必须保留") {
		t.Fatalf("expected full evidence to be preserved, got:\n%s", out)
	}
	if strings.Contains(out, "调用 read_document 查看全文") {
		t.Fatalf("did not expect per-item truncation marker, got:\n%s", out)
	}
}

func TestFormatRetrieveResultsAppliesTotalBudget(t *testing.T) {
	resp := &khclient.RetrieveResponse{
		Results: []khclient.RetrieveResult{
			{
				ID:           "seg-1",
				EvidenceType: "text",
				DocumentID:   "doc-1",
				DocumentName: "Doc",
				Content:      strings.Repeat("很长的证据内容", 80),
				Score:        0.9,
			},
		},
	}

	out := formatRetrieveResultsForPrompt(resp, retrievalFormatOptions{
		Query:            "怎么处理",
		MaxEvidence:      8,
		MaxEvidenceChars: 0,
		ContextBudget:    220,
	})

	if !strings.Contains(out, "证据已按上下文预算截断") {
		t.Fatalf("expected context budget truncation marker, got:\n%s", out)
	}
}

func TestCheckReplyQualityDisabledWhenReplyMaxLengthIsZero(t *testing.T) {
	svc := &Service{cfg: config.AgentConfig{ReplyMaxLength: 0}}
	reply := strings.Repeat("完整回答", 120)

	if got := svc.checkReplyQuality(reply); got != reply {
		t.Fatalf("expected reply hard truncation to be disabled")
	}
}
