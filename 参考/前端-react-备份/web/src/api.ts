// 后端 API 封装。所有请求走同源相对路径，单二进制部署时由 Go 直接提供。
export type Hit = {
  url: string;
  domain: string;
  title: string;
  description: string;
  labels: string[] | null;
  label_reasons?: Record<string, string> | null;
  score: number;
  reasons: string[] | null;
  breakdown: Record<string, number>;
  time_by_fetch: boolean;
  sort_time: number;
  prosperity: number;
};

export type SearchResp = {
  q: string; total: number; page: number; hits: Hit[];
  // 查询带「最新/最近」这类时效意图时，服务端会调高发布时间权重
  freshness_intent?: boolean;
  // 深网发现：相关但少被引用的内容（繁荣度倒数打捞）
  discover?: Hit[];
};

export type WhitelistSite = {
  id: number; domain: string; category: string; enabled: boolean;
  crawl_interval: number; last_crawled_at: number | null;
  last_status: string; last_error: string; created_at: string;
};

export type BlacklistSite = {
  id: number; domain: string; reason: string;
  reject_count: number; blocked_count: number; created_at: string;
};

export type ReviewItem = {
  id: number; url: string; domain: string; category: string;
  reason: string; precheck: string; precheck_at: number | null; status: string;
  created_at: string; reviewed_at: number | null;
};

const TOKEN_KEY = 'voidxo.adminToken';

export function getAdminToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? '';
}

export function setAdminToken(token: string) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}

// 后台请求统一带令牌（服务端 -admin-token 打印在启动日志）。
function adminFetch(input: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  const token = getAdminToken();
  if (token) headers.set('X-Admin-Token', token);
  return fetch(input, { ...init, headers });
}

async function j<T>(r: Response): Promise<T> {
  const text = await r.text();
  const data = text ? JSON.parse(text) : {};
  if (!r.ok) throw new Error((data as { error?: string }).error || r.statusText);
  return data as T;
}

export function search(q: string, opts: {
  exclude?: string[]; since?: number; page?: number; size?: number;
} = {}): Promise<SearchResp> {
  const sp = new URLSearchParams({ q });
  if (opts.exclude?.length) sp.set('no', opts.exclude.join(','));
  if (opts.since) sp.set('since', String(opts.since));
  sp.set('page', String(opts.page ?? 1));
  sp.set('size', String(opts.size ?? 10));
  return fetch(`/api/search?${sp}`).then(j<SearchResp>);
}

// 用户提交站点（公开接口，进审核队列）。服务端会同步做一次预检，失败不阻断提交。
// 返回的 ticket 是查询凭证：自增 id 可枚举，不能拿来做公开查询。
export function submitSite(body: { url: string; category: string; reason?: string }) {
  return fetch('/api/submit', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string; ticket: string }>);
}

export type SubmissionStatus = {
  domain: string; category: string; status: string;
  status_text: string; created_at: number; reviewed_at?: number;
};

export function getSubmission(ticket: string) {
  return fetch(`/api/submission?ticket=${encodeURIComponent(ticket)}`)
    .then(j<SubmissionStatus>);
}

// 标签误判反馈：verdict 为 false_positive（打错了）或 confirmed（打得对）。
// 服务端不即时改索引——单个判断不足以推翻规则，先累积样本再统一调规则重建。
export function submitLabelFeedback(body: {
  url: string; label: string; verdict: 'false_positive' | 'confirmed'; note?: string;
}) {
  return fetch('/api/label-feedback', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string }>);
}

export type LabelFeedback = {
  id: number; url: string; domain: string; label: string;
  verdict: string; note: string; created_at: string; updated_at: string;
};

export function listLabelFeedback(kw: string, verdict: string, page: number, size = 10) {
  const sp = new URLSearchParams({ kw, verdict, page: String(page), size: String(size) });
  return adminFetch(`/admin/label-feedback?${sp}`)
    .then(j<{ total: number; items: LabelFeedback[]; by_label: Record<string, number> | null }>);
}

export function deleteLabelFeedback(ids: number[]) {
  return adminFetch('/admin/label-feedback/delete', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids }),
  }).then(j<{ deleted: number }>);
}

