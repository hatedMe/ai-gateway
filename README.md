# 多协议 AI 网关

支持 Anthropic Messages、OpenAI Chat Completions 和 OpenAI Responses 三种客户端入口，以及这三种协议的多个上游服务。下游请求的 `model` 匹配配置中的公开名称，确定上游地址、认证和真实模型 ID。Go 1.26；配置解析使用 `go.yaml.in/yaml/v4`，其余功能使用标准库。

三种客户端入口为 `POST /v1/messages`、`POST /v1/chat/completions`、`POST /v1/responses`。同协议保留原生字段、JSON、SSE 和上游错误体；跨协议转换共同支持的请求和响应内容。

原有单 `upstream` 配置和仅环境变量启动方式继续兼容，仍使用 Responses 上游。下面原有详细转换说明主要针对该 Responses 转换路径。

参考 `D:/homework/CLIProxyAPI/internal/translator/codex/claude` 的处理方式，独立实现，不包含其 OAuth、调度模块。

## 启动

入口为 `cmd/server`。仅环境变量或旧单上游模式默认使用 OpenCode 地址、`muse-spark-1.3-contributor` 模型与本地 `7890` 代理；新模式使用每个上游的显式配置。上游密钥必须自行提供，程序不内置 token。上游是否允许当前地区访问，由服务提供方决定。

推荐复制示例配置并通过环境变量提供密钥：

```powershell
Copy-Item config.example.yaml config.yaml
$env:OPENAI_API_KEY = "你的上游密钥"
go run ./cmd/server --config ./config.yaml --check-config
go run ./cmd/server --config ./config.yaml
```

`--check-config` 只校验配置，不监听端口、不访问上游、不创建日志目录。真实 `config.yaml` 已加入 Git 忽略列表，示例不包含真实密钥。

也可保持仅环境变量启动：

```powershell
$env:OPENAI_API_KEY = "你的上游密钥"
$env:OPENAI_BASE_URL = "https://api.openai.com/v1"
$env:OPENAI_MODEL = "上游模型名称"
$env:GATEWAY_API_KEY = "local-test-key"
go run ./cmd/server
```

开发时也可以使用 Air 自动重新编译并重启网关：

```powershell
go install github.com/air-verse/air@latest
air
```

仓库中的 `.air.toml` 已配置 `cmd/server` 入口，监听 Go、YAML 文件变化，并忽略 `logs`、`bin` 和 `tmp` 目录。配置修改通过 Air 重启生效；直接启动的进程需要手动重启。

客户端 Anthropic 基地址设为 `http://127.0.0.1:8176`。支持 `x-api-key` 或 Bearer 客户端认证。响应保留客户端请求的模型名。

### 配置文件规则

配置文件选择：`--config <路径>` > 非空 `GATEWAY_CONFIG` > 当前工作目录 `config.yaml`。显式指定的文件不存在即失败；默认文件不存在时允许仅靠环境变量启动。

旧单上游字段优先级：**已设置的环境变量 > YAML 显式字段 > 内置默认值**。新配置使用 `upstreams`，每个服务的密钥可通过 `api_key_env` 显式读取环境变量，完整字段与注释见 [config.example.yaml](config.example.yaml)。不自动加载 `.env`，不做 `${VAR}` 插值。

- 字段缺省沿用默认值；显式 `false`、空字符串、`[]`、`{}` 保留原意。映射和列表整体替换，不做深度合并。
- 空环境变量会覆盖文件值：空 `OPENAI_MODEL` 清除固定模型，空 `GATEWAY_API_KEY` 关闭本地鉴权，空集合变量清空集合；空 `OPENAI_API_KEY`、`GATEWAY_ADDR`、`OPENAI_BASE_URL` 校验失败。
- `upstream.proxy_url: ""` 或空 `OPENAI_PROXY_URL` 取消显式代理，仍遵循标准库 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，不代表强制直连。
- `logging.requests.enabled: false` 关闭请求文件日志；启用时 `dir` 必须非空。相对日志路径以实际加载的配置文件目录为基准；未加载文件时以工作目录为基准。
- `server.read_header_timeout`、`server.shutdown_timeout` 默认为 `10s`、`5s`，必须为正数时长字符串。不设置覆盖完整 SSE 生命周期的总响应写超时。
- 拒绝未知字段、重复键、`null`、错误类型、YAML 别名及多个 YAML 文档。修改配置后重启生效，程序不自动写回配置。

迁移旧部署时，需要显式提供上游密钥，并检查原先设置为空的环境变量；旧版会把部分空环境变量当作未配置，现在使用上述覆盖规则。

