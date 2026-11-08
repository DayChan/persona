# Android 知识库

本模块提供项目、会话、人、文档、会议、日程六类档案的浏览、标签筛选、全文搜索、详情和打标。项目按重点、进行中、已完成分组，「不是项目」默认收起。搜索按条目归并命中，显示来源和清理过的命中行（查询词加粗），点击进入详情。会话包括私聊和群聊。条目页、打标和「开 Agent」见 `docs/android/android.md`「收件箱的 AI 草拟、交给 AI，知识库的六类、条目页和打标」。

详情支持可选择的 Markdown、最近时间线、关联档案、代码目录及折叠的聊天记录。用户填写问题后点击「问 Persona」，档案和最近时间线以 `<persona-context>` 附到消息中，经 `MainConversationSender.sendToMain` 先持久入队，成功后切到 Persona tab，由发送器在联网后绑定主对话。入队失败保留原操作 ID，重试沿用同一标识。网络排队、结果待核对和重试由现有 data 层处理。

依据为 `docs/assistant/knowledge.md`、`docs/assistant/assistant.md` 的知识库部分、`docs/archive/redesign/parity.md` 知识库一节、`ux.md`、`android.md` 及 `prototypes/android/`。交互参考 `origin/main` 中 `feat(ios): the knowledge base, redone` 的分类标签、项目分组和搜索结果归并。

## 当前 App 接线

App 已依赖 `:feature:knowledge`，Persona 入口跳转 `KnowledgeRoute`，`Root` 注册列表／详情；`AppServices` 随配对和活动路由更新 `HttpKnowledgeRepository`，复用受信客户端、凭据、助手和发送仓储，链接进入应用内阅读浮层。知识库通过 Hilt 取得共用的 `MainConversationSender`，无主对话时也可直接入队；相对附件路径仍无下载契约。

## 范围与验证

没有修改 `android/app/`、iOS 或 Go 服务端。contracts 和 hub 的新增内容均为新文件，settings 只增加本模块的 include 行。文档、会议、日程分类、打标和「开 Agent」在 2026-11-02 的移植里补上（「开 Agent」要外壳接上新建 Agent 的预填入口，见 `knowledgeEntries` 的 `onNewAgent`）。

已通过 `:feature:knowledge:assembleDebug`、`:feature:knowledge:testDebugUnitTest` 和 `:core:hub:test --tests persona.core.hub.KnowledgeRepositoryTest`。两条关键测试覆盖 HTTP 编码与旧响应、凭据不随重定向发送、退役配对拒绝请求、搜索取消和归并、群聊筛选、上下文封装、无主对话入队、入队后跳转与重试消息标识。

构建时需显式给 Gradle 指定已安装的 JDK 21 路径：`-Porg.gradle.java.installations.paths=<JDK 路径>`。当前沙箱不允许 Kotlin daemon 写默认目录，使用 `-Pkotlin.compiler.execution.strategy=in-process` 编译即可，无需修改公共构建配置。单测清理 ViewModel 后需先排空测试调度器，再恢复 `Dispatchers.Main`，避免残留的取消任务访问 Android 主线程。

页面使用 MaterialTheme 和现有设计系统的颜色、按钮与间距。App 已接入，深浅色、链接浮层与完整发送链路仍待真机验收。性能未实测。
