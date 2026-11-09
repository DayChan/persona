# 审批策略表

`InteractionCenter` 应统一查询这张表。输入入口、交互类型和锁屏状态，输出可用动作、计划模式及解锁要求；具体答案文本和表单门禁再由 [作答夹具](../answer/README.md) 判断。两层都允许后才能发送。

## 现状与统一目标

| 入口 | iOS 当前计划批准 | 统一后的批准 | 拒绝／打回的解锁规则 |
| --- | --- | --- | --- |
| 对话页 | `bypassPermissions` 或 `acceptEdits` | 保留两种，全部放行须读过全文，意见非空时两种都禁用 | 进入 App 需要解锁；没有额外生物识别要求 |
| 通知 | `acceptEdits` | 只允许 `acceptEdits`，批准前解锁 | 当前拒绝无需解锁，打回要求解锁；统一后两者都不要求 |
| 锁屏实时活动 | `acceptEdits` | 只允许 `acceptEdits`，批准前解锁 | 拒绝无需解锁；计划没有直接打回按钮，点查看进 App |
| 手表 | `acceptEdits` | 只允许 `acceptEdits`，批准要求认证 | 当前代码未声明动作级认证；统一表将拒绝设为无需额外认证 |
| Siri／快捷入口 | `acceptEdits` | 只允许 `acceptEdits`，批准要求认证 | 这个快捷入口只批准最早的待处理项，没有拒绝动作 |

依据：`docs/archive/redesign/README.md` 2.3、4.2，`docs/archive/redesign/ux.md` 3.4、5.3，`docs/archive/redesign/android.md` 5.8；当前值来自表后规则清单中的 iOS 源码和单测。

设计总纲写“5 个入口用了 3 种模式”，当前源码实际出现的批准模式是上表两种；协议还允许 `default`，当前这些入口没有选择它的按钮。因此夹具不编造第三种现状模式。通知打回的认证要求与 UX 目标冲突，两个版本分别保留，iOS 本轮不改。

所有外部入口的计划批准都收敛到 `acceptEdits`；手表的具体系统认证接法尚未实现。Android 本轮不做手表和语音助手，相关统一策略作为后续契约保留。通知上的“单题单选”按可一次提交完整答案处理，多题／多选需打开 App；这项范围限制避免通知按钮提交半份答案。

## JSON 结构与查询

两个文件均为 `schema_version: 1` 和 `cases` 数组：`ios_current.json` 固定现状，`unified.json` 固定目标；各自覆盖 5 个入口 × 4 种类型 × 锁定／未锁定。每条含唯一 `name`、中文 `description`、出处数组 `source`、`input`、`expected`，来源标记与作答领域一致。

`input` 字段：

| 字段 | 值 |
| --- | --- |
| `profile` | `ios_current`／`unified` |
| `entry` | `conversation`／`notification`／`lock_screen`／`watch`／`siri_shortcut` |
| `kind` | 现有协议的 `plan`／`permission`／`question`／`screen` |
| `locked` | 当前入口所在设备是否锁定；手表指手表自身，不把手机锁定推算成手表锁定 |

`expected.actions` 是动作描述数组，顺序只用于稳定夹具，客户端查询按 `id` 匹配；没有列出的动作不可用。

| 动作字段 | 含义 |
| --- | --- |
| `id` | `approve_all`、`approve_edits`、`approve_stepwise`（对话页的计划：批准，每步确认，mode default）、`reject`、`allow`、`deny`、`always`、`answer`、`screen_answer`、`choose_option`、`open_app`、`dismiss` |
| `mode` | 仅计划批准有值，直接传入 Resolution；其他动作不应带 mode |
| `authentication` | `required` 要求认证或打开已解锁 App；`none` 不要求额外认证；`platform_default` 当前源码未声明；`unspecified` 设计没有明确规定 |
| `requires_unlock` | 锁定且明确要求认证时为 true；已解锁或明确无需解锁时为 false；未知时为 null |
| `available_now` | 是否无需先解锁即可继续动作；与 `requires_unlock` 互补，未知同样为 null |
| `requirements` | 额外内容门禁，需要与作答夹具一起判断；不是请求体字段 |
| `input` | `feedback` 或 `message` 表示支持文本输入，`none` 表示没有文本框；省略时由对应表单决定 |
| `max_options` | 通知选项按钮最多 3 个；其余选项通过打开 App 处理 |

