# Trae CLI API Proxy

将 OpenAI / Claude API 请求转换为 Trae CLI 私有 API 格式的代理服务器，基于 Go + Gin 实现。

## 项目结构

```
traecli-api-proxy/
├── cmd/
│   ├── server/
│   │   └── main.go              # 入口：加载配置、注册路由、启动服务
│   └── test/
│       ├── reproduce_stream.go  # 直连 Trae API 测试脚本
│       ├── test_dummy.go        # 临时测试辅助
│       └── test_tools.go        # 工具调用测试辅助
├── config/
│   ├── config.go                # 配置层：模型映射、常量（TargetURL）、appkey/token 校验
│   └── config_test.go           # 配置层单元测试
├── model/
│   ├── openai.go                # 数据层：OpenAI 请求/响应结构体
│   ├── claude.go                # 数据层：Claude 请求/响应结构体
│   └── trae.go                  # 数据层：Trae 内部请求/响应结构体
├── handler/
│   ├── openai.go                # 处理层：/v1/chat/completions、/v1/models
│   ├── claude.go                # 处理层：/v1/messages、writeLog
│   ├── auth.go                  # 处理层：AuthMiddleware（appkey → 真实 token）
│   ├── context.go               # 处理层：AuthKey、GetRequestID 等上下文工具
│   └── handler_test.go          # 处理层单元测试
├── service/
│   ├── trae.go                  # 业务层：构建请求、发送请求、格式转换、SSE 解析
│   ├── models.go                # 业务层：FetchTraeModels（拉取远端模型列表）
│   ├── trae_test.go             # 业务层单元测试
│   └── history_test.go          # 业务层历史用例测试
├── logger/
│   └── logger.go                # 日志层：slog 封装，支持 request_id 透传
├── docs/                        # 设计文档与历史方案
├── logs/                        # 请求/响应日志（不纳入版本控制）
├── config.yaml                  # 运行时配置（不纳入版本控制）
├── config.example.yaml          # 配置示例
├── go.mod
└── go.sum
```

| 层   | 包           | 职责                                                                 |
| ---- | ------------ | -------------------------------------------------------------------- |
| 入口 | `cmd/server` | 加载 config.yaml、注册路由、启动 HTTP 服务                           |
| 配置 | `config`     | 模型映射表 `ModelMap`、目标 URL 常量 `TargetURL`、`GetModelConfig()`、`GetRealToken()` |
| 数据 | `model`      | 纯结构体定义，按协议分文件（OpenAI / Claude / Trae）                 |
| 处理 | `handler`    | HTTP 请求解析、认证校验、响应写入、流式/非流式处理、日志记录         |
| 业务 | `service`    | 构建请求、发送请求、内容转换、工具转换、SSE 读取、远端模型列表拉取   |
| 日志 | `logger`     | 基于 `log/slog` 的封装，支持按 `request_id` 串联日志                 |

## 快速开始

### 1. 安装依赖

```bash
go mod tidy
```

### 2. 配置

复制配置示例并修改：

```bash
cp config.example.yaml config.yaml
```

`config.yaml` 配置说明：

```yaml
# 服务配置
server:
    port: 8318

# 认证配置
# 客户端使用 appkey 调用 API，代理用真实 token 转发到 Trae，避免 token 泄露
auth:
    tokens:
        - "Cloud-IDE-JWT <your-real-token-1>"
        - "Cloud-IDE-JWT <your-real-token-2>"
    app_keys:
        - "<your-app-key-1>"
        - "<your-app-key-2>"

# Trae API 配置
trae:
    # 请求头，按需配置
    headers:
        X-App-Id: "<your-app-id>"
        X-Ide-Function: "chat"
        X-Ide-Version-Code: "20260206"
```

| 配置项             | 说明                                                              |
| ------------------ | ----------------------------------------------------------------- |
| `server.port`      | 代理服务监听端口，未配置时 fallback 为 `3000`                     |
| `auth.tokens`      | 真实 Trae token 池（格式 `Cloud-IDE-JWT <jwt>`），转发时随机选一个 |
| `auth.app_keys`    | 允许客户端使用的 appkey 白名单                                    |
| `trae.headers`     | 发往 Trae API 的额外请求头                                        |

> **认证模型**：客户端不直接持有真实 token，而是用 `appkey` 调用代理。代理校验 `appkey` 是否在白名单中，校验通过后从 `auth.tokens` 池中随机取一个真实 token 转发给 Trae API，避免 token 泄露。

### 3. 启动服务

