# Free 独立模块

本模块属于 New API，保留项目原有身份与许可证。包含独立后端规则、管理员设置页、用户额度与排队面板、五套预设、审计与回滚。Docker 通过 fork 的 GitHub Actions 构建并发布到 GHCR；本次按用户要求只保留编译，不设置测试或冒烟发布门槛。

## 模块边界

- `free` 是保留的令牌入口标识，不是用户的传统分组，不写入分组倍率列表。
- 初始配置 `enabled=false`、模型白名单开启且为空、排队关闭。没有预置模型、渠道或推荐策略。
- 渠道匹配仍走已有 ability、model mapping 和渠道约束机制；管理员自行配置支持 `free` 的渠道和模型。
- 只有通过独立资格检查和执行准入的请求才能跳过付费预扣与结算。单独填写 `group=free` 不能取得计费豁免。
- 模块停用时保留原有令牌，拒绝其请求；用户侧状态接口返回不可见状态。
- Redis、数据库或 Lua 失败时 Free 拒绝请求；管理员关闭某一项限制是独立的显式策略，不等同于组件故障。

核心文件：`model/free_group.go`、`service/free_group*.go`、`controller/free_group.go`。接入点见 `middleware/auth.go`、`middleware/distributor.go`、`relay/request_billing.go`。

## 1Panel 部署与模型选择

Free 的并发、排队和额度依赖 Redis。普通 New API 可以不配置 Redis，但这不代表 Free 能在相同部署条件下运行。旧版笼统的 `free group concurrency service is unavailable` 在未配置 Redis 时就会出现，与模型是否选对无关。

1. 在 1Panel 安装 Redis，或使用已经部署的 Redis。
2. 在 **New API 容器**的环境变量中添加连接串，然后重新创建或重启该服务以加载环境变量：

   ```text
   REDIS_CONN_STRING=redis://default:<URL编码后的密码>@<Redis连接地址>:6379/0
   ```

   使用 New API 容器能够访问的 Redis 地址。同一 Docker 网络可使用 Redis 的容器名；不要把另一个容器写成 `127.0.0.1`。端口以实际 Redis 配置为准，密码中的特殊字符需要 URL 编码。不要把连接串或密码粘贴到公开日志或截图中。
3. 打开「免费分组设置」。页面会明确提示 Redis 是否配置，以及配置是否已激活。缺少 Redis 时仍可保存设置，显示「等待生效」，不会绕过保护放行免费请求。
4. 到现有「渠道」页，编辑准备供免费入口使用的渠道，在分组中添加 `free`，确认相应模型已配置、渠道已启用。可以先配置渠道，再开启 Free。
5. 在 Free 的「模型」区域，从「免费可用模型」选择框多选模型并保存。**无需填写 JSON、渠道名或倍率**。候选来自已有启用渠道中的模型，不会自动添加模型或改变渠道路由。

「渠道已配置」表示该模型存在启用的 `free` 渠道记录；「未配置免费渠道」会提示前往渠道页处理。这是配置检查，不是上游连通性保证。默认开启模型限制且没有选中模型时，免费请求会被拒绝；页面会直接说明原因。关闭模型限制或放行未选模型仍需要高风险确认。

配置页复用 `SettingsPageFrame` 的页头保存/重置按钮、`SettingsCard`、`SettingsAccordion`、`SettingsSwitchField` 和已有 `MultiSelect`。每次展开一个规则分区，使用量、审计、历史版本和成员操作分别位于各自标签页。保存只计算差异与风险，不再强制扫描用户；「预览影响面」由管理员按需点击。

## 配置结构

配置为完整 JSON 文档。缺省值由 `model.DefaultFreeGroupConfig()` 提供；管理接口解析时先填充默认值。保存使用 `version` 乐观锁，冲突返回 409，不能覆盖别人已提交的修改。

下表路径相对于 `config`。普通整数保存范围为 `0..2147483647`；开启计数限制时通常必须为正。维度并发为 `1..1000000`，队列长度为 `1..10000`，等待为 `1..3600` 秒，TTL 为 `1..86400` 秒。具体互斥与范围检查以 `ValidateFreeGroupConfig` 为准。

