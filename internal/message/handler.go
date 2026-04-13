package message

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"git.pinquest.cn/ai-customer/internal/agent"
	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/model"
	"git.pinquest.cn/ai-customer/internal/store"
	"git.pinquest.cn/ai-customer/internal/wecom"
	"github.com/google/uuid"
)

// pendingAgentReq 等待执行的 Agent 请求（Agent 运行期间收到的更新问题）
type pendingAgentReq struct {
	query              string
	queryFromOwnerHist bool
	triggerID          string
	triggerName        string
	ownerID            string
	ownerName          string
	notifyMemberIDs    []string
	convID             string
	robotID            string
	groupID            string
	dialogKey          string
}

// Handler 群消息核心处理器
type Handler struct {
	agentSvc         *agent.Service
	agentCfg         config.AgentConfig
	wecom            *wecom.Client
	groupStore       *store.GroupStore
	groupMemberStore *store.GroupMemberStore
	robotStore       *store.RobotStore
	convStore        *store.ConversationStore
	msgStore         *store.MessageStore
	agentRunning     sync.Map // key: groupID:ownerID → true，防止同一个问题归属者并发触发多次 Agent
	pendingQuery     sync.Map // key: groupID:ownerID → *pendingAgentReq，Agent 运行期间最新待处理请求
}

const ownerHistoryFallbackWindow = 20 * time.Minute
const groupUniqueQuestionWindow = 30 * time.Minute

func NewHandler(
	agentSvc *agent.Service,
	agentCfg config.AgentConfig,
	wc *wecom.Client,
	gs *store.GroupStore,
	gms *store.GroupMemberStore,
	rs *store.RobotStore,
	cs *store.ConversationStore,
	ms *store.MessageStore,
) *Handler {
	return &Handler{
		agentSvc:         agentSvc,
		agentCfg:         agentCfg,
		wecom:            wc,
		groupStore:       gs,
		groupMemberStore: gms,
		robotStore:       rs,
		convStore:        cs,
		msgStore:         ms,
	}
}

