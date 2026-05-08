package message

import (
	"context"
	"testing"
	"time"

	"git.pinquest.cn/ai-customer/internal/model"
)

func TestWaitForAdjacentImageObservationWaitsForActiveObservation(t *testing.T) {
	h := &Handler{}
	state := h.beginImageObservation("group-1:user-1")

	done := make(chan struct{})
	go func() {
		h.waitForAdjacentImageObservation(context.Background(), "group-1:user-1", imageCoalesceWindow)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("wait returned before image observation finished")
	case <-time.After(20 * time.Millisecond):
	}

	state.finish()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait did not return after image observation finished")
	}
}

func TestQueueImageFollowupIfAgentRunning(t *testing.T) {
	h := &Handler{}
	dialogKey := "group-1:user-1"
	h.agentRunning.Store(dialogKey, true)

	h.queueImageFollowupIfAgentRunning(dialogKey, "robot-1", "group-1", "conv-1", "user-1", "张三")

	value, ok := h.pendingQuery.Load(dialogKey)
	if !ok {
		t.Fatal("pending query was not queued")
	}
	req := value.(*pendingAgentReq)
	if req.query == "" || req.ownerID != "user-1" || req.robotID != "robot-1" || req.groupID != "group-1" {
		t.Fatalf("unexpected pending request: %+v", req)
	}
}

func TestImageCoalesceDelayExtendsForVisualQuestion(t *testing.T) {
	if got := imageCoalesceDelay("这个图片里说的是啥？"); got != visualQuestionImageCoalesceWindow {
		t.Fatalf("visual coalesce delay = %s, want %s", got, visualQuestionImageCoalesceWindow)
	}
	if got := imageCoalesceDelay("订单怎么查询？"); got != imageCoalesceWindow {
		t.Fatalf("normal coalesce delay = %s, want %s", got, imageCoalesceWindow)
	}
}

func TestAppInfoObservationWecomID(t *testing.T) {
	if got, want := appInfoObservationWecomID(" app-1 "), "appinfo:app-1:image_observation"; got != want {
		t.Fatalf("appInfoObservationWecomID() = %q, want %q", got, want)
	}
}

func TestMessageWecomIDUsesAppInfoForQuoteLookup(t *testing.T) {
	msg := model.GroupMessage{
		MsgID:   "msg-1",
		AppInfo: " app-1 ",
	}
	if got, want := messageWecomID(msg), "appinfo:app-1:message"; got != want {
		t.Fatalf("messageWecomID() = %q, want %q", got, want)
	}

	msg.AppInfo = ""
	if got, want := messageWecomID(msg), "msg-1"; got != want {
		t.Fatalf("messageWecomID() without appinfo = %q, want %q", got, want)
	}
}

func TestShouldUseQuotedQuestion(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{text: "", want: true},
		{text: "帮他回复下", want: true},
		{text: "这个怎么说", want: true},
		{text: "重新回答", want: true},
		{text: "为什么今天才主加了3个人就限频不给我加了", want: false},
	}
	for _, tt := range cases {
		if got := shouldUseQuotedQuestion(tt.text); got != tt.want {
			t.Fatalf("shouldUseQuotedQuestion(%q) = %t, want %t", tt.text, got, tt.want)
		}
	}
}

func TestLastUnansweredQuestionSkipsAnsweredHistory(t *testing.T) {
	now := time.Now()
	history := []model.Message{
		{Role: "user", Content: "为什么马上回复客户也显示超时？", CreatedAt: now.Add(-10 * time.Minute)},
		{Role: "assistant", Content: "这是超时规则。", CreatedAt: now.Add(-9 * time.Minute)},
		{Role: "user", Content: "企微员工账号怎么接入？", CreatedAt: now.Add(-1 * time.Minute)},
	}

	got := lastUnansweredQuestionFromHistory(history, now.Add(-20*time.Minute))
	if got != "企微员工账号怎么接入？" {
		t.Fatalf("last unanswered question = %q", got)
	}
}

func TestLastUnansweredQuestionReturnsEmptyWhenLatestQuestionWasAnswered(t *testing.T) {
	now := time.Now()
	history := []model.Message{
		{Role: "user", Content: "为什么马上回复客户也显示超时？", CreatedAt: now.Add(-10 * time.Minute)},
		{Role: "assistant", Content: "这是超时规则。", CreatedAt: now.Add(-9 * time.Minute)},
	}

	got := lastUnansweredQuestionFromHistory(history, now.Add(-20*time.Minute))
	if got != "" {
		t.Fatalf("last unanswered question = %q, want empty", got)
	}
}
