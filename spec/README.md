# 共享行为规格

这里保存 Android 纯 Kotlin 核心和以后 iOS 核心共用的行为规格与夹具，依据是 [重设计总纲 D1、D4](../docs/archive/redesign/README.md)。Android 用 JUnit 读取；以后 iOS 跟进时，XCTest 读取同一批 JSON。

**行为以当前 iOS 实现及其单测为准。** 文档与实现有差异时，保留现有行为，在领域 README 中说明差异及其来源。更新行为应先更新规格和夹具，再调整各端实现。

## 目录

```text
spec/
  README.md
  fixtures/
    continuity/       对话同步、补洞、缓存和折叠工作
    lines/            对话行
    sort/             排序和分组
    answer/           回复与交互
    terminal/         终端
    wire/             协议解码
    injected/         hub 注入给模型的消息怎么认
    sync/             chat sync scenarios: hub state, frames with faults, what each device shows
    attention/        Persona Link's "Needs you" summary against the web badge
    tokens/           设计 token 的校验夹具（如该领域提供）
  tokens/             设计 token 的来源文件，见 D7
```

各目录由对应车道维护，表中链接可能在相应车道合入后才存在。

| 领域 | 规则与用例索引 |
| --- | --- |
| continuity | [连续性 README](fixtures/continuity/README.md)：连续位置、丢批补洞、历史变化、分页三态、重连、fold 替换与展开 |
| lines | 见各自 [README](fixtures/lines/README.md) |
| sort | 见各自 [README](fixtures/sort/README.md) |
| answer | 见各自 [README](fixtures/answer/README.md) |
| terminal | 见各自 [README](fixtures/terminal/README.md) |
| wire | 见各自 [README](fixtures/wire/README.md) |
| injected | 见各自 [README](fixtures/injected/README.md)：hub 注入给模型的消息（任务通知、定时任务……）怎么认 |
| attention | [README](fixtures/attention/README.md): Persona Link's "Needs you" summary counts what the web app's badge counts, checked by Go and the web app |
| sync | [README](fixtures/sync/README.md): stories of one chat on one or more devices (sends, cards, reconnects, hub restarts, wakes, long chats) with faults, checked at every step by Go, the clients and the end-to-end runs (#1073) |
| tokens | 见各自 [README](tokens/README.md)；校验夹具如放在 `fixtures/tokens/`，结构由该领域 README 说明 |

## 夹具约定

- 夹具放在 `spec/fixtures/<领域>/*.json`，UTF-8、2 空格缩进、文件末尾换行，按主题拆分。
- 每个领域提供中文 README：规则清单中每条规则一句话，附来源和覆盖用例名；说明 JSON 结构、缺省值、比较方法、验证命令及新增用例的方法。
- 每个用例必须有 `name`、`description`、`source`、`input`、`expected`。`name` 是领域内唯一的英文 snake_case；跨领域引用用 `<领域>/<name>`。`description` 是中文一句话。
- `source` 记录可定位的规则出处，例如 iOS 文件和函数、单测名、文档章节、Go 文件。直接取自 iOS 单测的用例增加 `from_test`；依据实现补充的边界用例增加 `derived: true`，并提供实现对照的验证方法。扩展已有单测场景时，保留测试来源并说明增加了什么。
- `input` 和 `expected` 由各领域定义，不依赖 Swift、Kotlin 或 Go 的运行时类型。保留数组顺序及 JSON 中缺失、`null`、`false`、`0` 的区别；哪些区别不影响行为，必须在领域 README 中明确。
- 推荐用 `{"version": 1, "cases": [...]}` 包装同主题用例；采用其他顶层结构的领域必须在自己的 README 中说明。调整现有格式时同步更新消费者，避免静默跳过未知事件或空用例集。
- 所有数据均为编造。消息、标识符、路径、地址、时间都使用明确的示例值；路径用 `/home/user/project`，域名用 `example.com`，IP 用文档地址段，不从真实会话复制内容。
- 有 Go 对应实现时，写测试读取夹具；只有 iOS 实现时，优先在临时目录编译并引用未修改的源文件核对，程序不入库。做不到的部分应逐项列明已有单测断言和验证限制。
- 编写规格的车道不修改 `ios/` 或 `android/`。提交只包含本车道的文件，执行 `python3 scripts/privacy_check.py --staged`，并扫描提交信息。

## 接入与新增

消费者递归发现自己负责领域的 JSON，按文件顺序之外的独立用例运行；每个用例从自己的初始状态开始。事件序列必须逐步比较状态和动作，不能只看最终结果，也不能跳过失败步骤。

新增用例时，先找到当前 iOS 的函数或单测，编造最小输入，再写明确的期望值。更新领域规则索引，运行已有实现的对照及相关测试；不要用待测实现自动生成期望后直接认定通过。

涉及等待的规格应区分调度常量与实测性能。连续性夹具的 `delay_ms: 1500` 是 `AppModel.settle` 的设定值；实际调度延迟未实测，不作为性能结论。
