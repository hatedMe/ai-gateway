# Trae CLI API Proxy

将 OpenAI / Claude API 请求转换为 Trae CLI 私有 API 格式的代理服务器，基于 Go + Gin 实现。

## 项目结构

```
traecli-api-proxy/
├── cmd/
│   ├── server/
│   │   └── main.go              # 入口：加载配置、注册路由、启动服务
│   └── test/
│       └── reproduce_stream.go  # 直连 Trae API 测试脚本
├── config/
│   ├── config.go                # 配置层：模型映射、常量（TargetURL）
│   └── config_test.go           # 配置层单元测试
├── model/
│   ├── openai.go                # 数据层：OpenAI 请求/响应结构体
│   ├── claude.go                # 数据层：Claude 请求/响应结构体
│   └── trae.go                  # 数据层：Trae 内部请求/响应结构体
├── handler/
│   ├── openai.go                # 处理层：/v1/chat/completions、/v1/models
│   ├── claude.go                # 处理层：/v1/messages
│   └── handler_test.go          # 处理层单元测试
├── service/
│   ├── trae.go                  # 业务层：构建请求、发送请求、格式转换、SSE 解析
│   └── trae_test.go             # 业务层单元测试
├── logs/                        # 请求/响应日志（不纳入版本控制）
├── config.yaml                  # 运行时配置（不纳入版本控制）
├── config.example.yaml          # 配置示例
├── go.mod
└── go.sum
```

| 层   | 包           | 职责                                                                 |
| ---- | ------------ | -------------------------------------------------------------------- |
| 入口 | `cmd/server` | 加载 config.yaml、注册路由、启动 HTTP 服务                           |
| 配置 | `config`     | 模型映射表 `ModelMap`、目标 URL 常量 `TargetURL`、`GetModelConfig()` |
| 数据 | `model`      | 纯结构体定义，按协议分文件（OpenAI / Claude / Trae）                 |
| 处理 | `handler`    | HTTP 请求解析、认证校验、响应写入、流式/非流式处理、日志记录         |
| 业务 | `service`    | 构建请求、发送请求、内容转换、工具转换、SSE 读取                     |

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

# Trae API 配置
trae:
    # 请求头，按需配置
    headers:
        X-App-Id: "7b3f9dc2-8a4e-5c6d-2f1b-9e4a3c5b7df0"
        X-Ide-Function: "chat"
        X-Ide-Version-Code: "20260206"
```

| 配置项         | 说明                          |
| -------------- | ----------------------------- |
| `server.port`  | 代理服务监听端口，默认 `8318` |
| `trae.headers` | 发往 Trae API 的额外请求头    |

> **认证信息不需要配置在文件中**，由客户端请求时通过 `Authorization` 或 `X-Api-Key` 头传入，代理会自动透传给 Trae API。

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

认证值格式为 `Cloud-IDE-JWT <token>`，代理会将其作为 `Authorization` 头发送给 Trae API。

**OpenAI 客户端示例：**

```bash
curl http://localhost:8318/v1/chat/completions \
  -H "Authorization: Bearer Cloud-IDE-JWT <your-token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.1","messages":[{"role":"user","content":"你好"}]}'
```

**Claude 客户端示例：**

```bash
curl http://localhost:8318/v1/messages \
  -H "X-Api-Key: Cloud-IDE-JWT <your-token>" \
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

| 包        | 测试数 | 覆盖内容                                                                                                                                                    |
| --------- | ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `config`  | 7      | Load 配置解析、TargetURL 常量、GetModelConfig 已知/未知模型、ModelMap 数量、文件不存在、Headers 读取                                                        |
| `handler` | 5      | /v1/models 完整响应与模型覆盖检查、OpenAI/Claude 请求参数校验、无认证 401 校验                                                                              |
| `service` | 13     | ContentToString、ClaudeContentToString、ConvertClaudeToolsToTrae（Claude 格式/OpenAI 格式/空名/nil）、ReadTraeSSE（正常/提前停止/空数据）、BuildTraeRequest |

## API 接口

### POST /v1/chat/completions

OpenAI 兼容接口，支持流式和非流式响应。

**请求示例（非流式）：**

```bash
curl http://localhost:8318/v1/chat/completions \
  -H "Authorization: Bearer Cloud-IDE-JWT <your-token>" \
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
  -H "Authorization: Bearer Cloud-IDE-JWT <your-token>" \
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
| `model`    | string | 模型名称，必填，见下方模型列表 |
| `messages` | array  | 消息列表，兼容 OpenAI 格式     |
| `stream`   | bool   | 是否流式返回                   |
| `tools`    | array  | 工具列表（可选）               |

**响应格式：** 标准 OpenAI Chat Completion 格式，包含 `reasoning_content`（思维链）和 `tool_calls`（工具调用）支持。

### POST /v1/messages

Claude / Anthropic 兼容接口，支持流式和非流式响应。

**请求示例：**

```bash
curl http://localhost:8318/v1/messages \
  -H "X-Api-Key: Cloud-IDE-JWT <your-token>" \
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
| `model`      | string       | 模型名称                                               |
| `max_tokens` | int          | 最大输出 token 数                                      |
| `messages`   | array        | 消息列表，兼容 Claude 格式                             |
| `system`     | string/array | 系统提示词（可选）                                     |
| `stream`     | bool         | 是否流式返回                                           |
| `tools`      | array        | 工具列表（可选），自动转换 Claude 工具格式为 Trae 格式 |

