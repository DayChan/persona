# 收件箱夹具

本目录固定当前 iOS 收件箱的列表行为，供 Android JUnit 和以后接入的 XCTest 共用。来源为 `docs/assistant/inbox.md`「AI triage」「App」、`apple/Shared/InboxModels.swift`、`apple/Persona/Views/Inbox/InboxView.swift`、`apple/Persona/App/InboxStore.swift` 和 `apple/PersonaTests/InboxTests.swift`。分拣模型、采集、发送、数据保留期限不属于本目录范围，输入视为列表已收到的数据。

## 规则与用例

下表的 InboxView、InboxStore、InboxModels 分别指上面列出的完整路径，每个用例也有 source 数组。

| 规则 | 来源 | 覆盖用例 |
| --- | --- | --- |
| tier 缺失、空值或未知时按 action 显示，但任何非空 tier 都算已分拣。 | InboxModels 的 `triage`、`isTriaged`；`InboxTests.decodesItemsAndOpensThem` | `inbox_decode_triage_assertions`、`unknown_tier_is_triaged_action` |
| 只有 done_at 非空才是已处理，read、done_by 或 AI 处理状态都不能替代它。 | InboxModels 的 `isDone`；InboxView 的 `list` | `read_and_agent_do_not_change_tier`、`untriaged_and_done_test_assertions` |
| 非空分段固定按需要你、知悉即可、已折叠、已处理排列。 | InboxView 的 `list`；InboxModels 的 `Tier.allCases` | `tiers_and_done_sections`、`empty_inbox` |
| 默认展开 action 和 fyi，折叠后仍保留成员及标题计数。 | InboxView 的 `expanded`、`sectionHeader` | `tiers_and_done_sections`、`expanded_noise_and_done` |
| hub 输出先按 updated_at 倒序，分拣档位不改变它的排序。 | `server/internal/assistant/inbox_api.go` 的 `Service.inboxList`（收件箱是管理者的，hub 原样转发） | `hub_orders_by_update`、`hub_order_reversed_input`、`hub_order_empty` |
| iOS 未处理事项在段内保持输入顺序，不自行重排 updated_at。 | InboxStore 的 `apply`；InboxView 的 `list` | `input_order_is_authoritative` |
| iOS 已处理事项按 done_at 倒序展示。 | InboxView 的 `shownDone` | `tiers_and_done_sections`、`expanded_noise_and_done` |
| 来源筛选比较 account，飞书的筛选值为 feishu，邮箱的筛选值为账号 id。 | InboxView 的 `matches`、`filters` | `inbox_filter_feishu`、`inbox_filter_mail_account`、`source_filter_compares_account` |
| 来源与类型条件取交集，group 包含 bot，dm 不包含 bot。 | InboxView 的 `matches` | `inbox_filter_group_includes_bot`、`inbox_filter_dm_excludes_bot`、`inbox_filter_mail_kind`、`inbox_filter_mention`、`inbox_filter_incompatible_source_kind` |
| 搜索不区分大小写，匹配 title、sender、preview、summary，不匹配 context 或 reason。 | InboxView 的 `matches` | `search_four_fields_only` |
| 未屏蔽的待处理交互按 created_at 升序放在 action 事项之前，助手交互也参与。 | `apple/Persona/App/AppModel.swift` 的 `pendingInteractions`；InboxView 的 `waitingAgents`、`list` | `waiting_agents_before_items` |
| 交互只在来源不限且类型为全部或 agent 时显示，搜索匹配 sessionTitle、summary 或计算后的 title。 | InboxView 的 `waitingAgents` | `agent_only`、`source_hides_agents`、`kind_hides_agents`、`search_agents` |
| 空间屏蔽可由单会话 allowed 覆盖。 | AppModel 的 `isMuted`；`apple/Shared/APIClient.swift` 的 `Pins.mutes` | `agent_mute_allow_override` |
| 段标题件数包括交互和事项，actionCount 只数所有未处理 action 事项，不受页面筛选影响。 | InboxView 的 `sectionHeader`；InboxStore 的 `actionCount` | `waiting_agents_before_items`、`source_hides_agents`、`inbox_filter_mail_account` |
| 筛选标签当前没有数字，因此 filter_counts 为空对象。 | InboxView 的 `chip` | `inbox_filter_feishu`、`inbox_filter_mail_kind` |
| 有 status（第一期）时按它分段：进行中排在需要你和知悉即可之间、永远展开；进行中的事项不算需要你（actionCount 不数它）；缺 status 或值不认识时按 tier。 | `docs/assistant/inbox.md`「Events and handling chain」；Android `InboxList.kt` 的 `section` | `status_sections_with_working`、`working_never_folds`、`old_hub_without_status` |
| 本任务补充同时间按 id 升序的规则，并保留当前 iOS 的对照结果。 | `CLAUDE.md`「Most common pitfalls」；InboxView 的 `shownDone`；AppModel 的 `pendingInteractions` | `equal_times_use_id_extension`、`equal_times_reversed_input` |