| 配置路径 | 初始状态 / 参数 | 执行语义 |
| --- | --- | --- |
| `enabled` | 关 | 控制全部 Free 入口 |
| `access_mode` | `all` | `all / condition / apply / invite` |
| `blacklist.enabled / user_ids` | 开 / 空 | 独立黑名单，优先于资格模式 |
| `blacklist.recovery` | `manual` | 自动移出的恢复：`auto / manual` |
| `admission.registration` | 开 / 24 小时 | condition 模式的注册时长 |
| `admission.bindings` | 开 / `[email]` | condition 模式要求全部选中绑定 |
| `admission.captcha` | 关 | condition/apply 可使用既有 Turnstile 校验 |
| `admission.ip_accounts` | 开 / 3 / 3600 秒 | condition 模式按活跃 IP 账号数移出 |
| `admission.fingerprint_accounts` | 关 | condition 模式指纹账号数；启用需配置正上限和窗口 |
| `models.whitelist_enabled / models` | 开 / 空 | 唯一 Free 模型白名单；关闭需高风险确认 |
| `models.outside_policy` | `reject` | 可显式改为 `allow`，属于高风险放行 |
| `models.per_model_quota_enabled` | 关 | 启用模型日请求/token 覆盖值 |
| `models.per_model_concurrency_enabled` | 关 | 启用模型并发覆盖值 |
| `models.per_model_limits` | 空 | 模型名映射到 `daily_requests / daily_tokens / concurrency`；正值覆盖对应默认限制 |
| `concurrency.user` | 开 / 2 | 单用户维度 |
| `concurrency.token` | 开 / 2 | 单令牌维度 |
| `concurrency.ip` | 开 / 5 | 单 IP 维度 |
| `concurrency.model` | 开 / 10 | 单模型维度 |
| `concurrency.global` | 开 / 50 | 全局 Free 维度 |
| `concurrency.user_share` | 开 / 10% | 以已配置全局并发为分母，至少一个名额 |
| 上述各维度 `.strategy` | `reject` | `reject / queue`，各自可配置 |
| 上述各维度 `.max_wait_seconds / max_queue_length` | 30 / 100 | 只在对应维度阻塞且选择 queue 时施加 |
| `concurrency.lease_ttl_enabled / lease_ttl_seconds` | 开 / 90 秒 | 名额超时兜底；关闭属于高风险变更 |
| `concurrency.renew` | 开 / 30 秒 | 活跃请求续租，失败取消请求 |
| `queue.enabled` | 关 | FIFO 排队总开关 |
| `queue.max_length / max_wait_seconds / poll_millis` | 100 / 30 / 250 | 全局队列边界，轮询 50..5000 毫秒 |
| `rate.rpm` | 开 / 3 / 突发 5 | token bucket，请求数 |
| `rate.tpm` | 开 / 2000 / 突发 2000 | token bucket，预留 token 数 |
| `rate.hourly_requests / hourly_tokens` | 关 | 每小时请求/token 上限 |
| `rate.daily_requests` | 开 / 20 | 自然日请求数 |
| `rate.daily_tokens` | 开 / 20000 | 自然日 token 数 |
| `rate.monthly_requests / monthly_tokens` | 关 | 自然月请求/token 上限 |
| `rate.timezone` | `Local` | 服务器本地或有效 IANA 时区；自然日考虑 DST |
| `rate.idle_credit` | 关 | `accrual_per_hour / maximum`；只增加每日请求信用，不增加 token 额度 |
| `request.max_tokens` | 开 / 512 / `reject` | `reject / truncate`；支持多种协议字段 |
| `request.prompt_tokens` | 关 | 输入 token 上限 |
| `request.body_size_kb` | 开 / 64 | 可重放请求体总大小 |
| `request.parameters` | 关 / 温度 0..2 | 温度及 `ranges` 中逐字段范围 |
| `request.streaming` | 开 / 允许 | 关闭 guard 即不限制流式 |
| `request.multimodal` | 开 / 不允许 | 输入媒体、文件引用、媒体输出声明和媒体端点 |
| `request.timeout_seconds` | 开 / 60 | 超时即取消并释放，不等待上游退出 |
| `request.output_input_ratio` | 关 | 总候选输出 / 输入 token 比例 |
| `anti_abuse.ip_active_accounts` | 开 / 3 / 1 小时 / alert | 同 IP 活跃账户 |
| `anti_abuse.fingerprint_active_accounts` | 开 / 3 / 1 小时 / alert | 同指纹活跃账户；无指纹时不应用此可选观察规则 |
| `anti_abuse.ip_aggregate_rate` | 关 / slowdown | 跨账号 IP 聚合频率 |
| `anti_abuse.prompt_hash_repeat` | 开 / 20 / 1 小时 / alert | 同用户 prompt 哈希重复 |
| `anti_abuse.machine_interval` | 关 / alert | 间隔方差与样本数 |
| `anti_abuse.night_high_frequency` | 关 / observe | `start_hour / end_hour` 与频率 |
| `anti_abuse.short_prompt` | 关 / observe | `prompt_token_minimum` 与次数 |
| `anti_abuse.failure_rate` | 开 / 20 / 5 分钟 / slowdown | 连续失败；成功响应清除失败计数 |
| `anti_abuse.request_retention` | 开 / 30 天 | IP、UA、指纹哈希、prompt 哈希与用量，不存 prompt 原文 |
| `anti_abuse.actions.observe / notify` | 开 | 独立动作阈值和窗口 |
| `anti_abuse.actions.slowdown / cooldown / remove` | 关 | 独立动作阈值和窗口；slowdown 有间隔参数 |
| `penalty.cooldown_on_limit` | 开 / 连续 5 次 | rate 或 concurrency 超限；执行准入成功清除连续超限计数 |
| `penalty.cooldown_minutes` | 开 / 10 分钟 | 可独立关闭冷却 |
| `penalty.exponential / multiplier / maximum_minutes` | 开 / ×2 / 240 分钟 | 递增及封顶 |
| `penalty.reject_during_cooldown` | 是 | 冷却期间直接拒绝 |
| `penalty.whitelist_enabled / whitelist_users / whitelist_ips` | 开 / 空 / 空 | 只豁免惩罚，不绕过其他限制 |
| `notify.webhook_enabled / webhook_url` | 关 / 空 | 使用已有 SSRF 防护通知客户端 |
| `notify.email_enabled / site_enabled` | 关 / 关 | 邮件发给超管；站内通知为可检索审计项 |
| `notify.user_limit_display` | 开 | 用户限额展示开关 |
| `notify.error_template` | 默认英文模板 | 支持 `{{rule}} / {{current}} / {{limit}} / {{retry_after}}` |

