// 展示层格式化。表格与卡片里的字段要短且稳定，避免同一页面出现多种时间写法。

const pad = (n: number): string => String(n).padStart(2, '0');

/**
 * 相对时间的展示窗口：超过这么多天就退回具体日期。
 *
 * 为什么是 7 天：一周内的「30分钟前」比「2026-10-31」更能判断新鲜度；
 * 再往前的相对量词（「1234天前」）反而没有信息量，不如直接给日期。
 */
const RELATIVE_WINDOW_DAYS = 7;

/** 收录/发布时间。byFetch 说明该时间来自抓取时刻而非原文标注。 */
export function resultTime(ts: number, byFetch: boolean): string {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const suffix = byFetch ? ' 收录' : '';
  const diff = Date.now() - d.getTime();

  // 只有"过去"才用相对写法。未来时间（源站标注错误或本机时钟偏差）
  // 走日期分支，避免出现「-3分钟前」这种读不懂的字符串。
  if (diff >= 0 && diff < RELATIVE_WINDOW_DAYS * 86_400_000) {
    return relativeTime(diff) + suffix;
  }

  const s = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return s + suffix;
}

/** 毫秒差转「刚刚 / N分钟前 / N小时前 / N天前」。 */
function relativeTime(diff: number): string {
  const minutes = Math.floor(diff / 60_000);
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes}分钟前`;

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}小时前`;

  return `${Math.floor(hours / 24)}天前`;
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

/** 时间戳（秒）转日期（不含时分）。「自 X 起」这类起点说明用。 */
export function dayOf(ts: number): string {
  if (!ts) return '—';
  return new Date(ts * 1000).toLocaleDateString('zh-CN');
}

/** 整数千分位。累计值很快就会到六位数，不分组的数字读不出量级。 */
export function count(n: number): string {
  return (n || 0).toLocaleString('zh-CN');
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
 * 时长（抓取间隔这类值）：不到 1 小时按分钟、不到 1 天按小时，否则按天。
 * 直接四舍五入成小时会把 30 分钟显示成「1 小时」——差一倍，看的人会以为配错了。
 */
export function durationText(sec: number): string {
  if (sec <= 0) return '—';
  if (sec < 3600) return `${Math.round(sec / 60)} 分钟`;
  if (sec < 86400) return `${Math.round(sec / 3600)} 小时`;
  return `${Math.round(sec / 86400)} 天`;
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
