import { signal } from '@angular/core';
import zhCN from './i18n/zh-CN';

/**
 * 前台文案的多语言（i18n）。
 *
 * 为什么不用 `@angular/localize`：那套是**构建期**方案，一个语言一份完整产物，
 * 切换要整页重载；而这个站的语言开关就在顶栏，用户随手点，重载等于丢掉当前结果。
 * 自己实现的核心只有三样：一份字典、一个 signal、一个 `t()`。
 *
 * 边界（很重要）：**只翻外壳，不翻语料**。结果卡的标题、摘要、站点名是中文站点的
 * 原文，翻译它们等于伪造来源。所以这里没有、也不该有「把搜索结果翻成英文」的入口。
 */

export type Locale = 'zh-CN' | 'en';

/** 顶栏切换的顺序。加语言：这里补一项 + 一份同 key 的字典。 */
export const LOCALES: readonly Locale[] = ['zh-CN', 'en'];

/** 语言写在 localStorage：首屏由 main.ts 先读它，再 bootstrap。
 *  cookie 同步写一份，留给以后服务端按语言注入 <html lang> / SEO meta。 */
const STORAGE_KEY = 'stdseek.lang';
const COOKIE_KEY = 'stdseek.lang';

type Dict = Record<string, string>;

/** 非源语言的字典按需加载：中文直接打进主包（它是回退表），英文多一次请求。 */
const LAZY: Record<Exclude<Locale, 'zh-CN'>, () => Promise<Dict>> = {
  en: () => import('./i18n/en').then((m) => m.default),
};

const locale = signal<Locale>('zh-CN');
const table = signal<Dict>({});
let loading: Promise<void> | null = null;

/** 当前语言（模板里读它会跟着切换重渲染）。 */
export const currentLocale = locale.asReadonly();

/** 系统语言是中文就中文，其余一律英文——不猜「繁体/日文」这类，
 *  猜错了用户还得再找开关。 */
export function detectLocale(): Locale {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === 'zh-CN' || saved === 'en') return saved;
  } catch {
    /* 隐私模式下 localStorage 会抛，忽略即可 */
  }
  return navigator.language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en';
}

/**
 * 取一条文案。`vars` 替换 `{name}` 这类占位符——**不要用字符串拼接**：
 * 中英语序不同，"共 3 条" 和 "3 results" 的位置对不上。
 *
 * 缺译回退顺序：当前语言 → 中文原文 → key 本身（最后这层只会在开发期看到）。
 */
export function t(key: string, vars?: Record<string, string | number>): string {
  let s = table()[key] ?? zhCN[key] ?? key;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      s = s.split(`{${k}}`).join(String(v));
    }
  }
  return s;
}

/**
 * 切换语言。`persist=false` 只用于启动时的初始化（那时已经是存下来的值，不必回写）。
 *
 * 不做整页重载：`table` 是 signal，模板与 `document.title` 会自己重算。
 */
export async function useLocale(next: Locale, persist = true): Promise<void> {
  locale.set(next);
  document.documentElement.lang = next;
  if (next === 'zh-CN') {
    table.set({});
  } else {
    const load = LAZY[next];
    table.set(await load());
  }
  if (!persist) return;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    /* 存不下就算了，本次会话照样是选中的语言 */
  }
  // 一年有效期：这个偏好跟主题一样，没必要反复问。
  document.cookie = `${COOKIE_KEY}=${next}; path=/; max-age=31536000; SameSite=Lax`;
}

/** 在中文 / 英文之间切换（顶栏那颗地球图标）。 */
export async function toggleLocale(): Promise<void> {
  await useLocale(locale() === 'zh-CN' ? 'en' : 'zh-CN');
}

/**
 * 启动前调用：把语言定下来再 bootstrap。
 *
 * 英文用户要多等一次 10KB 的字典请求，换来的是首帧就是英文——否则会先闪一下
 * 中文骨架再整体换成英文，比晚几十毫秒难看得多。中文用户没有任何额外请求。
 */
export async function initLocale(): Promise<void> {
  const next = detectLocale();
  if (next === 'zh-CN') {
    locale.set('zh-CN');
    document.documentElement.lang = 'zh-CN';
    return;
  }
  if (loading) return loading;
  loading = useLocale(next, false);
  await loading;
  loading = null;
}