每个反滥用规则有自己的 `enabled / threshold / window_seconds / action`；相应动作也必须开启。关闭规则或动作后，原来由它产生的自动惩罚停止生效；手工冷却独立保留。

## 持久化与热更新

权威模型及迁移入口在 `model/free_group.go` 与 `service.InitFreeGroupConfig()`，由 GORM 为 SQLite、MySQL、PostgreSQL 生成对应 DDL。没有手写数据库专属 JSON 字段或主键自增语法。

| 表 | 键与主要字段 | 用途 |
| --- | --- | --- |
| `free_group_config_records` | 主键 `id=1`；`version, preset, config(TEXT), updated_by, updated_at` | 当前配置 |
| `free_group_config_versions` | 主键 `id`；唯一索引 `version`；`config(TEXT), preset, updated_by, created_at, summary, high_risk` | 不可变版本快照 |
| `free_group_members` | 主键 `id`；唯一索引 `user_id`；`status, source, reason, updated_by, created_at, updated_at, captcha_until, expires_at` | 申请、邀请、黑名单与人机验证状态 |
| `free_group_audits` | 主键 `id`；`user_id, ip, fingerprint, rule_name, threshold, snapshot(TEXT), action, source, config_version, created_at` | 配置相关操作、拦截、处置与请求留痕 |

保存当前版本和历史快照在一个事务内完成。回滚读取旧快照并产生新的递增版本，历史不会被覆盖。名单修改和审计同样在事务内完成。

主节点迁移/初始化，工作节点只读取；Free 初始化失败不阻止普通分组启动，后台每 30 秒重试。配置写入后更新内存与 Redis 版本标记，Pub/Sub 广播，其他节点还有 1 秒轮询兜底。

数据库提交成功但 Redis 激活失败时，响应包含 `effective=false, activation_pending=true` 及已提交版本。界面必须展示待激活，不能误报“已生效”。请求读取内存配置，不逐请求读取配置表；资格/名单仍需要数据库校验。

## 执行顺序