## JSON 结构

文件顶层为 `{schema_version: 1, operation, cases: [...]}`。用例都有英文 snake_case 的 name、中文 description、出处 source、input、expected，并标 `from_test` 或 `derived: true`。所有业务内容与标识均为编造，时间使用 UTC RFC 3339，缺失或 null 的可选值沿用 iOS 解码语义。

| operation | 文件 | 输入与输出 |
| --- | --- | --- |
| `inbox_item_state` | `item_state.json` | 输入 items，输出 id、triage、is_triaged、is_done；sessions 为空，用于共享探针输入结构。 |
| `hub_inbox_order` | `hub_order.json` | 输入无序 items，输出 hub 列表中的 ids，直接以 Go 实现对照。 |
| `inbox_list` | `sections.json`、`filters.json`、`agents.json`、`ties.json`、`status.json` | 输入已加载列表、待处理交互和设置，输出分段、可见行和计数。 |

`inbox_list.input`：

- `items` 是 InboxItem 的 wire JSON，数组顺序对应 InboxStore.items；正常联网列表已由 hub 排序，本地缓存或乐观更新仍以数组为准。
- `sessions` 只用于判断交互所属空间的屏蔽状态，格式与 [Agents 夹具](../sort/README.md) 相同。
- `interactions` 是 pending 交互的投影，含 id、machine_id、session_id、kind、created_at、session_title、summary、title；title 是 UI 已计算的搜索标题，不是新增 wire 字段；此处不测试交互文案生成。
- `mutes` 使用 spaces、sessions、allowed，偏好键格式与 Agents 一致。
- `settings.source` 为 `""`、`feishu` 或邮箱账号 id；`kind` 为 `""`、mention、dm、group、mail 或 agent；query 为空表示不搜索。
- `settings.expanded` 明确列出展开的 action、fyi、noise、done，避免测试依赖 UI 状态初始化。

`inbox_list.expected`：

- `groups` 按实际分段顺序排列，只有非空段；每段有 id、title、count、rows、visible_rows、folded。
- rows 保存该段展开时应有的完整顺序；visible_rows 在折叠时为空，否则与 rows 相同；done 即使折叠也投影其展开后的顺序。
- 行引用为 `{type: "item" | "interaction", id}`，类型分开，不能让事项 id 与交互 id 相互覆盖。
- count 数的是行，不是消息总数，事项自身的 count 不会放大段标题计数。
- `action_count` 精确对应 InboxStore.actionCount，不能把它误当作筛选后的段标题数量或包含所有来源的 App 总角标。
- `filter_counts: {}` 表明当前 UI 没有筛选数字，消费端不能擅自从段计数补出标签计数。

## 同时间排序的兼容边界

`ties.json` 带 `compatibility: "id_tiebreak_extension"`。它的 expected 是本任务明确要求的确定性规则：事项的 updated_at 相同、已处理事项的 done_at 相同、交互的 created_at 相同时，分别按自身 id 升序。其余比较键和段顺序维持当前行为。没有时间戳的普通缓存事项仍保留输入顺序，不把缺失时间伪装成同一真实时间。

这里有实际差异，不能宣称已经与 iOS 全部一致：

