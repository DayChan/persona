# 线上 JSON 样本

这里的“线上”指 hub 实际发送的 JSON 结构，数据全部编造，没有连接线上 hub 或读取真实会话。样本由 [wire_samples_test.go](../../../server/internal/hub/wire_samples_test.go) 导出，供 Android 纯 Kotlin/JUnit 解码测试使用，以后 iOS/XCTest 读同一批文件。

依据是 [hub-changes.md 4.13](../../../docs/archive/redesign/hub-changes.md)、[设计总纲 D1、D4、D5](../../../docs/archive/redesign/README.md) 和 [协议文档](../../../docs/protocol/protocol.md)。客户端期望以当前 iOS 实现和单测为准；设计中的目标行为与现状有差异时，见下文。

## 文件与接口

| 文件 | 接口或事件 | 导出方式 |
| --- | --- | --- |
| [connection.json](connection.json) | WS `hello`、`revs`；GET `/api/v1/health` | 调用 `Hub.hello`、`Hub.health`；revs 使用实际信封结构和 `client.sendJSON` |
| [sessions.json](sessions.json) | GET `/api/v1/sessions`；WS `sessions` | `proto.Session` 经 `agentState`、`markStates` 后，用列表及事件的 map 结构发送 |
| [machines.json](machines.json) | GET `/api/v1/machines`；WS `machines` | 调用 `Machine.snapshot`，再经 `writeJSON` 或 `sendJSON` |
| [items.json](items.json) | GET `/api/v1/sessions/{mid}/{sid}/items` | `proto.Item`、`WithoutImageData`、`FoldWork`；增量和展开页调用 `Hub.sessionItems` |
| [item_events.json](item_events.json) | WS `items`，含订阅后的补发 | 调用 `Hub.itemsToSubscribers`、`Hub.sendBacklog`，读取客户端发送队列 |
| [interactions.json](interactions.json) | GET `/api/v1/interactions`；WS `interaction`、`interaction_resolved` | `proto.Interaction` 使用实际列表或信封结构；屏幕经 `ParseDialog`、`SecretPrompt` 处理 |
| [fleet.json](fleet.json) | GET `/api/v1/fleet`；WS `fleet` | 调用 `buildFleet`，再使用实际快照或信封结构 |
| [inbox.json](inbox.json) | GET `/api/v1/inbox` | `InboxItem.listed` 清理列表中的内部字段，再经 `writeJSON` |
| [feed.json](feed.json) | GET `/api/v1/assistant/conversations/{id}/feed` | `answeredBody` 由 `answeredItems` 等生成，再经 `writeJSON` |
| [errors.json](errors.json) | 未鉴权请求的 401；发送及屏幕操作拒绝的 409 | 调用 `Hub.auth`、`callErr`，同时断言 HTTP 状态 |
| [compatibility.json](compatibility.json) | 上述类型的旧格式、未知字段及异常类型 | 用 Go 编码器构造兼容性输入，交当前 iOS 解码；异常输入不代表 hub 正常输出 |

`wireNode` 通过内存管道提供固定的 `session.items` RPC 响应；hub 仍执行真实的分页包装、图片处理和补发逻辑。所有样本都没有启动完整 hub、node、herdr，不监听端口，也不访问数据库。

时间固定为 `2026-01-02T03:04:05Z`，零时间用 Go 原值；`hello.client_id` 是唯一在生产构造函数返回后替换的随机字段。标识、域名、路径、消息正文均为占位数据；示例核数、内存容量等也是编造输入，不代表实测结果。

## 规则与用例

表中路径均相对仓库根目录，函数和类型名可直接定位；每个 JSON 用例的 `source` 另有具体出处。

