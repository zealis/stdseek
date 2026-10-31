// 后端 API 类型。字段与 Go 侧一一对应（snake_case 原样保留，不做转换层），
// 改后端字段时这里同步。服务封装在 core/ 下，组件不直接发请求。

export type Hit = {
  url: string;
  domain: string;
  title: string;
  description: string;
  // 网站名称（whitelist.site_name，爬虫抓首页时提取的 og:site_name）。
  // 可选：老数据/未爬到首页时缺失，展示层回退域名。
  site_name?: string;
  labels: string[] | null;
  label_reasons?: Record<string, string> | null;
  /** 站点分类 key（见 CATEGORIES）。老索引没有这个字段，缺失时不要当空分类筛。 */
  category?: string;
  score: number;
  reasons: string[] | null;
  breakdown: Record<string, number>;
  time_by_fetch: boolean;
  sort_time: number;
  prosperity: number;
  // 这页有没有原文快照。false 说明爬虫还没抓过。
  // 能不能补抓：没快照 + 域名仍在白名单。索引里可能有「已从白名单移除」的历史页面，
  // 那种页面给按钮只会让用户点了吃 403。
};

/** 被识别出来的查询语法（后端 querysyntax 解析结果的回显）。 */
export type QuerySyntax = {
  required: string[] | null;
  excluded: string[] | null;
  phrases: string[] | null;
  site: string;
};

export type SearchResp = {
  q: string;
  total: number;
  page: number;
  hits: Hit[];
  // 查询带「最新/最近」这类时效意图时，服务端会调高发布时间权重
  freshness_intent?: boolean;
  // 深网发现：相关但少被引用的内容（繁荣度倒数打捞）
  discover?: Hit[];
  // 查询里用到了 + - "" site: 语法时才会返回
  syntax?: QuerySyntax;
};

export type WhitelistSite = {
  id: number;
  domain: string;
  category: string;
  /** 分类是谁定的：seed 提交默认 / manual 管理员手改（自动重判不覆盖）/ ai 模型判定 */
  category_source?: 'seed' | 'manual' | 'ai' | '';
  /** 站点级标签（AI/人工按整站判），写索引时会并进每篇文档 */
  labels?: string[];
  /** 上一次 AI 判定的时间（0 = 从未判过）与当时的文档数 */
  ai_classified_at?: number;
  ai_classified_docs?: number;
  /** 当前入库文档数（不含被折叠的副本） */
  doc_count?: number;
  /** 站点首页的 meta description（AI 判定输入） */
  site_desc?: string;
  site_name?: string;
  /** 抓取入口路径（默认 /） */
  seed_path?: string;
  /** 文章 URL 正则（空 = 不限；平台站用它挡掉导航/频道页） */
  article_pattern?: string;
  enabled: boolean;
  crawl_interval: number;
  last_crawled_at: number | null;
  last_status: string;
  last_error: string;
  created_at: string;
};

/**
 * 抓取异常名单的一条记录（后台「异常名单」页）。
 * kind 与 Go 侧 store.CrawlIssue* 一致：无法访问 / robots 规则变更 / 页面结构变化。
 */
export type CrawlIssue = {
  id: number;
  domain: string;
  kind: string;
  detail: string;
  /** 连续失败轮数（恢复正常即从名单删除，所以是连续值） */
  occurrences: number;
  /** RFC3339（Go 的 time.Time 序列化） */
  first_seen_at: string;
  last_seen_at: string;
};

/** 异常类别：顺序即排查顺序（站点没了 → 不让抓 → 抓到了解析不出正文）。 */
export const ISSUE_KINDS = [
  { value: 'unreachable', label: '无法访问', hint: '连接超时、DNS 失败、HTTP 4xx/5xx' },
  { value: 'robots', label: '抓取规则变更', hint: 'robots.txt 不再允许抓取' },
  { value: 'structure', label: '页面结构变化', hint: '抓得到页面但解析不出正文' },
] as const;

export function issueText(kind: string): string {
  return ISSUE_KINDS.find((k) => k.value === kind)?.label ?? kind;
}

/** 白名单里的抓取状态（last_status）转人话。 */
export function crawlStatusText(status: string): string {
  switch (status) {
    case 'ok':
      return '正常';
    case 'error':
      return '抓取失败';
    case 'no-document':
      return '无可用正文';
    case 'robots':
      return '规则拦截';
    default:
      return status || '—';
  }
}

