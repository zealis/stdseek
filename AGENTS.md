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

唯一事实来源是 `internal/labels/labels.go`（五个 key + 中文名 + 给模型的判定说明）。
**tracker（追踪器较多）**：规则在索引期按第三方追踪脚本特征判（≥2 类才打），
AI 判定时以 `tracker_tagged` 占比当证据（≥30%）。前端图标是 `spy`（隐身帽+墨镜）。
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

- **前台一次提交一个请求**（v0.79.17 起）：主表单 `POST /api/label-feedback` 带
  `labels` + `category`；漏报走「报告」弹窗的**独立请求**（只带 `should`）。
  人机验证 token 一次性，任何一份提交拆成两个请求第二个必定失败。
- **漏报也走 kind=0**（v0.79.8）：`verdict=false_negative`、`label`=用户认为该页面
  **应该有**的标签（页面上本来没有；前端排除已有，服务端对交集直接 400）。
  与误报共用冲突键（url+label+client），重复提交即改判；
  误判率统计不受影响（`LabelFalsePositiveCounts` 只数 false_positive）。
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
- **标识必须由服务端写进 HTML**（`injectBranding`）：`window.__VOIDXO_BRANDING__` +
  把首屏骨架 `<h1 class="boot-title">` 换成真实站名。只靠 `/api/branding` 的话，
  网络慢时会先画默认的 `</>` 章和默认站名再跳过去，越慢越明显。
  站名来自 settings（用户可填），`json.Marshal` 会把 `<` 转成 `\u003c`，
  `</script>` 这类输入逃不出 script 标签。

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
- `/sitemap.xml` 是**动态生成**（不落盘）：只列站点自己的可收录页面（首页/帮助/API 文档/
  用量/提交/隐私），与 robots 的 Disallow 保持一致。**不要把索引里的文档 URL 塞进去**——
  那些站点不是我们的，声明在自家 sitemap 里等于冒充站长，还会被判重复内容。
- robots.txt 里那行 `Sitemap:` 由服务端补：磁盘文件没写就追加（自己写了就不动），
  因为 Sitemap 必须是绝对地址，安装时还不知道域名。
  它以前只存库、界面没人读，表现就是「改了网站名称、点保存、页面没变化」。

## 检索入口防护（2026-10-30）

`/search?q=` 是唯一「把用户输入直接交给检索引擎」的地方，也是最容易被当放大器的入口。
防护集中在 `internal/api/queryguard.go`，**新增加收入口时复用这里的函数，别自己再写一遍**：

- `normalizeQuery`：剥掉控制字符（C0/C1、DEL）与不可见格式字符（零宽、双向覆盖、BOM），
  折叠空白，然后限字符数（`maxQueryRunes` 256）与词数（`maxQueryTerms` 32）。
  **先清洗再判长度**：反过来的话零宽字符能绕过长度检查，控制字符会进日志把行拆坏。
  纯符号查询拒掉，但 `C++` / `C#` / `.NET` 要保留（判定条件是「含字母或数字」）。
- `checkURISize`：整个请求行上限 `maxURIBytes` 2048，超了 414。
  Go 只按 `MaxHeaderBytes`（默认 1MB）限头部，靠它等于没限。
- `listArg`：`exclude` / `cat` / `fields` 这类逗号列表最多 `maxListArg` 32 项，超了报 400，
  **不静默截断**——静默截断会让人以为「筛全了」。
- `maxPage` 100：深翻页每页都要跑一次完整排序，再往后没人看（返回 400 而不是静默夹住，
  否则用户以为翻到了、看到的却是重复内容）。
- `/api/search` 有**独立限流**（`searchLimiter` 60/min/IP），不共用全局 120/min：
  检索一次能顶几十次 `/api/status`，共用额度等于让 120 次全花在最贵的路上。
- **搜索结果页 noindex**：`headTags` 对 `noIndexPath` 的路径给
  `<meta name="robots" content="noindex, nofollow">`，并且**不写 canonical**——
  canonical 等于告诉搜索引擎「正本是 /search」，那正是不要收录的那一页。
  同时 `securityHeaders` 给这些路径加 `X-Robots-Tag`，JSON 响应也覆盖。
  **robots.txt 的 Disallow 不等于不进索引**（被外链的 URL 仍可能收录），所以两层都要。