### 多上游和模型路由

```yaml
upstreams:
  chat-main:
    protocol: openai-compatible
    base_url: "https://chat.example.com/v1"
    api_key_env: "CHAT_API_KEY"
    models:
      - name: coding
        model: "your-chat-model"
      - name: coding-fast
        model: "your-fast-model"
  anthropic-main:
    protocol: anthropic
    base_url: "https://api.anthropic.com/v1"
    api_key_env: "ANTHROPIC_API_KEY"
    anthropic_version: "2023-06-01"
    models:
      - name: reasoning
        model: "your-claude-model"
```

`model: coding` 的下游请求选中 `chat-main`，真实出站模型为 `your-chat-model`。三个客户端入口都使用相同的模型索引。可以继续添加任意多个上游，每个服务声明多个模型；新增同类服务无需改代码。

| protocol | base_url 后追加的路径 | 出站认证 |
|---|---|---|
| `openai-responses` | `/responses` | Bearer 密钥 |
| `openai-compatible` | `/chat/completions` | Bearer 密钥 |
| `anthropic` | `/messages` | `x-api-key` 和配置的 `anthropic-version` |

- `models` 必须是非空列表，每项 `name`、`model` 必填；`name` 在所有上游间唯一，重复时启动失败。未知公开名称返回 `400`、`model_not_found`，不会访问任何上游，也不会自动按真实 ID 匹配。
- 每个上游独立设置地址、密钥、代理。`api_key` 与 `api_key_env` 二选一，后者引用的环境变量缺失或为空时启动失败。`anthropic_version` 仅用于 Anthropic，且必须显式填写。基地址须包含服务实际 API 前缀，不含最终资源路径。
- 新模式不能同时配置旧 `upstream` 或 `models.aliases`；旧别名迁为上游列表中的 `name/model`。旧 `OPENAI_BASE_URL`、`OPENAI_MODEL`、`OPENAI_PROXY_URL`、`OPENAI_MODEL_ALIASES` 等单上游覆盖变量需要清除。`OPENAI_API_KEY` 只有被 `api_key_env` 显式引用时才可作为新模式密钥来源，不会自动覆盖所有服务。全局监听、鉴权和推理配置变量仍可使用。
- `/v1/models` 及详情在新模式列出配置的公开 `name`，沿用现有 Anthropic 风格、排序和分页，不联网获取远端模型。配置清单不代表已在线验证模型可用。
- 计数接口按模型选路：Responses 使用 `/responses/input_tokens`，Anthropic 使用 `/messages/count_tokens`；Chat 上游返回 `501`，不估算、不切换上游。
- 文字和多轮函数工具调用支持九种入站/上游组合的 JSON/SSE。新增 Chat/Anthropic 出站转换目前支持文字、函数工具、token 限额、采样和工具选择；图片、文件、结构化输出、推理配置/历史签名、缓存和服务端历史引用等未映射内容会在出站前明确拒绝。同协议可保留服务原生能力；已有 Responses 出站转换保留其现有兼容能力。
- 同协议原生响应保留真实模型名；跨协议响应使用客户端公开模型名。上游强制 SSE 时可聚合为 JSON；跨协议流中断或工具参数不完整时发送错误，不补造成功终态。Responses 跨协议流提供参数、内容块和输出项的完成事件。请求取消会关闭上游调用。
- 每个请求只选择一个上游，失败不自动重试或切换。出站请求日志额外记录 `upstream` 和 `upstream_protocol`，认证头继续脱敏。

只有一个 Responses 服务时也可采用示例中的新格式。旧的 `upstream` 格式可继续使用，但其固定模型回退不应用于新模式。

### OpenCode 上游

```powershell
$env:OPENAI_API_KEY = "你的 token"
./examples/start-opencode.ps1 -Model "上游实际模型名称"
```

脚本将基地址设置为 `https://opencode.ai/zen/go/v1`，实际消息请求为 `POST https://opencode.ai/zen/go/v1/responses`，自动附带 `Authorization: Bearer <token>` 和 `Content-Type: application/json`。token 只填写原始值，不要再加 `Bearer ` 前缀。也可手动设置 OPENAI_BASE_URL 后启动；基地址不要包含末尾 `/responses`。本地客户端认证仍由 GATEWAY_API_KEY 单独控制。

访问 opencode.ai 时自动发送 `User-Agent: ai-gateway-go/1.0` 与 `x-opencode-session`，符合其接入要求。优先使用客户端的 X-Claude-Code-Session-Id，其次 x-opencode-session，再次 x-session-id；未提供时，以完整上游请求体的 SHA-256 哈希作为本地联调回退。相同请求保持同一 ID，不同历史、参数、工具或 metadata 会生成不同 ID，多用户仍应显式提供唯一会话 ID。该模型输出含推理 token，max_tokens 过低时可能只返回 thinking，建议联调使用 2048 或更高。

