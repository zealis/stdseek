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

/**
 * 距今多久（短格式）。
 * 后台「坏了多久」比「哪一刻开始坏的」更能说明该不该现在处理，
 * 精确时间放进 title，需要时悬停可查。
 */
export function sinceText(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '—';
  const sec = Math.max(0, (Date.now() - t) / 1000);
  if (sec < 3600) return `${Math.max(1, Math.round(sec / 60))} 分钟`;
  if (sec < 86400) return `${Math.round(sec / 3600)} 小时`;
  return `${Math.round(sec / 86400)} 天`;
}

export function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s;
}

/** 字节数 → 人类可读（1024 进制）。存储量级只用于后台，保留一位小数就够。 */
export function bytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
