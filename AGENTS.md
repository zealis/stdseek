# voidxo 工作约定（AGENTS.md）

## 设计铁律（用户明确要求，改 UI 前必读）

1. **不要在界面添加冗余的解释性描述文本。** 界面文字只保留功能必需（操作项、状态、
   空态引导）；规则说明、流程解释这类内容放文档（DESIGN.md / README），不进 UI——
   它们会弄脏界面、浪费用户注意力。新增页面/组件时先问：这段字删掉用户会不会迷失？
   不会就删。（2026-10-26 用户提出，永久生效）
2. 图标按钮不放文字（如后台「菜单」钮是纯 ☰ 图标，语义靠 aria-label）。
3. 视觉与令牌的唯一事实来源是 `/DESIGN.md`，改样式先改文档再改 `web/src/styles.css`。
4. **不要裸露原生 `<input type="file">`**：它带一颗系统按钮，字体、圆角、内边距都不受控，
   和旁边的 `.btn` 明显不是一套。做法是 `hidden` 的原生控件 + 一个 `.btn` 触发
   （见后台「站点标识」）。（2026-10-29 用户提出）

## 站点分类清单（改之前先读）

分类词表的唯一事实来源是 `internal/sitecat/sitecat.go` 的 `List`（同时用于 AI 提示词、
参数校验、中文名）。**新增/改分类必须同时改 `web/src/app/core/api.ts` 的 `CATEGORIES`**，
两边的 key 与顺序要保持一致；只改一边的症状是「能筛但下拉里没这个名字」或反过来。

## 页级标签词表

唯一事实来源是 `internal/labels/labels.go`（四个 key + 中文名 + 给模型的判定说明）。
`processor` 与 `classify` 都从这里取常量，**不要在别处再写一遍 key**——
两处各写一份的症状是「AI 打了标却筛不出来」。前端 `core/api.ts` 的 `LABEL_TEXT`
只做中文名映射，改 key 时两边一起改。

## 用户反馈（标签 + 分类）

`label_feedback` 一张表装两种反馈，`kind` 区分：`FeedbackKindLabel`(0) / `FeedbackKindCategory`(1)。
kind=1 时 `label` 恒为空（后台显示「无」），另存 `category`（当时的站点分类）与 `suggest`。
写入只有 `UpsertLabelFeedback` 一条路径；改判用 `UpdateFeedback`。

- **后台「修改」改的是链接，不是反馈记录**（`POST /admin/feedback/fix`）：
  标签反馈 → 改这一页的页级标签（`documents.labels`）；分类反馈 → 改站点分类
  （`whitelist.category`）。反馈是「用户说过什么」的历史，改它等于篡改证据。
  `/admin/label-feedback/update` 只剩纠正录错反馈的用途，界面不再调用。
- **人工改过的标签要锁定**：`documents.labels_source='manual'`，`UpsertDocument` 与
  爬虫都按它跳过规则结果。不锁的话下次抓取就把修正抹掉，等于没修。
- **改标签会顺带重建这一页的索引**（从 KV 快照重新 `processor.Process`）：
  Bleve 没有「改一个字段」，正文只索引不存储、取不回来，只有重处理才有正文。
  没快照时等下次抓取。分类改动一律等下次抓取（分类写进每篇文档的索引，逐篇重建不划算）。

- **前台必须一个请求提交完**（`POST /api/label-feedback` 带 `labels` + `category`）：
  人机验证 token 一次性，拆开第二个必定失败。
- **误判率统计只数 kind=0**（`LabelFalsePositiveCounts`）。分类不是标签。
- 结果卡上反馈入口的条件是「有标签 **或** 有分类」——无标签的页面也要能报分类错。
- **改了收集内容必须同步 `web/src/app/pages/privacy.ts` 并 bump `privacy.updated_at`**。

## 抓取异常类别

后台「异常名单」的三类异常（无法访问 / 抓取规则变更 / 页面结构变化）是词表：
key 定义在 `internal/store/store.go` 的 `CrawlIssue*` 常量，归因逻辑在
`internal/crawler/classifyCrawl`，前端 `core/api.ts` 的 `ISSUE_KINDS` 只做中文名与提示映射。

