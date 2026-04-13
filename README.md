# ai-customer

`ai-customer` 是一个企微群 AI 客服服务。

它负责企微回调、群消息过滤、群上下文、知识检索策略、客服工具和会话落库；底层 model turn loop / tool loop 由 [`turnmesh`](https://github.com/Jayleonc/turnmesh) 提供。

## 当前架构

```text
企微回调
  -> callback / dispatcher / message
  -> agent.Service
     -> preSearch / query rewrite / prompt 组装
     -> turnmesh.RunOneShot(...)    // query rewrite
     -> turnmesh.New(...).RunTurn() // 主问答链路
     -> tools: search_knowledge / read_document / check_feature_tag / ask_clarification
  -> wecom send message
```

## 最小启动

```bash
go mod download
go generate ./cmd/server/
go run ./cmd/server/
```

## 配置

配置示例在 [configs/config.example.yaml](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/configs/config.example.yaml:1)。

第一次本地启动至少需要填这些值：

- `database.dsn`
- `wecom.app_key`
- `wecom.app_secret`
- `wecom.callback.token`
- `wecom.callback.aes_key`
- `knowledge_hub.host`
- `knowledge_hub.api_key`
- `agent.base_url`
- `agent.api_key`
- `agent.model`

其中：

- 应用策略配置：`history_limit`、`reply_max_length`、`pre_search_*`
- runtime 相关配置：`token_budget`、`tool_timeout_seconds`

## 仓库边界

保留在 `ai-customer`：

- 企微消息路由与触发规则
- 群组 / 客户 / 会话上下文
- knowledge-hub 检索和文档精读策略
- prompt、query rewrite、fallback 规则
- 客服工具实现

下沉到 `turnmesh`：

- provider session
- 单次 one-shot 调用
- 多轮 turn loop
- tool call dispatch
- tool result continuation

## 关键入口

- [cmd/server/main.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/cmd/server/main.go:1)
- [internal/message/handler.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/internal/message/handler.go:1)
- [internal/agent/service.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/internal/agent/service.go:1)
- [internal/agent/rewrite.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/internal/agent/rewrite.go:1)

## 相关文档

- [CLAUDE.md](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/CLAUDE.md:1)
- [DEV_LOG.md](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/DEV_LOG.md:1)
- [spec.md](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/spec.md:1)
