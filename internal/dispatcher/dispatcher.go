package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"git.pinquest.cn/ai-customer/internal/model"
	"git.pinquest.cn/ai-customer/internal/store"
	"git.pinquest.cn/ai-customer/internal/wecom"
	"github.com/google/uuid"
)

// MessageHandler 处理群消息的接口
type MessageHandler interface {
	HandleGroupMessage(ctx context.Context, evt *model.ReceiveGroupMsgEvent, raw []byte)
}

// Dispatcher 根据 event_type 分发事件到对应处理器
type Dispatcher struct {
	msgHandler       MessageHandler
	robotStore       *store.RobotStore
	groupStore       *store.GroupStore
	groupMemberStore *store.GroupMemberStore
	wecom            *wecom.Client
}

func NewDispatcher(
	mh MessageHandler,
	rs *store.RobotStore,
	gs *store.GroupStore,
	gms *store.GroupMemberStore,
	wc *wecom.Client,
) *Dispatcher {
	return &Dispatcher{
		msgHandler:       mh,
		robotStore:       rs,
		groupStore:       gs,
		groupMemberStore: gms,
		wecom:            wc,
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, eventType string, raw []byte) error {
	switch eventType {

	// ============ 机器人 ============

	case model.EventLoginSuccess:
		return d.handleLoginSuccess(ctx, raw)

	case model.EventLogout:
		return d.handleLogout(ctx, raw)

	case model.EventRobotInitialized:
		return d.handleRobotInitialized(ctx, raw)

	// ============ 群生命周期 ============

	case model.EventCreateContactGroup:
		return d.handleCreateContactGroup(ctx, raw)

	case model.EventDismissGroup:
		return d.handleDismissGroup(ctx, raw)

	case model.EventGroupUpdateName:
		return d.handleGroupUpdateName(ctx, raw)

	case model.EventGroupOwnerChange:
		return d.handleGroupOwnerChange(ctx, raw)

	case model.EventRobotJoinGroup:
		return d.handleRobotJoinGroup(ctx, raw)

	case model.EventWatchGroup:
		return d.handleWatchGroup(ctx, raw)

	case model.EventQuitGroup:
		return d.handleQuitGroup(ctx, raw)

	// ============ 群成员 ============

	case model.EventMemberJoinGroup:
		return d.handleMemberJoinGroup(ctx, raw)

	case model.EventMemberQuitGroup, model.EventRemoveGroupMember:
		return d.handleMemberLeaveGroup(ctx, raw)

	// ============ 消息 ============

	case model.EventReceiveGroupMsg:
		var evt model.ReceiveGroupMsgEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			return fmt.Errorf("unmarshal receive.group.msg: %w", err)
		}
		// 异步处理，不阻塞回调响应
		go d.msgHandler.HandleGroupMessage(context.Background(), &evt, raw)
		return nil

	case model.EventReceiveContactMsg:
		return d.handleReceiveContactMsg(ctx, raw)

	case model.EventSendGroupMsg:
		var evt model.SendGroupMsgAsyncEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			return fmt.Errorf("unmarshal send.group.msg: %w", err)
		}
		if evt.ErrCode != 0 {
			slog.Warn("send group msg failed", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg, "uniq_sn", evt.UniqSN)
		} else {
			slog.Info("send group msg succeeded", "uniq_sn", evt.UniqSN)
		}
		return nil

	// ============ 主动拉取回调 ============

	case model.EventGetGroup:
		return d.handleGetGroup(ctx, raw)

	case model.EventGetGroupMemberList:
		return d.handleGetGroupMemberList(ctx, raw)

	default:
		slog.Debug("unhandled event", "event_type", eventType)
		return nil
	}
}

// ---- 机器人事件 ----

func (d *Dispatcher) handleLoginSuccess(ctx context.Context, raw []byte) error {
	var evt model.LoginSuccessEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal login.success: %w", err)
	}
	slog.Info("robot login success",
		"robot_id", evt.RobotID,
		"name", evt.Data.Name,
		"corp_name", evt.Data.CorpName,
	)
	return d.robotStore.UpsertOnLogin(ctx, &evt.Data, evt.RobotID)
}

func (d *Dispatcher) handleLogout(ctx context.Context, raw []byte) error {
	var evt model.LogoutEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal logout: %w", err)
	}
	slog.Info("robot logout",
		"robot_id", evt.RobotID,
		"type", evt.Data.Type,
		"detail", evt.Data.DetailInfo,
	)
	return d.robotStore.UpdateStatus(ctx, evt.RobotID, 2) // 2=offline
}