**响应格式：** 标准 Claude Messages API 格式，包含 `thinking`（思维链）、`text` 和 `tool_use` 内容块，工具参数通过 `input_json_delta` 增量传递。

### GET /v1/models

返回可用模型列表，兼容 OpenAI 格式。

**请求示例：**

```bash
curl http://localhost:8318/v1/models
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

## 支持的模型

| config_name            | model_name                 | 说明                   |
| ---------------------- | -------------------------- | ---------------------- |
| `Doubao-Seed-2.0-Code` | Doubao-Seed-2.0-Code\_\_v2 | 豆包 Seed 2.0 代码模型 |
| `Doubao_1_6`           | Doubao_1_6\_\_v2           | 豆包 Seed 代码模型     |
| `glm-5.1`              | glm-5\_\_v2                | GLM-5.1                |
| `glm-5`                | glm-5\_\_v2                | GLM-5                  |
| `glm-4.7`              | glm-4.7\_\_v2              | GLM-4.7                |
| `minimax-m2.7`         | minimax-m2.7\_\_v2         | MiniMax M2.7           |
| `minimax-m2.5`         | minimax-m2.5\_\_v2         | MiniMax M2.5           |
| `qwen3-coder`          | qwen3-coder\_\_v2          | 通义千问3 Coder        |
| `kimi-k2.6`            | kimi-k2.6\_\_v2            | Kimi K2.6              |
| `kimi-k2.5`            | kimi-k2.5\_\_v2            | Kimi K2.5              |
| `deepseek-V3.2`        | deepseek-V3.2\_\_v2        | DeepSeek V3.2          |
| `deepseek-V3.1`        | deepseek-V3.1\_\_v2        | DeepSeek V3.1          |

未在映射表中的模型名会自动拼接 `__v2` 后缀透传。

## 硬编码常量

以下内容不常变更，硬编码在 `config/config.go` 中：

| 常量        | 值                                                           | 说明              |
| ----------- | ------------------------------------------------------------ | ----------------- |
| `TargetURL` | `https://console.enterprise.trae.cn/api/ide/v2/llm_raw_chat` | Trae API 目标地址 |
| `ModelMap`  | 12 个模型映射                                                | 模型名称转换表    |

## 转换逻辑

### 请求转换

- **OpenAI → Trae**：将 `messages` 中的 `content` 统一转为 `TraeContent` 格式，`tool_calls` 转为文本标记，最后一条 `user` 消息提取为 `user_input`，`tools` 直接透传
- **Claude → Trae**：将 `system` 字段转为 `system` 角色消息，`content` 中的多模态块（`text`/`tool_use`/`tool_result`）合并为纯文本，`tools` 中的 `input_schema` 转为 JSON 字符串格式的 `parameters`

### 响应转换

- **Trae SSE → OpenAI**：`output` 事件映射为 `chat.completion.chunk`，`tool_calls` 按 index 累积拼接分片参数后一次性输出，`done` 事件携带 `finish_reason`（有工具调用时为 `tool_calls`），非流式模式累积所有输出后一次性返回
- **Trae SSE → Claude**：`output` 事件中的 `reasoning_content` 映射为 `thinking` 内容块，`response` 映射为 `text` 内容块，`tool_calls` 按 index 累积拼接分片参数后通过 `tool_use` 内容块输出（`content_block_start` 的 `input` 为空对象，参数通过 `input_json_delta` 增量传递），遵循 Claude SSE 协议（`message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta` → `message_stop`）

### 工具调用处理

- **流式分片累积**：Trae API 返回的 `tool_calls` 参数可能分多个 SSE 事件到达（第一个携带 `id` 和部分参数，后续只有参数续接），代理按 `index` 累积拼接，确保参数完整
- **消息转换**：Claude Code 发回的 `tool_use` 和 `tool_result` 内容块转为文本标记（如 `[Tool Use: Bash (id: xxx) Input: {...}]`、`[Tool Result (id: xxx): ...]`），让模型知道工具已执行过，避免重复调用
- **finish_reason**：有工具调用时 OpenAI 返回 `tool_calls`，Claude 返回 `tool_use`

## 日志

每次请求会在 `logs/` 目录下生成日志文件：

| 文件名格式                            | 内容                         |
| ------------------------------------- | ---------------------------- |
| `request_YYYYMMDD_HHMMSS.json`        | Claude 接口收到的完整请求    |
| `response_YYYYMMDD_HHMMSS.log`        | Trae API 返回的原始 SSE 数据 |
| `openai_request_YYYYMMDD_HHMMSS.json` | OpenAI 接口收到的完整请求    |
| `openai_response_YYYYMMDD_HHMMSS.log` | Trae API 返回的原始 SSE 数据 |

`logs/` 目录已加入 `.gitignore`，不会纳入版本控制。
