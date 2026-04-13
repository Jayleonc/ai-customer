package model

import "encoding/json"

// ---- 企微回调事件结构体 ----

// CallbackPayload 外部平台推送的加密回调
type CallbackPayload struct {
	AppKey          string `json:"app_key"`
	Nonce           string `json:"nonce"`
	Timestamp       string `json:"timestamp"`
	Signature       string `json:"signature"`
	EncodingContent string `json:"encoding_content"`
}

// ---- 事件类型常量 (以 source-doc/002-事件码.md 为准) ----
const (
	// 机器人
	EventLoginSuccess          = "login.success"
	EventLogout                = "logout"
	EventRobotInitialized      = "robot.initialized.success"

	// 群生命周期
	EventCreateContactGroup    = "create.contact.group"
	EventDismissGroup          = "dismiss.group"
	EventGroupUpdateName       = "group.update.name"
	EventGroupOwnerChange      = "group.owner.change"
	EventRobotJoinGroup        = "robot.join.group"
	EventWatchGroup            = "watch.group"
	EventQuitGroup             = "quit.group"

	// 群成员
	EventMemberJoinGroup       = "member.join.group"
	EventMemberQuitGroup       = "member.quit.group"   // 废弃，但仍可能收到
	EventRemoveGroupMember     = "remove.group.member"

	// 消息
	EventReceiveGroupMsg       = "receive.group.msg"
	EventReceiveContactMsg     = "receive.contact.msg"
	EventSendGroupMsg          = "send.group.msg"

	// 主动拉取回调
	EventGetGroup           = "get.group"             // GetRemoteGroup 异步结果
	EventGetGroupMemberList = "get.group.member.list" // GetGroupMemberList 异步结果
)

// ---- 基础事件结构 ----

// BaseEvent 所有回调事件的公共字段
type BaseEvent struct {
	EventType string          `json:"event_type"`
	RobotID   string          `json:"robot_id"`
	SerialNo  string          `json:"serial_no"`
	UniqSN    string          `json:"uniq_sn"`
	ErrCode   int             `json:"err_code"`
	ErrMsg    string          `json:"err_msg"`
	Hint      string          `json:"hint"`
	RawData   json.RawMessage `json:"data"`
}

// ---- 机器人登录成功 (login.success) ----

type LoginSuccessEvent struct {
	BaseEvent
	Data LoginSuccessData `json:"-"`
}

type LoginSuccessData struct {
	Name        string `json:"name"`
	Avatar      string `json:"avatar"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	Gender      int    `json:"gender"`
	CorpID      string `json:"str_corp_id"`
	CorpName    string `json:"corp_name"`
	LoginStatus int    `json:"login_status"`
	AccountID   string `json:"account_id"`
}

func (e *LoginSuccessEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 机器人登出 (logout) ----

type LogoutEvent struct {
	BaseEvent
	Data LogoutData `json:"-"`
}

type LogoutData struct {
	Name       string `json:"name"`
	Type       int    `json:"type"`
	DetailInfo string `json:"detail_info"`
}

func (e *LogoutEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 机器人初始化成功 (robot.initialized.success) ----

type RobotInitializedEvent struct{ BaseEvent }

// ---- 机器人进群 (robot.join.group) ----

type RobotJoinGroupEvent struct {
	BaseEvent
	Data RobotJoinGroupData `json:"-"`
}

type RobotJoinGroupData struct {
	GroupID string `json:"group_id"`
}

func (e *RobotJoinGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 关注群 (watch.group) ----

type WatchGroupEvent struct {
	BaseEvent
	Data WatchGroupData `json:"-"`
}

type WatchGroupData struct {
	GroupID string `json:"group_id"`
}

func (e *WatchGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 机器人退群 (quit.group) ----

type QuitGroupEvent struct {
	BaseEvent
	Data QuitGroupData `json:"-"`
}

type QuitGroupData struct {
	GroupID string `json:"group_id"`
}

func (e *QuitGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 创建外部群 (create.contact.group) ----

type CreateContactGroupEvent struct {
	BaseEvent
	Data CreateContactGroupData `json:"-"`
}

type CreateContactGroupData struct {
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name"`
	IsWatch   bool   `json:"is_watch"`
}

