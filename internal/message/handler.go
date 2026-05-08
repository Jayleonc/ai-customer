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
	"git.pinquest.cn/ai-customer/internal/imagectx"
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

type imageObservationState struct {
	createdAt time.Time
	done      chan struct{}
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
	imageObserver    imagectx.Observer
	agentRunning     sync.Map // key: groupID:ownerID → true，防止同一个问题归属者并发触发多次 Agent
	pendingQuery     sync.Map // key: groupID:ownerID → *pendingAgentReq，Agent 运行期间最新待处理请求
	imageObserving   sync.Map // key: groupID:ownerID → *imageObservationState，相邻图文消息合并等待
}

const ownerHistoryFallbackWindow = 20 * time.Minute
const groupUniqueQuestionWindow = 30 * time.Minute
const adjacentImageWindow = 2 * time.Minute
const imageCoalesceWindow = 1200 * time.Millisecond
const visualQuestionImageCoalesceWindow = 3 * time.Second
const imageObservationWaitTimeout = 25 * time.Second

func NewHandler(
	agentSvc *agent.Service,
	agentCfg config.AgentConfig,
	wc *wecom.Client,
	gs *store.GroupStore,
	gms *store.GroupMemberStore,
	rs *store.RobotStore,
	cs *store.ConversationStore,
	ms *store.MessageStore,
	imageObserver imagectx.Observer,
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
		imageObserver:    imageObserver,
	}
}

