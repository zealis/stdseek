/*
 * voidxo 的 service worker。
 *
 * 这个文件不是静态发布的：internal/api/pwa.go 在响应 /sw.js 时把下面两个占位符
 * 替换掉（预缓存清单随前端构建变化，版本号由清单+站名+图标算出），
 * 所以这里不要写死资源名，也不要自己算版本。
 *
 * 缓存分两层：
 *   vx-shell-<版本>  app 外壳（入口 js/css + 离线页 + manifest + 图标），安装时一次性预取
 *   vx-runtime       懒加载 chunk 等运行期遇到的东西，先进先出裁到 RUNTIME_LIMIT 条
 *
 * 刻意不做的事：
 *   - 不缓存页面 HTML。index.html 是服务端注入 SEO/站名/页脚代码的，
 *     缓存住就会出现「后台改了站名，装了 PWA 的人半年后还是旧名字」。
 *   - 不拦截 /api 与 /admin。搜索结果是实时数据，离线时假装有结果比报错更糟。
 *   - 不在 install 里 skipWaiting。新版本要等页面提示用户点刷新（见 message 处理），
 *     否则用户正在输入时页面会自己刷掉。
 */

const VERSION = '__VX_VERSION__';
const SHELL_CACHE = 'vx-shell-' + VERSION;
const RUNTIME_CACHE = 'vx-runtime';
const PRECACHE = __VX_PRECACHE__;
const OFFLINE_URL = '/offline.html';
const RUNTIME_LIMIT = 60;

self.addEventListener('install', (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(SHELL_CACHE);
      // 逐条 add 而不是 addAll：清单里可能带可选图标（没上传时是内置那份），
      // 一条 404 不该让整个安装在浏览器里变成 failed。
      await Promise.allSettled(PRECACHE.map((url) => cache.add(new Request(url, { cache: 'reload' }))));
    })(),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys();
      await Promise.all(
        keys.filter((k) => k.startsWith('vx-shell-') && k !== SHELL_CACHE).map((k) => caches.delete(k)),
      );
      await self.clients.claim();
    })(),
  );
});

// 页面点了「刷新」才会走到这里。
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'VX_SKIP_WAITING') void self.skipWaiting();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;

  const url = new URL(req.url);
  // 跨域的（维基摘要、地图、对象存储）一律不碰：它们的缓存策略由各自的响应头决定。
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/admin/')) return;
  if (url.pathname === '/metrics' || url.pathname === '/healthz') return;

  // 导航请求（首开、刷新、从桌面图标启动）：先网络，断了给离线页。
  if (req.mode === 'navigate') {
    event.respondWith(networkFirst(req));
    return;
  }

  if (isStatic(url.pathname)) {
    event.respondWith(cacheFirst(req));
  }
});

async function networkFirst(req) {
  try {
    return await fetch(req);
  } catch {
    return (await caches.match(OFFLINE_URL)) || Response.error();
  }
}

async function cacheFirst(req) {
  const hit = await caches.match(req);
  if (hit) return hit;

  const res = await fetch(req);
  // 只留同源、成功、非 opaque 的响应；带 Set-Cookie 的也别进缓存。
  if (res.status === 200 && res.type === 'basic' && !res.headers.has('Set-Cookie')) {
    const cache = await caches.open(RUNTIME_CACHE);
    await cache.put(req, res.clone());
    void trimRuntime();
  }
  return res;
}

function isStatic(p) {
  return /\.(js|css|woff2?|svg|ico|png|webp|jpe?g|webmanifest)$/.test(p);
}

// 懒加载 chunk 会随版本累积（每版一套新哈希），按写入顺序裁掉最老的。
async function trimRuntime() {
  const cache = await caches.open(RUNTIME_CACHE);
  const keys = await cache.keys();
  for (const k of keys.slice(0, Math.max(0, keys.length - RUNTIME_LIMIT))) {
    await cache.delete(k);
  }
}
