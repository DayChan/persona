<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/banner-dark.zh-CN.svg">
  <img src="docs/assets/readme/banner-light.zh-CN.svg" alt="Persona：你的 SuperAgent，一个入口解决所有问题。" width="100%">
</picture>

[![Persona 视频介绍预览](docs/assets/promo-preview-zh.webp)](#视频介绍)

[English](README.md) · 简体中文

### 一个 SuperAgent，搞定你的所有事。<br>懂你的上下文，指挥你的 Agent，走到哪儿跟到哪儿。

**扩展市场：[Persona Extensions](https://github.com/nwverse/persona-extensions)。** 接入数据源，给助手加工具和技能，给 App 加界面。[一行就能加上](#扩展)

[![client: iPhone](docs/assets/readme/badge-iphone.svg)](docs/apple/ios.md) [![client: Apple Watch](docs/assets/readme/badge-apple-watch.svg)](docs/apple/ios.md) [![client: Mac](docs/assets/readme/badge-mac.svg)](docs/apple/mac.md) [![client: Vision Pro](docs/assets/readme/badge-vision-pro.svg)](docs/apple/vision.md) [![client: Android](docs/assets/readme/badge-android.svg)](docs/android/android.md) [![client: Web](docs/assets/readme/badge-web.svg)](docs/web/web.md)<br>
[![agent: Claude Code](docs/assets/readme/badge-claude-code.svg)](docs/integrations/claude-integration.md) [![agent: Codex](docs/assets/readme/badge-codex.svg)](docs/integrations/codex.md)<br>
[![engine: herdr](docs/assets/readme/badge-herdr.svg)](docs/integrations/engines.md) [![engine: Orca](docs/assets/readme/badge-orca.svg)](docs/integrations/engines.md)

[体验 Demo](#开始使用) · [主要功能](#主要功能) · [扩展](#扩展) · [怎么工作](#怎么工作) · [文档](#文档)

</div>

<br>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/readme/hero-dark.zh-CN.webp">
  <img src="docs/assets/readme/hero-light.zh-CN.webp" alt="iPhone 上的 Persona：从助手切到 Agents，打开正在提问的 Claude，点一下就回答了；两边是收件箱、等你审批的计划、实时进度和机器" width="100%">
</picture>

<p align="center"><sub>体验模式录屏，数据是演示用的。</sub></p>

## 视频介绍

https://github.com/user-attachments/assets/5ef97ffd-f6d8-4136-834c-ffcddbf56846

<sub>[高清视频与字幕](https://github.com/nwverse/persona/releases/tag/promo-v1)。</sub>

## 主要功能

- **SuperAgent，一个入口解决所有问题**。在一个对话里说一句，Persona 带上你的上下文，拆好活，交给 Agent 去做。[助手](docs/assistant/assistant.md)
- **手机和电脑无缝衔接**。Claude Code、Codex 一直跑在你自己的电脑上。手机上实时看、回答、审批，回到桌前接着用同一个会话。[引擎](docs/integrations/engines.md)
- **完整的扩展系统**。工具、Agent 运行时、数据源、渠道、语音都能以扩展接入，有 SDK，也有扩展市场。[扩展](docs/extensions/extensions.md) · [扩展市场](https://github.com/nwverse/persona-extensions)
- **灵动岛、Siri 和快捷指令**。锁屏上一眼看清所有 Agent；用语音、快捷指令或手表提问、审批、派活。[实时活动](docs/apple/live-activity.md) · [Siri](docs/apple/siri.md)
- **知识库和收件箱**。聊天、人、项目沉淀成共享记忆；飞书、邮件、GitHub 汇进一个由 AI 分拣的收件箱。[知识库](docs/assistant/knowledge.md) · [收件箱](docs/assistant/inbox.md)
- **用上手机里的数据**。通讯录、日历、提醒事项、照片、健康、位置都成了工具，写入要先在手机上确认。[手机工具](docs/apple/phone-tools.md)
- **连接器和渠道**。接入飞书、邮件、GitHub、iMessage、Telegram、微信；在 App、飞书、微信里或用语音都能找 Persona。[连接器](docs/extensions/connectors.md) · [渠道](docs/extensions/channels.md)
- **全端客户端**。iPhone、Apple Watch、Mac、Vision Pro、Android 和网页，支持中文、英文、日文。[客户端](docs/apple/ios.md)

## 扩展

<a href="https://github.com/nwverse/persona-extensions/blob/main/README.zh-CN.md#视频介绍"><img src="https://raw.githubusercontent.com/nwverse/persona-extensions/main/docs/assets/promo-preview-zh.webp" alt="Persona Extensions 宣传片：加一行地址，再挑需要的扩展" width="100%"></a>

[Persona Extensions](https://github.com/nwverse/persona-extensions) 是 Persona 的公开扩展市场：接入数据源，给助手加工具和技能，给 App 加界面。

在 App 里打开 **设置 → 扩展 → 扩展市场**。较新的 hub 已经把它列为官方市场；没有的话，用这个地址添加一个市场（不用 GitHub token）：

```text
github:nwverse/persona-extensions
```

然后挑需要的装上，比如：

- **GitHub、Telegram 和 X**：需要你处理的进收件箱，助手也能搜、能读。
- **Home Assistant 和行情**：App 里各多一个页签，管家里的设备和自动化，看自选和价格提醒。
- **Hacker News、LINUX DO 和 V2EX**：在 App 里刷帖、读帖，助手也能帮你查。
- **微信**：在微信里和 Persona 对话。

<sub>[全部扩展](https://github.com/nwverse/persona-extensions/tree/main/extensions) · [视频介绍](https://github.com/nwverse/persona-extensions/blob/main/README.zh-CN.md#视频介绍) · [自己写扩展](docs/extensions/extensions.md)</sub>

## 怎么工作

在一台常开的机器上跑一个小小的 hub，你的设备只连它，它再连到你的每台电脑，Agent 跑在那里的 herdr、Orca 或 Paseo 里。数据留在你自己的机器上。
[架构](docs/server/architecture.md) · [网络与安全](docs/operations/network.md)

## 开始使用

- **先看看**：装上 [TestFlight 公开测试版](https://testflight.apple.com/join/vsjfy3hE)，在配对页点「没有 hub？先体验一下」，不用服务器也不用账号。
- **自己部署**：一行命令装好 hub，再扫个码。看 [开始使用](docs/operations/getting-started.md)。
- **从源码构建**：[配置](docs/operations/config.md) · [部署](docs/server/server.md) · [参与开发](CLAUDE.md)

## 文档

[开始使用](docs/operations/getting-started.md) · [架构](docs/server/architecture.md) · [协议](docs/protocol/protocol.md) · [Docker](docs/operations/docker.md) · [iOS](docs/apple/ios.md) · [Mac](docs/apple/mac.md) · [Android](docs/android/android.md) · [网页](docs/web/web.md) · [全部文档](CLAUDE.md#docs-index)

## License

Persona 采用 [MIT 许可证](LICENSE)。

<sub>第三方许可：[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。</sub>