func (d *Dispatcher) handleRobotInitialized(_ context.Context, raw []byte) error {
	var evt model.RobotInitializedEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal robot.initialized.success: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("robot initialized with error",
			"robot_id", evt.RobotID,
			"err_code", evt.ErrCode,
			"err_msg", evt.ErrMsg,
		)
		return nil
	}
	slog.Info("robot initialized", "robot_id", evt.RobotID)
	return nil
}

// ---- 群生命周期事件 ----

func (d *Dispatcher) handleCreateContactGroup(ctx context.Context, raw []byte) error {
	var evt model.CreateContactGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal create.contact.group: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("create group callback error", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg)
		return nil
	}

	slog.Info("group created",
		"group_id", evt.Data.GroupID,
		"group_name", evt.Data.GroupName,
		"robot_id", evt.RobotID,
	)

	// 记录群
	if err := d.groupStore.UpsertFromCallback(ctx, evt.Data.GroupID, evt.Data.GroupName, evt.RobotID, evt.RobotID); err != nil {
		return fmt.Errorf("upsert group: %w", err)
	}

	// 机器人作为群主加入成员表
	if err := d.groupMemberStore.Upsert(ctx, &model.GroupMember{
		GroupID:  evt.Data.GroupID,
		MemberID: evt.RobotID,
		Role:     2, // 群主
		JoinTime: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("upsert robot as member: %w", err)
	}

	return d.robotStore.IncrGroupCount(ctx, evt.RobotID, 1)
}

func (d *Dispatcher) handleDismissGroup(ctx context.Context, raw []byte) error {
	var evt model.DismissGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal dismiss.group: %w", err)
	}
	if evt.ErrCode != 0 {
		return nil
	}

	slog.Info("group dismissed", "group_id", evt.Data.GroupID, "robot_id", evt.RobotID)

	if err := d.groupStore.UpdateStatus(ctx, evt.Data.GroupID, 2); err != nil {
		return fmt.Errorf("update group status: %w", err)
	}
	if err := d.groupMemberStore.ClearByGroup(ctx, evt.Data.GroupID); err != nil {
		return fmt.Errorf("clear group members: %w", err)
	}
	return d.robotStore.IncrGroupCount(ctx, evt.RobotID, -1)
}

func (d *Dispatcher) handleGroupUpdateName(ctx context.Context, raw []byte) error {
	var evt model.GroupUpdateNameEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal group.update.name: %w", err)
	}

	slog.Info("group name updated", "group_id", evt.Data.GroupID, "new_name", evt.Data.Name)
	return d.groupStore.UpdateName(ctx, evt.Data.GroupID, evt.Data.Name)
}

func (d *Dispatcher) handleGroupOwnerChange(ctx context.Context, raw []byte) error {
	var evt model.GroupOwnerChangeEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal group.owner.change: %w", err)
	}

	slog.Info("group owner changed", "group_id", evt.Data.GroupID, "new_owner", evt.Data.OwnerID)
	return d.groupStore.UpdateOwner(ctx, evt.Data.GroupID, evt.Data.OwnerID)
}

func (d *Dispatcher) handleRobotJoinGroup(ctx context.Context, raw []byte) error {
	var evt model.RobotJoinGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal robot.join.group: %w", err)
	}

	slog.Info("robot joined group", "robot_id", evt.RobotID, "group_id", evt.Data.GroupID)

	// 先兜底创建 robot，避免尚未收到 login.success 时 group_count 更新丢失
	if err := d.robotStore.EnsureExists(ctx, evt.RobotID); err != nil {
		return fmt.Errorf("ensure robot exists: %w", err)
	}

	// 记录群（如果不存在则创建基础记录）
	if err := d.groupStore.UpsertFromCallback(ctx, evt.Data.GroupID, "", evt.RobotID, ""); err != nil {
		return fmt.Errorf("upsert group: %w", err)
	}

	// 机器人作为成员加入
	if err := d.groupMemberStore.Upsert(ctx, &model.GroupMember{
		GroupID:  evt.Data.GroupID,
		MemberID: evt.RobotID,
		Role:     3, // 普通成员（被邀请进群通常不是群主）
		JoinTime: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("upsert robot member: %w", err)
	}

	// 异步拉取群信息和成员列表以自动填充 group_name / owner_id / customer_name
	go d.fetchGroupInfo(evt.RobotID, evt.Data.GroupID)

	return d.robotStore.IncrGroupCount(ctx, evt.RobotID, 1)
}