改类别时三处一起改；另外**新增一类之前先想清楚它的修复动作是否真的不同**——
三类之所以分开，是因为「等站方恢复 / 换抓取入口 / 调正文解析」互不通用。
如果新类别最后还是这三件事之一，说明它应该并进现有类别。

## AI 站点判定的语料与触发

- 语料构造只在一处：`internal/api/aicorpus.go`。提示词模板在 `internal/classify/classify.go`
  的 `buildPrompt`。**改语料不要改到 handler 里**——手动批量与自动判定必须吃同一份输入，
  否则同一站两次判定结果会不一样，看起来像模型不稳定。
- 语料按信心排序：站点自述（名 + 首页描述）→ 标题（按发布时间，过滤站务页）→
  3 段正文摘录（跳过列表页）→ 抓取信号（页数、中位字数、URL 形态、规则标签计数）。
- **`ad_heavy` / `ai_suspect` 的证据来自规则引擎，不是模型读正文**：processor 在索引期
  用完整 HTML 判（广告位 DOM 占比/联盟链接、AI generator meta + 套话密度 + 句长方差），
  判定时只把这些计数当证据喂给模型并写死门槛（≥30%）。**不要为了让模型「更聪明」
  把整站正文塞进提示词**，成本涨一个数量级而准确率不会更好。
- 自动判定门槛：分类为空 → 首判；之后 30 → 300 → 每 +3000 篇。游标是
  `whitelist.ai_classified_docs`（判定当时的篇数），**不写游标就会每轮重复判**。
  管理员手改的分类（`category_source=manual`）永远不自动覆盖——改这个规则前先想清楚
  「半夜把手改的值改回去」用户会怎么感受。
- 判定代价：一次调用约 700~900 token 输入，20 个站一轮。加语料前先估一下 token。
- **AI 接口有三组**（`ai.*` / `ai.backup_*` / `ai.backup2_*`，后台「功能开关 → AI 接口」里配）。
  主接口超时或 5xx 自动退到备用，再退到第二备用；30 分钟故障期后自动试回；
  **切换与恢复都发邮件给全部管理员**，所以备用悄悄顶上不会没人知道。
  只有 HTTP 层失败才换接口，模型答非所问不算（落 `other`，别换供应商）。
  后台可设 `ai.summary_endpoint`（primary/backup/backup2）指定 **AI 概览**优先用哪组；
  它只改 AI 概览的尝试顺序，不改站点分类的主备判定。
  **AI 概览只在提问式查询时触发**（`looksLikeQuestion`，如「X 是什么」「如何…」），
  普通关键词不调模型；用户可在设置里关掉本机入口（后台开关仍总闸）。
  逐条「原文摘要」（`ai_summary`）仍走启动参数 `-llm-*`（未配置即抽取式），
  **不走后台 AI 接口**——别把两者混在一起，否则会悄悄改变原先零成本的行为。
  新增调用 AI 的地方：分类走 `s.aiClient(ctx)`，AI 概览走 `s.summaryEndpoints(ctx)` +
  `overview.Generate`，逐条摘要用启动注入的 `s.sumPrv`；别自己按 `ai.base_url` 拼。
- **AI 判定只改分类与标签，绝不改抓取间隔**（`ApplySiteClassify` 不含这一列）：
  间隔按分类给默认值（`sitecat.Category.Interval`），管理员可在添加站点时自选。
  改分类不带间隔一起改，是因为间隔反映的是站点体量与礼貌策略，不是内容性质。

- 白名单入库只有 `upsertWhitelist` 一条路径（添加站点与审核通过共用）；
  重复提交覆盖分类 + 间隔 + 来源，没填的 seed_path 与站点名/描述留旧值。

## 原文快照与云冷备（2026-10-29）

- 快照读写只经过 `snapshot.Store`（`internal/snapshot`），默认实现是 Pebble；
  业务代码不要直接依赖 `*kv.Store`——换存储后端时只改这一层。
