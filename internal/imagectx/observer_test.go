package imagectx

import (
	"errors"
	"strings"
	"testing"
)

func TestParseObservationFromFencedJSON(t *testing.T) {
	t.Parallel()

	obs, err := ParseObservation("```json\n{\"summary\":\"报错截图\",\"visible_text\":[\"ERR_001\"],\"needs_clarification\":true}\n```")
	if err != nil {
		t.Fatalf("ParseObservation() error = %v", err)
	}
	if obs.Summary != "报错截图" {
		t.Fatalf("summary = %q", obs.Summary)
	}
	if len(obs.VisibleText) != 1 || obs.VisibleText[0] != "ERR_001" {
		t.Fatalf("visible_text = %#v", obs.VisibleText)
	}
	if !obs.NeedsClarification {
		t.Fatal("needs_clarification = false, want true")
	}
}

func TestObservationHistoryText(t *testing.T) {
	t.Parallel()

	obs := &Observation{
		Summary:       "客户在配置页看到报错",
		Surface:       "配置页",
		VisibleText:   []string{"保存失败", "保存失败", "Token"},
		ErrorCodes:    []string{"HTTP 500"},
		QueryHints:    []string{"配置页", "保存失败"},
		Redactions:    []string{"已隐藏 token"},
		Confidence:    "high",
		LikelyProblem: "保存配置失败",
	}

	text := obs.HistoryText("张三")
	for _, want := range []string{
		"[图片观察]",
		"发送者: 张三",
		"摘要: 客户在配置页看到报错",
		"可见文字: 保存失败 / Token",
		"错误码: HTTP 500",
		"检索提示词: 配置页 / 保存失败",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected history text to contain %q, got:\n%s", want, text)
		}
	}
}

func TestFallbackObservation(t *testing.T) {
	t.Parallel()

	obs := FallbackObservation(Input{Name: "screen.png"}, errors.New("timeout"))
	if obs == nil {
		t.Fatal("FallbackObservation() = nil")
	}
	if !obs.NeedsClarification {
		t.Fatal("NeedsClarification = false, want true")
	}
	if !strings.Contains(obs.Summary, "screen.png") || strings.Contains(obs.Summary, "timeout") {
		t.Fatalf("summary = %q", obs.Summary)
	}
	if obs.LikelyProblem == "" {
		t.Fatal("LikelyProblem is empty")
	}
}

func TestImageMIMETypeUsesHeaderDetectionAndExtension(t *testing.T) {
	t.Parallel()

	pngHeader := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if got := imageMIMEType("application/octet-stream", Input{}, pngHeader); got != "image/png" {
		t.Fatalf("detected mime = %q, want image/png", got)
	}
	if got := imageMIMEType("", Input{Name: "photo.jpg"}, nil); got != "image/jpeg" {
		t.Fatalf("extension mime = %q, want image/jpeg", got)
	}
	if got := imageMIMEType("image/webp; charset=binary", Input{}, nil); got != "image/webp" {
		t.Fatalf("header mime = %q, want image/webp", got)
	}
}