```mermaid
flowchart LR
  A[现有认证与令牌有效期] --> B[总开关与资格]
  B --> C[冷却状态]
  C --> D[模型白名单]
  D --> E[单请求校验及 token 计算]
  E --> F[原子频率与额度预留]
  F --> G[原子多维名额或 FIFO 排队]
  G --> H[现有 relay 与渠道机制]
  H --> I[独立用量结算与名额释放]
```

冷却预检独立读取一次；限流/额度预留一次 Lua，并发检查与占位一次 Lua。成功请求的“限流 + 并发”合计两次 Redis 往返；排队期间会按配置轮询，续租和最终清理另计。rate Lua 再次检查惩罚，防止预检后并发修改造成绕过。

并发 Lua 先检查全部已开启维度，只在全部可用时统一占位；失败不产生部分名额。`defer`、panic 路径与请求取消都清理名额。超时取消有独立回调，不依赖上游返回。

未写输出上限且有 token 额度约束时，原子预留剩余额度并设置额度导出的输出预算；并不要求管理员重新开启 max_tokens 限制。`n`、候选数、批量 completion prompt 的所有可能输出合并预留；有实际用量则结算，没有可信用量则保留预留值。排队失败或取消且未进入 relay 时退回预留。

## Redis 模板

统一前缀 `P = new-api:{free-group}:v1`，`{free-group}` 保证跨维度 Lua 的 key 位于同一 Cluster slot。IP、模型及 prompt 等敏感或不适合直接作为 key 的值经过 SHA-256 编码。不要在共享生产 Redis 上直接使用通配删除。

| 模板 | 类型 | 过期/回收 | 用途 |
| --- | --- | --- | --- |
| `P:config` | hash | 持久 | 当前版本与 enabled，Lua 防过期配置 |
| `P:config:changed` | Pub/Sub channel | 无存储 | 配置广播 |
| `P:rate:<user>:rpm/tpm` | hash | 桶恢复时间 + 1 小时 | token bucket |
| `P:rate:<user>:<window>:<start>` | hash | 窗口结束 + 24 小时 | 请求与 token 额度 |
| `P:rate:<user>:model_requests/model_tokens:<model_hash>:<day>` | hash | 当日结束 + 24 小时 | 模型覆盖额度 |
| `P:usage:user:<user>:requests/tokens:<day>` | hash | 当日结束 + 24 小时 | 用户看板，限制全关也计数 |
| `P:usage:global:requests/tokens:<day>` | hash | 当日结束 + 24 小时 | 全局看板 |
| `P:credit:user:<user>` | hash | 满信用累积时长 + 48 小时 | 空闲请求信用 |
| `P:reservation:<uuid>` | hash | 24 小时；活跃心跳续期 | 幂等预留和结算；崩溃后不永久堆积 |
| `P:concurrency:<dimension>:<identity>` | zset | 每个名额独立 expiry score；key 保留 TTL + 24 小时 | 原子名额、续租、过期回收及泄漏计数 |
| `P:queue:ticket:<user>:<uuid>` | hash | 排队等待 + 60 秒；已准入 60 秒 | 状态及取消标记 |
| `P:queue:order` | zset | 24 小时 + 等待时间 | 全局严格 FIFO 序号 |
| `P:queue:deadlines` | zset | 同上，票据独立 deadline | 回收崩溃队头 |
| `P:abuse:<rule_scope>` | hash | 对应规则窗口 | 次数、活跃账号、样本和动作命中 |
| `P:penalty:user/ip/fingerprint:<identity>` | hash | 惩罚窗口 + 24 小时 | 冷却、降速、连续超限 |
| `P:metrics` | hash | 持久 | 维度拒绝、过期回收及队列序号 |

关闭名额 TTL 会让名额持久化，属于管理员显式高风险选择。租约泄漏计数在下一次对应维度申请时回收并增加；不是独立实时扫表进程。

## 排队交互

总开关 `queue.enabled` 默认关闭。启用后还需把允许排队的各维度 `.strategy` 设为 `queue`。同时受总队列和被阻塞维度的长度、等待上限约束，取更严格的值。

调用方可生成 UUID 放入 `X-Free-Queue-ID`，再查询当前名次、剩余等待或主动取消。票据绑定登录用户，不能取消其他人的请求。HTTP/SSE 请求在获得名额后再开始正常协议输出，不插入非标准 OpenAI 事件；客户端应预先持有 UUID，以便等待响应头时也能查询排队状态。