| 规则 | 来源 | 覆盖用例 |
| --- | --- | --- |
| hello 与 health 从当前 `proto.Features` 导出能力列表。 | `server/internal/hub/hello.go`：`hello`、`health`；`apple/Shared/LiveConnection.swift`：`ServerEvent.decode` | `hello_features`、`health_features` |
| revs 是会话标识到整数版本号的映射。 | `apple/PersonaTests/SyncEventTests.swift`：`decodesRevs` | `revs_changed_sessions` |
| 会话列表保留模式、队列、后台任务、子任务和循环信息，事件用信封中的机器标识设置会话归属。 | `server/internal/proto/session.go`：`Session`；`apple/Shared/Models.swift`：`Session.init(from:)`；`ServerEvent.decode` | `sessions_list`、`sessions_event` |
| 在线与离线机器使用同一类型，系统信息和版本字段可选。 | `server/internal/hub/machine.go`：`Machine.snapshot`；`apple/Shared/Models.swift`：`Machine` | `machines_list`、`machines_event` |
| items 用 kind 区分内容块，工具输入中的自定义键名原样保留。 | `server/internal/proto/item.go`：`Item`；`apple/Shared/Models.swift`：`Item`；`apple/Shared/JSONValue.swift`：`JSONValue` | `items_all_kinds` |
| 工具调用与结果共用 `tool_use_id`，失败结果携带 `is_error`。 | `proto.Item`；`apple/Shared/Models.swift`：`Item` | `items_all_kinds` |
| 懒加载图片的 `data` 仍是空字符串，并携带尺寸和 lazy 标记，普通图片保留 base64。 | `proto.Image.Placeholder`、`WithoutImageData`；`apple/Shared/Models.swift`：`ImagePayload` | `items_all_kinds`、`items_inline_image`、`items_live_lazy`、`items_http_after` |
| 后台任务通知仍是 notice，task 区分 shell、monitor 和 agent，退出码零值也可显式出现。 | `proto.TaskNote`；`apple/Shared/Models.swift`：`TaskNote` | `items_all_kinds`、`items_task_notifications` |
| fold 保存被折叠条目的计数和终点，展开范围分页使用 more。 | `server/internal/transcript/fold.go`：`FoldWork`；`apple/Shared/Models.swift`：`Fold`；`APIClient.foldedWork` | `items_folded_work`、`items_fold_range` |
| fold 的 tasks 保存折叠掉的任务快照及其原调用位置（source_item_id、source_tool_use_id、source_seq），旧 iOS 忽略这些新增字段。 | `server/internal/transcript/fold.go`：`FoldWork`；`proto.FoldTaskSnapshot`；`apple/Shared/Models.swift`：`Fold`；`docs/protocol/protocol.md`「折叠的工作」 | `items_folded_tasks` |
| 轮次耗时是 `turn_duration` 条目，保留耗时来源和原生关联，实时下发沿用订阅 items 事件，不新增事件或顶层字段；旧客户端把它当未知 kind。 | `server/internal/transcript/transcript.go`：`ParseLine`；`proto.TurnDuration`；`Hub.itemsToSubscribers`；`apple/Shared/Models.swift`：`ItemKind.init`；`ServerEvent.decode` | `items_recorded_turn_duration`、`items_live_recorded_turn_duration` |
| 实时事件的 from_seq 可以是零，空 items 也能推进 to_seq。 | `Hub.itemsToSubscribers`；`ServerEvent.decode`；`docs/protocol/protocol.md`「items 的连续性」 | `items_live_lazy`、`items_empty_advance` |
| 最新页补发携带 has_more，接续补发不携带它，达到补发上限时最后一批带 more_after。 | `Hub.sendBacklog`；`ServerEvent.decode`；`docs/protocol/protocol.md`「WebSocket」 | `items_backlog_latest`、`items_backlog_catchup`、`items_backlog_capped` |
| HTTP 增量页携带 history、from_seq、to_seq 和 more_after。 | `Hub.sessionItems`；`apple/Shared/APIClient.swift`：`NewerResponse` | `items_http_after` |
| question、plan、permission、screen 分别携带相应载荷，question 支持单选和多选。 | `proto.Interaction`；`apple/Shared/Models.swift`：`Interaction`、`Question` | `interaction_question`、`interaction_plan`、`interaction_permission`、`interaction_screen`、`interactions_list` |
| 屏幕对话框携带选项和 rev，密码提示使用 secret 且不记录密码输入。 | `server/internal/screen/dialog.go`：`ParseDialog`；`secret.go`：`SecretPrompt`；`apple/Shared/Models.swift`：`ScreenDialog`、`Interaction` | `interaction_screen`、`interaction_secret` |
| elicitation 交互携带 MCP 服务名和 form（requested_schema）或 url 载荷，只发给声明 elicitation 能力的连接，旧 iOS 收不到。 | `server/internal/hub/http.go`：`Hub.broadcast`、`broadcastTo`；`proto.Elicitation`；`android/core/contracts/src/main/kotlin/persona/core/contracts/Elicitation.kt` | `interaction_elicitation_form`、`interaction_elicitation_url` |
| interaction_resolved 按 id 撤回交互。 | `server/internal/hub/hub.go`：`resolve`；`ServerEvent.decode` | `interaction_resolved` |
| Fleet 保留驼峰键名，时间使用 Unix 秒数字。 | `server/internal/hub/liveactivity.go`：`FleetState`、`buildFleet`；`apple/Shared/FleetState.swift` | `fleet_snapshot`、`fleet_event` |
| 收件箱列表统一承载飞书和邮件，移除 context 等内部字段并保留草拟回复。 | `server/internal/assistant/inbox.go`：`InboxItem.listed`；`apple/Shared/InboxModels.swift`：`InboxItem` | `inbox_list` |
| 回答记录保留一行摘要，`items` 成对保存完整问答并保留换行；旧记录没有 `items`。 | `server/internal/assistant/answered.go`：`answeredBody`、`answeredItems`；`apple/Shared/Tasks.swift`：`FeedItem` | `feed_answered_items` |
| 401 保留字符串 error，409 的 code、delivered、draft、clearable 与 error 平铺。 | `Hub.auth`、`callErr`；`apple/Shared/APIClient.swift`：`APIError.fields(in:)`；`docs/protocol/protocol.md`「发消息」 | `unauthorized`、`conflict_input_draft`、`conflict_dialog_open`、`conflict_prompt_changed` |
| 未知事件忽略，未知键忽略，旧 hello 缺 features 时返回空数组。 | `apple/Shared/LiveConnection.swift`：`ServerEvent.decode`、`Envelope` | `unknown_event_ignored`、`hello_unknown_field`、`hello_missing_features` |
| 旧 items 事件中的 null 转为空数组，缺失的连续性字段保留为空。 | `ServerEvent.decode` | `items_legacy_null` |
| 未知会话状态和 item kind 降级为 unknown，旧会话缺字段时采用 iOS 默认值。 | `apple/Shared/Models.swift`：`SessionStatus`、`AgentState`、`ItemKind`、`Session.init(from:)` | `session_unknown_enums`、`item_unknown_kind`、`session_missing_fields` |
| Fleet 的未知阶段降级为空阶段，未知条目 kind 降级为 screen，错误 stats 单独丢弃。 | `apple/Shared/FleetState.swift`：`init(from:)`；`apple/PersonaTests/FleetStateTests.swift`：`lenientDecoding` | `fleet_unknown_values`、`fleet_bad_optional_stats` |
| 当前 iOS 会丢弃已知信封键类型错误、会话增强字段类型错误或未知交互类型的整条事件。 | `ServerEvent.decode`、`Session.init(from:)`、`InteractionKind` | `hello_wrong_known_type`、`session_bad_optional_field`、`interaction_unknown_kind` |
| Go 零时间可解码，`isMeaningful` 仅接受严格晚于 2000 年起点的日期。 | `apple/Shared/JSONValue.swift`：`JSON.decoder`、`Date.isMeaningful` | `session_zero_time`、`date_at_cutoff` |