- 后台「原文快照 → 管理」可配置 S3/B2（endpoint/bucket/region/Access Key/Secret/前缀/path-style）
  并测试连接。**热层永远是本地**，云存储只做冷备，不在检索路径上。
- 冷备实现在 `internal/backup`：按包（默认 128MB，可调 16–512）上传，包格式是自定义记录流
  `uint32 keyLen | uint32 valLen | key | val`，配合 `idx/<sha256首字节>.json` 的 manifest
  做**范围读**。**不要改成一页一对象**：对象存储按对象数计费，下游 LIST 也会被小文件拖死。
- 包内**逐条 gzip**（`entry.z`）：Pebble 存的是 snappy 压过的，取出来是原始 HTML，
  1145 页要 133MB，逐条压缩后 33MB。整包压缩会把「只取一页」变成「下载整包」，不要换。
- **两种存放方式**（`storage.backup.prune_local`）：`false` 本地全量 + 云端备份；
  `true` 上传成功后删本地、读取回源（`snapshot.NewCloud` 那层）。选 `true` 必须先开启归档，
  否则本地删了又没传上去 = 丢数据，服务端已挡死。
- 回源读失败要区分「云端读不到」与「没有这一页」（`snapshot.ErrRemote`）：
  前者提示重试，后者才引导补抓。
- **删本地后必须 Flush**：KV 是 DisableWAL + NoSync，不刷盘时强杀进程会让删除回滚，
  表现是「刚清空的快照重启后又回来了」。
- 打包/导出是分钟级操作，接口是**异步**的（202 + 轮询 `/admin/snapshot` 的 `job` 字段）：
  路由上挂着 30 秒请求超时，同步跑会被一刀切断，前端只看到失败而包已经传了一半。
- manifest 分 256 片（按 key 的 sha256 首字节），回源只读一片；判断「是否已归档」靠 manifest
  里有没有这个 key，**不要用游标**——key 按 URL 排序，新页面会插到游标前面被漏掉。
- 文档元数据（不含正文）导成 JSONL.gz 放 `exports/`；正文在快照包里已有一份，不重复。
- 开关默认关（上传意味着数据离开这台机器），开启前要求配置完整；
  **改了收集范围必须同步 `web/src/app/pages/privacy.ts` 的第三方清单**。
- **整库备份**（`internal/backup.Runner.DB`）：`store.BackupTo` 用 `VACUUM INTO` 导出一致性副本，
  再清掉会话/验证码/缓存表（`sessions`、`email_tokens`、`geo_cache`、`wiki_cache`、`usage_*`、
  `overview_cache`）——**活令牌绝不能进桶**。保留最近 7 份，更旧的从桶里删。
- 恢复三步（写进 `docs/self-host.md`）：`-restore-db <文件.gz>` → 后台「取回全部快照」→ `-reindex`。
  顺序不能换：索引由快照重建，快照没回来就重建出空库。
- SQLite/Bleve 不进对象存储（Bleve 由快照重建，DB 走整库备份）。

## 站点标识（2026-10-29）

- 顶栏的 `.logo-mark`（那枚 `</>` 渐变章）和浏览器标签页图标，都可以在**后台「功能开关 → 站点标识」**
  上传替换。**不要改代码换 logo**：`internal/webui/dist` 是 go:embed 编译期快照，
  改它等于改代码 + 重编译 + 重启，是这个项目最常犯的那类错。
- 实现：文件存 `<data>/brand/icon-<版本>.<ext>`，settings 记 `brand.icon` / `brand.version`。
  `/favicon.ico`、`/favicon.svg` 由服务端顶掉内嵌文件（标签页图标只认这两个路径，
  不顶的话会出现「顶栏换了、标签页还是旧的」）；`/brand/icon?v=<版本>` 供顶栏用，长缓存。
- 上传按**文件头魔数**认类型（PNG/ICO/JPEG/WebP/SVG），不信扩展名与客户端 Content-Type；
  HTML 之类直接 400。图标响应带 `CSP: default-src 'none'` + `nosniff`，
  SVG 里的脚本在直接打开该地址时也不会执行。
