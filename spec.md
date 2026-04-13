# ai-customer Spec: 企微群 AI 智能客服服务

> 版本: v0.1 | 日期: 2026-03-26

> 2026-04-14 同步说明：
> 本文档包含一部分早期方案描述。
> 当前代码实现已经接入 `turnmesh`：
> - 多轮主链路走 `turnmesh.New(...).RunTurn(...)`
> - query rewrite 走 `turnmesh.RunOneShot(...)`
> 因此阅读本文时，应把“业务壳在 ai-customer，runtime 在 turnmesh”作为当前事实。

## 1. 项目定位

ai-customer 是一个**独立的企微群 AI 客服服务**。它接收企微群聊回调消息，经过触发规则过滤后，调用 knowledge-hub (kh) 的知识检索 API 获取答案，再通过企微 API 回复到群内。

ai-customer 不是 kh 的附庸。kh 对 ai-customer 而言是"知识数据源"，地位等同于数据库或缓存——是依赖，不是主人。ai-customer 拥有自己的业务 Agent 壳、会话管理、prompt 策略和业务逻辑；底层 runtime 由 `turnmesh` 提供。

```
┌──────────────────────────────────────────────────────┐
│                   ai-customer (本服务)                 │
│                                                      │
│  [企微回调网关]                                        │
│       ↓                                              │
│  [消息过滤器] ── @机器人? 文本消息? ──→ 丢弃             │
│       ↓ (命中)                                        │
│  [群上下文加载] ── 群标签、客户特性、成员身份               │
│       ↓                                              │
│  [Agent 引擎]                                         │
│    ├── tool: search_knowledge   (HTTP → kh)          │
│    ├── tool: read_document      (HTTP → kh)          │
│    ├── tool: check_feature_tag  (本地数据库)            │
│    └── tool: ask_clarification  (群聊定制)             │
│       ↓                                              │
│  [会话管理器] ── 群聊模型 (group_id 维度)               │
│       ↓                                              │
│  [消息发送器] ── 企微 SendGroupMsg API                  │
└───────────────┬──────────────────────────────────────┘
                │ HTTP (Knowledge API, API Key 认证)
┌───────────────▼──────────────────────────────────────┐
│              knowledge-hub (知识平台)                   │
│                                                      │
│  POST /api/rag/retrieve     ← 语义检索                 │
│  POST /api/document/detail  ← 文档精读                 │
│  POST /api/document/search  ← 全文关键词搜索            │
│                                                      │
│  运营在此上传/管理知识文档，ai-customer 只读消费           │
└──────────────────────────────────────────────────────┘
```

---

## 2. 核心概念

### 2.1 触发规则

群内消息量大、噪声高。服务**不做全量自动回复**，仅在以下条件满足时触发 Agent：

| 触发方式 | 说明 |
|---------|------|
| **客户 @机器人** | 回调事件 `at_list` 中包含机器人的 `member_id` |
| **内部人员 @机器人 + 引用消息** | 内部客服/技术人员引用客户的问题，@机器人让 AI 代答 |

不触发的情况一律忽略，不产生任何回复。

### 2.2 群组上下文

每个企微群绑定一个客户主体，携带特性标签 (Tags)。

```
enterprise_group (群组表)
├── group_id        (企微群 ID, PK)
├── group_name      (群名称)
├── customer_id     (客户主体 ID, FK)
├── robot_id        (绑定的机器人 ID)
├── dataset_id      (kh 中对应的知识库 ID)
├── feature_tag     (JSON: 开通的特性标签)
└── status          (active / archived)

feature_tags 示例:
{
  "product": "海量",
  "max_groups": 200,
  "features": ["圈量", "自动标签"],
  "tier": "enterprise"
}
```

Agent 在作答前读取该群的 Tags。若客户问了未开通特性的问题，Agent 识别后拒绝或引导，而非盲目检索。

### 2.3 会话模型

不复用 kh 的会话。ai-customer 维护自己的群聊会话，设计上以 **group_id** 为维度：

```
conversation (会话表)
├── id                (UUID, PK)
├── group_id          (企微群 ID)
├── status            (active / closed)
├── created_at
└── last_active_at

message (消息表)
├── id                (UUID, PK)
├── conversation_id   (FK)
├── role              (user / assistant / system)
├── sender_id         (企微 sender_id, 仅 role=user)
├── sender_name       (发送者昵称)
├── content           (消息文本)
├── citations         (JSON: 引用来源)
├── tool_calls        (JSON: Agent 工具调用记录)
├── created_at
└── wecom_msg_id      (企微消息 ID, 用于去重)
```

