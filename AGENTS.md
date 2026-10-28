# voidxo 工作约定（AGENTS.md）

## 设计铁律（用户明确要求，改 UI 前必读）

1. **不要在界面添加冗余的解释性描述文本。** 界面文字只保留功能必需（操作项、状态、
   空态引导）；规则说明、流程解释这类内容放文档（DESIGN.md / README），不进 UI——
   它们会弄脏界面、浪费用户注意力。新增页面/组件时先问：这段字删掉用户会不会迷失？
   不会就删。（2026-10-26 用户提出，永久生效）
2. 图标按钮不放文字（如后台「菜单」钮是纯 ☰ 图标，语义靠 aria-label）。
3. 视觉与令牌的唯一事实来源是 `/DESIGN.md`，改样式先改文档再改 `web/src/styles.css`。

## 站点分类清单（改之前先读）

分类词表的唯一事实来源是 `internal/sitecat/sitecat.go` 的 `List`（同时用于 AI 提示词、
参数校验、中文名）。**新增/改分类必须同时改 `web/src/app/core/api.ts` 的 `CATEGORIES`**，
两边的 key 与顺序要保持一致；只改一边的症状是「能筛但下拉里没这个名字」或反过来。

## 页级标签词表

唯一事实来源是 `internal/labels/labels.go`（四个 key + 中文名 + 给模型的判定说明）。
`processor` 与 `classify` 都从这里取常量，**不要在别处再写一遍 key**——
两处各写一份的症状是「AI 打了标却筛不出来」。前端 `core/api.ts` 的 `LABEL_TEXT`
只做中文名映射，改 key 时两边一起改。

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
- **AI 接口有主用/备用两组**（`ai.*` / `ai.backup_*`，后台「功能开关 → AI 接口」里配）。
  主接口超时或 5xx 自动退到备用，30 分钟故障期后自动试回；
  **切换与恢复都发邮件给全部管理员**，所以备用悄悄顶上不会没人知道。
  只有 HTTP 层失败才换接口，模型答非所问不算（落 `other`，别换供应商）。
  新增调用 AI 的地方走 `s.aiClient(ctx)` 拿客户端，别自己按 `ai.base_url` 拼——
  那样会绕过故障期，主接口挂了还去撞它。
- **AI 判定只改分类与标签，绝不改抓取间隔**（`ApplySiteClassify` 不含这一列）：
  间隔按分类给默认值（`sitecat.Category.Interval`），管理员可在添加站点时自选。
  改分类不带间隔一起改，是因为间隔反映的是站点体量与礼貌策略，不是内容性质。

- 白名单入库只有 `upsertWhitelist` 一条路径（添加站点与审核通过共用）；
  重复提交覆盖分类 + 间隔 + 来源，没填的 seed_path 与站点名/描述留旧值。

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