- **缓存准入**：`cacheableQuery`（≤64 字符、≤8 词）过滤机器长尾，`searchCache.put`
  要求「同一查询出现两次」才占位。否则 512 个随机串就能把 512 条缓存填满，
  真实查询从此全部 miss（0.2ms → 26ms），攻击成本几乎为零。
- 日志里出现查询词一律 `logSafe(q)`：截断 64 并再挡一次控制字符（伪造日志行）。
- **AI 概览有按 IP 每日配额**（`ai.overview_daily_quota`，默认 20，0=不限制，
  后台「功能开关 → AI 接口」可改）。概览是唯一真的花钱的公开接口，缓存挡得住
  「同一个问题问两遍」，挡不住「每次换一种问法」。三条规矩：
  ① **在确实要调模型的那一刻才扣**（缓存命中、找不到材料都不消耗）；
  ② **调用失败要退回**（`OverviewQuotaRefund`），一次 502 不该吃掉用户一天；
  ③ **计数表写不进去时放行**——配额是省钱手段，不能因为它挂了让人搜不了。
  存储是 SQLite 的 `overview_quota(day, ip, n)`，按服务器本地自然日切分，每天清一次旧的。

## 网站图标代理（2026-10-31）

结果卡站名前的 16px 站点图标走 `GET /api/favicon?d=<域名>`（`internal/api/favicon.go`）：
服务器向 Google 图标服务代取，访客浏览器只跟本站通信。
上游是一条**写死的链**：google.com/s2 → gstatic.cn（Google 国内镜像，同一服务，
URL 形态不同），取第一个通的；域名只作查询参数（正则校验），这条路径
没有任意 URL 的 SSRF 面，不要把它泛化成「通用图片代理」。
三条红线：

- **上游是一条写死的链**（google.com/s2 → gstatic.cn），域名只作查询参数（正则校验），
  这条路径没有任意 URL 的 SSRF 面；不要把它泛化成「通用图片代理」。
- **只缓存在内存里（TTL 三天）+ 失败负缓存一小时，不落盘、不进 KV、不进备份**——
  图标随时可再取，持久化它只会让备份变重、让「缓存几天」的产品约定落空。
- 前端 `<img onerror>` 整个藏掉，不做占位图；隐私政策第 4 节已列此项（始终生效），
  改取图来源时记得同步。

## 域名 WHOIS（2026-10-31）

排序质量分的域名信任度信号：`whitelist.whois_created_at`（注册时间）+
`whois_checked_at`（复查时间），由 `internal/api/whois.go` 的 `WhoisSweep` 定时补数
（main 里 30 分钟一轮、每轮 5 个域名，90 天复查一次）。

- **默认接口 xxapi.cn v2**（`v2.xxapi.cn/api/whois?domain=`，服务端自带 3 天缓存）；
  后台「功能开关 → 域名 WHOIS 查询」可换自定义接口（`whois.api_url`，URL 里
  `{domain}` 占位）。**自定义失效自动退回默认**——一个坏配置不该让数据断供。
- 响应解析宽容：常见字段名认不出就全表扫 ISO 日期（1990 后、早于现在才算），
  自定义接口的返回结构不必对齐 xxapi。
- 查不到的域名也推进 `whois_checked_at`，否则每轮扫描都会打一遍上游。
- 权重压得很低（10 年最多 +0.1）：老域名只是小幅信任信号，理由 ≥3 年才出现。
- 隐私政策第 4 节已列此项（发送的只有域名本身）。

## 前端首访体积（2026-10-30）

- **不要用 `PreloadAllModules`**。它会在首屏画完后把所有懒加载 chunk 一起拉下来，
  而后台已经长到 15 个页面：实测 37 个 chunk / 210KB(gzip)，其中 12 个是后台，
  **首访白下 61KB**，还要和首页那次 `/api/status` 抢连接（HTTP/1.1 每域 6 条）。
  现在用 `core/preload.ts` 的 `PreloadPublicOnly`：`manage` 一律不预取，
  其余访客页面延后 800ms 再取。