// HandleGroupMessage 实现 dispatcher.MessageHandler 接口
// 所有有效文本/图片消息都会被存储到会话历史中，只有 @机器人 的消息才触发 Agent
func (h *Handler) HandleGroupMessage(ctx context.Context, evt *model.ReceiveGroupMsgEvent, raw []byte) {
	msg := evt.Data.Msg
	groupID := msg.ReceiverID
	robotID := evt.RobotID

	slog.Info("[msg] received",
		"group_id", groupID,
		"sender_id", msg.SenderID,
		"msg_type", msg.MsgType,
		"app_info_present", strings.TrimSpace(msg.AppInfo) != "",
		"quote_app_info_present", strings.TrimSpace(msg.QuoteAppInfo) != "",
		"quote_app_info", truncate(msg.QuoteAppInfo, 80),
		"at_list", fmt.Sprintf("%+v", msg.AtList),
	)

	// 1. 过滤：只处理文本和图片消息
	isTextMessage := msg.MsgContent.Text != nil
	imageMedia, imageSource := imageMediaFromMessage(msg)
	isImageMessage := imageMedia != nil && strings.TrimSpace(imageMedia.URL) != ""
	if !isTextMessage && !isImageMessage {
		slog.Info("[msg] skipped: unsupported message", "msg_type", msg.MsgType)
		return
	}

	// 2. 顺手更新群成员表：发送者 + at_list 里的人
	h.ensureGroupMembers(ctx, groupID, msg)

	// 3. 检查群是否已注册，没有则自动创建并触发同步信息
	group, err := h.groupStore.GetByGroupID(ctx, groupID)
	if err != nil {
		slog.Warn("[msg] group not registered, auto-creating...", "group_id", groupID, "error", err)
		if err := h.groupStore.UpsertFromCallback(ctx, groupID, "", robotID, ""); err != nil {
			slog.Error("[msg] auto-create group failed", "error", err)
			return
		}

		// 异步触发群信息和成员列表拉取，以便在接下来完善群对象属性（被动接收事件中触发同样逻辑）
		go func(bgCtx context.Context, rID, gID string) {
			if e := h.wecom.GetRemoteGroup(bgCtx, rID, gID, uuid.NewString()); e != nil {
				slog.Warn("[msg] GetRemoteGroup failed", "group_id", gID, "error", e)
			}
			if e := h.wecom.GetGroupMemberList(bgCtx, rID, gID, uuid.NewString()); e != nil {
				slog.Warn("[msg] GetGroupMemberList failed", "group_id", gID, "error", e)
			}
		}(context.Background(), robotID, groupID)

		// 再次获取群对象
		group, err = h.groupStore.GetByGroupID(ctx, groupID)
		if err != nil {
			slog.Error("[msg] reload group failed", "error", err)
			return
		}
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
		"raw_text", rawTextContent(msg),
		"cleaned", textContent,
		"robot_name", robotName,
		"is_at_robot", isAtRobot,
		"has_image", isImageMessage,
		"image_source", imageSource,
	)

	// 5. 解析发言人名称
	senderName := h.resolveSenderName(ctx, groupID, msg.SenderID)
	quotedUserMessage := h.findQuotedUserMessage(ctx, msg.QuoteAppInfo)
	useQuotedQuestion := isAtRobot && quotedUserMessage != nil && shouldUseQuotedQuestion(textContent)
	ownerID := msg.SenderID
	delegatedByMention := false
	if useQuotedQuestion {
		ownerID = quotedUserMessage.SenderID
		delegatedByMention = ownerID != msg.SenderID
	} else if isAtRobot && textContent == "" {
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
			WecomMsgID:     messageWecomID(msg),
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

	imageObservationText := ""
	if isImageMessage {
		imageObservationText = h.observeAndSaveImage(ctx, groupID, robotID, conv.ID, msg, senderName, imageMedia, imageSource)
	}
	if imageObservationText == "" {
		imageObservationText = h.attachQuotedImageObservation(ctx, groupID, conv.ID, msg, senderName)
	}

	// 8. 如果没有 @机器人，不触发 Agent
	if !isAtRobot {
		return
	}

	// 9. 确定要发给 Agent 的 query
	// 空 @ 仅回退到 owner 最近 10 分钟内的问题型消息，避免串线到无关发言
	agentQuery := textContent
	queryFromOwnerHistory := false
	if useQuotedQuestion {
		agentQuery = strings.TrimSpace(quotedUserMessage.Content)
		queryFromOwnerHistory = true
		slog.Info("[msg] @robot using quoted user question",
			"trigger_id", msg.SenderID,
			"owner_id", ownerID,
			"quote_app_info", truncate(msg.QuoteAppInfo, 80),
			"resolved_query", agentQuery)
	}
	if agentQuery == "" {
		if imageObservationText != "" {
			agentQuery = "请根据我刚发送的图片判断客户问题，并结合知识库回答。"
			slog.Info("[msg] image-only @robot, using image observation as query context",
				"owner_id", ownerID, "conv_id", conv.ID)
		}
	}
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
	h.waitForAdjacentImageObservation(ctx, req.dialogKey, imageCoalesceDelay(req.query))

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
			GroupID:        req.groupID,
			ConversationID: req.convID,
			SenderID:       req.ownerID,
			SenderName:     req.ownerName,
			UserQuery:      req.query,
			SystemPrompt:   systemPrompt,
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

func imageMediaFromMessage(msg model.GroupMessage) (*model.MediaContent, string) {
	if msg.MsgContent.Image == nil || strings.TrimSpace(msg.MsgContent.Image.URL) == "" {
		return nil, ""
	}
	return msg.MsgContent.Image, "direct"
}

func (h *Handler) observeAndSaveImage(ctx context.Context, groupID, robotID, convID string, msg model.GroupMessage, senderName string, image *model.MediaContent, imageSource string) string {
	if image == nil || strings.TrimSpace(image.URL) == "" {
		return ""
	}

	dialogKey := h.buildDialogKey(groupID, msg.SenderID)
	state := h.beginImageObservation(dialogKey)
	defer state.finish()
	defer h.cleanupImageObservationLater(dialogKey, state)

	imageURL := strings.TrimSpace(image.URL)
	imageName := strings.TrimSpace(image.Name)
	var resolveErr error
	if h.wecom != nil {
		var result wecom.DownloadChatFileResult
		result, resolveErr = h.wecom.ResolveChatFileURL(ctx, robotID, imageURL)
		if resolveErr != nil {
			slog.Warn("[msg] resolve image url failed",
				"wecom_msg_id", msg.MsgID,
				"image_ref", imageURL,
				"error", resolveErr)
		} else {
			imageURL = result.FileURL
			if imageName == "" {
				imageName = strings.TrimSpace(result.FileName)
			}
		}
	}

	var observation *imagectx.Observation
	var err error
	if resolveErr != nil {
		err = resolveErr
	} else if h.imageObserver != nil {
		observation, err = h.imageObserver.Observe(ctx, imagectx.Input{
			URL:        imageURL,
			Name:       imageName,
			SenderID:   msg.SenderID,
			SenderName: senderName,
		})
	}
	if err != nil {
		slog.Warn("[msg] image observation failed",
			"wecom_msg_id", msg.MsgID,
			"image_name", imageName,
			"error", err)
		observation = imagectx.FallbackObservation(imagectx.Input{
			URL:        imageURL,
			Name:       imageName,
			SenderID:   msg.SenderID,
			SenderName: senderName,
		}, err)
	}
	if observation == nil {
		observation = imagectx.FallbackObservation(imagectx.Input{
			URL:        imageURL,
			Name:       imageName,
			SenderID:   msg.SenderID,
			SenderName: senderName,
		}, nil)
	}

	content := observation.HistoryText(senderName)
	if strings.TrimSpace(content) == "" {
		return ""
	}
	wecomMsgID := strings.TrimSpace(msg.MsgID)
	if imageSource == "direct" && strings.TrimSpace(msg.AppInfo) != "" {
		wecomMsgID = appInfoObservationWecomID(msg.AppInfo)
	} else if wecomMsgID != "" {
		wecomMsgID += ":image_observation"
	} else {
		wecomMsgID = uuid.NewString()
	}
	userMsg := &model.Message{
		ID:             uuid.NewString(),
		ConversationID: convID,
		Role:           "user",
		SenderID:       msg.SenderID,
		SenderName:     senderName,
		Content:        content,
		WecomMsgID:     wecomMsgID,
	}
	inserted, saveErr := h.msgStore.Create(ctx, userMsg)
	if saveErr != nil {
		slog.Error("[msg] save image observation failed", "error", saveErr, "wecom_msg_id", msg.MsgID)
		return content
	}
	if inserted {
		slog.Info("[msg] image observation saved",
			"conv_id", convID,
			"sender_name", senderName,
			"image_name", imageName,
			"image_source", imageSource,
		)
		h.queueImageFollowupIfAgentRunning(dialogKey, robotID, groupID, convID, msg.SenderID, senderName)
	}
	return content
}

func (h *Handler) attachQuotedImageObservation(ctx context.Context, groupID, convID string, msg model.GroupMessage, senderName string) string {
	quoteAppInfo := strings.TrimSpace(msg.QuoteAppInfo)
	if quoteAppInfo == "" {
		return ""
	}

	content := h.findImageObservationByAppInfo(ctx, quoteAppInfo)
	if content == "" {
		slog.Info("[msg] quoted image observation not found",
			"wecom_msg_id", msg.MsgID,
			"quote_app_info", truncate(quoteAppInfo, 80))
		return ""
	}

	content = strings.TrimSpace(content)
	if !strings.Contains(content, "引用关系:") {
		content += "\n引用关系: 当前问题引用了这张图片"
	}
	wecomMsgID := strings.TrimSpace(msg.MsgID)
	if wecomMsgID == "" {
		wecomMsgID = uuid.NewString()
	}
	userMsg := &model.Message{
		ID:             uuid.NewString(),
		ConversationID: convID,
		Role:           "user",
		SenderID:       msg.SenderID,
		SenderName:     senderName,
		Content:        content,
		WecomMsgID:     wecomMsgID + ":quoted_image_observation",
	}
	inserted, err := h.msgStore.Create(ctx, userMsg)
	if err != nil {
		slog.Error("[msg] save quoted image observation failed", "error", err, "wecom_msg_id", msg.MsgID)
		return content
	}
	if inserted {
		slog.Info("[msg] quoted image observation attached",
			"conv_id", convID,
			"sender_name", senderName,
			"quote_app_info", truncate(quoteAppInfo, 80))
	}
	return content
}

func (h *Handler) findQuotedUserMessage(ctx context.Context, quoteAppInfo string) *model.Message {
	quoteAppInfo = strings.TrimSpace(quoteAppInfo)
	if h.msgStore == nil || quoteAppInfo == "" {
		return nil
	}
	msg, err := h.msgStore.GetByWecomMsgID(ctx, appInfoMessageWecomID(quoteAppInfo))
	if err != nil || msg == nil {
		return nil
	}
	if msg.Role != "user" {
		return nil
	}
	if strings.TrimSpace(msg.SenderID) == "" || strings.TrimSpace(msg.Content) == "" {
		return nil
	}
	if isImageObservationContent(msg.Content) {
		return nil
	}
	return msg
}

func (h *Handler) findImageObservationByAppInfo(ctx context.Context, appInfo string) string {
	if h.msgStore == nil {
		return ""
	}
	msg, err := h.msgStore.GetByWecomMsgID(ctx, appInfoObservationWecomID(appInfo))
	if err != nil || msg == nil {
		return ""
	}
	if isImageObservationContent(msg.Content) {
		return msg.Content
	}
	return ""
}

func appInfoObservationWecomID(appInfo string) string {
	return "appinfo:" + strings.TrimSpace(appInfo) + ":image_observation"
}

func appInfoMessageWecomID(appInfo string) string {
	return "appinfo:" + strings.TrimSpace(appInfo) + ":message"
}

func messageWecomID(msg model.GroupMessage) string {
	if appInfo := strings.TrimSpace(msg.AppInfo); appInfo != "" {
		return appInfoMessageWecomID(appInfo)
	}
	return strings.TrimSpace(msg.MsgID)
}

func shouldUseQuotedQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	compact := strings.Join(strings.Fields(text), "")
	if compact == "" {
		return true
	}
	if isShortReferenceToQuote(compact) {
		return true
	}
	return !isQuestionLike(compact)
}

func isShortReferenceToQuote(text string) bool {
	if len([]rune(text)) > 18 {
		return false
	}
	return strings.Contains(text, "这") ||
		strings.Contains(text, "那") ||
		strings.Contains(text, "上面") ||
		strings.Contains(text, "上条") ||
		strings.Contains(text, "引用")
}

func isImageObservationContent(content string) bool {
	return strings.Contains(content, "[图片观察]")
}

func (h *Handler) beginImageObservation(dialogKey string) *imageObservationState {
	state := &imageObservationState{
		createdAt: time.Now(),
		done:      make(chan struct{}),
	}
	h.imageObserving.Store(dialogKey, state)
	return state
}

func (s *imageObservationState) finish() {
	close(s.done)
}

func (h *Handler) cleanupImageObservationLater(dialogKey string, state *imageObservationState) {
	time.AfterFunc(adjacentImageWindow, func() {
		if current, ok := h.imageObserving.Load(dialogKey); ok && current == state {
			h.imageObserving.Delete(dialogKey)
		}
	})
}

func (h *Handler) waitForAdjacentImageObservation(ctx context.Context, dialogKey string, coalesceWindow time.Duration) {
	if h.waitForImageObservation(ctx, dialogKey) {
		return
	}

	if coalesceWindow <= 0 {
		coalesceWindow = imageCoalesceWindow
	}
	timer := time.NewTimer(coalesceWindow)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	h.waitForImageObservation(ctx, dialogKey)
}

func imageCoalesceDelay(query string) time.Duration {
	if looksLikeImageQuestion(query) {
		return visualQuestionImageCoalesceWindow
	}
	return imageCoalesceWindow
}

func looksLikeImageQuestion(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	lower := strings.ToLower(query)
	return strings.Contains(query, "图片") ||
		strings.Contains(query, "截图") ||
		strings.Contains(query, "图里") ||
		strings.Contains(query, "图上") ||
		strings.Contains(query, "图中") ||
		strings.Contains(query, "这张图") ||
		strings.Contains(query, "这个图") ||
		strings.Contains(query, "照片") ||
		strings.Contains(lower, "screenshot")
}

func (h *Handler) waitForImageObservation(ctx context.Context, dialogKey string) bool {
	value, ok := h.imageObserving.Load(dialogKey)
	if !ok {
		return false
	}
	state, ok := value.(*imageObservationState)
	if !ok || time.Since(state.createdAt) > adjacentImageWindow {
		return false
	}

	select {
	case <-state.done:
		return true
	default:
	}

	slog.Info("[msg] waiting for adjacent image observation", "dialog_key", dialogKey)
	timer := time.NewTimer(imageObservationWaitTimeout)
	defer timer.Stop()
	select {
	case <-state.done:
		return true
	case <-ctx.Done():
		return true
	case <-timer.C:
		slog.Warn("[msg] adjacent image observation timeout", "dialog_key", dialogKey)
		return true
	}
}

func (h *Handler) queueImageFollowupIfAgentRunning(dialogKey, robotID, groupID, convID, senderID, senderName string) {
	if _, ok := h.agentRunning.Load(dialogKey); !ok {
		return
	}
	h.pendingQuery.Store(dialogKey, &pendingAgentReq{
		query:           "请结合我刚发送的图片重新判断客户问题，并基于知识库回答。",
		triggerID:       senderID,
		triggerName:     senderName,
		ownerID:         senderID,
		ownerName:       senderName,
		notifyMemberIDs: buildNotifyMemberIDs(senderID, senderID),
		convID:          convID,
		robotID:         robotID,
		groupID:         groupID,
		dialogKey:       dialogKey,
	})
	slog.Info("[msg] image observation queued as pending query",
		"group_id", groupID,
		"sender_id", senderID,
		"conv_id", convID)
}

func rawTextContent(msg model.GroupMessage) string {
	if msg.MsgContent.Text == nil {
		return ""
	}
	return msg.MsgContent.Text.Content
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
	history, err := h.msgStore.ListRecentConversationByGroupAndSender(ctx, groupID, ownerID, 50)
	if err != nil {
		return ""
	}
	return lastUnansweredQuestionFromHistory(history, time.Now().Add(-window))
}

func lastUnansweredQuestionFromHistory(history []model.Message, cutoff time.Time) string {
	hasAssistantAfter := false
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == "assistant" {
			hasAssistantAfter = true
			continue
		}
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if msg.CreatedAt.Before(cutoff) {
			continue
		}
		if hasAssistantAfter {
			continue
		}
		if isImageObservationContent(content) {
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
	candidateSenders := make(map[string]struct{})

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
		candidateSenders[senderID] = struct{}{}
	}

	latestQuestionBySender := make(map[string]string)
	for senderID := range candidateSenders {
		if question := h.findLastOwnerQuestion(ctx, groupID, senderID, window); question != "" {
			latestQuestionBySender[senderID] = question
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