2026-10-16 已通过此地址和本地代理对 `muse-spark-1.3-contributor` 完成真实 SDK 联调：普通 JSON、SSE、流式推理载体历史回放均正常返回 end_turn。上游可能在 item.done 与 response.completed 对同一推理项重新加密；网关保留已发送且可回放的载体，继续校验项身份、摘要和直接推理内容，不以密文本身作为身份。

| 环境变量 | 用途 | 默认值 |
|---|---|---|
| OPENAI_API_KEY | 上游密钥，必填 | 无 |
| OPENAI_BASE_URL | API 基地址，程序追加 `/responses` 等路径 | `https://opencode.ai/zen/go/v1` |
| OPENAI_MODEL | 固定上游模型，空字符串使用客户端模型名 | `muse-spark-1.3-contributor` |
| OPENAI_MODEL_ALIASES | 客户端名到上游名的 JSON 对象 | 无 |
| OPENAI_REASONING_EFFORTS | 支持的推理强度，逗号分隔 | 由上游验证 |
| OPENAI_MODEL_REASONING_EFFORTS | 按上游模型配置强度集合的 JSON 对象 | 使用全局集合 |
| GATEWAY_API_KEY | 本地密钥 | 不验证 |
| GATEWAY_ADDR | 监听地址 | `127.0.0.1:8176` |
| OPENAI_PROXY_URL | 上游出站代理，支持 http/https/socks5/socks5h | `http://127.0.0.1:7890` |

通过本地代理访问上游：

```powershell
$env:OPENAI_PROXY_URL = "http://127.0.0.1:7890"
go run ./cmd/server
```

也可使用 `socks5://127.0.0.1:1080` 或带认证的 `http://用户名:密码@主机:端口`。HTTPS 上游经 HTTP 代理使用 CONNECT；所有上游接口共享此设置。代理的实际出口能否访问模型仍由上游限制决定。

```powershell
$env:OPENAI_MODEL_ALIASES = '{"claude-compatible":"your-openai-model"}'
$env:OPENAI_MODEL_REASONING_EFFORTS = '{"your-openai-model":["none","low","medium","high"]}'
```

别名优先于固定模型配置。配置读取进程环境，不自动加载 `.env`。不向上游转发客户端密钥、Anthropic 版本或 beta 头。

## 接口

- `POST /v1/messages`：普通与流式消息，接受 `?beta=true`。
- `POST /v1/chat/completions`：Chat Completions 普通与流式消息，OpenAI SDK 基地址使用 `http://127.0.0.1:8176/v1`。
- `POST /v1/responses`：原生 Responses 普通与流式请求，仍请求上游 `/responses`。
- `POST /v1/messages/count_tokens`：按模型调用 Responses 或 Anthropic 的真实计数接口；Chat 上游或上游不支持时返回 501，不使用字符估算。
- `GET /v1/models`、`GET /v1/models/{id}`：多上游模式列出配置的公开模型名；旧模式配置别名时列出别名，否则转换上游 `/models`。支持 limit/before_id/after_id 本地分页；配置模型创建时间为 epoch，不自动追取源模型列表的额外分页。

```powershell
curl.exe -N http://127.0.0.1:8176/v1/messages -H "Content-Type: application/json" -H "x-api-key: local-test-key" --data-binary "@examples/message.json"
```

示例 stream 设为 true 即可接收 Anthropic SSE。如果上游强制返回 SSE，普通客户端也能得到聚合后的 JSON。

## 原生 Responses

OpenAI SDK 使用 `base_url="http://127.0.0.1:8176/v1"`，调用 `client.responses.create(model="muse-spark-1.3-contributor", input="你好", max_output_tokens=2048)`。真实联调脚本为 `python examples/responses_live_smoke.py`。

路由到 Responses 上游时，此入口直接保留原生 input、instructions、tools、reasoning、include、text、metadata 等字段及未来字段，不经过 Messages/Chat 转换。仅按模型配置替换请求 model；响应 model 保留上游实际值。沿用出站代理、密钥、会话头优先级与完整请求文件日志。路由到其他协议时执行上述共同能力转换与校验。