// HandleGroupMessage 实现 dispatcher.MessageHandler 接口
// 所有有效文本消息都会被存储到会话历史中，只有 @机器人 的消息才触发 Agent
func (h *Handler) HandleGroupMessage(ctx context.Context, evt *model.ReceiveGroupMsgEvent, raw []byte) {
	msg := evt.Data.Msg
	groupID := msg.ReceiverID
	robotID := evt.RobotID

	slog.Info("[msg] received",
		"group_id", groupID,
		"sender_id", msg.SenderID,
		"msg_type", msg.MsgType,
		"at_list", fmt.Sprintf("%+v", msg.AtList),
	)

	// 1. 过滤：只处理文本消息
	if msg.MsgType != 2 {
		slog.Info("[msg] skipped: not text", "msg_type", msg.MsgType)
		return
	}

	// 2. 顺手更新群成员表：发送者 + at_list 里的人
	h.ensureGroupMembers(ctx, groupID, msg)

	// 3. 检查群是否已注册
	group, err := h.groupStore.GetByGroupID(ctx, groupID)
	if err != nil {
		slog.Warn("[msg] group not registered, ignoring", "group_id", groupID, "error", err)
		return
	}

	// 3.5 守卫：当前机器人不在本群的允许响应列表中，直接跳过
	// 防止群内多个机器人重复处理同一条消息
	if !slices.Contains(group.RobotIDs, robotID) {
		slog.Debug("[msg] skipped: robot not in group's robot list", "robot_id", robotID, "group_id", groupID)
		return
	}
	// 3.6 若配置了默认回复机器人，则只允许该机器人回复
	if group.RobotID != "" && group.RobotID != robotID {
		slog.Debug("[msg] skipped: robot is not the designated reply bot",
			"robot_id", robotID, "designated_robot_id", group.RobotID, "group_id", groupID)
		return
	}

	// 4. 提取消息文本（去掉 @前缀 + 机器人名字）
	robotName := h.resolveRobotName(ctx, robotID)
	textContent := extractQuery(msg, robotName)
	isAtRobot := h.isAtRobot(msg, group, robotName)

	slog.Info("[msg] extracted",
		"raw_text", msg.MsgContent.Text,
		"cleaned", textContent,
		"robot_name", robotName,
		"is_at_robot", isAtRobot,
	)

	// 5. 解析发言人名称
	senderName := h.resolveSenderName(ctx, groupID, msg.SenderID)
	ownerID := msg.SenderID
	delegatedByMention := false
	if isAtRobot && textContent == "" {
		if delegatedOwnerID := h.findDelegatedOwner(msg, group); delegatedOwnerID != "" {
			ownerID = delegatedOwnerID
			delegatedByMention = true
		}
	}
	ownerName := h.resolveSenderName(ctx, groupID, ownerID)
	if ownerName == "" && ownerID == msg.SenderID {
		ownerName = senderName
	}

	// 6. 获取/创建 owner 会话
	conv, err := h.convStore.FindOrCreateActive(ctx, groupID, ownerID)
	if err != nil {
		slog.Error("[msg] find/create conversation failed", "error", err)
		return
	}

	// 7. 如果有实际文本内容，保存到会话历史
	if textContent != "" {
		userMsg := &model.Message{
			ID:             uuid.NewString(),
			ConversationID: conv.ID,
			Role:           "user",
			SenderID:       msg.SenderID,
			SenderName:     senderName,
			Content:        textContent,
			WecomMsgID:     msg.MsgID,
		}
		inserted, err := h.msgStore.Create(ctx, userMsg)
		if err != nil {
			slog.Error("[msg] save failed", "error", err, "content", textContent)
			return
		}
		if !inserted {
			slog.Info("[msg] duplicate ignored", "wecom_msg_id", msg.MsgID)
			return
		}
		slog.Info("[msg] saved",
			"conv_id", conv.ID,
			"sender_name", senderName,
			"content", textContent,
		)
	} else {
		slog.Info("[msg] empty after cleaning, not saving to history")
	}

	// 8. 如果没有 @机器人，不触发 Agent
	if !isAtRobot {
		return
	}

	// 9. 确定要发给 Agent 的 query
	// 空 @ 仅回退到 owner 最近 10 分钟内的问题型消息，避免串线到无关发言
	agentQuery := textContent
	queryFromOwnerHistory := false
	if agentQuery == "" {
		agentQuery = h.findLastOwnerQuestion(ctx, groupID, ownerID, ownerHistoryFallbackWindow)
		if agentQuery != "" {
			queryFromOwnerHistory = true
			slog.Info("[msg] empty @robot, using owner recent question",
				"owner_id", ownerID, "window", ownerHistoryFallbackWindow.String(), "resolved_query", agentQuery)
		}
	}
	if agentQuery == "" && !delegatedByMention && ownerID == msg.SenderID {
		candidateOwnerID, candidateQuery, candidateCount := h.findUniqueRecentGroupQuestion(ctx, groupID, msg.SenderID, groupUniqueQuestionWindow)
		if candidateOwnerID != "" {
			ownerID = candidateOwnerID
			ownerName = h.resolveSenderName(ctx, groupID, ownerID)
			ownerConv, convErr := h.convStore.FindOrCreateActive(ctx, groupID, ownerID)
			if convErr != nil {
				slog.Error("[msg] find/create owner conversation failed", "error", convErr, "owner_id", ownerID)
				return
			}
			conv = ownerConv
			agentQuery = candidateQuery
			queryFromOwnerHistory = true
			slog.Info("[msg] empty @robot, using unique recent group question",
				"owner_id", ownerID,
				"window", groupUniqueQuestionWindow.String(),
				"candidate_count", candidateCount,
				"resolved_query", agentQuery)
		} else {
			slog.Info("[msg] empty @robot, no unique recent group question",
				"window", groupUniqueQuestionWindow.String(), "candidate_count", candidateCount)
		}
	}
	if agentQuery == "" {
		slog.Info("[msg] no recent owner question found, asking clarification",
			"owner_id", ownerID, "window", ownerHistoryFallbackWindow.String())
		h.sendReply(
			ctx,
			robotID,
			groupID,
			buildNotifyMemberIDs(msg.SenderID, ownerID),
			"我没定位到最近可回答的问题。请直接发完整问题，或 @要代答的人并补一句具体问题。",
		)
		return
	}

	// 10. 防抖：同一个群同一个 owner 如果已有 Agent 正在执行，将本次请求存入 pending（覆盖旧的），不丢弃
	req := &pendingAgentReq{
		query:              agentQuery,
		queryFromOwnerHist: queryFromOwnerHistory,
		triggerID:          msg.SenderID,
		triggerName:        senderName,
		ownerID:            ownerID,
		ownerName:          ownerName,
		notifyMemberIDs:    buildNotifyMemberIDs(msg.SenderID, ownerID),
		convID:             conv.ID,
		robotID:            robotID,
		groupID:            groupID,
		dialogKey:          h.buildDialogKey(groupID, ownerID),
	}
	if _, loaded := h.agentRunning.LoadOrStore(req.dialogKey, true); loaded {
		h.pendingQuery.Store(req.dialogKey, req)
		slog.Info("[msg] agent already running, queued as pending",
			"group_id", groupID, "owner_id", ownerID, "query", agentQuery)
		return
	}
	defer h.agentRunning.Delete(req.dialogKey)

	h.runAgent(ctx, group, req)
}

