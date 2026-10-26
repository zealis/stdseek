// 展示层格式化。表格与卡片里的字段要短且稳定，避免同一页面出现多种时间写法。

const pad = (n: number): string => String(n).padStart(2, '0');

/** 收录/发布时间。byFetch 说明该时间来自抓取时刻而非原文标注。 */
export function resultTime(ts: number, byFetch: boolean): string {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const s = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return byFetch ? `${s} 收录` : s;
}

/** 表格用短格式：年份在后台表格里是噪声，MM-DD HH:mm 足够定位一条记录。 */
export function shortTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 时间戳（秒）转完整本地时间。 */
export function fullTime(ts: number): string {
  if (!ts) return '—';
  return new Date(ts * 1000).toLocaleString('zh-CN');
}

/** 日期（不含时分），用于反馈列表这类低频更新的时间列。 */
export function dayTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleDateString('zh-CN');
}

export function hours(sec: number): string {
  return `${Math.round(sec / 3600)} 小时`;
}

export function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s;
}
