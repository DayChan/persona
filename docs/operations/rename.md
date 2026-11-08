# Persona naming cutover

代码与界面使用 **Persona**；2026-11-07 的新约定取代 #277 的一次性硬切换方案，原生端保留旧数据读取、显式保存双写和稳定系统身份，支持回装旧版。网络与服务端兼容由关联任务接入，不能从原生端验收推导为全链路已完成。**合并代码不授权部署或发布**；历史[切换操作](persona-cutover.md)也不能因此直接执行。

## Registered application identities remain unchanged

Keep the actual values in private configuration for `app.bundle_id`, `app.app_group`, `android.application_id`, iCloud containers, keychain access groups, and App Store / Google registration. Only the variables that carry them change. Examples in the repository are placeholders, not registration values. Do not register a new app, regenerate keys, or discard pairing credentials.

Provisioning profile display names and service labels are operational configuration, not registered app IDs. Make sure the configured signing profiles exist before archiving on a Mac. The old app's pending notifications, queued workers, saved Shortcuts and caches are not guaranteed to survive renamed class/type/storage identities; test and migrate them explicitly during the maintenance window.

### 可逆的原生端兼容（#6152）

2026-11-07 的约定要求新旧版本兼容和回装，不再做破坏性转换。历史字面量集中在 `apple/Shared/NativeStateMigration.swift`；独立 target 通过 storage-only 条件编译同一物理文件，不复制第二份常量。读取先用有效的新偏好、选择和 Keychain 数据，再回退到旧数据。启动只补齐缺失的新值，显式保存同时写新旧值，不删除旧存储。兼容逻辑不递归重写目录、图片、草稿、发件箱、录音或不透明归档。冲突的推送密钥保留两份身份：新键优先读取，旧键仍可解开经过认证的旧推送。

公开 App Intent 的 Swift 名称和标题可以更新，系统 `persistentIdentifier` 保持稳定；隐藏适配器接纳中间版本保存的新身份。具体类型 extension 中的标识必须直接写成字面量，仍只放在兼容文件。验收分别检查编译后的 metadata、旧版实际保存的 Shortcut，以及保存后回装旧版的结果。不能修改 action blob 来让测试通过。

Control Center 的旧 kind 继续注册，共用同一绘制和操作配方。Live Activity 同时注册旧的真实 attributes 类型和中间版本的新类型；新建和 push-to-start 使用旧 hub 认识的稳定类型。Swift alias 或编译成功不是系统验收，必须检查原位升级后旧活动的实际 ID 和更新结果。Session 快照保留旧 started-by 字段，供旧 App 和手表读取。合并代码不授权部署、线上重启、执行维护 apply 或发布 TestFlight。

<a id="源码位置构建入口与兼容名字"></a>

## Canonical source and build entry points

| Category | Canonical name |
| --- | --- |
| Go module | `github.com/nwverse/persona/server` |
| Hub / node / hook / MCP binary | `personad`, source `server/cmd/personad` |
| Connector / built-in program | `persona-link`, `personad-wechat` |
| Linux services | `personad.service`, `personad.socket`, `personad-node.service`, `persona-link.service` |
| macOS LaunchAgents | `com.persona.hub`, `com.persona.node` (custom connector jobs are inventoried separately) |
| Runtime root / build config | `~/.persona`, `~/.config/persona/config.json` |
| Config override / runtime variables | `PERSONA_CONFIG`, `PERSONA_*` |
| Config / simulator command tools | `scripts/persona_config.py`, `scripts/app/personactl.py`, `PERSONACTL_*` |
| Apple project / modules | `apple/Persona.xcodeproj`, `Persona`, `PersonaWidgets`, `PersonaNotify`, `PersonaLink` |
| Android source / build conventions | `persona.*`, `personaApplicationId`, `persona.android.application` |
| MCP server / tool prefix | `persona`, `mcp__persona__*`, `persona_settings`, `ask_persona` |
| URL scheme / assistant route | `persona://`, `/persona`, `/persona/<id>`, `/persona/new` |
| Message context / file tags | `persona-context`, `persona-file` |
| Header / runtime subprotocol | `X-Persona-*`, `persona-runtime.v1` |
| Default database / container names for new installs | `persona`, `persona_test`, `persona-postgres` |