TRAE 类交互在这里指“显示等待位置、等待剩余时间、可取消、按序准入”。令牌页中的“我的免费额度”面板显示本人排队位置、剩余等待时间和取消按钮，每 3 秒刷新。

## 管理与用户接口

管理端统一在 `/api/free-group`，使用现有 `RootAuth`；用户接口在 `/api/user/free-group`，使用已有会话认证。

| 方法与路径 | 用途 |
| --- | --- |
| `GET /api/free-group/config` | 当前配置、defaults、版本、修改人/时间、实际生效版本 |
| `PUT /api/free-group/config` | `{version, config, summary, confirm_high_risk}` |
| `GET /api/free-group/config/versions` | 历史版本分页 |
| `GET /api/free-group/config/versions/:version` | 版本配置内容，供 diff/回滚预览 |
| `POST /api/free-group/config/versions/:version/rollback` | `{expected_version, confirm_high_risk}` |
| `GET /api/free-group/dashboard` | 今日量、占用、队列、拒绝和封禁用户 |
| `GET /api/free-group/audits` | 时间、用户、IP、指纹、规则过滤，分页最大 200 |
| `GET /api/free-group/members` | 资格名单及审批状态 |
| `PUT /api/free-group/members/:user_id` | `{status: approved/pending/blocked, reason}` |
| `DELETE /api/free-group/members/:user_id` | 移出独立名单并写审计；重新按当前准入模式判断 |
| `POST /api/free-group/penalties` | 用户/IP/指纹的手工冷却或解封，需理由 |
| `GET /api/user/free-group` | 可见性、资格、可用模型及独立用量 |
| `POST /api/user/free-group/apply` | 提交申请，不能覆盖管理员拒绝 |
| `POST /api/user/free-group/verify` | 使用既有人机校验能力 |
| `GET/DELETE /api/user/free-group/queue/:queue_id` | 查询 / 取消自己的队列票据 |

Free 拒绝响应使用 `error.type=free_group_error`，`error.details` 带 `rule/current/limit/retry_after/reset_at/recovery`。配置性拒绝无固定恢复时间时，`reset_at=0` 且 `recovery` 给出恢复动作，避免伪造恢复时间。

## 前端线框与可复用资产

当前分支实际使用 React 19、Base UI、shadcn/base-nova、Tailwind 4，不使用 Semi。`web/components.json` 和 `web/package.json` 是来源；下列资产在 Free 页面中按现有骨架组合，不增加 UI、图标、CSS 或字体依赖。

```text
免费分组设置                                  保存 / 预览影响
总开关  当前预设  配置版本  修改人/时间  历史与回滚
全局占用卡片 | 今日请求 | 今日拒绝 | 封禁用户
仅看已开启项
准入 / 模型 / 并发与排队 / 频率额度 / 单请求 / 反滥用 / 惩罚 / 通知
  每项：开关 + 参数 + 默认值 + tooltip；关闭后原生 disabled
审计筛选 / 日志表格 / 分页 / 手工处置
保存 diff 对话框；宽松/高风险变化使用危险确认

令牌抽屉：在现有分组选项中插入合资格 Free 选项和额度说明
我的免费额度：沿用现有 Card/StatCard/EmptyState 显示独立用量
```