`requirements` 可取 `not_rejecting`、`feedback_empty`、`reason_empty`、`full_plan_read`、`session_present`、`single_question_single_choice`、`plain_dialog_option`、`always_offered`（授权卡是 Claude 的，或者别的 Agent 的卡带了 `always_label`：Codex / TRAEX / Agent 运行时的卡只有在终端界面给出「总是同意」会放行什么时才有这个按钮，iOS `AgentPermissionForm` 同理）。`choose_option` 是待展开的按钮模板，仅在要求满足时按原始选项顺序取前 3 个；文本输入／转回对话选项不冒充普通按钮。`open_app` 是导航，不产生 Resolution；`answer`、`screen_answer` 是打开或操作对应表单，不是批准权限。

null 是明确的未知值，不能当 false 放行：iOS 没显式声明手表的认证要求，不足以证明锁定时能执行。通知上的提问／屏幕选项按钮原来没有定义是否要认证；协调者 2026-10-31 补定：**和「允许」一样要先解锁**（选项会让 Agent 往下做，屏幕对话框的选项常常就是命令审批），只有拒绝和打回不用。统一表已按此填 `required`。

对话页的 `required` 表示锁屏时要先进入已解锁的 App，不意味着 App 内每个按钮再弹一次 Face ID。`available_now` 只回答锁屏维度，填写原因、是否读过计划、请求是否过期等门禁要叠加作答规则。通知和锁屏的 `open_app` 包括点击通知／卡片本身，其他 action 表示按钮。

## 对照与限制

从仓库根目录运行：

```sh
cd server
PERSONA_VERIFY_IOS_FIXTURES=1 go test -count=1 -p 1 -run 'TestResolutionFixture' -v ./internal/proto
```

共用测试位于 `server/internal/proto/resolution_fixtures_test.go`。Go 检查唯一名称、来源、矩阵内动作不重复、解锁与可立即执行的关系，以及统一版外部入口只能使用 `acceptEdits`。临时 Swift 程序执行当前通知、Siri 和各视图的真实 Resolution 构造逻辑，对照批准模式；程序及二进制仅存在临时目录，运行方法详见作答 README。

认证能力没有脱离系统单独实测：通知和锁屏的要求来自 `NotificationActionsTests.sayingYesNeedsUnlockSayingNoDoesNot`、`FleetIntentsTests.sayingYesNeedsUnlock` 的现有断言，测试会检查这些断言仍在；普通按钮与导航来自源码。Siri's unlock requirement comes from the `authenticationPolicy` declaration in `apple/Persona/Intents/ApproveFirstIntent.swift` and the assertion in `IntentsTests.approvingFromSiriNeedsUnlock`; the test checks that both are still there, and the Siri cases' `from_test` names the test for the kinds Siri handles and its plan mode. 手表无对应认证单测，表中用 null，未猜测系统默认行为。

`unified` 是设计规格，当前 iOS 不应被要求通过它；这里没有修改生产实现。通知“打回不用解锁”、原因输入、手表的认证，需要各平台车道接入。MCP `elicitation` 尚未进入现有 Interaction 协议，不加入这张现有类型表；作答夹具明确记录其不可直接提交。

## 规则与覆盖用例

每行同时列出现状和统一目标在锁定／未锁定下的用例。动作及模式的完整期望在 JSON；表中来源列用于回查实现。