func (d *Dispatcher) handleWatchGroup(ctx context.Context, raw []byte) error {
	var evt model.WatchGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal watch.group: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("watch group callback error", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg, "group_id", evt.Data.GroupID)
		return nil
	}

	slog.Info("watch group", "robot_id", evt.RobotID, "group_id", evt.Data.GroupID)

	if err := d.robotStore.EnsureExists(ctx, evt.RobotID); err != nil {
		return fmt.Errorf("ensure robot exists: %w", err)
	}
	if err := d.groupStore.UpsertFromCallback(ctx, evt.Data.GroupID, "", evt.RobotID, ""); err != nil {
		return fmt.Errorf("upsert watched group: %w", err)
	}
	if err := d.groupMemberStore.Upsert(ctx, &model.GroupMember{
		GroupID:  evt.Data.GroupID,
		MemberID: evt.RobotID,
		Role:     3,
		JoinTime: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("upsert watched group robot member: %w", err)
	}

	// 异步拉取群信息和成员列表
	go d.fetchGroupInfo(evt.RobotID, evt.Data.GroupID)

	return d.robotStore.IncrGroupCount(ctx, evt.RobotID, 1)
}

func (d *Dispatcher) handleQuitGroup(ctx context.Context, raw []byte) error {
	var evt model.QuitGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal quit.group: %w", err)
	}

	slog.Info("robot quit group", "robot_id", evt.RobotID, "group_id", evt.Data.GroupID)

	if err := d.groupMemberStore.Remove(ctx, evt.Data.GroupID, []string{evt.RobotID}); err != nil {
		return fmt.Errorf("remove robot member: %w", err)
	}
	if err := d.groupStore.RemoveRobotID(ctx, evt.Data.GroupID, evt.RobotID); err != nil {
		slog.Warn("remove robot from group robot_ids failed", "robot_id", evt.RobotID, "error", err)
	}
	return d.robotStore.IncrGroupCount(ctx, evt.RobotID, -1)
}

// ---- 群成员事件 ----

func (d *Dispatcher) handleMemberJoinGroup(ctx context.Context, raw []byte) error {
	var evt model.MemberJoinGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal member.join.group: %w", err)
	}
	if evt.ErrCode != 0 {
		return nil
	}

	slog.Info("members joined group",
		"group_id", evt.Data.GroupID,
		"count", len(evt.Data.MemberList),
	)

	for _, m := range evt.Data.MemberList {
		role := 3 // 普通成员
		if m.AdminType == 2 {
			role = 2 // 群主
		}
		nickname := m.NickName
		if nickname == "" {
			nickname = m.Name
		}
		if err := d.groupMemberStore.Upsert(ctx, &model.GroupMember{
			GroupID:         evt.Data.GroupID,
			MemberID:        m.MemberID,
			Nickname:        nickname,
			Role:            role,
			JoinTime:        m.JoinTime,
			InvitorMemberID: m.InviterID,
		}); err != nil {
			slog.Error("upsert group member failed", "member_id", m.MemberID, "error", err)
		}

		// 检查新成员是否是已注册的机器人（user_type=1 表示员工，机器人也归类为员工）
		// 场景：群主机器人 A 拉了另一个机器人 B 进群，A 只会收到 member.join.group，
		//       而 robot.join.group 只推送给 B 自己。因此需要在此处补充维护 B 的群关联数据。
		if m.UserType == 1 {
			robot, err := d.robotStore.FindByRobotOrMemberID(ctx, m.MemberID)
			if err != nil || robot == nil {
				continue // 不是机器人，跳过
			}

			// 幂等追加到 robot_ids，只在首次添加时 +1 group_count，避免和 robot.join.group 重复计数
			added, err := d.groupStore.AddRobotIfAbsent(ctx, evt.Data.GroupID, robot.RobotID)
			if err != nil {
				slog.Error("add robot to group robot_ids failed",
					"group_id", evt.Data.GroupID, "robot_id", robot.RobotID, "error", err)
				continue
			}
			if added {
				if err := d.robotStore.IncrGroupCount(ctx, robot.RobotID, 1); err != nil {
					slog.Error("incr robot group count failed", "robot_id", robot.RobotID, "error", err)
				}
				slog.Info("detected robot in member.join.group, synced to group",
					"group_id", evt.Data.GroupID,
					"member_id", m.MemberID,
					"robot_id", robot.RobotID,
				)
			}
		}
	}
	return nil
}

