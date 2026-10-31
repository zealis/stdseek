/**
 * 首屏状态：服务端把这个对象写进 index.html。
 *
 * 有它就不用再发 /api/features 与 /api/status——那两个请求由 main.js 发起，
 * 会把「HTML → JS → XHR」串成一条关键链（Lighthouse 报 1.2 秒）。
 * 取不到（老版本二进制、注入失败）时两个字段都是 undefined，调用方退回发请求。
 */
export type BootSeed = {
  features?: Record<string, boolean>;
  /** 索引里的文档数（用于决定空库引导是否出现） */
  documents?: number;
};

export function readBoot(): BootSeed {
  const w = window as unknown as { __VOIDXO_BOOT__?: BootSeed };
  return w.__VOIDXO_BOOT__ ?? {};
}