| 规则 | 来源 | 覆盖用例 |
| --- | --- | --- |
| 对话页按对应版本选择计划批准模式，并在锁定时遵守认证要求。 | `apple/Persona/Views/Interactions/InteractionViews.swift：PlanForm、PermissionForm、QuestionForm、ScreenForm`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_conversation_plan_unlocked`、`ios_current_conversation_plan_locked`、`unified_conversation_plan_unlocked`、`unified_conversation_plan_locked` |
| 对话页只提供该入口已有的授权动作，统一版允许动作必须满足认证要求。 | `apple/Persona/Views/Interactions/InteractionViews.swift：PlanForm、PermissionForm、QuestionForm、ScreenForm`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_conversation_permission_unlocked`、`ios_current_conversation_permission_locked`、`unified_conversation_permission_unlocked`、`unified_conversation_permission_locked` |
| 对话页通过作答或打开 App 处理提问，通知快捷选项只适用于完整的单选答案。 | `apple/Persona/Views/Interactions/InteractionViews.swift：PlanForm、PermissionForm、QuestionForm、ScreenForm`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_conversation_question_unlocked`、`ios_current_conversation_question_locked`、`unified_conversation_question_unlocked`、`unified_conversation_question_locked` |
| 对话页通过终端作答或打开 App 处理屏幕卡，未规定的认证不得视为免解锁。 | `apple/Persona/Views/Interactions/InteractionViews.swift：PlanForm、PermissionForm、QuestionForm、ScreenForm`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_conversation_screen_unlocked`、`ios_current_conversation_screen_locked`、`unified_conversation_screen_unlocked`、`unified_conversation_screen_locked` |
| 通知按对应版本选择计划批准模式，并在锁定时遵守认证要求。 | `apple/Persona/App/NotificationActions.swift：categories、resolution`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_notification_plan_unlocked`、`ios_current_notification_plan_locked`、`unified_notification_plan_unlocked`、`unified_notification_plan_locked` |
| 通知只提供该入口已有的授权动作，统一版允许动作必须满足认证要求。 | `apple/Persona/App/NotificationActions.swift：categories、resolution`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_notification_permission_unlocked`、`ios_current_notification_permission_locked`、`unified_notification_permission_unlocked`、`unified_notification_permission_locked` |
| 通知通过作答或打开 App 处理提问，通知快捷选项只适用于完整的单选答案。 | `apple/Persona/App/NotificationActions.swift：categories、resolution`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_notification_question_unlocked`、`ios_current_notification_question_locked`、`unified_notification_question_unlocked`、`unified_notification_question_locked` |
| 通知通过终端作答或打开 App 处理屏幕卡，未规定的认证不得视为免解锁。 | `apple/Persona/App/NotificationActions.swift：categories、resolution`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_notification_screen_unlocked`、`ios_current_notification_screen_locked`、`unified_notification_screen_unlocked`、`unified_notification_screen_locked` |
| 锁屏实时活动按对应版本选择计划批准模式，并在锁定时遵守认证要求。 | `ios/LiveActivity/FleetIntents.swift：authenticationPolicy`；`apple/Persona/App/FleetActions.swift：resolution`；`ios/PersonaWidgets/FleetLiveActivity.swift：FleetActionRow、widgetURL`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_lock_screen_plan_unlocked`、`ios_current_lock_screen_plan_locked`、`unified_lock_screen_plan_unlocked`、`unified_lock_screen_plan_locked` |
| 锁屏实时活动只提供该入口已有的授权动作，统一版允许动作必须满足认证要求。 | `ios/LiveActivity/FleetIntents.swift：authenticationPolicy`；`apple/Persona/App/FleetActions.swift：resolution`；`ios/PersonaWidgets/FleetLiveActivity.swift：FleetActionRow、widgetURL`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_lock_screen_permission_unlocked`、`ios_current_lock_screen_permission_locked`、`unified_lock_screen_permission_unlocked`、`unified_lock_screen_permission_locked` |
| 锁屏实时活动通过作答或打开 App 处理提问，通知快捷选项只适用于完整的单选答案。 | `ios/LiveActivity/FleetIntents.swift：authenticationPolicy`；`apple/Persona/App/FleetActions.swift：resolution`；`ios/PersonaWidgets/FleetLiveActivity.swift：FleetActionRow、widgetURL`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_lock_screen_question_unlocked`、`ios_current_lock_screen_question_locked`、`unified_lock_screen_question_unlocked`、`unified_lock_screen_question_locked` |
| 锁屏实时活动通过终端作答或打开 App 处理屏幕卡，未规定的认证不得视为免解锁。 | `ios/LiveActivity/FleetIntents.swift：authenticationPolicy`；`apple/Persona/App/FleetActions.swift：resolution`；`ios/PersonaWidgets/FleetLiveActivity.swift：FleetActionRow、widgetURL`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_lock_screen_screen_unlocked`、`ios_current_lock_screen_screen_locked`、`unified_lock_screen_screen_unlocked`、`unified_lock_screen_screen_locked` |
| 手表按对应版本选择计划批准模式，并在锁定时遵守认证要求。 | `apple/PersonaWatch/PersonaWatchApp.swift：WatchInteractionView.body`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_watch_plan_unlocked`、`ios_current_watch_plan_locked`、`unified_watch_plan_unlocked`、`unified_watch_plan_locked` |
| 手表只提供该入口已有的授权动作，统一版允许动作必须满足认证要求。 | `apple/PersonaWatch/PersonaWatchApp.swift：WatchInteractionView.body`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_watch_permission_unlocked`、`ios_current_watch_permission_locked`、`unified_watch_permission_unlocked`、`unified_watch_permission_locked` |
| 手表通过作答或打开 App 处理提问，通知快捷选项只适用于完整的单选答案。 | `apple/PersonaWatch/PersonaWatchApp.swift：WatchInteractionView.body`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_watch_question_unlocked`、`ios_current_watch_question_locked`、`unified_watch_question_unlocked`、`unified_watch_question_locked` |
| 手表通过终端作答或打开 App 处理屏幕卡，未规定的认证不得视为免解锁。 | `apple/PersonaWatch/PersonaWatchApp.swift：WatchInteractionView.body`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_watch_screen_unlocked`、`ios_current_watch_screen_locked`、`unified_watch_screen_unlocked`、`unified_watch_screen_locked` |
| Siri／快捷入口按对应版本选择计划批准模式，并在锁定时遵守认证要求。 | `apple/Persona/Intents/ApproveFirstIntent.swift：authenticationPolicy、resolution(for:)、perform`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_siri_shortcut_plan_unlocked`、`ios_current_siri_shortcut_plan_locked`、`unified_siri_shortcut_plan_unlocked`、`unified_siri_shortcut_plan_locked` |
| Siri／快捷入口只提供该入口已有的授权动作，统一版允许动作必须满足认证要求。 | `apple/Persona/Intents/ApproveFirstIntent.swift：authenticationPolicy、resolution(for:)、perform`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_siri_shortcut_permission_unlocked`、`ios_current_siri_shortcut_permission_locked`、`unified_siri_shortcut_permission_unlocked`、`unified_siri_shortcut_permission_locked` |
| Siri／快捷入口通过作答或打开 App 处理提问，通知快捷选项只适用于完整的单选答案。 | `apple/Persona/Intents/ApproveFirstIntent.swift：authenticationPolicy、resolution(for:)、perform`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_siri_shortcut_question_unlocked`、`ios_current_siri_shortcut_question_locked`、`unified_siri_shortcut_question_unlocked`、`unified_siri_shortcut_question_locked` |
| Siri／快捷入口通过终端作答或打开 App 处理屏幕卡，未规定的认证不得视为免解锁。 | `apple/Persona/Intents/ApproveFirstIntent.swift：authenticationPolicy、resolution(for:)、perform`；目标见 `ux.md` 3.4、`android.md` 5.8 | `ios_current_siri_shortcut_screen_unlocked`、`ios_current_siri_shortcut_screen_locked`、`unified_siri_shortcut_screen_unlocked`、`unified_siri_shortcut_screen_locked` |

## 新增用例

新增入口或交互类型时，先给现状列出源码证据，再明确统一目标，补齐锁定与未锁定两行；不把“未声明认证”写成无需解锁。修改批准模式时同步作答领域中的 Resolution，并确认外部入口不会发送 `bypassPermissions`。新增内容门禁时说明对应表单如何提供上下文，不能仅凭交互类型假定条件满足。

运行对照命令并更新规则清单。现有实现衍生边界标 `derived`；iOS 单测直接覆盖的断言标 `from_test`；设计新增或覆盖规则标 `design_change`。只使用合成数据，不修改 iOS、Android 或顶层 `spec/README.md`。