export type BlacklistSite = {
  id: number;
  domain: string;
  reason: string;
  reject_count: number;
  blocked_count: number;
  created_at: string;
};

export type ReviewItem = {
  id: number;
  url: string;
  domain: string;
  category: string;
  reason: string;
  precheck: string;
  precheck_at: number | null;
  status: string;
  created_at: string;
  reviewed_at: number | null;
};

export type SubmissionStatus = {
  domain: string;
  category: string;
  status: string;
  status_text: string;
  created_at: number;
  reviewed_at?: number;
};

export type LabelFeedback = {
  id: number;
  url: string;
  domain: string;
  /** 空串 = 这条是分类反馈（页面没有标签，或用户反馈的是站点分类）。 */
  label: string;
  verdict: string;
  note: string;
  /** 0 = 标签反馈，1 = 分类反馈。 */
  kind: number;
  /** 反馈当时的站点分类（仅 kind=1 有意义）。 */
  category: string;
  /** 用户认为正确的分类（仅 kind=1 有意义，可为空）。 */
  suggest: string;
  /** 标签反馈成立且该页面不该有任何标签（仅 kind=0 有意义）。 */
  no_label: boolean;
  /** 这一页当前的页级标签（改链接时的初值）。 */
  page_labels: string[];
  /** 这个站点当前的分类（改链接时的初值）。 */
  site_category: string;
  created_at: string;
  updated_at: string;
};

export type PrecheckResult = {
  url: string;
  final_url: string;
  reachable: boolean;
  status_code: number;
  content_type: string;
  is_html: boolean;
  robots_allowed: boolean;
  robots_found: boolean;
  blacklist_history?: boolean;
  checked_at?: number;
  error?: string;
};

// 队列里存的预检是历史 JSON，字段可能缺失（例如只有 blacklist_history 的旧条目）
export type QueuePrecheck = Partial<PrecheckResult>;

export type FeatureFlag = {
  key: string;
  label: string;
  desc: string;
  enabled: boolean;
  danger?: boolean;
  updated_at?: number;
};

/** 原文快照的占用与开关状态（后台快照面板）。 */
export type SnapshotStorage = {
  /** 热层恒为本地（Pebble）；冷备按包推到下面的桶。 */
  backend: string;
  s3_configured: boolean;
  endpoint: string;
  bucket: string;
  region: string;
  prefix: string;
  has_access_key: boolean;
  has_secret: boolean;
  path_style: boolean;
  /** 直连对象存储，不走系统代理（HTTPS_PROXY）。 */
  no_proxy: boolean;
};

export type SnapshotStats = {
  enabled: boolean;
  pages: number;
  orphans: number;
  bytes: number;
  cap_bytes: number;
  documents: number;
  available: boolean;
  storage: SnapshotStorage;
  backup: SnapshotBackup;
  job: SnapshotJob;
};

/** 云端任务（打包/导出）：跑的时候页面轮询这个字段。 */
export type SnapshotJob = { job: string; started_at: string; message: string; progress: string };

/** 云冷备状态：把快照与文档元数据按包推到对象存储。 */
export type SnapshotBackup = {
  enabled: boolean;
  /** 只留云端：上传成功后删除本地副本，读取时回源。 */
  prune_local: boolean;
  part_mb: number;
  parts: number;
  keys: number;
  last_run: string;
  last_error: string;
  last_key: string;
  exported_at: string;
  export_key: string;
  export_docs: number;
  /** 整库备份：频率（0=关闭）、上次时间、体积、保留份数。 */
  db_every_hours: number;
  db_last_at: string;
  db_key: string;
  db_size: number;
  db_parts: number;
};

// ---- 证据链摘要：每条结论都带 citations；citations 为空 = 无法核验，UI 必须标「未验证」
export type SummaryCitation = { id: number; text: string };
export type SummaryConclusion = { text: string; citations: SummaryCitation[] | null };
export type SummaryPassage = { id: number; text: string; score: number };
export type SummaryResp = {
  conclusions: SummaryConclusion[] | null;
  passages: SummaryPassage[] | null;
  provider: string;
  // empty=true 表示没有可靠来源，UI 要显示「未找到可靠来源」而不是空白
  empty: boolean;
  reason?: string;
  dropped_citations?: number;
};