同一个群的所有 @机器人 的消息共享会话上下文。Agent 能看到群内此前的问答记录，实现连续对话。

### 2.4 Agent 引擎

当前实现不是 langchaingo Agent。

当前边界是：

- `ai-customer` 保留 prompt、preSearch、query rewrite、工具定义和业务 fallback
- `turnmesh` 提供 provider session、one-shot 调用、多轮 turn loop 和 tool dispatch

**工具集：**

| 工具名 | 来源 | 说明 |
|--------|------|------|
| `search_knowledge` | HTTP → kh `/api/rag/retrieve` | 在知识库中语义检索 |
| `read_document` | HTTP → kh `/api/document/detail` | 精读完整文档内容 |
| `check_feature_tag` | 本地 DB | 查询当前群的客户特性标签 |
| `ask_clarification` | 内置 | 当问题模糊时，生成追问让客户补充信息 |

**Prompt 策略（客服场景定制）：**

```
你是企业微信群内的 AI 客服助手。

## 身份
- 你服务的客户是：{customer_name}
- 该客户开通的产品：{product_name}
- 该客户的特性标签：{feature_tags}

## 行为准则
1. 只基于知识库检索结果作答，不得编造。
2. 检索不到时明确回复"暂未找到相关信息，建议联系您的专属客服 {contact_name}"。
3. 当客户提问模糊时，主动追问关键信息（如版本号、操作步骤、报错截图），不要猜测。
4. 客户问了未开通特性的问题时，明确告知该功能未开通，引导联系商务。
5. 回复简洁，避免长篇大论。群聊场景下，3-5 句话为佳。
6. 语气专业友好，不用 emoji，不用"亲"。

## 工具使用策略
- 先检查会话历史是否已有答案，有则直接引用，不重复检索。
- search_knowledge 失败后改写关键词至少重试 1 次。
- 检索到片段但信息不完整时，调用 read_document 精读全文。
- 不确定客户是否开通某功能时，调用 check_feature_tag 确认。
```

---

## 3. 对接 knowledge-hub 的 API 边界

ai-customer 通过 **API Key** 认证调用 kh，API Key 格式为 `sk-<hex>`，具有数据集级别的权限隔离。

### 3.1 语义检索

```
POST {KH_HOST}/api/rag/retrieve
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json

Request:
{
  "query": "如何设置自动标签？",
  "dataset_ids": ["ds_abc123"],       // 可选: 限定检索范围
  "top_k": 10,
  "score_threshold": 0.3
}

Response:
{
  "data": {
    "list": [
      {
        "id": "seg_001",
        "document_id": "doc_abc",
        "document_name": "自动标签操作手册.md",
        "content": "...",
        "snippet": "...高亮摘要...",
        "score": 0.87,
        "header_path": ["功能说明", "自动标签", "设置步骤"],
        "vfs_path": "操作手册/自动标签.md"
      }
    ]
  }
}
```

### 3.2 文档精读

```
POST {KH_HOST}/api/document/detail
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json

Request:
{
  "id": "doc_abc"
}

Response:
{
  "data": {
    "id": "doc_abc",
    "name": "自动标签操作手册.md",
    "content": "完整的文档 Markdown 内容...",
    "content_type": "text/markdown",
    ...
  }
}
```

### 3.3 全文搜索 (可选)

```
POST {KH_HOST}/api/document/search
Authorization: Bearer sk-xxxxxxxx
Content-Type: application/json

Request:
{
  "query": "ERR_TAG_LIMIT_EXCEEDED",
  "dataset_ids": ["ds_abc123"],
  "limit": 5
}
```

---

## 4. 对接企微平台 API

平台 API Host: `s2.xunjinet.com.cn` (以实际为准)

所有 API 使用 POST + HTTPS，Header 携带 `Token: <access_token>`。

### 4.1 回调接收 (Callback Gateway)

外部平台通过 HTTP POST 推送事件到本服务的回调 URL。