流式事件原样转发并及时刷新，保留事件名、sequence_number、推理载体和工具参数；不补造结束事件。普通 JSON 原样返回；若上游强制 SSE，则取终态 response 返回 JSON。HTTP 错误保留上游状态码和响应体。此处只接入 POST 创建接口，检索、取消、删除等 Responses 子接口尚未接入；store、previous_response_id、background 等能力取决于上游实际支持。

## Chat Completions

OpenAI SDK 的 `base_url` 设置为 `http://127.0.0.1:8176/v1`，通过 `client.chat.completions.create(...)` 调用；当前未设置本地密钥时，SDK 的 `api_key` 可填写任意非空值。

```powershell
curl.exe -N http://127.0.0.1:8176/v1/chat/completions -H "Content-Type: application/json" --data-binary "@examples/chat.json"
python examples/chat_live_smoke.py
```

支持 system/developer/user/assistant/tool 历史、文字和图片、函数工具与并行结果回传、tool_choice、JSON/schema 输出、停止词和 reasoning_effort（none/minimal/low/medium/high/xhigh，max 映射 xhigh）。支持 max_tokens/max_completion_tokens，后者优先。流式返回 `chat.completion.chunk` 和 `[DONE]`；开启 `stream_options.include_usage` 时，最后额外返回 choices 为空的用量块。上游失败或断流发送错误，不发送成功结束标记。

推理摘要通过 `reasoning_content` 扩展返回，完整载体通过 `reasoning_blocks` 扩展返回；普通响应可将 assistant 消息原样放入下一轮历史。流式载体按 index 合并 thinking/signature，回传时去掉 index。仅提供 reasoning_content 时无法恢复加密推理状态，会省略并提示。引用通过 `citations` 扩展保留，流式对应 delta.citations。正常拒答使用 message.refusal/delta.refusal，内容过滤中止使用 finish_reason=content_filter。

当前仅支持 n=1；音频、旧式 functions/function_call 等不支持的字段明确报错。seed、presence_penalty、frequency_penalty、logprobs、top_logprobs、logit_bias 无对应映射，省略并通过 X-Gateway-Warnings 提示。模型列表沿用现有 Anthropic 格式，此次新增的是聊天完成接口。

当前 OpenCode 模型实际只接受 tool_choice=auto；none、required 和指定函数虽可转换为 Responses 请求，仍会被此上游拒绝。切换到支持这些选项的上游即可使用。

## 兼容矩阵

| 能力 | 处理方式与边界 |
|---|---|
| system、文字、图片、多轮历史 | 顶层 system 与 messages 内的 system/developer 文本转 developer，保留消息顺序；URL/base64 图片与工具图片结果 |
| 函数工具、tool_choice、并行控制 | tool_use ↔ function_call、tool_result ↔ function_call_output，保留调用 ID；错误结果加 `[tool_error]` |
| custom、strict、input_examples、defer_loading | strict schema 规范化，optional 属性采用 nullable required；examples 放描述，defer 改为立即加载并提示 |
| 超长/非法/MCP 工具名 | 确定性哈希短名，定义、历史及选择一致；响应恢复原名 |
| bash、text_editor、computer | 转普通函数 schema，由客户端执行 |
| thinking | enabled/disabled/adaptive、完整 effort、摘要及 reasoning_text、载体往返 |
| 结构化输出 | output_config.format 转 text.format，独立于 thinking；strict schema 规范化 |
| PDF、文档、搜索输入 | PDF base64/URL/上游 file_id；文本/内容文档、搜索及工具文档结果；需上游支持文件 |
| cache_control | 验证后移除并警告；缓存由上游决定，不伪造 TTL |
| token usage | cached_tokens 转 cache_read_input_tokens；有真实缓存写入字段才报告写入 |
| web_search | 转上游内置搜索，结果转 server_tool_use/result；无法映射的信息保留文本 |
| code_execution | 转 code_interpreter 自动容器；有真实退出码才生成完整结果，否则保留原始输出文本 |
| web_fetch | 近似转 web_search 并警告，不保证精确 URL 抓取语义 |
| 内置工具历史 | 保留可读上下文，不重新执行 |
| 引用与注解 | URL 引用与 citations_delta；缺定位信息的注解保留来源文本；URL 加密索引为空，不能作为原生签名回放 |
| stop_sequences | 匹配可见文本，支持跨分片及相邻文本块；不跨 thinking/工具块，不匹配工具参数；隐藏命中后的内容 |
| top_k | 校验后省略并警告，Responses 无等价参数 |
| service_tier、speed | standard_only→default，fast→priority 并警告；取决于上游支持 |
| assistant prefill | 转继续草稿指令并警告，不保证逐字续写 |
| 外部 thinking/redacted_thinking | 原生 Anthropic 隐藏状态无法转换，移除并警告；本网关载体可恢复 |
| context_management | clear_thinking_20251015 + keep:all 视为保留全部历史；其它编辑策略保留历史并返回兼容警告，不当作 OpenAI compaction 转发 |
| 工具参数截断 | SSE 保留部分 JSON 并 max_tokens 结束；普通响应用文本保留未完成调用，不伪造空对象调用 |
| 错误与取消 | 保留请求 ID/code/param/Retry-After；断流报 error；取消传至上游 |