// 抓取时保存的原文快照（证据链）。返回的 html 是不可信内容，
// 必须放进 sandbox iframe 渲染，禁止直接 innerHTML 或 dangerouslySetInnerHTML。
export function pageSnapshot(url: string) {
  return fetch(`/api/page?url=${encodeURIComponent(url)}`)
    .then(j<{ url: string; key: string; size: number; html: string }>);
}

export type PrecheckResult = {  url: string;
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

// 提交前预检：可达性、robots、内容类型；黑名单历史由后端合并返回。
export function precheck(url: string) {
  return adminFetch('/admin/precheck', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }),
  }).then(j<{ precheck: PrecheckResult; blacklisted: boolean }>);
}

export function listWhitelist(kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ kw, page: String(page), size: String(size) });
  return adminFetch(`/admin/whitelist?${sp}`).then(j<{ total: number; items: WhitelistSite[] }>);
}

export function addWhitelist(body: { domain: string; category: string; crawl_interval?: number }) {
  return adminFetch('/admin/whitelist', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string }>);
}

export function toggleWhitelist(id: number, enabled: boolean) {
  return adminFetch(`/admin/whitelist/${id}/toggle`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }),
  }).then(j<{ status: string }>);
}

export function listBlacklist(kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ kw, page: String(page), size: String(size) });
  return adminFetch(`/admin/blacklist?${sp}`).then(j<{ total: number; items: BlacklistSite[] }>);
}

export function addBlacklist(body: { domain: string; reason: string }) {
  return adminFetch('/admin/blacklist', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string }>);
}

export function removeBlacklist(id: number) {
  return adminFetch(`/admin/blacklist/${id}`, { method: 'DELETE' }).then(j<{ status: string }>);
}

export function listReview(status: string, kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ status, kw, page: String(page), size: String(size) });
  return adminFetch(`/admin/review?${sp}`).then(j<{ total: number; items: ReviewItem[] }>);
}

// 对审核队列条目重新预检（结果写回队列）
export function precheckReview(id: number) {
  return adminFetch(`/admin/review/${id}/precheck`, { method: 'POST' })
    .then(j<{ id: number; precheck: PrecheckResult; blacklisted: boolean }>);
}

export function approveReview(id: number) {
  return adminFetch(`/admin/review/${id}/approve`, { method: 'POST' }).then(j<{ status: string }>);
}

export function rejectReview(id: number, reason: string) {
  return adminFetch(`/admin/review/${id}/reject`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reason }),
  }).then(j<{ status: string }>);
}

// 功能开关：后台改、即时生效、落 SQLite（见 internal/api/features.go）。
// 标题与说明由服务端下发，前端不维护开关清单——新增开关只需改 Go 侧一处。
export type FeatureFlag = {
  key: string;
  label: string;
  desc: string;
  enabled: boolean;
  danger?: boolean;
  updated_at?: number;
};

export function listFeatures() {
  return adminFetch('/admin/settings').then(j<{ features: FeatureFlag[] }>);
}

export function setFeature(key: string, enabled: boolean) {
  return adminFetch('/admin/settings/feature', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key, enabled }),
  }).then(j<{ key: string; enabled: boolean }>);
}

// 公开接口：前台据此决定是否渲染 AI 摘要入口。失败时按「全关」处理，
// 免得开关接口抖一下就在结果页露出一个点了会报错的按钮。
export function publicFeatures() {
  return fetch('/api/features')
    .then(j<{ features: Record<string, boolean> }>)
    .catch(() => ({ features: {} as Record<string, boolean> }));
}

// ---- 证据链摘要（功能 D） ----
// 每条结论都带 citations；citations 为空 = 无法核验，UI 必须标「未验证」。
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

export function summarize(url: string, q: string) {
  return fetch('/api/summary', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url, q }),
  }).then(j<SummaryResp>);
}

// 运行状态：首次部署时索引可能是空的，首页据此给引导而不是让人对着 0 结果发呆。
export type Status = { documents: number; sites: number; features: Record<string, boolean> };

export function status() {
  return fetch('/api/status')
    .then(j<Status>)
    .catch(() => ({ documents: 0, sites: 0, features: {} as Record<string, boolean> }));
}