**通用回调结构：**
```json
{
  "app_key": "企业 app_key",
  "nonce": "随机数",
  "timestamp": "时间戳",
  "signature": "签名",
  "encoding_content": "AES 加密的事件数据"
}
```

**验签流程：**
1. 将 `app_key`, `token`, `timestamp`, `nonce`, `encoding_content` 按 ASCII 值排序
2. 拼接为字符串，MD5 加密
3. 与 `signature` 比对

**解密流程：**
1. `encoding_content` Base64 解码
2. AES/CBC/PKCS5Padding 解密 (Key = EncodingAESKey, IV = Key 前 16 字节)

**响应：** HTTP 200 表示收到成功。

### 4.2 接收群消息事件

**Event Type:** `receive.group.msg`

```json
{
  "event_type": "receive.group.msg",
  "robot_id": "机器人 ID",
  "data": {
    "receive_time": 1711440000,
    "msg_source": 1,
    "msg": {
      "sender_id": "发送人 ID",
      "sender_type": 1,
      "receiver_id": "群 ID",
      "msg_id": "消息 ID",
      "msg_type": 2,
      "msg_content": {
        "text": { "content": "@AI客服 如何设置自动标签？" }
      },
      "is_at_all": false,
      "at_list": [
        { "nickname": "AI客服", "member_id": "robot_member_id_xxx" }
      ],
      "at_location": 0
    }
  }
}
```

**关键字段：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `msg.at_list` | array | 被 @ 的成员列表，含 `member_id` 和 `nickname` |
| `msg.is_at_all` | bool | 是否 @所有人 |
| `msg.at_location` | int | @ 在文本中的位置 (0=开头, 1=结尾) |
| `msg.sender_type` | int | 1=企微联系人, 2=微信好友, 3=企微内部成员 |
| `msg.msg_type` | int | 2=文本, 3=图片, 13=文件 等 |
| `data.msg_source` | int | 1=外部, 2=客服工作台 |

### 4.3 发送群消息

```
POST {API_HOST}/gateway/jzopen/SendGroupMsg
Token: <access_token>

{
  "robot_id": "robot_xxx",
  "uniq_sn": "unique_tracking_id",
  "msg": {
    "sender_id": "robot_sender_id",
    "receiver_id": "group_id",
    "msg_type": 2,
    "msg_content": {
      "text": { "content": "@张三 关于自动标签的设置..." }
    },
    "at_list": [
      { "nickname": "张三", "member_id": "sender_member_id" }
    ],
    "at_location": 0
  }
}
```

**发送结果异步回调：** `send.group.msg` 事件返回 `err_code` 和 `uniq_sn`。

### 4.4 辅助 API

| API | 用途 | 备注 |
|-----|------|------|
| `GetRobotList` | 获取机器人列表及在线状态 | 异步返回 |
| `GetGroupList` | 获取机器人所在的群列表 | 异步返回，支持翻页 |
| `GetGroupMemberList` | 获取群成员列表 | 异步返回，含 `user_type`, `admin_type` |
| `GetAccessToken` | 获取 API 访问令牌 | 有效期 7200 秒 |

### 4.5 需要监听的回调事件

| 事件 | event_type | 用途 |
|------|-----------|------|
| 接收群消息 | `receive.group.msg` | **核心**: 触发 AI 回复 |
| 发送群消息结果 | `send.group.msg` | 追踪消息发送状态 |
| 机器人登录成功 | `login.success` | 更新机器人在线状态 |
| 机器人进群 | `robot.join.group` | 自动注册群组上下文 |
| 成员入群 | `member.join.group` | 更新群成员缓存 |
| 成员退群 | `remove.group.member` | 更新群成员缓存 |
| 群名称变更 | `group.update.name` | 更新群组信息 |

---

## 5. 消息处理流程 (核心链路)