```bash
go run ./cmd/server/
```

或编译后运行：

```bash
go build -o traecli-api-proxy.exe ./cmd/server/
.\traecli-api-proxy.exe
```

热重载开发（需安装 [Air](https://github.com/cosmtrek/air)）：

```bash
air
```

## 认证方式

代理支持两种认证头，优先级从高到低：

1. **`Authorization`** — 标准方式，值会自动去除 `Bearer ` 前缀
2. **`X-Api-Key`** — Claude API 兼容方式

客户端传入的值为 **appkey**（在 `config.yaml` 的 `auth.app_keys` 中配置）。代理校验 appkey 通过后，从 `auth.tokens` 池中随机选取一个真实 `Cloud-IDE-JWT <token>` 作为 `Authorization` 头转发给 Trae API，确保真实 token 不暴露给客户端。

**OpenAI 客户端示例：**

```bash
curl http://localhost:8318/v1/chat/completions \
  -H "Authorization: Bearer <your-app-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.1","messages":[{"role":"user","content":"你好"}]}'
```

**Claude 客户端示例：**

```bash
curl http://localhost:8318/v1/messages \
  -H "X-Api-Key: <your-app-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.1","max_tokens":4096,"messages":[{"role":"user","content":"你好"}]}'
```

## 测试

运行全部测试：

```bash
go test ./... -v
```

运行指定包的测试：

```bash
go test ./config/... -v
go test ./service/... -v
go test ./handler/... -v
```

测试覆盖情况：

| 包        | 测试数 | 覆盖内容                                                                                                                                                                        |
| --------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `config`  | 7      | Load 配置解析、TargetURL 常量、GetModelConfig 已知/未知模型、ModelMap 数量（**当前失败，期望 12 实际 23，待同步**）、文件不存在、Headers 读取                                    |
| `handler` | 5      | /v1/models 完整响应（远端 401 时回退本地）、OpenAI/Claude 请求参数校验、无认证 401 校验                                                                                         |
| `service` | 16     | ContentToString（String/Nil/Map）、ClaudeContentToString（String/ContentBlocks/NonTextBlock）、ConvertClaudeToolsToTrae（OpenAI 格式/Claude 格式/空名/nil）、ReadTraeSSE（正常/提前停止/空数据）、BuildTraeRequest、AccumulateToolCall、ConvertClaudeMessagesToTrae_ToolHistory |

> 已知失败：`config/config_test.go:67` 的 `TestModelMapCount` 仍断言 `12`，但 `ModelMap` 已扩展到 23 项，需同步更新断言值。

## API 接口

### POST /v1/chat/completions

OpenAI 兼容接口，支持流式和非流式响应。

**请求示例（非流式）：**

```bash
curl http://localhost:8318/v1/chat/completions \
  -H "Authorization: Bearer <your-app-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.1",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'
```

**请求示例（流式）：**

```bash
curl http://localhost:8318/v1/chat/completions \
  -H "Authorization: Bearer <your-app-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.1",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

**支持的字段：**

| 字段       | 类型   | 说明                           |
| ---------- | ------ | ------------------------------ |
| `model`    | string | 模型名称，必填，可通过 `/v1/models` 查询 |
| `messages` | array  | 消息列表，兼容 OpenAI 格式     |
| `stream`   | bool   | 是否流式返回                   |
| `tools`    | array  | 工具列表（可选）               |

**响应格式：** 标准 OpenAI Chat Completion 格式，包含 `reasoning_content`（思维链）和 `tool_calls`（工具调用）支持。

### POST /v1/messages

Claude / Anthropic 兼容接口，支持流式和非流式响应。

**请求示例：**

```bash
curl http://localhost:8318/v1/messages \
  -H "X-Api-Key: <your-app-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.1",
    "max_tokens": 4096,
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

**支持的字段：**

| 字段         | 类型         | 说明                                                   |
| ------------ | ------------ | ------------------------------------------------------ |
| `model`      | string       | 模型名称，为空时默认 `claude-3-5-sonnet-latest`        |
| `max_tokens` | int          | 最大输出 token 数                                      |
| `messages`   | array        | 消息列表，兼容 Claude 格式                             |
| `system`     | string/array | 系统提示词（可选）                                     |
| `stream`     | bool         | 是否流式返回                                           |
| `tools`      | array        | 工具列表（可选），自动转换 Claude 工具格式为 Trae 格式 |

**响应格式：** 标准 Claude Messages API 格式，包含 `thinking`（思维链）、`text` 和 `tool_use` 内容块，工具参数通过 `input_json_delta` 增量传递。

### GET /v1/models

返回可用模型列表，兼容 OpenAI 格式。默认调用 Trae 的 `get_config_list` 拉取远端列表（按 `usage == "chat_completion"` 过滤）；远端接口失败时回退到本地 `ModelMap`。未在 `ModelMap` 中的模型名会自动拼接 `__v2` 后缀透传。

**请求示例：**

```bash
curl http://localhost:8318/v1/models \
  -H "Authorization: Bearer <your-app-key>"
```

**响应示例：**

```json
{
    "object": "list",
    "data": [
        {
            "id": "glm-5.1",
            "object": "model",
            "created": 1750118400,
            "owned_by": "trae"
        }
    ]
}
```

## 硬编码常量

以下内容不常变更，硬编码在 `config/config.go` 中：

| 常量        | 值                                                           | 说明              |
| ----------- | ------------------------------------------------------------ | ----------------- |
| `TargetURL` | `https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat` | Trae API 目标地址 |
| `ModelMap`  | 23 个模型映射                                                | 模型名称转换表    |

> `service/models.go` 中 `FetchTraeModels` 调用的 `https://console.enterprise.trae.cn/api/ide/v1/cli/get_config_list` 也是硬编码的 Trae 模型列表接口地址。

## 转换逻辑

### 请求转换

- **OpenAI → Trae**：将 `messages` 中的 `content` 统一转为 `TraeContent` 格式；`tool_calls` 转为 Trae 的 `tool_calls` 结构化字段（`function_call.name` / `function_call.arguments`）；`tool_call_id` 透传；最后一条 `user` 消息的 content 提取为 `user_input`；`tools` 经 `ConvertClaudeToolsToTrae` 转换后透传
- **Claude → Trae**：将 `system` 字段转为 `system` 角色消息；`content` 中的 `text` 块合并为 `TraeContent`，`tool_use` 块转为 `tool_calls` 结构化字段，`tool_result` 块转为 `tool` 角色消息（设置 `tool_call_id`，`is_error` 时前缀 `ERROR:`）；`tools` 中的 `input_schema` 转为 JSON 字符串格式的 `parameters`

### 响应转换

- **Trae SSE → OpenAI**：`output` 事件映射为 `chat.completion.chunk`，`tool_calls` 按 index 累积拼接分片参数后一次性输出，`done` 事件携带 `finish_reason`（有工具调用时为 `tool_calls`），非流式模式累积所有输出后一次性返回
- **Trae SSE → Claude**：`output` 事件中的 `reasoning_content` 映射为 `thinking` 内容块，`response` 映射为 `text` 内容块，`tool_calls` 按 index 累积拼接分片参数后通过 `tool_use` 内容块输出（`content_block_start` 的 `input` 为空对象，参数通过 `input_json_delta` 增量传递），遵循 Claude SSE 协议（`message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta` → `message_stop`）

### 工具调用处理

- **流式分片累积**：Trae API 返回的 `tool_calls` 参数可能分多个 SSE 事件到达（第一个携带 `id` 和部分参数，后续只有参数续接），代理按 `index` 累积拼接（`AccumulateToolCall`），确保参数完整
- **消息转换**：历史消息中的 `tool_use` / `tool_result` 转为 Trae 的结构化字段（`tool_calls` / `tool` 角色消息），让模型知道工具已执行过，避免重复调用
- **finish_reason**：有工具调用时 OpenAI 返回 `tool_calls`，Claude 返回 `tool_use`

## 日志

日志写入由环境变量 `DEBUG_REQUESTS=true` 控制，默认关闭。开启后，每次请求会在 `logs/` 目录下生成日志文件（文件名中的 `<rid>` 为请求 ID）：

| 文件名格式                       | 内容                                  |
| -------------------------------- | ------------------------------------- |
| `claude_request_<rid>.json`      | Claude 接口收到的完整请求             |
| `claude_payload_<rid>.json`      | 转换后发往 Trae 的 payload            |
| `openai_request_<rid>.json`      | OpenAI 接口收到的完整请求             |
| `openai_payload_<rid>.json`      | 转换后发往 Trae 的 payload            |
| `openai_raw_sse_<rid>.log`       | OpenAI 流式响应中 Trae 返回的原始 SSE |

运行时日志（非请求 dump）通过 `logger` 包输出到 stdout，基于 `log/slog`，自动附带 `request_id` 字段，便于串联单次请求的全链路日志。

`logs/` 目录已加入 `.gitignore`，不会纳入版本控制。