- **`path: ''`（首页）是静态导入**，不走 `loadComponent`。它是绝大多数人的落地页，
  懒加载会多一次往返——骨架已经把 hero 画出来了，用户这时敲字却没反应，
  这就是「首页感觉慢」的那一段。代价是 main 多 2KB(gzip)。
- 首屏必需只有三个文件：`main`（37KB gz）+ 共享 chunk（56KB gz）+ 2KB，
  合计约 95KB。新增依赖前先看它会不会挤进这条路径。
- **上传的图标会进首屏**：它和 main 一样是首屏资源。当前那张 295 字节的 SVG 无所谓，
  但别再传几百 KB 的矢量化图片——一张 93KB 的 SVG(gzip 32KB) 画在 22px 上，
  比整个首页的 JS 还占带宽。建议 128×128 的 PNG / 精简过的 SVG。
- **不要引第三方公共 CDN**（BootCDN/360/jsDelivr 等）：它们不托管我们的构建产物；
  真要放上去等于自建 CDN，而中间人改一行 JS 就是全站接管，也和隐私政策
  「无第三方脚本」冲突。真要加速：HTTPS + HTTP/2，或备案后用自己的域名挂国内 CDN 回源。

## 页头页脚代码（2026-10-30）

后台「站点信息 → 页头与页脚代码」可以让管理员放进统计脚本、验证 meta、自定义 CSS。
**这是全站唯一「把管理员写的原文塞进每个访客页面」的地方，等于一个自带的后门**，
所以边界在 `internal/api/sitehtml.go` 里写死，不要在后面的人手里放松：

- **只注入访客页面**：`/manage`、`/admin`、`/api`、`/login`、`/register`、`/reset`、
  `/verify`、`/settings` 一律不注入。理由不是洁癖——后台页面里有账号列表、白名单、
  令牌输入框，第三方统计脚本一旦被投毒（供应链最常见的一步就是「统计代码里多了一行」），
  它读到的就是这些；登录页同理，脚本能在提交前读到密码。
- **只做两个锚点插入**（`</head>` 前、`</body>` 前），用 `bytes` 直接拼，
  不走 `fmt`/模板——代码里的 `%s`、`{{ }}` 就是字面量。
- **上限 32KB / 段，拒绝 NUL**。不做语法校验：写错的 JS 只会静默失败，判不出来。
- 读接口只有 `GET /admin/site`（要令牌）。**不要加公开读接口**，否则这段代码成了指纹。
- 保存按「请求里出现的键」写入（`handleSaveSite`），因为页头页脚单独一张卡、
  单独一次保存——整表覆盖写会让「改个描述」把代码清空。
- 这两个设置会让站点向第三方发数据，**改隐私政策时一起改** `privacy.ts` 第 4 节最后一条。

## 版本号显示（2026-10-30）

- 后台「管理后台」标题右边那行小字来自 `GET /admin/version`（要令牌），
  数据在构建期用 `-ldflags "-X main.version=..."` 注入；源码直接 `go build` 时
  回落到仓库根目录的 `VERSION` 文件（否则装的人只会看到 `dev`）。
- **不要放到公开接口**（如 `/api/status`）：版本号会帮人挑已知漏洞，后台看到就够了。
- `AppTitleStrategy` **必须带 `@Injectable()`**：`useClass` 指向一个没有 DI 元数据的类时，
  Angular 不报错、直接继续用默认实现——表现是「代码在产物里，标题却完全没带站名」。
  这个坑从「站点信息生效」那轮起一直存在（标签页标题没站名、搜索词不进标题），
  2026-10-30 才定位到。判断方法：改 title 后看浏览器标签，不是 `document.title` 的
  `curl` 结果（服务端注入的是另一份）。
- 例外是**控制台横幅**（`web/src/main.ts`）：访客打开开发者工具能看到
  `站名 v版本 — commit 链接 — Angular v版本` 与一行版权。这是刻意的取舍——
  自托管最常见的问题是「你这版复现不了」，而确认构建版本只有这一条路。
  它读的是注入 HTML 的 `window.__VOIDXO_BRANDING__.app_version / commit`。
  注意 `Branding.version` 是**图标版本号**，两者不要混。

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