```
                    企微平台
                      │
                      ▼
              ┌───────────────┐
              │  Callback     │ 1. 验签 + AES 解密
              │  Gateway      │ 2. 解析 event_type
              └───────┬───────┘
                      │
                      ▼
              ┌───────────────┐
              │  Event        │ 根据 event_type 分发:
              │  Dispatcher   │ - receive.group.msg → MessageHandler
              │               │ - login.success → RobotHandler
              └───────┬───────┘ - robot.join.group → GroupHandler
                      │         - ...
                      ▼
              ┌───────────────┐
              │  Message      │ 1. 检查 at_list 是否包含机器人 member_id
              │  Filter       │ 2. 仅处理 msg_type=2 (文本), 忽略图片等
              │               │ 3. 检查 group_id 是否已注册
              └───────┬───────┘ 4. 不满足条件 → 丢弃
                      │ (通过)
                      ▼
              ┌───────────────┐
              │  Context      │ 1. 加载 group → customer → feature_tags
              │  Loader       │ 2. 加载/创建该群的 conversation
              │               │ 3. 提取用户问题文本 (去除 @前缀)
              └───────┬───────┘ 4. 加载最近 N 条会话历史
                      │
                      ▼
              ┌───────────────┐
              │  Agent        │ LangChain Agent 循环:
              │  Engine       │ 1. 构建 system prompt (含群标签)
              │               │ 2. 注入会话历史
              │               │ 3. 执行工具调用 (search/read/clarify)
              └───────┬───────┘ 4. 生成最终回复
                      │
                      ▼
              ┌───────────────┐
              │  Reply        │ 1. 格式化回复文本 (长度控制)
              │  Sender       │ 2. 构造 at_list (@ 提问者)
              │               │ 3. 调用 SendGroupMsg
              └───────┬───────┘ 4. 落库 (assistant message)
                      │
                      ▼
                    企微群
```

---

## 6. 技术栈

| 组件 | 选型 | 说明 |
|------|------|------|
| 语言 | Go 1.24+ | 与 kh、metax_rag 保持一致 |
| Web 框架 | Gin | 与 kh 保持一致 |
| Agent 框架 | langchaingo | 与 kh 保持一致，但独立实例 |
| 数据库 | PostgreSQL | 群组映射、会话、消息 |
| 缓存 | Redis | Access Token 缓存、消息去重、群成员缓存 |
| 配置 | Viper + YAML | |
| 依赖注入 | Google Wire | 与 kh 保持一致 |

---

## 7. 项目结构 (初版)

```
ai-customer/
├── cmd/
│   └── server/
│       ├── main.go
│       ├── wire.go
│       └── wire_gen.go
├── configs/
│   ├── config.example.yaml
│   └── config.yaml
├── internal/
│   ├── callback/             # 企微回调网关
│   │   ├── handler.go        # HTTP handler: 验签、解密、分发
│   │   ├── crypto.go         # MD5 验签 + AES/CBC 解密
│   │   └── routes.go
│   ├── dispatcher/           # 事件分发器
│   │   └── dispatcher.go     # event_type → handler 路由
│   ├── message/              # 群消息处理 (核心)
│   │   ├── filter.go         # @机器人检测、消息类型过滤
│   │   ├── handler.go        # 消息处理主流程
│   │   └── extractor.go      # 从消息中提取用户问题文本
│   ├── agent/                # AI Agent 引擎
│   │   ├── service.go        # Agent 核心循环
│   │   ├── prompt.go         # 客服场景 prompt 模板
│   │   └── tools/            # Agent 工具集
│   │       ├── search.go     # search_knowledge (调 kh)
│   │       ├── read_doc.go   # read_document (调 kh)
│   │       ├── feature.go    # check_feature_tag (本地)
│   │       └── clarify.go    # ask_clarification
│   ├── group/                # 群组上下文管理
│   │   ├── service.go        # 群 ↔ 客户 ↔ 标签 映射
│   │   ├── repo.go           # 数据库操作
│   │   └── model.go          # 数据模型
│   ├── conversation/         # 会话管理
│   │   ├── service.go
│   │   ├── repo.go
│   │   └── model.go
│   ├── robot/                # 机器人状态管理
│   │   └── service.go
│   ├── wecom/                # 企微 API 客户端
│   │   ├── client.go         # HTTP client, Token 管理
│   │   ├── send.go           # SendGroupMsg 封装
│   │   └── query.go          # GetGroupList 等查询封装
│   ├── khclient/             # knowledge-hub API 客户端
│   │   ├── client.go         # HTTP client, API Key 认证
│   │   ├── search.go         # /api/rag/retrieve
│   │   └── document.go       # /api/document/detail
│   ├── config/               # 配置加载
│   │   └── config.go
│   └── model/                # 共享数据模型
│       ├── event.go          # 企微回调事件结构体
│       └── message.go        # 消息类型定义
├── pkg/                      # 可复用公共包
│   ├── crypto/               # AES/MD5 加解密工具
│   └── logger/
├── source-doc/               # 企微 API 文档 (参考)
├── spec.md                   # 本文档
└── think.md                  # 业务背景文档
```

