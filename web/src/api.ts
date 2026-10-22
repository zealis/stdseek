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
  reason: string; precheck: string; status: string;
  created_at: string; reviewed_at: number | null;
};

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

export function listWhitelist(kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ kw, page: String(page), size: String(size) });
  return fetch(`/admin/whitelist?${sp}`).then(j<{ total: number; items: WhitelistSite[] }>);
}

export function addWhitelist(body: { domain: string; category: string; crawl_interval?: number }) {
  return fetch('/admin/whitelist', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string }>);
}

export function toggleWhitelist(id: number, enabled: boolean) {
  return fetch(`/admin/whitelist/${id}/toggle`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }),
  }).then(j<{ status: string }>);
}

export function listBlacklist(kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ kw, page: String(page), size: String(size) });
  return fetch(`/admin/blacklist?${sp}`).then(j<{ total: number; items: BlacklistSite[] }>);
}

export function addBlacklist(body: { domain: string; reason: string }) {
  return fetch('/admin/blacklist', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }).then(j<{ status: string }>);
}

export function removeBlacklist(id: number) {
  return fetch(`/admin/blacklist/${id}`, { method: 'DELETE' }).then(j<{ status: string }>);
}

export function listReview(status: string, kw: string, page: number, size = 10) {
  const sp = new URLSearchParams({ status, kw, page: String(page), size: String(size) });
  return fetch(`/admin/review?${sp}`).then(j<{ total: number; items: ReviewItem[] }>);
}

export function approveReview(id: number) {
  return fetch(`/admin/review/${id}/approve`, { method: 'POST' }).then(j<{ status: string }>);
}

export function rejectReview(id: number, reason: string) {
  return fetch(`/admin/review/${id}/reject`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reason }),
  }).then(j<{ status: string }>);
}
