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
  score: number;
  reasons: string[] | null;
  breakdown: Record<string, number>;
  time_by_fetch: boolean;
  sort_time: number;
  prosperity: number;
  // 这页有没有原文快照。false 说明爬虫还没抓过。
  has_snapshot: boolean;
  // 能不能补抓：没快照 + 域名仍在白名单。索引里可能有「已从白名单移除」的历史页面，
  // 那种页面给按钮只会让用户点了吃 403。
  fetchable: boolean;
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
};

export type WhitelistSite = {
  id: number;
  domain: string;
  category: string;
  enabled: boolean;
  crawl_interval: number;
  last_crawled_at: number | null;
  last_status: string;
  last_error: string;
  created_at: string;
};

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
  label: string;
  verdict: string;
  note: string;
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

export const CATEGORIES = [
  { value: 'official', label: '官方文档' },
  { value: 'opensource', label: '开源项目' },
  { value: 'blog', label: '技术博客' },
  { value: 'platform', label: '技术社区' },
  { value: 'media', label: '技术媒体' },
] as const;

export function categoryText(value: string): string {
  return CATEGORIES.find((c) => c.value === value)?.label ?? value;
}

// 标签中文名。raw label 是索引里的机器名（ai_suspect 等），
// 直接展示等于让用户猜，也谈不上「可解释」。
export const LABEL_TEXT: Record<string, string> = {
  ai_suspect: '疑似 AI 生成',
  ad_heavy: '广告较多',
  personal: '个人站点',
  ugc_platform: 'UGC 平台',
};

export function labelText(label: string): string {
  return LABEL_TEXT[label] ?? label;
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
export type Prefs = { exclude: string[]; since: number };

export const FILTERS = [
  { key: 'ai', label: 'no AI', hint: '排除疑似 AI 生成的页面' },
  { key: 'ads', label: 'no 广告', hint: '排除广告占比过高的页面' },
  { key: 'personal', label: 'no 个人', hint: '排除个人博客与主页' },
  { key: 'platform', label: 'no 平台', hint: '排除知乎 / CSDN 等 UGC 平台' },
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

/** 公开的每日用量（前台「每日用量」页）：只有检索维度。 */
export type PublicUsageRow = {
  day: string;
  searches: number;
  succeeded: number;
  api: number;
  hits: number;
};
