/**
 * 品牌改名（voidxo → stdseek）带来的本地存储键迁移。
 *
 * 为什么不直接换键：换完老用户的表现是「主题被重置、后台令牌要重新输入、
 * 搜索偏好回到默认」，看起来像我们把设置清了。这里在启动最早期把旧键的值
 * 搬到新键——只在新键缺失时搬，绝不覆盖新版本里已有的值；也不删旧键，
 * 万一回滚到旧前端，它还能读到自己那份。
 *
 * 调用点必须是 main.ts 里 bootstrap 之前：Angular 服务一构造就会读这些键，
 * 放进组件构造函数就已经晚了。
 */
const RENAMED: ReadonlyArray<readonly [string, string]> = [
  ['voidxo.prefs', 'stdseek.prefs'],
  ['voidxo.theme', 'stdseek.theme'],
  ['voidxo.pref.overview', 'stdseek.pref.overview'],
  ['voidxo.adminToken', 'stdseek.adminToken'],
];

export function migrateLegacyStorage(): void {
  try {
    for (const [oldKey, newKey] of RENAMED) {
      if (localStorage.getItem(newKey) === null) {
        const legacy = localStorage.getItem(oldKey);
        if (legacy !== null) localStorage.setItem(newKey, legacy);
      }
    }
  } catch {
    /* 隐私模式或禁用存储：读不到就用默认值，不该因为迁移失败中断启动 */
  }
}
