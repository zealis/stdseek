// 后端 API 封装。所有请求走同源相对路径，单二进制部署时由 Go 直接提供。
export type Hit = {
  url: string;
  domain: string;
  title: string;
  description: string;
  labels: string[] | null;
  score: number;
  reasons: string[] | null;
  breakdown: Record<string, number>;
  time_by_fetch: boolean;
  sort_time: number;
  prosperity: number;
};

export type SearchResp = { q: string; total: number; page: number; hits: Hit[] };

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