func (e *CreateContactGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 解散群 (dismiss.group) ----

type DismissGroupEvent struct {
	BaseEvent
	Data DismissGroupData `json:"-"`
}

type DismissGroupData struct {
	GroupID string `json:"group_id"`
}

func (e *DismissGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 群名变更 (group.update.name) ----

type GroupUpdateNameEvent struct {
	BaseEvent
	Data GroupUpdateNameData `json:"-"`
}

type GroupUpdateNameData struct {
	GroupID string `json:"group_id"`
	Name    string `json:"name"`
}

func (e *GroupUpdateNameEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 群主变更 (group.owner.change) ----

type GroupOwnerChangeEvent struct {
	BaseEvent
	Data GroupOwnerChangeData `json:"-"`
}

type GroupOwnerChangeData struct {
	GroupID string `json:"group_id"`
	OwnerID string `json:"owner_id"`
}

func (e *GroupOwnerChangeEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 成员入群 (member.join.group) ----

type MemberJoinGroupEvent struct {
	BaseEvent
	Data MemberJoinGroupData `json:"-"`
}

type MemberJoinGroupData struct {
	GroupID    string         `json:"group_id"`
	MemberList []JoinedMember `json:"member_list"`
}

type JoinedMember struct {
	MemberID  string `json:"member_id"`
	Name      string `json:"name"`
	NickName  string `json:"nick_name"`
	UserType  int    `json:"user_type"`  // 1=员工, 2=微信用户, 3=企微外部联系人
	AdminType int    `json:"admin_type"` // 1=群管理员, 2=群主
	JoinTime  int64  `json:"join_time"`
	InviterID string `json:"inviter_id"`
	Avatar    string `json:"avatar"`
	Gender    int    `json:"gender"`
	CorpName  string `json:"corp_name"`
	JoinScene int    `json:"join_scene"` // 1=直接入群, 2=邀请链接, 3=扫码
}

func (e *MemberJoinGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 成员退群/移除 (member.quit.group / remove.group.member) ----

type MemberLeaveGroupEvent struct {
	BaseEvent
	Data MemberLeaveGroupData `json:"-"`
}

type MemberLeaveGroupData struct {
	GroupID      string   `json:"group_id"`
	MemberIDList []string `json:"member_id_list"`
}

func (e *MemberLeaveGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- 群消息事件 (receive.group.msg) ----

type ReceiveGroupMsgEvent struct {
	BaseEvent
	Data ReceiveGroupMsgData `json:"-"`
}

func (e *ReceiveGroupMsgEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

type ReceiveGroupMsgData struct {
	ReceiveTime int64        `json:"receive_time"`
	MsgSource   int          `json:"msg_source"` // 1=外部, 2=客服工作台
	LoginID     string       `json:"login_id"`
	Msg         GroupMessage `json:"msg"`
}

type GroupMessage struct {
	SenderID   string     `json:"sender_id"`
	SenderType int        `json:"sender_type"` // 1=企微联系人, 2=微信好友, 3=内部成员
	ReceiverID string     `json:"receiver_id"` // 群 ID
	MsgID      string     `json:"msg_id"`
	MsgType    int        `json:"msg_type"`
	AppInfo    string     `json:"app_info"`
	MsgContent MsgContent `json:"msg_content"`
	IsAtAll    bool       `json:"is_at_all"`
	AtList     []AtMember `json:"at_list"`
	AtLocation int        `json:"at_location"` // 0=开头, 1=结尾
}

type AtMember struct {
	Nickname string `json:"nickname"`
	MemberID string `json:"member_id"`
}

type MsgContent struct {
	Text  *TextContent  `json:"text,omitempty"`
	Image *MediaContent `json:"image,omitempty"`
	Video *MediaContent `json:"video,omitempty"`
	File  *MediaContent `json:"file,omitempty"`
}

type TextContent struct {
	Content string `json:"content"`
}

type MediaContent struct {
	URL  string `json:"url,omitempty"`
	Name string `json:"name,omitempty"`
}

// ---- 发送群消息 ----

type SendGroupMsgReq struct {
	RobotID string           `json:"robot_id"`
	UniqSN  string           `json:"uniq_sn,omitempty"`
	Msg     OutboundGroupMsg `json:"msg"`
}

type OutboundGroupMsg struct {
	SenderID   string     `json:"sender_id"`
	ReceiverID string     `json:"receiver_id"` // 群 ID
	MsgType    int        `json:"msg_type"`
	MsgContent MsgContent `json:"msg_content"`
	AtList     []AtMember `json:"at_list,omitempty"`
	AtLocation int        `json:"at_location,omitempty"`
}

// ---- 发送群消息异步结果 (send.group.msg) ----

type SendGroupMsgAsyncEvent struct {
	BaseEvent
}

// ---- 私聊消息事件 (receive.contact.msg) ----

type ReceiveContactMsgEvent struct {
	BaseEvent
}

// ---- GetRemoteGroup 异步结果 (get.group) ----

type GetGroupEvent struct {
	BaseEvent
	Data GetGroupData `json:"-"`
}

type GetGroupData struct {
	RobotID string    `json:"robot_id"`
	Group   GroupInfo `json:"group"`
}

type GroupInfo struct {
	GroupID   string `json:"group_id"`
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id"`
	OwnerName string `json:"owner_name"`
}

func (e *GetGroupEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}

// ---- GetGroupMemberList 异步结果 (get.group.member.list) ----

type GetGroupMemberListEvent struct {
	BaseEvent
	Data GetGroupMemberListData `json:"-"`
}

type GetGroupMemberListData struct {
	GroupID    string               `json:"group_id"`
	MemberList []RemoteGroupMember  `json:"member_list"`
	HasMore    bool                 `json:"has_more"`
}

type RemoteGroupMember struct {
	MemberID  string `json:"member_id"`
	Name      string `json:"name"`
	NickName  string `json:"nick_name"`
	UserType  int    `json:"user_type"`  // 1=员工, 2=微信用户, 3=企微外部联系人
	AdminType int    `json:"admin_type"` // 1=群管理员, 2=群主
	JoinTime  int64  `json:"join_time"`
	CorpName  string `json:"corp_name"`
}

func (e *GetGroupMemberListEvent) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.BaseEvent); err != nil {
		return err
	}
	return json.Unmarshal(e.RawData, &e.Data)
}
