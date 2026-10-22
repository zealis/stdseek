// 过滤偏好持久化（规划书：设置页 localStorage，登录后再同步）。
export type Prefs = { exclude: string[]; since: number };

const KEY = 'voidxo.prefs';

export const FILTERS = [
  { key: 'ai', label: 'no AI', hint: '排除 AI 生成内容（仅过滤，不影响排序）' },
  { key: 'ads', label: 'no 广告', hint: '排除广告泛滥页' },
  { key: 'personal', label: 'no 个人', hint: '排除个人博客/个人站' },
  { key: 'platform', label: 'no 平台', hint: '排除 CSDN/知乎等 UGC 平台' },
] as const;

export const TIME_WINDOWS = [
  { label: '全部', since: 0 },
  { label: '1 天', since: 86400 },
  { label: '3 天', since: 259200 },
  { label: '1 周', since: 604800 },
  { label: '1 月', since: 2592000 },
] as const;

export function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const p = JSON.parse(raw) as Prefs;
      return { exclude: p.exclude ?? [], since: p.since ?? 0 };
    }
  } catch {
    /* 忽略损坏的本地存储 */
  }
  return { exclude: [], since: 0 };
}

export function savePrefs(p: Prefs) {
  localStorage.setItem(KEY, JSON.stringify(p));
}