The `/` front door still opens the main conversation. Previous short assistant URLs are not aliases; previously sent links stop working. The existing avatar data model and `personas/` directory remain. Mochi's built-in ID is `persona` and the bundled frog pack is `nwverse`. The one-time naming cutover preserves old appearance IDs as data, but the five retired bundled choices now display Mochi through the look API's effective-ID fallback; they are not runtime brand aliases. See [Avatars](../apple/persona.md).

## Runtime root: one directory, no automatic migration

The runtime reads only `PERSONA_HOME`, lexically resolves an absolute path, `~/…`, or a path relative to HOME, and otherwise uses `~/.persona`. A query does not create files. Initialization creates that one directory and never creates a compatibility symlink or merges another store. An existing valid custom symlink may be used; a dangling or cyclic link is refused rather than repaired into an empty store. A custom root does not create default-directory aliases.

**Two independent stores** (#744). A machine that has not been through the cutover can still have its older default directory (`~/.<previous-name>`) next to `~/.persona` as a store of its own, not an alias. In that case:

- With `PERSONA_HOME` naming one of them, as a node's or Persona Link's service definition does, initialization uses that one. It never aliases the other over it or merges the two.
- Without `PERSONA_HOME`, initialization (hub, node, Persona Link) refuses: "independent state directories exist". It changes neither directory.
- A client run without that variable (`personad mcp`, `hook`, `launch`, `notify`) resolves the older directory and dials a socket no node listens on.

Agents never run with the node's environment, so the node writes its root into everything it hands them: `PERSONA_HOME` in the `env` of `mcp.json`'s server, in front of the hook command, and in front of the commands it types into a pane (`docs/integrations/engines.md`, "What a start types").

`PERSONA_CONFIG` selects the build/deploy config; otherwise only `~/.config/persona/config.json` is read. The config script emits only `PERSONA_*` build variables and uses the existing configured registration values unchanged. It does not export tokens or bridge environment names.

<a id="部署脚本按服务恢复运行目录"></a>

## Recover the runtime directory per service

`scripts/persona_identity.py --service hub|node|link` reads only the matching canonical service definition. It never borrows a colocated service's root when the selected role has a unit of its own. A `PERSONA_HOME` already present in the environment, including an empty value, takes precedence.

The resolver supports systemd unit resets, sorted drop-ins, `XDG_CONFIG_HOME`, `%h` / `%%`, external `EnvironmentFile` overrides and `UnsetEnvironment`. It extracts only `PERSONA_HOME`, never executes shell text and never returns other credentials. Missing required environment files fail closed. Auto mode rejects different independent roots; explicit role selection avoids ambiguity. `--prepare` creates only the selected directory. `--prefix` resolves the remote root remotely and exports only `PERSONA_HOME`, not the caller's local root.

## Verification boundaries

- Scan tracked file contents and names, including generated schema bundles.
- Run Go build and the full suite against a dedicated real Postgres test database using `PERSONA_TEST_DATABASE_URL`.
- Run the web typecheck, unit tests, production build and available source checks; explicitly report if no lint command is configured.
- Compile and unit-test Android with the available Linux SDK; keep the Health Connect `ViewPermissionUsageActivity` alias, which is a platform permission entry rather than a previous-brand alias.
- On this Linux lane, Apple changes are text-only. Validate plist/YAML/symbol references statically, then build the project, all apps/extensions, tests, AppIntents and signing artifacts on a Mac. Screenshots and real-device upgrade behavior are separate acceptance checks.
- Generate extension wire code centrally with `python3 scripts/gen/extension_schema.py`; `--check` must show no drift. Do not hand-edit generated Swift/Kotlin/Go/TypeScript.
- Rebase onto the latest `origin/main` before the single PR is pushed, rename any newly merged previous-brand additions, rerun affected verification, and scan the merged `origin/main` again.

Historical [acceptance](rename.md) and [audit](rename.md) records describe earlier stages, not authorization to run this incompatible cutover. Previous compatibility guarantees no longer apply to this version. Do not deploy, publish, restart live services, move live data or change live MCP configuration until the user approves the operational plan.