近似和省略策略通过 `X-Gateway-Warnings` 提示；逗号分隔，每项使用 URL QueryEscape 编码。停止词在输出侧生效，仍读取上游完整响应，usage 保留实际上游用量，不减少已生成 token 的计费。流式暂存可能匹配的尾部与块边界。交错工具按输出项顺序发送。

音频、真实 Anthropic 缓存预热、无法映射的专属工具/字段、会话存储及工具自动执行不在映射范围；不支持的请求明确报错。协议转换不意味着上游模型拥有 Claude 的能力。

## Thinking

参考 `examples/thinking.json`。enabled 预算至少 1024 且小于 max_tokens；≤1024→low、≤8192→medium、≤24576→high、更高→xhigh。adaptive 默认 high；output_config.effort 接受 low/medium/high/xhigh/max，max→xhigh；disabled→none。

配置能力集合时按集合钳制强度；disabled 要求支持 none，否则报错。未配置时交由上游验证，不自动重试其它强度。优先显示摘要，没有摘要时显示可用 reasoning_text，不保证提供隐藏推理。

thinking.display 支持 summarized、omitted、updates。summarized 请求摘要；omitted 隐藏 thinking 正文/增量但保留签名与完整可回传状态；updates 因当前上游无法等价提供 Claude 进度更新，按 omitted 处理并返回兼容提示。普通回答文本不受影响。隐藏模式的载体带 omitted 标记，下一轮空 thinking 可正常恢复原始推理项。

signature 的 `openai-reasoning-v1:` 是本网关载体，保存 ID、摘要、直接推理及 encrypted_content，并非 Anthropic 签名。下一轮提交完整历史可恢复；无加密内容不伪造隐藏状态。SSE 等待最终推理项再发送 signature_delta。不要把载体发送给真实 Anthropic。

## 工具闭环和验证

开发版记录 `POST /v1/messages`、`POST /v1/chat/completions`、`POST /v1/responses`，不记录模型列表、详情或 token 计数。日志由 `logging.requests` 控制，默认目录为 `logs/requests/`；加载配置文件时相对该文件目录，否则相对工作目录。每个请求生成同一 request_id 的四个文件：`_inbound.json/.http` 和 `_upstream.json/.http`。JSON 包含时间、方法、URL、query、完整请求头、解析后的 body、未改写的 raw_body 和实际 body_bytes；`.http` 直接展示请求行、请求头和原始入参。

上游通过 httptrace 捕获传输层实际写出的请求头，包含自动添加的 Content-Length、Accept-Encoding 或 HTTP/2 伪头，header_source 为 transport。若连接失败、尚未发出请求，则保留 prepared 记录。`.http` 为便于查看的文本展示，不是 HTTP/2 帧或 TLS 抓包。转换/鉴权失败仍保留入站记录。认证头、Cookie 和敏感 query 值为 `[REDACTED]`；正文保持原样。日志目录已加入忽略列表。旧版生成的模型列表日志仍保留，新版不再新增。

收到 tool_use 后由客户端执行，将 assistant content 原样加入历史，再提交紧接的 user tool_result；多个调用全部回传，下轮继续提供工具定义。SSE 参数截断时须检查 stop_reason，不能执行不完整 JSON。

请求体上限 16 MiB，普通响应 32 MiB，单 SSE 行/事件 4 MiB；不自动重试可能已执行的请求。

```powershell
go test -count=1 ./...
go vet ./...
go build -o bin/gateway.exe ./cmd/server
```

环境已安装 Python `anthropic` SDK 时，可运行 `python examples/sdk_smoke.py`，使用编译后的网关与本地模拟上游验证真实 SDK 的模型/计数、JSON/SSE、thinking、停止词和多轮工具闭环。无需真实密钥。

测试使用本地模拟上游，不消耗真实 API 额度。真实联调需要有效密钥及支持对应能力的模型。

协议参考：[OpenAI 流式事件](https://developers.openai.com/api/reference/resources/responses/streaming-events)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[OpenAI 输入计数](https://developers.openai.com/api/reference/resources/responses/subresources/input_tokens/methods/count)。