## JSON 结构

每个文件是用例数组，UTF-8，2 空格缩进，末尾换行。

- `name`：跨本领域所有文件唯一的英文 snake_case 名称。
- `description`：中文一句话说明。
- `source`：规则出处数组，包含文件和函数、类型、单测或文档章节。
- `from_test`：直接搬用 iOS 单测断言时填写测试名；其余用例标 `derived: true`，两者互斥。
- `decoder`：解码入口，见下表。
- `http_status`：错误样本的 HTTP 状态，Go 捕获响应时已核对；客户端调用时把它作为响应状态。
- `input`：实际 JSON 消息或响应体，可直接交给客户端解码器；无需把整个夹具外壳送入解码器。
- `expected.accepted`：当前 iOS 是否接受这条输入；false 表示整条事件被忽略或解码失败。
- `expected.fields`：解码结果的局部断言，键使用 JSON Pointer，数组下标从零开始；未列出的字段也属于 golden 输入，但没有额外的语义断言。

对照时，将真实模型重新编码成线上键名的对象，再检查 `fields`；WS 事件则把关联值放回相应信封键名。缺失值或 nil 统一映射为 null，不能把 null 与 false、零或空数组混为一谈。日期用 ISO8601 重新编码；`compat_session` 另提供 `updated_at_meaningful`、`seen_at_meaningful`，值来自真实的 `Date.isMeaningful`。这两个键只在对照结果中出现，不是协议字段。

| decoder | iOS 对照入口 |
| --- | --- |
| `event`、`compat_event` | `ServerEvent.decode`，检查事件关联值，nil 对应 `accepted: false` |
| `compat_session` | `JSON.decoder.decode(Session.self, ...)`，另检查日期有效性 |
| `sessions`、`machines`、`interactions`、`inbox` | 解码对应数组中的真实 `Session`、`Machine`、`Interaction`、`InboxItem` |
| `items` | `APIClient.ItemsResponse` |
| `feed` | `APIClient.FeedResponse`（`FeedItem` 数组和计划） |
| `items_after` | `APIClient.NewerResponse` |
| `items_range` | `APIClient.foldedWork` 中的 `Range` |
| `fleet` | 用默认 `JSONDecoder` 解码 `FleetState`，与 ActivityKit 一致 |
| `error` | `APIError.fields(in:)`，返回平铺的字符串字段 |
| `health` | 与 `APIClient.health` 相同的 `ok: Bool` 解码；version、features 是额外的线上 JSON 字段断言 |