| 配置/交互 | 优先复用路径 | 说明 |
| --- | --- | --- |
| 全页骨架 | `web/src/components/layout/components/authenticated-layout.tsx`、`section-page-layout.tsx` | 复用管理员/用户认证布局、页头、动作区和断点 |
| 参考设置页 | `web/src/features/system-settings/auth/bot-protection-section.tsx` | 表单、校验、开关联动模式 |
| 分区卡片、折叠 | `web/src/features/system-settings/components/settings-card.tsx`、`settings-section.tsx`、`settings-accordion.tsx` | 不另造设置页美术体系 |
| 开关、参数布局 | `web/src/features/system-settings/components/settings-form-layout.tsx`、`web/src/components/ui/form.tsx`、`switch.tsx`、`input.tsx` | 复用 `SettingsSwitchField` 等组合及标准 disabled |
| 模型、绑定多选 | `web/src/components/multi-select.tsx` | 搜索、多选，结合已上架模型数据 |
| 单项说明 | `web/src/components/ui/tooltip.tsx` | 现有说明交互 |
| 危险确认、回滚 | `web/src/components/confirm-dialog.tsx` | 使用 `destructive`，不自行拼确认弹窗 |
| 保存 diff、详细信息 | `web/src/components/dialog/` | 现有通用弹窗布局加业务内容 |
| 统计看板 | `web/src/features/dashboard/components/ui/stat-card.tsx` | 复用统计卡片，图标遵循其现有类型接口 |
| 审计表格、检索、分页 | `web/src/components/data-table/`、`web/src/features/usage-logs/audit/` | 先用公共入口，沿用工具栏和分页 |
| 空/加载/错误状态 | `web/src/components/empty-state.tsx`、`loading-state.tsx`、`error-state.tsx` | 不重写占位状态 |
| 用户令牌表单 | `web/src/features/keys/components/api-keys-mutate-drawer.tsx` | 在既有抽屉中组合 Free 状态 |
| 路由、菜单 | `web/src/routes/_authenticated/`、`web/src/components/layout/config/system-settings.config.ts` | 独立路由及仅超管菜单，保留后端权限校验 |
| 主题 | `web/src/styles/theme.css`、`theme-presets.css`、`index.css` | CSS 变量及已有语义化 Tailwind token |
| 图标 | `web/components.json` 的 Hugeicons；既有统计卡片的 Lucide 接口 | 只使用已安装图标能力 |
| i18n | `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` | 英文原文扁平 key；使用项目 i18n-translate 流程 |
| 数值、日期 | `web/src/lib/format.ts`、`web/src/i18n/languages.ts` | 显示 token/次数，界面语言先转换为 Intl locale |

需要新增的仅是 Free 业务组合组件：配置编辑器、diff 内容、审计列定义、用户额度/排队面板。它们承载本模块数据和动作，底层控件复用上表资产。尚未新增通用 UI 组件或样式资源。

## 本阶段明确边界

1. HTTP 文本生成与 Responses WebSocket 已接入独立限制；Realtime 音频及 UnrealSpeech WebSocket 在握手前明确拒绝 Free，尚无逐消息计量。
2. 不透明异步任务和无法约束输出 token 的媒体端点，开启 token 类保护时明确拒绝，不把原始 JSON 长度冒充真实用量。使用这些端点需管理员显式关闭相关 token 保护；请求次数、并发、模型和其他适用限制仍独立执行。异步任务完成后的独立 token 计量尚未实现。
3. 当前用户模型没有手机绑定字段，配置校验拒绝不支持的 phone 要求，界面仅列出已有绑定能力。指纹来自客户端声明，只能作为风控线索，不能当成可信设备身份。
4. 邮件/webhook 通知使用有界异步投递，饱和时保留审计并计错；暂没有可靠 outbox 重试。站内通知目前记录为审计项。
5. 用户面板分别显示默认额度和逐模型覆盖额度；影响面预览仅对前 1000 个账号采样估算资格变化，不预测未来用量、IP 与指纹。
6. 全库发布版升级与重复启动检查已完成；按用户后续要求，不再追加测试。完整付费回归及所有开关的逐项验收未完成，不以编译通过代替这些结论。

## 验证记录

运行环境：Go 1.26.2 linux/arm64；Redis 7.0.15；MySQL 8.0.46；PostgreSQL 16.15；SQLite 使用项目现有 glebarez 驱动。

本阶段针对性命令（DSN 指向隔离测试库，不是业务库）：

```sh
FREE_GROUP_TEST_REDIS=redis://127.0.0.1:16379/15 \
TEST_MYSQL_DSN='root@unix(/var/run/mysqld/mysqld.sock)/new_api_free_test?parseTime=true' \
TEST_POSTGRES_DSN='host=/var/run/postgresql user=root dbname=new_api_free_test sslmode=disable' \
go test ./service ./controller ./middleware ./relay ./relay/helper ./model \
  -run '^TestFreeGroup|^TestShouldRetryTaskRelay' -count=1 -v

git diff --check
```

数据库用例验证 Free 表新建、重复迁移与初始化、唯一约束、版本冲突、已有配置和审计保存、名单增删。它不是全库发布版升级测试；最终兼容性结论需等该矩阵完成。

