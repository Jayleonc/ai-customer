# DEV_LOG

**最后同步**: 2026-04-14
**状态**: turnmesh 主链路接入完成，query rewrite 也已切到 one-shot API

## 1. 当前结论

`ai-customer` 不再自己维护一套完整的 model/tool loop。

当前边界已经明确：

- `ai-customer` 保留业务壳
- `turnmesh` 提供 runtime

具体来说：

- 主问答链路走 [internal/agent/service.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/internal/agent/service.go:1) 里的 `turnmesh.New(...).RunTurn(...)`
- query rewrite 走 [internal/agent/rewrite.go](/Users/jayleonc/Developer/work/git.pinquest.cn/ai-customer/internal/agent/rewrite.go:1) 里的 `turnmesh.RunOneShot(...)`
- `search_knowledge`、`read_document`、`check_feature_tag`、`ask_clarification` 仍然是本仓库自己的业务工具

## 2. 这次变更的关键决策

### 2.1 为什么主链路下沉到 turnmesh

因为 model 调用、tool call dispatch、tool result continuation 和多轮 turn loop 都是 runtime 公共问题，不应该继续留在业务仓库里复制实现。

### 2.2 为什么 rewrite 也切到 turnmesh

因为 rewrite 虽然不需要 tool loop，但它本质上仍然是一次模型调用。

如果主链路已经走 `turnmesh`，rewrite 继续自己拼 `/chat/completions` 会带来两套 provider 边界、两套错误语义和两套后续维护路径，所以这次统一成了 `RunOneShot(...)`。

### 2.3 为什么保留 preSearch 和 fallback

因为这些都是客服业务策略，不是 runtime 能替你决定的东西：

- 是否预检索
- 如何改写 query
- 没结果时怎么降级
- `ask_clarification` 怎么约定返回格式

这些仍然应该留在 `ai-customer`。

## 3. 当前配置事实

当前 `agent` 配置分成两层理解：

- 应用策略：`history_limit`、`reply_max_length`、`pre_search_*`、`query_rewrite_*`
- runtime 约束：`token_budget`、`tool_timeout_seconds`

已经删除的旧字段：

- `max_iterations`
- `llm_retry_count`

## 4. 已知限制

- `turnmesh` 目前仍以本地验证和嵌入式接入为主，还没有完整 examples 体系
- query rewrite 现在虽已走 `RunOneShot(...)`，但 rewrite prompt 仍是本仓库自定义策略
- `ask_clarification` 仍通过字符串前缀约定回传，不是正式事件类型

## 5. 后续最值得做的事

- 给 `ai-customer` 增加 turnmesh runtime 的灰度开关或对比开关
- 把 `ask_clarification` 从字符串约定升级成正式语义
- 继续把更多可复用的业务前处理抽象成 turnmesh 外围能力，而不是继续塞回业务仓库
