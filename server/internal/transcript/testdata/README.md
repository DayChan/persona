# transcript 解析的样本

| 文件 | 来源 | 覆盖 |
| --- | --- | --- |
| `claude-session.jsonl` | 真实 Claude Code 2.1.3xx 会话的几段 | 会话开头（`/clear`、`local_command`、各种 attachment）、并行工具、带图片的用户消息、away_summary、排队消息（queue-operation + queued_command）、中断、带图片的工具结果、`system/turn_duration.durationMs` 及 `parentUuid` |
| `claude-compact.jsonl` | 真实会话的两段 | 后台任务通知、上下文压缩（compact_boundary、isCompactSummary、informational） |
| `claude-notifications.jsonl` | 真实会话 | 同一个后台任务的三次通知（内容不同，都是真事件，不能按 task-id 去重） |
| `claude-subagent.jsonl` | 真实 subagent transcript 的开头 | 全是 sidechain 的行 |
| `codex-0.163.jsonl` | 真实 Codex 0.163 rollout 的几段 | session_meta、response_item（跳过）、Extension（web.search）、McpToolCall、12 个文件的 FileChange、ImageView、compacted / ContextCompaction、turn_aborted、`task_complete.duration_ms` 及 `turn_id` |
| `claude-queued-images.jsonl` | 真实会话的 6 行 | Claude 工作中从 App 发的带图消息：queue-operation + `attachment/queued_command`，prompt 是 [text, image] 块（图片以前被丢掉，气泡里看不到） |
| `claude-background.jsonl` | 两个真实会话的 10 行 | 没写 description 的后台 heredoc 命令（通知里是整条命令、带 `&gt;` `&lt;`）、Monitor 启动（`Monitor started (task …`）、Monitor 事件（只有 task-id）、Monitor 结束（带最后几行） |
| `claude-workflow-notices.jsonl` | 本机真实会话的 2 条完成通知；只读核对后仅保留消息结构，路径、id、时间和内容全换为占位值 | `Dynamic workflow "…" completed` 的 user 消息和 queued_command attachment；保留 task-id / tool-use-id 供卡片跳转。本机没有 failed / was stopped 语料，这两种按 issue 原文写在单测里 |
| `claude-synthetic.jsonl` | 手写（照真实行的结构） | 本机样本里没有的：api_error 重试、/loop 到点（scheduled_task_fire）、同一通知的两处记录、`/model` 的 local_command、continued-in |
| `claude-ultracode.jsonl` | 合成；判词前缀按 #3427，附件字段按 Claude Code 2.1.318 的静态代码核对，没有启动真实会话 | 开 → 两轮 → 关、电脑上切换的 full / sparse / exit、sidechain 过滤、命令与延迟附件只出一次 notice；进程边界另有单测 |
| `codex-turn-failed.jsonl` | 真实 Codex 0.166 rollout 的 6 行（2026-11-05，指向一个只回 503 的本地假服务；基础指令裁短，路径换成 `/work/project/f1`） | 被模型服务打断的一轮：没有 Agent 回复，`task_complete.error` 带 `codex_error_info.http_connection_failed.http_status_code`（错误文字脱敏成了随机词，种类只能从结构化字段来），`turn_error` / `turn_error_kind`（#2185） |
| `claude-usage-limit.jsonl` | Hand-built in the shape of real Claude Code 2.1.282 lines (two sessions waiting on a five-hour limit, read only for their structure): types, subtypes, error fields, `quotaLimits`, `isSidechain` / `isMeta`, timing kept; text, ids, paths and the reset time replaced, notices are Claude Code's fixed strings with a neutral time zone | A usage limit after work, the "continuing automatically" notice, background-task wakes refused again at once with no new notice, a subagent's reply and limit in the session's transcript, the reset notice, the `isMeta` auto-continuation prompt and the resumed work (`turn_error_*` cleared; `TestClaudeUsageLimitWait` checks each step) |
| `claude-api-errors.jsonl` | Hand-built in the shape of real lines (2.1.263 `authentication_failed` 403, 2.1.318 gateway 429 without `quotaLimits`); text replaced | A sign-in failure after a reply (kind user), then a gateway's 429 (`rate_limit` without a quota: no kind, no reset) |
| `codex-usage-limit.jsonl` | Hand-built in the shape of a Codex 0.166 rollout and its `token_count.rate_limits` | A turn that worked, a full 5-hour window, `usage_limit_exceeded`: kind `usage_limit`, reset from the window (not the message's "try again at") |
| `claude-duration.jsonl` / `codex-duration.jsonl` | 手写结构夹具；两种字段均已对照上面的真实脱敏 fixture，Codex 另核对了本机只读 rollout | 显式轮次耗时、原生关联、零值和缺失值；时间戳差故意与记录耗时不同，禁止推算 |

`*.golden.json` 是解析结果（全部 item + 会话级 Meta），`fixtures_test.go` 比对；改了解析就
`go test ./internal/transcript -run Fixture -update`，把 golden 的 diff 当成评审内容逐条看。

## 加样本

1. `python3 scripts/diag/transcript_stats.py` 看本机有哪些类型、在哪个文件哪一行。
2. `python3 -I scripts/diag/redact_transcript.py --words -o testdata/<名字>.jsonl <transcript>:<行号范围>`：白名单式脱敏
   （JSON 结构、像标识符的键、已知键的枚举值、一小组 shell / Claude Code 词保留，其余每个词换成同长度的随机词；
   uuid / 消息 id 重新映射、时间平移到 2026-01-01、路径换成 `/work/project/fN`、IP 换成 192.0.2.x、图片换成 1×1 PNG）。
3. **人工复核再提交**（红线）：`--words` 打印原样保留的词；再 grep 一遍内网域名 / IP、主机名、人名、邮箱、仓库名、token，
   以及所有 JSON 键和枚举值（键是原样保留的）。拿不准的行直接删掉。每份控制在 250 KB 以内。
4. 跑 `-update` 生成 golden，看 diff。