// runAgent 执行 Agent，结束后检查 pendingQuery 并继续处理
func (h *Handler) runAgent(ctx context.Context, group *model.EnterpriseGroup, req *pendingAgentReq) {
	for req != nil {
		slog.Info("[agent] executing",
			"group_id", req.groupID,
			"trigger_id", req.triggerID,
			"owner_id", req.ownerID,
			"conv_id", req.convID,
			"trigger_name", req.triggerName,
			"owner_name", req.ownerName,
			"query", req.query,
		)
		systemPrompt := agent.BuildSystemPrompt(group, h.agentCfg)
		reply, err := h.agentSvc.Execute(ctx, &agent.Request{
			GroupID:          req.groupID,
			ConversationID:   req.convID,
			SenderID:         req.ownerID,
			SenderName:       req.ownerName,
			UserQuery:        req.query,
			SkipQueryRewrite: req.queryFromOwnerHist,
			SystemPrompt:     systemPrompt,
		})
		if err != nil {
			slog.Error("[agent] execute failed", "error", err)
		} else {
			slog.Info("[agent] result",
				"group_id", req.groupID,
				"reply_length", len(reply),
				"reply_preview", truncate(reply, 200),
			)
			normalizedReply, usedFallback := normalizeAgentReply(reply)
			if usedFallback {
				slog.Info("[agent] fallback reply used",
					"group_id", req.groupID, "owner_id", req.ownerID)
			}
			aiMsg := &model.Message{
				ID:             uuid.NewString(),
				ConversationID: req.convID,
				Role:           "assistant",
				Content:        normalizedReply,
			}
			if _, err := h.msgStore.Create(ctx, aiMsg); err != nil {
				slog.Error("[msg] save AI reply failed", "error", err)
			}
			h.sendReply(ctx, req.robotID, req.groupID, req.notifyMemberIDs, normalizedReply)
		}

		// 检查是否有新的 pending 请求（Agent 运行期间收到的更新问题）
		if v, ok := h.pendingQuery.LoadAndDelete(req.dialogKey); ok {
			req = v.(*pendingAgentReq)
			slog.Info("[agent] processing pending query",
				"group_id", req.groupID, "owner_id", req.ownerID, "query", req.query)
		} else {
			req = nil
		}
	}
}

func (h *Handler) buildDialogKey(groupID, ownerID string) string {
	return groupID + ":" + ownerID
}

