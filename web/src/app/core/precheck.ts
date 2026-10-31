/**
 * 预检结论的措辞。
 *
 * 为什么需要单独一个模块：以前所有非 2xx/3xx 都显示成「不可达 403」，读者
 * 第一反应是查网络、查 DNS、查防火墙——而 403 恰恰说明 TCP/TLS/HTTP 全通了，
 * 是站点主动拒绝（登录墙、屏蔽爬虫 UA、机房 IP 段被封）。排查方向一开始就跑偏。
 *
 * 同一个坑还有 404（页面不存在，跟网络无关）与 5xx（站点自己出错）。这里按
 * 状态码分开说，且不改变任何判定逻辑：reachable 仍然是 2xx/3xx。
 */

export interface PrecheckLike {
  reachable?: boolean;
  status_code?: number;
  error?: string;
  /** 谁在拦：CDN 厂商 + Cloudflare 的挑战/封禁标记，只在 4xx/5xx 时有值 */
  blocked_by?: string;
}

/** 一句话结论，用于徽标与详情。 */
export function precheckStatus(p: PrecheckLike): string {
  const code = p.status_code ?? 0;

  if (!code) {
    // 没有状态码 = 请求根本没走完。error 里有网络层的具体原因（超时/DNS/证书），
    // 那些由调用方单独展示，这里只给结论。
    if (p.error) return '无法连接';
    return p.reachable ? '可达' : '不可达';
  }

  if (p.reachable) return `可达 ${code}`;
  // 4xx/5xx 追一句「谁在拦」：同样是 403，Cloudflare 挑战和登录墙的处理方式完全不同。
  const hint = p.blocked_by ? ` · ${p.blocked_by}` : '';
  if (code === 401 || code === 403) return `被拒绝 ${code}${hint}`;
  if (code === 404 || code === 410) return `不存在 ${code}`;
  if (code === 429) return `限流 ${code}${hint}`;
  if (code >= 500) return `站点异常 ${code}${hint}`;
  return `异常 ${code}`;
}

/**
 * 是不是「站点主动拒绝」。
 *
 * 提交页要用它换一句提示：这类站点加进白名单也会一篇都抓不到，
 * 而且原因不在我们这边（要么要登录，要么封了爬虫/机房 IP）。
 */
export function precheckRejected(p: PrecheckLike): boolean {
  const code = p.status_code ?? 0;
  return !p.reachable && (code === 401 || code === 403 || code === 429);
}