func (d *Dispatcher) handleMemberLeaveGroup(ctx context.Context, raw []byte) error {
	var evt model.MemberLeaveGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal member leave: %w", err)
	}
	if evt.ErrCode != 0 {
		return nil
	}

	slog.Info("members left group",
		"group_id", evt.Data.GroupID,
		"member_ids", evt.Data.MemberIDList,
	)

	// 检查机器人是否在被移除列表中
	for _, mid := range evt.Data.MemberIDList {
		if mid == evt.RobotID {
			if err := d.robotStore.IncrGroupCount(ctx, evt.RobotID, -1); err != nil {
				slog.Error("decr robot group count failed", "error", err)
			}
			break
		}
	}

	return d.groupMemberStore.Remove(ctx, evt.Data.GroupID, evt.Data.MemberIDList)
}

func (d *Dispatcher) handleReceiveContactMsg(ctx context.Context, raw []byte) error {
	var evt model.ReceiveContactMsgEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal receive.contact.msg: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("receive contact msg callback error", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg, "robot_id", evt.RobotID)
		return nil
	}

	if err := d.robotStore.EnsureExists(ctx, evt.RobotID); err != nil {
		return fmt.Errorf("ensure robot exists: %w", err)
	}
	return nil
}

// ---- 主动拉取回调处理 ----

// fetchGroupInfo 在后台触发 GetRemoteGroup 和 GetGroupMemberList 以自动填充群信息
func (d *Dispatcher) fetchGroupInfo(robotID, groupID string) {
	ctx := context.Background()
	sn := uuid.NewString()

	if err := d.wecom.GetRemoteGroup(ctx, robotID, groupID, sn); err != nil {
		slog.Warn("[dispatcher] GetRemoteGroup failed", "group_id", groupID, "error", err)
	}
	if err := d.wecom.GetGroupMemberList(ctx, robotID, groupID, uuid.NewString()); err != nil {
		slog.Warn("[dispatcher] GetGroupMemberList failed", "group_id", groupID, "error", err)
	}
}

// handleGetGroup 处理 GetRemoteGroup 的异步回调，更新群名和群主（群主是内部客服）
func (d *Dispatcher) handleGetGroup(ctx context.Context, raw []byte) error {
	var evt model.GetGroupEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal get.group: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("[dispatcher] get.group error", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg)
		return nil
	}

	g := evt.Data.Group
	slog.Info("[dispatcher] get.group", "group_id", g.GroupID, "name", g.Name, "owner_id", g.OwnerID)
	return d.groupStore.UpdateGroupInfo(ctx, g.GroupID, g.Name, g.OwnerID)
}

// handleGetGroupMemberList 处理 GetGroupMemberList 的异步回调，批量 upsert 成员昵称
// customer_id / customer_name 是运营手动配置的业务字段（客户公司），不在此处自动推断
func (d *Dispatcher) handleGetGroupMemberList(ctx context.Context, raw []byte) error {
	var evt model.GetGroupMemberListEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return fmt.Errorf("unmarshal get.group.member.list: %w", err)
	}
	if evt.ErrCode != 0 {
		slog.Warn("[dispatcher] get.group.member.list error", "err_code", evt.ErrCode, "err_msg", evt.ErrMsg)
		return nil
	}

	slog.Info("[dispatcher] syncing group members", "group_id", evt.Data.GroupID, "count", len(evt.Data.MemberList))
	for _, m := range evt.Data.MemberList {
		role := 3 // 普通成员
		if m.AdminType == 2 {
			role = 2 // 群主
		}
		// NickName 优先，外部微信用户可能只有 Name
		nickname := m.NickName
		if nickname == "" {
			nickname = m.Name
		}
		if err := d.groupMemberStore.Upsert(ctx, &model.GroupMember{
			GroupID:  evt.Data.GroupID,
			MemberID: m.MemberID,
			Nickname: nickname,
			Role:     role,
			JoinTime: m.JoinTime,
		}); err != nil {
			slog.Warn("[dispatcher] upsert member failed", "member_id", m.MemberID, "error", err)
		}
	}
	return nil
}