2026-10-23 实际执行结果：上述命令退出码 0。`service` 2.442 秒、`controller` 0.252 秒、`relay/helper` 0.082 秒、`model` 2.812 秒；这些是测试程序报告的执行时间，不包含首次编译时间。`middleware`、`relay` 完成包编译但没有匹配此筛选器的测试，不能把它们计作行为回归。模型矩阵的 SQLite、MySQL、PostgreSQL 子用例全部通过，原生 Redis Lua 用例通过。`git diff --check` 通过。数据库日志中的重复键错误是唯一约束断言预期的结果。

认证相关集成沿用现有服务端认证、令牌失效、会话和人机校验；审计排除可用凭证。参考 OWASP Authentication Cheat Sheet 与 Session Management Cheat Sheet。当前未完成完整 ASVS 要求映射，不能宣称全量符合 ASVS。

前端复用上表组件与主题 token，没有新增 UI、图标、字体或样式依赖。云端构建使用现有 Dockerfile，发布 linux/amd64 与 linux/arm64 镜像。


## 预设与操作

预设只替换下表中的保护阈值，不选择模型或渠道，不修改总开关、准入条件、通知、队列及管理员已有逐模型覆盖值。其他配置项保持当前值，可逐项关闭或微调；任何手动调整会将档位标记为“自定义”。

| 档位 | 用户/令牌/IP/模型/全局并发 | RPM/突发 | TPM/突发 | 日请求 | 日 token | max_tokens |
| --- | --- | --- | --- | --- | --- | --- |
| 极严格 | 1/1/2/3/10 | 1/2 | 1000/1000 | 5 | 5000 | 256 |
| 严格 | 1/1/3/5/25 | 2/3 | 1500/1500 | 10 | 10000 | 384 |
| 标准 | 2/2/5/10/50 | 3/5 | 2000/2000 | 20 | 20000 | 512 |
| 宽松 | 4/4/10/25/100 | 10/15 | 10000/10000 | 100 | 100000 | 2048 |
| 极宽松 | 10/10/30/100/500 | 60/90 | 100000/100000 | 1000 | 1000000 | 8192 |

1. 超管进入“系统设置 → 免费分组设置”（`/system-settings/free-group`）。
2. 选择模型白名单，在渠道编辑页自行选择 Free 路由标识，并沿用原有渠道模型配置。
3. 选择预设，核对 diff，再保存；宽松变更需危险确认。开启总开关后，合资格用户的令牌分组选项出现免费分组。
4. 开启排队需要同时打开 `queue.enabled`，并将希望排队的并发维度策略设为 `queue`；默认关闭。
5. “版本历史”中预览旧版 diff 并确认回滚；产生新版本，保留旧历史。
6. “Free 名单”中可批准/禁止用户，或按用户、IP、指纹手动冷却/解封；不修改付费分组。
7. 关闭总开关并保存，保留 Free 令牌但拒绝其请求。

新增接口：`POST /api/free-group/config/preset`、`POST /api/free-group/config/preview`、`GET /api/user/free-group/queue`、`GET /api/channel/groups`。渠道专用分组接口独立于传统 `/api/group/`，避免 Free 出现在用户传统分组变更与倍率列表。

## Docker

Fork：`Tangmjiu/new-api`，开发分支：`feat/free-group`。
在 Actions 手动运行 `docker-build.yml`，选择该分支。工作流直接构建并发布，不运行单元测试或容器冒烟测试。

```sh
docker pull ghcr.io/tangmjiu/new-api:free-group
```

生产运行沿用项目已有 Docker Compose 与 Redis/数据库配置，替换镜像地址即可。Free 启用需要可用 Redis。若包为私有，需用有 `read:packages` 权限的 GitHub 凭证登录 GHCR。

## 已完成的数据库升级检查

2026-10-23 在真实 SQLite、MySQL 8.0.46、PostgreSQL 16.15 上，使用发布版 `v1.0.0-rc.21-fork.1` 和当前程序执行全量启动：每种数据库分别新建、发布版升级、当前程序连续启动两次；MySQL/PostgreSQL 含独立日志库。配置单例和初始版本未重复，已有用户余额、用量与请求计数保持原值。

```sh
python3 scripts/verify-free-group-migration.py --released <发布版程序> --current <当前程序>
```

此记录是先前已实际执行的结果；本次不再重跑测试。