Go 测试逐字节比较整份 golden，并重新读取仓库文件，用真实 Go 类型解码正常样本。`compat_*` 专门验证 Swift 的兼容规则，不用 Go 的字符串枚举冒充 Swift 的降级行为。

## 生成与校验

在仓库根目录运行：

```sh
cd server
go test -count=1 -p 1 ./internal/hub -run '^TestWireSamples$'
go test -count=1 -p 1 ./internal/hub -run '^TestWireSamples$' -update
```

第一条只比较，缺文件、内容漂移或多出未登记的 JSON 都会失败；第二条重写本领域全部 JSON。**hub 改了协议就跑 `-update`，检查 diff，并在提交里说明字段和行为变化。** 不要直接编辑生成的 JSON。

新增用例时，在 `wire_samples_test.go` 的相应主题中构造编造输入，优先调用实际处理函数；只有没有可独立调用的路径时，才使用相同的 map 结构和生产编码器。填写出处和 iOS 期望，更新本页规则表，再执行 Go golden 校验和 Swift 对照。若更改兼容性规则，先核对 iOS 真实行为，不能只修改 expected 让测试通过。

本分支导出的是当前代码。其他车道合入 hello、health 或错误体字段后，协调者需要再次 `-update` 并跑解码对照；新增字段若与 `Envelope` 的已知键同名，必须保持原有类型。

## iOS 只读对照

本次全部 47 个用例已由临时 Swift 程序读取 input，调用上表中的真实解码实现并逐项核对 expected；没有只凭单测断言而未执行对照的用例。未修改 `ios/`，也没有启动 App 或联网。

完整编译 `ChatStore.swift`、`LiveConnection.swift` 时，当前沙箱不允许 Observation 宏插件启动。临时程序因此直接引用四个纯模型文件，并把其他文件中需要的完整声明按边界原样摘取到 `/tmp/wire-swift/Referenced.swift`；没有修改声明内的解码逻辑。摘取范围是：

- `LiveConnection.swift` 中的 `TerminalEvent`、`ServerEvent`，包括私有 `Envelope` 和完整 `decode`。
- `ChatStore.swift` 中的 `ItemSpan`、`ScreenParser`、`Character.isCJK` 扩展。
- `APIClient.swift` 中的 `APIError`、`ItemsResponse`、`NewerResponse`、`foldedWork.Range`。
- `InboxModels.swift` 中 APIClient 扩展之前的模型，以及 `LarkModels.swift` 中 APIClient 扩展之前的模型和 `LarkMessages`。

临时程序不提交。本次工作机保留了 `/tmp/wire-extract.py`（按上述边界摘取当前源码）和 `/tmp/wire-swift/Check.swift`（夹具驱动）。在仓库根目录重新执行：

```sh
python3 /tmp/wire-extract.py
swiftc -swift-version 6 -enable-bare-slash-regex \
  -module-cache-path /tmp/wire-swift/cache \
  -o /tmp/wire-swift/check \
  apple/Shared/JSONValue.swift apple/Shared/Models.swift \
  apple/Shared/FleetState.swift apple/Shared/MachineMetrics.swift \
  /tmp/wire-swift/Referenced.swift /tmp/wire-swift/Check.swift
/tmp/wire-swift/check spec/fixtures/wire
```

如果临时目录已清理，按上述声明范围重新摘取，或在支持 Observation 宏的环境直接引用原文件及其依赖，再写一个遍历本目录 JSON 数组的小驱动：依据 decoder 表调用真实模型；把返回值按“JSON 结构”一节投影；核对 accepted 和每个 JSON Pointer；任一不符即失败。不要重写 iOS 解码器。新增解码入口时，临时驱动也要同步扩展。

## 与设计文档的差异

- D5 提出的“未知枚举值降级为 unknown”尚未覆盖所有 iOS 类型：`InteractionKind` 遇到未知值会失败；Fleet 则分别降级为空阶段和 screen。
- D5 提出的“可选增强字段解码失败只丢该字段”只在部分类型中实现，例如 Fleet.stats；Session.background 类型错误仍会使事件失败。
- D5 将早于 2000 年的日期视为空值；当前 iOS 实际先解码成 Date，再用严格大于 `2000-01-01T00:00:00Z` 的 `isMeaningful` 判断，起点本身也无效。
- 协议文档的“所有 JSON 字段 snake_case”有现存例外：Fleet 使用驼峰字段，飞书 parts 保留上游 refId 和 metadata 的键名。

这些用例记录当前行为，避免把尚未落地的设计当成现有契约；本车道没有修改协议或客户端实现。