---

## 8. 数据表设计

```sql
-- 群组表: 群 ↔ 客户 ↔ 特性标签映射
CREATE TABLE enterprise_group (
    group_id        TEXT PRIMARY KEY,           -- 企微群 ID
    group_name      TEXT NOT NULL DEFAULT '',
    customer_id     TEXT,                        -- 客户主体 ID
    customer_name   TEXT NOT NULL DEFAULT '',
    robot_id        TEXT NOT NULL,               -- 绑定的机器人 ID
    robot_member_id TEXT NOT NULL,               -- 机器人在群内的 member_id
    dataset_id      TEXT,                        -- kh 知识库 ID
    feature_tag     JSONB NOT NULL DEFAULT '{}', -- 开通的特性标签
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 会话表: 以群为维度
CREATE TABLE conversation (
    id              TEXT PRIMARY KEY,
    group_id        TEXT NOT NULL REFERENCES enterprise_group(group_id),
    status          TEXT NOT NULL DEFAULT 'active',  -- active / closed
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conversation_group ON conversation(group_id, status);

-- 消息表
CREATE TABLE message (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversation(id),
    role            TEXT NOT NULL,            -- user / assistant / system
    sender_id       TEXT,                     -- 企微 sender_id (role=user 时)
    sender_name     TEXT,
    content         TEXT NOT NULL,
    citations       JSONB,                   -- Agent 引用来源
    tool_calls      JSONB,                   -- Agent 工具调用记录
    wecom_msg_id    TEXT,                    -- 企微消息 ID (去重)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_message_conv ON message(conversation_id, created_at);
CREATE UNIQUE INDEX idx_message_wecom_dedup ON message(wecom_msg_id) WHERE wecom_msg_id IS NOT NULL;

-- 机器人状态表
CREATE TABLE robot (
    robot_id        TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    avatar          TEXT,
    login_status    INT NOT NULL DEFAULT 2,  -- 1=online, 2=offline, 3=initializing
    member_id       TEXT,                    -- 机器人作为群成员时的 ID
    last_login_at   TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

---

## 9. 配置文件结构

```yaml
server:
  port: "8080"

# 企微平台配置
wecom:
  api_host: "https://s2.xunjinet.com.cn"
  app_key: "${WECOM_APP_KEY}"
  app_secret: "${WECOM_APP_SECRET}"
  callback:
    token: "${WECOM_CALLBACK_TOKEN}"
    aes_key: "${WECOM_CALLBACK_AES_KEY}"

# knowledge-hub 配置
knowledge_hub:
  host: "http://localhost:9090"
  api_key: "${KH_API_KEY}"
  timeout: 30s

# 数据库
database:
  dsn: "postgres://user:pass@localhost:5432/ai_customer?sslmode=disable"

# Redis
redis:
  addr: "localhost:6379"

# Agent 配置
agent:
  model: "gpt-4o-mini"
  temperature: 0.3
  history_limit: 20          # 注入 Agent 的历史消息条数
  reply_max_length: 500      # 回复最大字符数
```

---

## 10. 边界与约束

| 约束 | 说明 |
|------|------|
| 仅文本回复 | v1 阶段只处理和回复文本消息 (msg_type=2) |
| @ 位置限制 | 企微 API `at_location` 仅支持 0(开头) 和 1(结尾)，不支持任意位置 |
| 异步 API | 企微的 GetGroupList / GetGroupMemberList 等为异步返回，需通过回调接收结果 |
| Token 刷新 | Access Token 有效期 7200 秒，需提前刷新 |
| 消息去重 | 用 `wecom_msg_id` 唯一索引防止重复处理 |
| kh 降级 | kh 不可用时 Agent 工具调用失败，回复"知识库暂时不可用，请稍后再试" |

---

## 11. 未来扩展 (不在 v1 范围)

- 图片消息处理 (截图识别)
- 引用消息解析 (内部人员引用客户消息后 @机器人)
- 群组自动注册 (robot.join.group 事件触发)
- 管理后台 (群标签配置、知识库绑定、对话日志查看)
- 多机器人支持
- 消息限流与排队