export type Status = { documents: number; sites: number; features: Record<string, boolean> };

/** 站点标识：后台可换的图标，同时用于顶栏品牌章与浏览器标签页。 */
export type Branding = {
  has_icon: boolean;
  icon_url: string;
  /** 图标文件的版本号，只用于 URL 指纹；不是产品版本。 */
  version: string;
  name: string;
  /** 产品版本与提交号（构建期注入），控制台横幅用。 */
  app_version?: string;
  commit?: string;
};

// ---- AI 概览：搜索页顶部的一段回答；只有提问式查询才生成
export type OverviewSource = { id: number; title: string; url: string; domain: string };
export type OverviewResp = {
  enabled: boolean;
  question: boolean;
  answer?: string;
  sources?: OverviewSource[] | null;
  provider?: string;
  reason?: string;
};

/**
 * 站点分类。**与 internal/sitecat/sitecat.go 的 List 保持一致**（顺序即展示顺序）。
 * 前五项是历史就有的分类，优先排前；其余按现实使用频率从高到低。
 * 新增分类要两边一起改：Go 那边是 AI 提示词与校验的唯一来源，
 * 这边只负责显示中文名；`interval` 是该分类的默认抓取间隔（秒），
 * 判据是「这类站的内容多久会变一次」——新闻半小时、官方站一天、素材站两天。
 */
export const CATEGORIES = [
  { value: 'official', label: '官方网站', interval: 86400 },
  { value: 'media', label: '新闻阅读', interval: 1800 },
  { value: 'platform', label: '信息论坛', interval: 3600 },
  { value: 'blog', label: '技术博客', interval: 21600 },
  { value: 'opensource', label: '开源项目', interval: 86400 },
  { value: 'docs', label: '知识文档', interval: 86400 },
  { value: 'image', label: '图片素材', interval: 172800 },
  { value: 'download', label: '软件下载', interval: 172800 },
  { value: 'design', label: '设计创意', interval: 172800 },
  { value: 'tool', label: '效率工具', interval: 86400 },
  { value: 'job', label: '求职招聘', interval: 7200 },
  { value: 'life', label: '生活服务', interval: 43200 },
  { value: 'game', label: '游戏影音', interval: 21600 },
  { value: 'social', label: '社交平台', interval: 3600 },
  { value: 'dev', label: '开发工具', interval: 86400 },
  { value: 'shopping', label: '购物平台', interval: 3600 },
  // 兜底档：AI 判不出来时落这里（见 internal/sitecat 的 Other）
  { value: 'other', label: '其他', interval: 86400 },
] as const;

/** 分类 → 默认抓取间隔（秒）。未知分类按一天算（与 Go 侧 DefaultInterval 一致）。 */
export function categoryInterval(cat: string): number {
  return CATEGORIES.find((c) => c.value === cat)?.interval ?? 86400;
}

/** 可选的抓取间隔（秒）。留空（0）表示跟随分类默认值。 */
export const CRAWL_INTERVALS = [
  { value: 1800, label: '30 分钟' },
  { value: 3600, label: '1 小时' },
  { value: 7200, label: '2 小时' },
  { value: 21600, label: '6 小时' },
  { value: 43200, label: '12 小时' },
  { value: 86400, label: '1 天' },
  { value: 172800, label: '2 天' },
];

export function categoryText(value: string): string {
  return CATEGORIES.find((c) => c.value === value)?.label ?? value;
}

// 标签中文名。raw label 是索引里的机器名（ai_suspect 等），
// 直接展示等于让用户猜，也谈不上「可解释」。
export const LABEL_TEXT: Record<string, string> = {
  ai_suspect: 'AI 生成',
  ad_heavy: '广告较多',
  personal: '独立网站',
  ugc_platform: 'UGC 平台',
};

/** 全部标签的 key，顺序与 LABEL_TEXT 一致。反馈弹窗的「应该是」chips 按它渲染。 */
export const KNOWN_LABELS = Object.keys(LABEL_TEXT);

export function labelText(label: string): string {
  return LABEL_TEXT[label] ?? label;
}