func normalizeAgentReply(reply string) (string, bool) {
	trimmed := strings.TrimSpace(reply)
	if trimmed == "" || strings.Contains(trimmed, "[NO_ANSWER]") {
		return "这个问题我暂时没在知识库里检索到明确答案。你可以补充一下具体功能页面、接口名或报错关键词，我再帮你精确定位。", true
	}
	return trimmed, false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ensureGroupMembers 收到消息时，把发送者和 at_list 里的人 upsert 到 group_member
func (h *Handler) ensureGroupMembers(ctx context.Context, groupID string, msg model.GroupMessage) {
	// 发送者
	h.groupMemberStore.Upsert(ctx, &model.GroupMember{
		GroupID:  groupID,
		MemberID: msg.SenderID,
		Role:     3,
	})

	// at_list 里的人（可能带 nickname）
	for _, at := range msg.AtList {
		if at.MemberID == "" {
			continue
		}
		m := &model.GroupMember{
			GroupID:  groupID,
			MemberID: at.MemberID,
			Role:     3,
		}
		if at.Nickname != "" {
			m.Nickname = at.Nickname
		}
		h.groupMemberStore.Upsert(ctx, m)
	}
}

// resolveSenderName 从群成员表获取发言人昵称
func (h *Handler) resolveSenderName(ctx context.Context, groupID, senderID string) string {
	member, err := h.groupMemberStore.GetByMemberID(ctx, groupID, senderID)
	if err != nil || member.Nickname == "" {
		return ""
	}
	return member.Nickname
}

// resolveRobotName 从 robot 表获取机器人名称
func (h *Handler) resolveRobotName(ctx context.Context, robotID string) string {
	if robotID == "" {
		return ""
	}
	robot, err := h.robotStore.GetByRobotID(ctx, robotID)
	if err == nil && robot.Name != "" {
		return robot.Name
	}

	// 兜底：表里没有机器人或名称为空时，实时拉取一次，兼容“机器人已登录但服务错过 login.success”场景
	list, syncErr := h.wecom.SyncGetRobotList(ctx, []string{robotID})
	if syncErr != nil {
		slog.Warn("[msg] sync robot info failed", "robot_id", robotID, "error", syncErr)
		return ""
	}
	for _, item := range list {
		if item.RobotID != robotID {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.NickName)
		}
		loginStatus := item.LoginStatus
		if loginStatus == 0 {
			loginStatus = 2
		}
		if upErr := h.robotStore.UpsertFromSync(ctx, &model.Robot{
			RobotID:     item.RobotID,
			Name:        name,
			Avatar:      item.Avatar,
			Phone:       item.Phone,
			Email:       item.Email,
			LoginStatus: loginStatus,
		}); upErr != nil {
			slog.Warn("[msg] upsert robot info failed", "robot_id", robotID, "error", upErr)
		}
		return name
	}
	return ""
}

// findDelegatedOwner 在 @列表中找第一个非机器人成员，作为代答目标
func (h *Handler) findDelegatedOwner(msg model.GroupMessage, group *model.EnterpriseGroup) string {
	for _, at := range msg.AtList {
		memberID := strings.TrimSpace(at.MemberID)
		if memberID == "" {
			continue
		}
		if slices.Contains(group.RobotIDs, memberID) {
			continue
		}
		return memberID
	}
	return ""
}

// findLastOwnerQuestion 仅从 owner 本人的跨会话历史里回退问题，且限制时间窗口和问题型文本
func (h *Handler) findLastOwnerQuestion(ctx context.Context, groupID, ownerID string, window time.Duration) string {
	history, err := h.msgStore.ListRecentByGroupAndSender(ctx, groupID, ownerID, 30)
	if err != nil {
		return ""
	}
	cutoff := time.Now().Add(-window)
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if msg.CreatedAt.Before(cutoff) {
			continue
		}
		if !isQuestionLike(content) {
			continue
		}
		return content
	}
	return ""
}

