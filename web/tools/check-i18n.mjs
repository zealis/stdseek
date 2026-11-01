// 字典自检：中英两份的 key 必须一一对应。
//
// 为什么要这一步：`t()` 缺译时会回退中文，所以漏翻**不会报错**，只会静默显示中文。
// 加了新 key 忘了补英文，页面上看不出任何异常，直到有人真的切到英文才发现。
// 构建前跑一遍，把它变成一个会失败的检查。
//
// 不引测试框架：前端目前没有单测，为这一个检查装 vitest 不划算。
// 直接按行扫 `'key':` 比 import TS 稳（不用额外 loader，也不会被执行到浏览器环境）。
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const files = { 'zh-CN': 'src/app/core/i18n/zh-CN.ts', en: 'src/app/core/i18n/en.ts' };

function keysOf(rel) {
  const src = readFileSync(join(root, rel), 'utf8');
  const out = new Set();
  for (const line of src.split('\n')) {
    const m = line.match(/^\s*'([a-z0-9][a-z0-9.]*)':/i);
    if (m) out.add(m[1]);
  }
  return out;
}

const zh = keysOf(files['zh-CN']);
const en = keysOf(files.en);

const missingEn = [...zh].filter((k) => !en.has(k));
const extraEn = [...en].filter((k) => !zh.has(k));

if (missingEn.length || extraEn.length) {
  if (missingEn.length)
    console.error(`en 缺 ${missingEn.length} 条：\n  ${missingEn.join('\n  ')}`);
  if (extraEn.length)
    console.error(`en 多 ${extraEn.length} 条（zh-CN 里没有）：\n  ${extraEn.join('\n  ')}`);
  process.exit(1);
}

// 顺手查占位符：zh 里写了 {name}、en 里漏掉，运行时就会少一段信息。
const zhSrc = readFileSync(join(root, files['zh-CN']), 'utf8');
const enSrc = readFileSync(join(root, files.en), 'utf8');
const vars = (src) => {
  const map = new Map();
  for (const line of src.split('\n')) {
    const m = line.match(/^\s*'([a-z0-9][a-z0-9.]*)':\s*'([^']*)'/i);
    if (!m) continue;
    const found = [...m[2].matchAll(/\{(\w+)\}/g)].map((x) => x[1]).sort();
    if (found.length) map.set(m[1], found.join(','));
  }
  return map;
};
const zv = vars(zhSrc);
const ev = vars(enSrc);
const badVars = [...zv].filter(([k, v]) => ev.get(k) !== undefined && ev.get(k) !== v);

if (badVars.length) {
  for (const [k, v] of badVars) console.error(`占位符不一致 ${k}: zh={${v}} en={${ev.get(k)}}`);
  process.exit(1);
}

console.log(`[i18n] ${zh.size} 条 key，中英对齐`);