// 少数标签用字母徽标，不用线条图标。
// AD 是广告的通用缩写（AdSense、各处广告位都用它）；喇叭（megaphone）表达的是
// 「宣传/公告」，读成「这块在喊话」而不是「这页广告多」，在 15px 下更像一个通知铃。
export const LABEL_BADGE: Record<string, string> = {
  ad_heavy: 'AD',
};

/** 这个标签该画字母徽标吗（返回 null 表示走图标）。 */
export function labelBadge(label: string): string | null {
  return LABEL_BADGE[label] ?? null;
}

// 排序理由的展示文案。后端 explain() 的理由是面向调试的完整句子，
// 展示层按设计稿（voidxo-ui-v2 / DESIGN.md §10）换成更短的标签；
// 映射不到的（重复聚合、首页降权等）原样保留。动态数字用正则改写。
const REASON_TEXT: Record<string, string> = {
  '标题精确匹配查询词': '标题完整命中',
  '部分关键词命中标题/摘要': '部分命中',
  '来自官方文档站点': '官方文档加权',
  '来自开源项目站点': '开源项目加权',
  'UGC 平台站点（质量分较低）': 'UGC 平台（质量分较低）',
  '语料内无人引用，被算法埋住': '少被引用 · 深网打捞',
  '来自独立博客/开源项目站点': '独立博客/开源项目',
};

export function reasonText(reason: string): string {
  const mapped = REASON_TEXT[reason];
  if (mapped) return mapped;
  const days = /^发布时间较新（(\d+) 天前）$/.exec(reason);
  if (days) return `近 ${days[1]} 天发布`;
  if (/^反向链接较少（语料内 \d+ 次引用）$/.test(reason)) return '少被引用 · 深网打捞';
  return reason;
}

// 过滤偏好（规划书：设置页 localStorage，登录后再同步）
export type Prefs = {
  exclude: string[];
  /** 只看这些站点分类（OR）；空数组表示不限。 */
  cats?: string[];
  since: number;
  /** 检索走 GET（词显示在地址栏）还是 POST（不显示）。缺省按 GET 处理。 */
  getRequest?: boolean;
};

/** 页级标签的排除项。label 就是标签本名（与结果卡上的 chip 同一套叫法），
 *  「排除」由分组标题承担，不再给每个名字加 no 前缀——否则一句话说两遍。 */
export const FILTERS = [
  { key: 'ai', label: 'AI 生成', hint: '疑似由模型生成，查不到可靠出处' },
  { key: 'ads', label: '广告较多', hint: '正文里广告位占比过高' },
  { key: 'personal', label: '独立网站', hint: '个人博客、个人主页这类' },
  { key: 'platform', label: 'UGC 平台', hint: '知乎 / CSDN 等用户发帖平台' },
] as const;

export const TIME_WINDOWS = [
  { label: '全部', since: 0 },
  { label: '1 天', since: 86400 },
  { label: '3 天', since: 259200 },
  { label: '1 周', since: 604800 },
  { label: '1 月', since: 2592000 },
] as const;

/** 一天的用量计数（后台「站点统计」页）。 */
export type UsageRow = {
  day: string;
  searches: number; // 检索请求总数
  succeeded: number; // 其中命中至少一条的次数
  api: number; // 其中来自非网页端的请求
  hits: number;
  crawls: number;
  indexed: number;
  summaries: number;
};

/** 累计口径（后台统计卡「今日 + 累计」里的后半截）。 */
export type UsageTotals = {
  searches: number;
  crawls: number;
  indexed: number;
  summaries: number;
  /** 全部账号数（含管理员） */
  users: number;
  users_today: number;
  /** 当前索引里的文档数（不是累加入库：去重折叠后仍要能计数） */
  documents: number;
  /** 最早的管理员账号创建时间（Unix 秒），运行时间起点；0 = 还没有管理员 */
  since: number;
};

/** 公开的每日用量（前台「每日用量」页）：只有检索维度。 */
export type PublicUsageRow = {
  day: string;
  searches: number;
  succeeded: number;
  api: number;
  hits: number;
};

/** 维度排行：某个取值（来源域名 / 地区码）及其累计次数。 */
export type DimRow = {
  name: string;
  count: number;
};