- hub 的 `inboxList` 只比较 updated_at，输入来自 map，相等时间的返回次序没有保证。
- iOS 未处理事项保留 hub 或缓存次序，已处理事项只比较 done_at；AppModel 的 pendingInteractions 只比较 created_at，其输入同样来自字典。
- `ios_expected` 记录探针以 input 数组作为遍历顺序时的结果；它能证明缺少 id 比较，但不保证真实字典枚举也产生这一个次序。
- 两个同时间用例的 expected 完全一致，ios_expected 随输入顺序改变；Swift 对照检查 ios_expected，确定性规则另外检查 expected 的 id 次序与反向输入不变性。

Android 的目标断言应读取 expected，以后 iOS 跟进时补上比较键。协调者需将这一差异交给实现车道，当前车道不修改 hub 或 iOS。其余用例均以现有实现的输出为 expected。`docs/assistant/inbox.md` 的「新的在前」由 hub 输出保证，不能据此给 iOS 的缓存列表额外加时间重排。

## 对照与复跑

本目录共 31 个用例：Swift 客户端 25 个，Go hub 3 个，第一期的 status 分段 3 个（`status.json`，按契约写的 expected，只由 Android 验证）；其中两个同时间用例按上面的双结果方式验证。`inbox_decode_triage_assertions` 直接迁移 InboxTests 的分拣及完成状态断言，替换所有内容为编造的数据；其余用例依据实现补齐。

临时程序和命令与 [Agents README](../sort/README.md#对照验证) 共用，按任务约定不提交探针。重建收件箱部分时：

- 原文提取 InboxModels 中的 InboxItem 及嵌套类型，配合 Models 的 MailAddress、SessionKey 和原文件 JSONValue.swift 解码输入。
- 原文提取 InboxView.matches、waitingAgents，以及 InboxStore.actionCount；以输入 sessions 和 mutes 填充 AppModel 的查询适配器。
- 交互只建已列字段的投影，使用 AppModel.pendingInteractions 的原比较表达式排序；不运行真实字典枚举或交互标题生成逻辑。
- 原文沿用 list 的 partition、byTier、doneItems 和 doneAt 比较表达式；按 Tier.allCases 生成段，action 先输出 waitingAgents，再输出事项，最后输出 done 段；expanded 只控制 visible_rows。
- 解码各 case，结构化比较 expected；若有 ios_expected，则对照该字段，不能用目标排序替换 iOS 比较器后再声称一致。
- Go 临时 `package assistant` 测试（收件箱 2026-11-04 起是管理者的）读取 hub_order.json，逐用例创建 Service 并调用 inbox.init，把 input.items 放入 inbox.items，直接调用 inboxList，将返回 id 数组与 expected.ids 比较；无数据库或网络请求。

本次 Swift 客户端对照及 Go overlay 测试全部通过。SwiftUI 屏幕、真实 AppModel 字典同时间枚举次序未测试；排序、匹配和状态方法在 `/tmp` 编译执行，页面条件由数组适配层验证。这一验证层级与真实 App UI 测试分开记录。

## 新增用例

按主题追加用例并更新规则表，保证两个领域的 name 唯一。先编造输入并独立写 expected，再用真实实现对照；未知档位、空列表、交叉筛选、屏蔽例外和时间相等应分别覆盖。发现当前实现与新规则冲突时，明确标 compatibility 和 ios_expected，并写明谁需要跟进，不能悄悄改 expected。提交前检查 UTF-8、2 空格缩进和隐私扫描。

## Hand to AI memory (`handoff_memory.json`)

Hand to AI remembers, per source, the target an item was last handed to (#2121; `docs/assistant/inbox.md` "Item actions"). Every app follows the same rules:

- `keys`: the source key is the item's `source`, `|`, and the most specific non-empty mute option, in the order `repo`, `chat`, `sender` (`tag:value`). An item without one has no key and no memory.
- `resolve`: a remembered target is preselected and marked Last time.
  - A remembered Agent that is no longer listed resolves to the main chat, with `gone` set so the app says so once.
  - Nothing remembered, or an unreadable value, gives `null`: the platform's own default.
  - Cases with `needs_new_chat` apply only where New Chat is a target.
- `recency`: one entry per source, latest first, at most `limit` sources.