- 只换图标，不换站名文字（`voidxo` / `维空` 仍在前端模板里）。

## 站点信息与 SEO（2026-10-29）

- 站名/描述/关键词存 `settings` 的 `site.*`。**描述与关键词留空时按索引现状自动生成**
  （`internal/api/seo.go`：文章数、站点数、分类名、文档量最高的站点名）。
  不要把生成的句子抄回后台——抄回去就再也不更新了。
- 站点级 meta（description / keywords / OG / Twitter / canonical）由**服务端注入**
  index.html：`spaHandler` 包在 `webui.Handler()` 外面，只处理没有扩展名的路径。
  不要改回前端的 Title/Meta 服务——分享卡片的抓取器不执行 JS。
- 自动生成部分缓存 10 分钟；后台保存站点信息会 `invalidateSEO()` 立刻重算。
- `/og.png` 是**运行时画**的 1200×630 卡片图（渐变底 + 上传的图标，没图标画 V 折线），
  ETag 跟图标版本走。不要往仓库放静态 og 图：换图标后卡片会一直是旧的。
- `/robots.txt`、`/.well-known/security.txt`（含兼容的 `/security.txt`）、`/llms.txt`
  由 `internal/api/seofiles.go` 生成并**落盘到 `<data>/web/`**，不要放进 `web/public`：
  内容里带站点名、收录规模、联系邮箱，写死在 dist 里就等于每次改都要重新构建。
  - **首次启动自动生成**（`EnsureSEOFiles`），已存在的不覆盖——这三份是给人手改的；
    强制重写用 `-emit-web-files`（配好站点地址后跑一次）。
  - 服务**优先读磁盘上的文件**，手改完不用重启；读不到才现生成。
  - 站点地址统一走 `site.base_url`（后台「站点信息」可填），`-public-url` 是兜底，
    两者都没有才按 `X-Forwarded-*` / `Host` 推断。og:url、og:image、
    security.txt 的 Canonical、llms.txt 的链接全用它。
  - robots：`?q=` 的搜索结果页、/api、/admin、/manage 与账号页一律不开放。
  - security.txt：`Contact` 优先用后台「管理员邮箱」（mailto），没填退回 `/help` 的 URL。
  - llms.txt：标题 + 一句话说明 + 规模 + 主要页面 + 检索接口 + 使用约束，刻意简短。
  它以前只存库、界面没人读，表现就是「改了网站名称、点保存、页面没变化」。

## 技术约定

- 前端：Angular 22（web/），zoneless + signals-first，standalone 不显式声明 OnPush，
  原生控制流 `@if/@for/@switch`，禁止 ngClass/ngStyle/CommonModule。
- 路由 `withComponentInputBinding` 会把缺省 query param 绑成 undefined、有值时绑成
  字符串——组件里必须归一化（见 `pages/search.ts` 的 `pageIndex`）。
- 构建：`cd web && npm run build` 会自动同步产物到 `internal/webui/dist`（go:embed）。
  必须用 Node ≥ 22.22.3 构建 Angular 22（本机用 `D:\sdk\Nodejs`，managed 的 22.22.2 不达标）。
- 开发预览：`cd web && npm start`（:4200，代理 /api /admin 到 127.0.0.1:8080）。

## 发版约定（2026-10-26 用户提出，永久生效）

- **版本号只写在根目录 `VERSION`（不带 `v`），不要在代码或 README 里另起一份。**
- 每次更新必须同时做三件事：bump `VERSION`、往 `CHANGELOG.md` 加一段、打 `vX.Y.Z` tag。
  三处由 `./scripts/release.sh` 一次完成，禁止手工分开改（必然漂）。
- 提交推送到 GitHub（`git@github.com:zealis/voidxo.git`）也由该脚本负责。
- `CHANGELOG.md` 段落格式：`## vX.Y.Z（YYYY-MM-DD）`，条目写
  `- **类型**：内容`，类型限定为 安全 / 修复 / 新增 / 改进 / 重构 / 移除 / 文档。
- 条目写「用户能感知的变化」，不写「改了哪个文件」；一次发版里的同类改动合并成一条。
