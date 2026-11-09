# Android 手机工具

本模块对应 `docs/archive/redesign/android.md` 5.12、`parity.md` 第 21 节和设置中的「手机数据」。提供通讯录搜索、日历查询与新建、当前位置、照片元数据与缩放取图、剪贴板、设备信息、本地通知、健康数据汇总（Health Connect）及 Persona 当前通知列表，共 12 个工具。

Android 没有统一的系统提醒事项接口，故不注册 `phone_reminders_*`。健康数据通过 Health Connect 提供每天的汇总（`phone_health_summary`，安卓 14 起系统自带；写入健康数据 `phone_health_write_sample` 为 iOS 独有）。不申请通知监听权限，不读取其他 App 的通知。`phone_notifications_list` 是 Android 新增工具，只返回本应用当前显示的通知，不包含操作确认通知。

## 和 iOS 对齐（2026-11-02）

hub 把各台手机同名的工具合并成一份定义（`server/internal/hub/hub.go` 的 `mcpTools` 取说明更长的那份），调用却可能发给任一台声明了它的手机（`phoneClient`）。所以除 `phone_notifications_list` 外，每个工具的名字、英文说明、参数（类型、说明、最小 / 最大值）和必填项都照 `apple/Persona/PhoneTools/*.swift` 逐字写（`PhoneToolCatalog`），连 iOS 说明里的「iOS version」「Allow Paste?」也不改。`PhoneToolCatalogTest` 拿 `src/test/resources/ios-phone-tools.json` 逐个比，夹具由 `python3 scripts/assets/export_ios_phone_tools.py` 从 iOS 源码导出（`--check` 只检查是否过期；iOS 独有的提醒事项和写健康数据不导出）。

- 参数照 iOS 读：数字超出范围夹到范围内，不是数字的当没传、用默认值；多余参数忽略；字符串去掉首尾空白，空的当没传（剪贴板写入除外，空字符串就是清空）。必填项由各工具自己检查，缺了回「参数错误 / Invalid argument: title is required」这样的句子。
- 返回结构和 iOS 一样：通讯录 `{query, count, results:[{id, name, nickname, company, job_title, phones[{label, number}], emails[{label, email}], birthday}]}`（没结果带英文 `note`，空查询按通讯录自己的顺序取前几个）；日程 `{start, end, count, truncated, events:[{id, title, start, end, all_day, calendar, location, notes 前 300 字, attendees[{name, role, status, is_current_user}]}]}`，`query` 同时查标题、地点和备注；照片 `{count, access: full|limited, assets:[{id, created, type, width, height, favorite, subtypes:["screenshot"], duration_seconds}]}`（没结果带 `note`，不传 `days` 不限时间）；取图是一段文字（`id, created, width, height, returned_width, returned_height, bytes`）加一个 `{"type":"image","data":…,"mime_type":"image/jpeg"}`；设备信息、剪贴板、通知、定位的键名也照 iOS（Android 用 `android_version` / `sdk` 代替 `ios_version`，定位多带 `provider` 和坐标系说明）。空字段不输出。时间一律是带偏移、到秒的 ISO 8601；只给日期或不带偏移的时间按手机时区读。
- 和 iOS 不同、有意保留的：剪贴板和定位只在 App 在前台时声明和执行（Android 后台读不到剪贴板）；视频取图返回预览帧（iOS 报错）；没有可写日历时建一个本地的 Persona 日历（iOS 报错）。
- Only Deny (or dismissing the dialog) returns the iOS sentence `The user declined on the phone.` (#1875). `PhoneConfirmations.confirm` says how a confirmation ended (`PhoneConfirmation`): nobody answering within 90 seconds, a confirmation that couldn't be shown (in the background with notifications off) and one withdrawn when the tools close each get their own result, never the decline. The phone keeps 90 seconds although the hub waits 30 minutes for a confirmed write, because the registry runs one call at a time and an open confirmation holds back the other calls. Other failures are bilingual as on iOS, with the settings path "Persona › Settings › Connectors › This phone".
- `register_phone` 的 `device_name` 是「厂商 型号」；调用里 `args` 缺省或为 null 当成空对象。

## 当前 App 接线

App 已依赖 `:platform:phonetools`；`AppServices` 按配对及同一 device_id 创建 runtime／transport，将原始 tool_call 回调挂到唯一活动 socket，并转发应用前后台和常驻服务实际状态。`Root` 已注册手机工具页和全局确认浮层，配对结束撤下处理器并关闭 runtime／transport；旧配对缺 device_id 时不供给工具，需重新配对。

工具开关默认关闭并按配对保存，权限变化及执行条件失效会刷新 register_phone；真实授权、后台确认和与 iOS 同时在线的设备路由仍待验收。

## 行为与边界

工具执行前检查开关、权限、在线状态和前台限制；确认后再次检查。日历新建在前台展示完整参数摘要，在后台通过高优先级通知请求确认；允许按钮要求解锁，通知不可用、拒绝、超时或断开连接都不会继续等待授权。通知中的令牌只在本次待确认期间有效，进程重启后失效。

日历优先使用指定的可写日历，其次默认可写日历；完全没有可写日历时创建 Persona 本地日历。指定名称找不到时直接报错，防止误写到其他日历。全天日程以 UTC 日期保存，结束日期不包含在内。本地日历在国产系统日历 App 中的展示尚待真机验证。

照片只读取系统允许的集合，支持 Android 14 部分授权；视频取图返回预览。没有申请媒体位置权限，因此照片元数据不含经纬度。联系人查询和日历返回量均有输入上限，照片缩放后以 MCP image 块返回。

定位使用平台 fused、GPS 和网络提供者，不依赖 Google 服务；GPS 标明 WGS-84，厂商定位标明坐标系待核对，不自动转换 GCJ-02。地址解析不可用时只返回坐标。旧版系统不调用阻塞的 Geocoder 接口。

延迟通知交给 WorkManager，受系统调度限制，不保证准点。已经创建的延迟通知属于用户已安排的本地操作；关闭工具或解除配对不会取消它们。通知投递时重新检查通知开关和渠道。

同一 runtime 内保留最近调用结果，重复 `call_id` 不重复写入；进程退出后不自动重放调用。插入操作发生后如果连接断开，服务端可能收不到结果，应先核对日历再重新调用。

权限行为参照 [Android 部分照片访问](https://developer.android.com/about/versions/14/changes/partial-photo-video-access)，通知确认参照 [Notification.Action.Builder](https://developer.android.com/reference/android/app/Notification.Action.Builder)。本模块运行性能、系统授权弹窗和国产系统后台行为均未实测。

## 验证

单元测试：`PhoneToolCatalogTest`（工具集合、每个工具的说明和 schema 与 iOS 夹具逐字一致、只有新建日程要确认、前台限定的工具、参数读取和夹取、时间解析和确认摘要、照片 id 和截图判断）、`PhoneToolRegistryTest`（开关、权限、前后台、离线、未知工具、超范围不拒绝、重复调用只执行一次、日历的拒绝原句 / 批准 / 批准后状态变化 / 参数错误不打扰用户）、`PhotoContentTest`（图片块用 `mime_type`），`core:hub` 的 `HubPhoneToolsTransportTest`（`args` 缺省或 null 当空对象）。2026-11-02 跑过 `:platform:phonetools:testDebugUnitTest`、`:core:hub:test` 和 `:app:assembleDebug`。

仍需在设备上验证：真实 WebSocket 往返（假 hub 的 `GET /mock/tool_call` 和 `GET /mock/phone`）、授权弹窗、前台确认对话框和后台通知的解锁确认、国产系统日历显示。