// findUniqueRecentGroupQuestion 在群最近窗口中找“唯一候选”的问题型消息（排除触发者）
// 仅当候选发送者唯一时返回，避免多人并发提问时串线
func (h *Handler) findUniqueRecentGroupQuestion(ctx context.Context, groupID, excludeSenderID string, window time.Duration) (string, string, int) {
	history, err := h.msgStore.ListRecentByGroup(ctx, groupID, 50)
	if err != nil {
		return "", "", 0
	}
	cutoff := time.Now().Add(-window)
	latestQuestionBySender := make(map[string]string)

	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		senderID := strings.TrimSpace(msg.SenderID)
		if senderID == "" || senderID == excludeSenderID {
			continue
		}
		if msg.CreatedAt.Before(cutoff) {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" || !isQuestionLike(content) {
			continue
		}
		// 倒序遍历，首次命中的即该发送者最近问题
		if _, exists := latestQuestionBySender[senderID]; !exists {
			latestQuestionBySender[senderID] = content
		}
	}

	if len(latestQuestionBySender) != 1 {
		return "", "", len(latestQuestionBySender)
	}
	for senderID, question := range latestQuestionBySender {
		return senderID, question, 1
	}
	return "", "", 0
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

// isAtRobot 检查消息是否 @了本群任意一个允许响应的机器人
// 先检查 at_list（平台解析），如果为空则回退到检查原始文本是否包含 @机器人名字
func (h *Handler) isAtRobot(msg model.GroupMessage, group *model.EnterpriseGroup, robotName string) bool {
	if msg.IsAtAll {
		return true
	}
	for _, at := range msg.AtList {
		if slices.Contains(group.RobotIDs, at.MemberID) {
			return true
		}
	}
	// 兜底：平台未解析 at_list 时，检查文本中是否包含 @机器人名字
	if robotName != "" && msg.MsgContent.Text != nil {
		rawText := msg.MsgContent.Text.Content
		if strings.Contains(rawText, "@"+robotName) {
			slog.Info("[msg] at_list empty but text contains @robot", "robot_name", robotName)
			return true
		}
	}
	return false
}

// extractQuery 从消息中提取实际问题
// 去除 @xxx、机器人名字、残留的 @ 符号，返回纯净的用户问题文本
func extractQuery(msg model.GroupMessage, robotName string) string {
	if msg.MsgContent.Text == nil {
		return ""
	}
	text := strings.TrimSpace(msg.MsgContent.Text.Content)

	// 去除 @机器人名字（连带 @ 符号）
	if robotName != "" {
		text = strings.ReplaceAll(text, "@"+robotName, "")
		text = strings.ReplaceAll(text, robotName, "")
	}

	// 去除 @其他人
	for _, at := range msg.AtList {
		if at.Nickname != "" {
			text = strings.ReplaceAll(text, "@"+at.Nickname, "")
		}
	}

	// 清理残留的 @提及 模式（含昵称未知的 @人名）
	text = regexp.MustCompile(`@\S+`).ReplaceAllString(text, "")

	// 清理多余空格
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func buildNotifyMemberIDs(triggerID, ownerID string) []string {
	seen := make(map[string]struct{}, 2)
	out := make([]string, 0, 2)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	add(triggerID)
	add(ownerID)
	return out
}

// sendReply 发送回复到企微群，@触发者（代答场景再额外 @问题归属者）
func (h *Handler) sendReply(ctx context.Context, robotID, groupID string, mentionMemberIDs []string, reply string) {
	atList := make([]model.AtMember, 0, len(mentionMemberIDs))
	for _, memberID := range mentionMemberIDs {
		memberID = strings.TrimSpace(memberID)
		if memberID == "" {
			continue
		}
		atList = append(atList, model.AtMember{MemberID: memberID})
	}

	payload := model.SendGroupMsgReq{
		RobotID: robotID,
		UniqSN:  uuid.NewString(),
		Msg: model.OutboundGroupMsg{
			SenderID:   robotID,
			ReceiverID: groupID,
			MsgType:    2,
			MsgContent: model.MsgContent{
				Text: &model.TextContent{Content: reply},
			},
			AtList:     atList,
			AtLocation: 0,
		},
	}

	if err := h.wecom.SendGroupMsg(ctx, payload); err != nil {
		slog.Error("send group reply failed", "group_id", groupID, "error", err)
	}
}
