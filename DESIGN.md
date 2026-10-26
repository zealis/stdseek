# voidxo 前端设计规范（DESIGN.md）

> 适用范围：`web/` 下的 Angular 前端（Angular 22，zoneless，signals-first）。
> 本文档是视觉与交互的**唯一事实来源**：改颜色、改间距、改圆角，先改这里的令牌表，
> 再同步 `web/src/styles.css` 里的 CSS 自定义属性。实现与本文冲突时，以本文为准；
> 本文遗漏时，按「设计原则」推导，然后把结论补进来。

---

## 0. 设计原则

1. **内容优先**：这是搜索引擎。界面是玻璃，内容是风景——任何装饰不得比结果本身更抢眼。
2. **可解释**：排序理由、标签理由、证据链编号是一等公民，用强调色呈现而不是藏在 tooltip 里。
3. **单二进制、零外部依赖**：不用任何 CDN 字体 / 图标库 / 远程资源。全部资源打进二进制，
   断网可用。这条产品红线直接决定了「系统字体栈 + 内联 SVG 图标」两个技术选型。
4. **移动优先（mobile-first）**：CSS 从 375px 开始写，桌面是放大而非缩小。
   触控目标、安全区、底部导航、底部弹层是移动端的一等公民。
5. **克制**：Swiss / Minimalism 基调（ui-ux-pro-max 推荐方向）。动效只用于反馈与空间连续性，
   不做纯装饰动画。深浅两套主题都达到 WCAG AA。
6. **界面零冗余文本**（用户铁律）：不放解释性描述——规则、流程说明放文档不进 UI；
   界面文字只保留功能必需（操作项、状态、防错提示）。图标按钮不放文字，语义走 aria-label。

---

## 1. 色彩

### 1.1 品牌色

品牌色 v2 起切换为原型的**紫罗兰（violet #8514F5）**——angular.dev 语言的主强调色，
teal 全线退役。不引入色阶包：soft/line 用 `color-mix` 从主色派生（原型的做法），
暗色用提亮档保对比度。

| 令牌 | 浅色值 | 暗色值 | 用途 |
|---|---|---|---|
| `--brand` | `#8514F5`（6.2:1） | `#A78BFA`（6.3:1） | 链接、强调文字、激活态 |
| `--brand-hover` | `#7C0EE0` | `#C4B5FD` | 链接/文字 hover |
| `--brand-solid` | `#8514F5` | `#7C3AED` | 实色按钮底、当前页 |
| `--brand-on-solid` | `#FFFFFF`（6.2:1） | `#FFFFFF`（5.7:1） | 实色底上的文字 |
| `--brand-subtle` | `color-mix(#8514F5 8%, #fff)` | `rgba(167,139,250,.14)` | 强调底色（chips、原文摘要块、头像） |
| `--brand-line` | `color-mix(#8514F5 32%, #fff)` | `rgba(167,139,250,.45)` | 强调边线、聚焦边 |
| `--focus-ring` | `#8514F5` | `#A78BFA` | 键盘焦点环 |

### 1.2 中性色

冷灰（slate 倾向），中性不偏色。`n-*` 为原始刻度，语义令牌在 1.4 节引用它。

| 令牌 | 浅色值 | 暗色值 |
|---|---|---|
| `--n-0` | `#FFFFFF` | `#0D131C`（页面底） |
| `--n-25` | `#FCFCFD` | `#10161F` |
| `--n-50` | `#F9FAFB` | `#151C28` |
| `--n-100` | `#F2F4F7` | `#1B2330` |
| `--n-200` | `#E4E7EC` | `#232C3B` |
| `--n-300` | `#D0D5DD` | `#2E3949` |
| `--n-400` | `#98A2B3` | `#66748A` |
| `--n-500` | `#667085` | `#8B99AD` |
| `--n-600` | `#475467` | `#B3BECB` |
| `--n-700` | `#344054` | `#D0D7DF` |
| `--n-800` | `#1D2939` | `#E6EBF0` |
| `--n-900` | `#101828` | `#F5F8FA` |

### 1.3 语义色

`danger / warning / success` 三族，每族四个槽位：文字色、subtle 底、边线、实色底。
文字色全部在白/黑底上验证过 ≥ 4.5:1。

| 令牌 | 浅色 | 暗色 |
|---|---|---|
| `--danger` | `#D92D20`（4.8:1） | `#F97066`（6.7:1） |
| `--danger-subtle` | `#FEF3F2` | `rgba(249,112,102,.12)` |
| `--danger-line` | `#FDA29B` | `rgba(249,112,102,.45)` |
| `--warning` | `#B54708`（5.4:1） | `#FDB022`（10.1:1） |
| `--warning-subtle` | `#FFFAEB` | `rgba(253,176,34,.12)` |
| `--warning-line` | `#FEDF89` | `rgba(253,176,34,.45)` |
| `--success` | `#067647`（5.7:1） | `#32D583`（9.8:1） |
| `--success-subtle` | `#ECFDF3` | `rgba(50,213,131,.12)` |
| `--success-line` | `#ABEFC6` | `rgba(50,213,131,.45)` |

规则：**语义色永远成组使用**（文字 + subtle 底 + 边线），不允许拿 `--warning` 直接当
大面积底色；scrim（弹层遮罩）用 `rgba(10,14,20,.5)`，暗色下 `.65`。

### 1.4 语义令牌（组件只允许用这一层）

组件样式**禁止**引用原始色阶，只能用下面的语义令牌。这是暗色模式一次适配的基础。

| 语义令牌 | 浅色模式 | 暗色模式 |
|---|---|---|
| `--bg` | `--n-50`（页面底） | `--n-0`（#0D131C） |
| `--surface` | `--n-0`（卡片/输入框底） | `#151C28` |
| `--sunken` | `--n-100`（下陷底：hover、代码块） | `#1B2330` |
| `--text-1` | `--n-900` | `--n-900`（#F5F8FA） |
| `--text-2` | `--n-600` | `--n-500` |
| `--text-3` | `--n-500` | `--n-400` |
| `--line` | `--n-200` | `#232C3B` |
| `--line-strong` | `--n-300` | `#2E3949` |
| `--grid-line` | `#0000000D`（5% 黑） | `#FFFFFF0F`（6% 白） |
| 品牌 7 项 | 见 §1.1（violet 系） | 见 §1.1 |

暗色模式的实色按钮用 `#7C3AED`（violet-600）而非浅紫 `#A78BFA`——白字对 #A78BFA
只有 ~2.5:1，对 #7C3AED 是 5.7:1。

### 1.5 品牌渐变（仅限品牌时刻）

三个渐变令牌与主强调同族，**只允许**出现在：`</>` logo 章、首页 hero 标题、桌面导航
激活下划线、深网发现容器描边与标题、后台侧栏激活左条、原文摘要左条、结果卡 hover 描边。
组件不得把渐变当普通颜色用（排序得分条的 `--viz-*` 三色是数据可视化特例，见 §10）。

| 令牌 | 值 | 用途 |
|---|---|---|
| `--grad-brand` | `linear-gradient(90deg,#8514f5,#f637e3)` | logo 章底、渐变描边、原文摘要左条 |
| `--grad-text` | `linear-gradient(90deg,#f637e3,#8514f5 52%,#0546ff)` | 首页标题渐变字 |
| `--grad-underline` | `linear-gradient(90deg,#f637e3,#8514f5,#0546ff)` | 导航激活下划线（`border-image`） |

渐变文字只用于 ≥22px 的展示型标题与深网发现这类区块标签（AA 大文本门槛），
正文与按钮禁止使用。原型的 Inter/DM Mono/Material Symbols webfonts **不引入**——
单二进制断网可用是产品红线，字体维持 §2.1 系统栈。

### 1.6 对比度硬线

- 正文、链接、按钮文字：≥ 4.5:1（AA）。上表已逐项验证。
- `--text-3` 只用于 ≥12px 的辅助说明；不得用于按钮文字或关键信息。
- 禁止删除焦点环、禁止灰上加灰的低对比占位文字（placeholder 用 `--text-3`）。

---

## 2. 字体

### 2.1 字体栈

零外部依赖 ⇒ **系统字体栈**，不引入任何 webfont（CJK webfont 动辄数十 MB，且断网不可用）。

```css
--font-sans: system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB',
             'Microsoft YaHei', 'Noto Sans CJK SC', sans-serif;
--font-mono: ui-monospace, 'Cascadia Code', SFMono-Regular, Menlo, Consolas,
             'Courier New', monospace;
```

- 等宽用于：查询码、域名、时间戳、数字列（`font-variant-numeric: tabular-nums`）。
- 中文不使用斜体；强调用颜色与字重表达。

### 2.2 字号阶梯

移动端正文比桌面大一号（`--fs-base` 随断点切换 14 → 15px），其余档位固定。

| 令牌 | 值 | 行高 | 用途 |
|---|---|---|---|
| `--fs-xs` | 12px | 1.5 | 辅助说明、表头、时间戳 |
| `--fs-sm` | 13px | 1.5 | 次要 meta、chip |
| `--fs-base` | 14px（≥768px）/ **15px**（<768px） | 1.65 | 正文、列表、表单 |
| `--fs-md` | 16px | 1.6 | 强调正文、弹层标题辅助 |
| `--fs-lg` | 18px | 1.45 | 卡片标题、区块标题 |
| `--fs-xl` | 22px | 1.35 | 页面标题 |
| `--fs-hero` | `clamp(28px, 7vw, 40px)` | 1.25 | 首页主标题 |

- 字重只用三档：400（正文）/ 500（标题、强调）/ 600（品牌、页面标题）。
- 最小可读字号 12px；移动端正文 ≥15px，输入框字号 ≥16px（防 iOS 聚焦自动放大）。
- 标题不加 letter-spacing；`--fs-hero` 允许 `-0.02em`。

---

## 3. 间距

4px 基准网格。所有 margin / padding / gap 必须落在令牌刻度上（允许 2px 半档给 chip 类小件）。

| 令牌 | 值 | 典型用途 |
|---|---|---|
| `--sp-0-5` | 2px | 图标与文字微调 |
| `--sp-1` | 4px | chip 间距、紧凑行 |
| `--sp-1-5` | 6px | 按钮内图标间距 |
| `--sp-2` | 8px | 控件间最小间距（触控安全下限） |
| `--sp-3` | 12px | 卡片内段落、列表行距 |
| `--sp-4` | 16px | 卡片内边距（移动端）、表单组间距 |
| `--sp-5` | 20px | 卡片内边距（桌面）、区块间距 |
| `--sp-6` | 24px | 页面水平内边距（桌面） |
| `--sp-8` | 32px | 区块分隔、页面垂直节奏 |
| `--sp-12` | 48px | 页脚上边距、空状态 |
| `--sp-16` | 64px | 首页 hero 呼吸空间 |

- 页面水平内边距：移动端 16px（`--sp-4`），≥768px 24px（`--sp-6`）。
- 结果列表行间距：12px；卡片统一内边距 16px（移动）/ 20px（桌面）。
- 相邻触控目标间距 ≥ 8px（`--sp-2`），见 §8。

---

## 4. 圆角

v2 起**控件走 4px 直角感**（angular.dev 药方样式），面性容器保持柔和——两套并存：

| 令牌 | 值 | 用途 |
|---|---|---|
| `--r-xs` | 4px | 按钮、输入框、select、chip、分页钮、菜单项、cite 徽标 |
| `--r-sm` | 6px | 小菜单、logo 章、域名头像、notice、代码块 |
| `--r-md` | 8px | tooltip、强调块 |
| `--r-lg` | 10px | 卡片、弹层（桌面）、表格容器、原文摘要块容器 |
| `--r-xl` | 20px | 底部弹层（移动端顶部两角） |
| `--r-full` | 999px | **搜索框**（药丸形，产品唯一的全圆角控件）、开关滑轨 |

规则：
- 搜索框是产品最核心的控件，唯一保留 `--r-full` 药丸形（浅色加 `--line` 边、聚焦转
  `--brand-line`）；原型的小圆角搜索框**不采纳**。
- chip 一律 `--r-xs`；嵌套圆角遵循「内圆角 = 外圆角 − 内边距」，绝对值不小于 4px。
- 弹层：桌面 `--r-lg` 四角；移动端从底部滑出，只圆顶部两角 `--r-xl`。

---

## 5. 边框、阴影与层级

- 边框统一 **1px**（弃用上一代的 0.5px——高分屏上渲染发虚，且 `border: 0.5px` 在部分
  Android WebView 上会取整为 0）。浅色模式 1px 边框是主要分层手段；暗色模式靠面差分层。
- 阴影只给浮层（弹层、下拉菜单、底部导航），页面内容不用阴影：

| 令牌 | 值 |
|---|---|
| `--shadow-sm` | `0 1px 2px rgba(16,24,40,.06)` |
| `--shadow-md` | `0 8px 24px -8px rgba(16,24,40,.18)` |
| `--shadow-lg` | `0 20px 48px -12px rgba(16,24,40,.28)` |
| 暗色 | 同形状，颜色换 `rgba(0,0,0,.5)` 系 |

- z-index 阶梯：内容 0 / sticky 头部 20 / 底部导航 30 / 下拉菜单 40 / 弹层 50 / 快照遮罩内元素不受此表约束。
- 滚动条不定制（尊重系统样式），仅表格容器 `overflow-x: auto`。

---

## 6. 控件尺寸

| 令牌 | 值 | 用途 |
|---|---|---|
| `--h-sm` | 30px（触屏 34px） | 工具栏小按钮、chip 按钮 |
| `--h-md` | 36px（触屏 40px） | 标准按钮、输入框、select、分页钮 |
| `--h-lg` | 44px | 主操作大按钮 |
| `--nav-h` | 56px | 顶栏高度 |
| `--tabbar-h` | 56px + `env(safe-area-inset-bottom)` | 移动端底部导航 |

搜索框：药丸容器内输入区 40px，整体约 52px（v2 从 60px 降档，移动端不低于触控标准）。

触控规则（CRITICAL）：
- 所有可点元素实际触控区 ≥ 44×44px（含 padding 与透明扩展）；视觉高度可小于 44，
  但点击热区必须够。相邻目标间距 ≥ 8px。
- `touch-action: manipulation`（消灭 300ms 点按延迟与双击缩放误触）；
  `-webkit-tap-highlight-color: transparent`，按压态改用 `--sunken` 背景 + 120ms 过渡表达。
- 输入框在移动端字号 16px 起步。
- 开关滑块 44×24 可视，热区 44×44。
- 页面禁用下拉刷新误触：`overscroll-behavior-y: contain`（仅结果页容器）。

---

## 7. 断点与布局

mobile-first，三档断点：

| 断点 | 宽度 | 变化 |
|---|---|---|
| 默认 | < 640px | 单列；底部导航；正文 15px；页面内边距 16px |
| `--bp-sm` | ≥ 640px | 底部导航隐藏、顶部导航出现；正文回 14px |
| `--bp-md` | ≥ 768px | 页面内边距 24px；容器加宽 |
| `--bp-lg` | ≥ 1024px | 后台表格完整列展示 |

容器宽度：搜索结果与正文页 **720px**；后台与管理页 **960px**；首页 hero 表单 **560px**。

**首页背景**：40px 方格（横向/纵向各一层 1px 渐变，线色 `--grid-line`），由 `.hero::before`
铺满视口宽度——`.page` 只有 744px，直接给 `.hero` 加背景会让网格缩成中间一条。
`.hero` 必须配 `position: relative` + `isolation: isolate`：没有层叠上下文时伪元素的
`z-index: -1` 会掉到 body 背景后面，网格整个不可见。
深色用 6% 白而不是和参考站一样的 5%——底是 `#0D131C`，5% 几乎看不出来。
容器居中，不做满宽。

移动端专属结构：
- **底部导航**（<640px 显示）：搜索 / 提交 / 设置 / 后台 四项，`backdrop-filter: blur` + 半透明
  `--surface`，顶部 1px `--line`，图标 20px + 12px 文字，激活态用 `--brand`。
  顶部导航在移动端只保留品牌名与当前页标题。
- **搜索页**：搜索框吸顶（sticky，`--bg` 底 + 下边线），过滤 chips 横向滚动
  （`overflow-x: auto`，隐藏滚动条，两端渐隐遮罩），不换行堆叠。
- **表格**：≥640px 用 `<table>`；<640px 每行折叠为卡片（`td::before { content: attr(data-label) }`
  的标签-值堆叠），操作列变整行按钮。列定义同时服务两种形态。
- **弹层**：移动端为底部弹层（bottom sheet）：全宽、顶部 `--r-xl` 圆角、`max-height: 86dvh`、
  内部滚动；进入动画 `translateY(24px) → 0` + scrim 淡入 280ms；退出 180ms（快进慢出）。
- **快照 iframe**：移动端弹层高度 76dvh；桌面 60vh。

安全区：`viewport-fit=cover`；底部导航 `padding-bottom: env(safe-area-inset-bottom)`；
弹层 footer 同理；顶栏 `padding-top: env(safe-area-inset-top)`（刘海屏横竖切换不裁内容）。

---

## 8. 动效

| 令牌 | 值 | 用途 |
|---|---|---|
| `--dur-fast` | 120ms | 按压反馈、开关滑块 |
| `--dur` | 180ms | hover / 颜色过渡（默认） |
| `--dur-slow` | 280ms | 弹层 / 底部导航出现 |
| `--ease-out` | `cubic-bezier(.25, 1, .5, 1)` | 默认缓动（快进慢出） |

- 只动 `color / background-color / border-color / opacity / transform`；不动宽高（无布局抖动）。
- 结果列表不做逐条入场动画（搜索结果要的是即时性）；hero 元素允许一次 280ms 淡入 + 8px 上移。
- `prefers-reduced-motion: reduce` 时：所有 transform/opacity 动画时长归零，仅保留颜色过渡。
- 加载态：搜索 ≥ 200ms 才出现 spinner（快路径不闪）；骨架屏不做，spinner 14/20px 两档。

---

## 9. 图标

- 全部**内联 SVG**（stroke 1.75px、round cap/join，Lucide 线型），封装为 `Icon` 组件，
  走 `name` 注册表；尺寸 16px（行内）/ 20px（导航）/ 24px（空状态）。
- 颜色继承 `currentColor`；装饰性图标 `aria-hidden="true"`，按钮内图标必须伴随可见文字，
  纯图标按钮必须有 `aria-label`。
- 禁止用 emoji 当图标。

---

## 10. 组件规范

### Button
变体：`solid`（`--brand-solid` 实色）/ `outline`（1px `--line` + `--surface`）/ `ghost`
（无边框，hover `--sunken`）/ `danger`（`--danger` 实色）/ `text`（无边框小文字钮）。
尺寸：sm 32px / md 40px / lg 48px；圆角 `--r-md`（solid/outline/ghost）、`--r-full`（text）。
禁用：`opacity .45` + `cursor: default`。点击后立即给 loading 文案（提交类按钮），不闪空窗。

### 输入控件
Input/Select/Textarea：高 40px（移动端同高、字号 16px），1px `--line`，圆角 `--r-md`；
focus 边框转 `--brand-line` + 2px `--focus-ring` 外圈（offset 1px）。
Field：label 在上（`--fs-sm`，`--text-2`），hint 在下（`--fs-xs`，`--text-3`），错误信息
就近显示在控件下方且用 `--danger`，`aria-describedby` 关联。

### Chip
高 23px（可视），`--r-xs` 直角小圆角（v2 药方样式），1px `--line`；可点 chip 高度 ≥30px（触控 34px）。
语义变体（accent / success / warning / danger）为**软底无边框**：`-*-subtle` 底 + 对应文字色；
中性 chip 保持 `--line` 边。激活态 `--brand-subtle` 底 + `--brand-line` 边 + `--brand` 字
（v2 起不再用实色反白，更轻）。

### Switch
44×24 滑轨，关闭 `--n-300` 底白 knob，开启 `--brand-solid` 底白 knob（暗色同），
120ms transform；`role="switch"` + `aria-checked`。

### Modal / Bottom Sheet
桌面：居中卡，宽 420–900px，`--r-lg`，`--shadow-lg`，scrim `rgba(10,14,20,.5)`。
移动：底部弹层（§7）。结构固定：标题区（`--fs-lg`）+ 可滚动 body + footer（按钮右对齐，
移动端按钮变全宽块状，主按钮在上）。Esc、scrim 点击、关闭按钮三种退出都要有；
打开时焦点移入弹层，关闭后归还触发元素，焦点圈定在弹层内。

### Table（响应式）
桌面：表头 `--fs-xs` `--text-2`，单元格 10×12px padding，数字右对齐等宽字体，行 hover
`--sunken`。移动（<640px）：每行折叠为**定义列表卡片**——64px 固定标签列（`--text-3`
`--fs-xs`）+ 值列左对齐（多子单元格纵向堆叠；chip/内容 `justify-self: start` 不拉伸；
桌面右对齐列回正），复选框绝对定位卡片右上（首列让位 44px），操作行 `is-actions`
底部细分隔线右对齐。工具条移动端：标题整行（ellipsis），搜索框占满剩余宽。

### ResultCard（搜索结果）
结构：第一行是**标题**（`--fs-md`，`--brand` 色，访达链接），**单行截断**
（`text-overflow: ellipsis`，全端一致）；
第二行 meta：等宽**直达链接**（`hit.url` 去掉 `https://` 的完整路径，**路径超过 3 段
截断成 `host/a/b/c/…`**，另有 56 字符兜底；用直达链接而不是域名，是因为用户要确认的
是点下去到哪一页）· 时间 · **排序得分条** · 标签 chip ·「标签不准」文字钮；
爬虫还没抓过、且域名仍在白名单的页面（`fetchable`）在链接后跟一个 **「抓取」文字钮**
（`--fs-xs`、`--brand`，点击只补抓这一页，成功后置「已入库」、失败置「重试抓取」
且原因进 `title`；已从白名单移除的历史页面不显示，避免点了吃 403）；随后摘要（**clamp 2 行**、`--fs-sm`）/ 底部操作行：**左侧网站名称**（`hit.site_name`，
whitelist 表里爬虫抓首页时提取的 og:site_name，`--text-3` 小字单行截断；老数据没有则
不渲染，meta 行的链接仍在）+ 三个**纯图标方钮**（list-checks 排序
理由 · sparkles 原文摘要 · history 查看快照，30px，`aria-label` + `title` 提供语义），
展开态 `is-on` 高亮（`--brand-subtle` 底 + `--brand` 图标），原文摘要加载中显示 spinner。
排序理由默认**收进按钮**，点击在卡片下方展开（细分隔线 + `--brand-subtle` chips 包裹行）
——可解释是卖点，但默认视图让位给内容本身。
卡片 hover 换**品牌渐变描边**（双层背景：padding-box 回填表面色、border-box 画
`--grad-brand` 渐变，边框透明），仅 `@media (hover: hover)` 生效，不做阴影抬升；
卡片带 `--shadow-sm` 常驻微影（v2）。

**排序理由文案**：后端 `explain()` 的理由串是面向调试的完整句子，展示层经 `reasonText()`
映射为设计稿的短标签——标题精确匹配查询词→**标题完整命中**、来自官方文档站点→**官方文档加权**、
来自开源项目站点→**开源项目加权**、部分关键词命中标题/摘要→**部分命中**、发布时间较新（N 天前）→
**近 N 天发布**、语料内无人引用，被算法埋住→**少被引用 · 深网打捞** 等；映射不到的
（重复聚合、首页降权、同域多样性）原样保留。改后端理由串时同步 `core/api.ts` 的 `REASON_TEXT`。

**排序得分条**：meta 行右端，「三段条 76×5px + 等宽总分」。三段是 `hit.breakdown`
按默认权重聚合的占比：相关（BM25+Exact+Vector → `--viz-1` 紫）、时效（Freshness →
`--viz-2` 粉）、站点权重（0.25·Prosperity+0.2·Quality → `--viz-3` 蓝）。三个 `--viz-*`
是**原型强调色**，独立于品牌色、双主题不换值；批准用途仅两处——排序得分条与分页当前页
（`--viz-2` 粉另用于后台待审数徽标），其余组件禁止引入 violet/pink/blue。
悬浮/聚焦/点按显示纯 CSS tooltip（`.vx-tip`，反色底）给出「排序得分 x.xx：相关 n% ·
时效 n% · 站点权重 n%」。**<640px 不渲染**（移动端 meta 行空间有限，分解说明走
「排序理由」展开区）。全零分量或 0 分不渲染。

**原文摘要块**：`--brand-subtle` 底 + 2px 品牌渐变左条（`--grad-brand`）、
圆角 `--r-md`、内边距 12×16。结论行 `--fs-base`/1.7；引用是**徽标**不是上标——等宽 11px
`[n]` 小按钮，`--surface` 底 + `--brand-line` 边，hover 实色填充；无引用的结论挂「未验证」
warning chip。出处说明用 `.summary-note`（`--fs-xs`、`--text-3`）。
动作图标：原文摘要用**实心四角星**（auto_awesome 造型），查看快照用 **history 回溯时钟**。

**深网发现**：**无容器框**——标题行（radar 图标 `--viz-2` 粉 +「深网发现」渐变字
`--grad-brand` 600 字重 + 灰色提示「相关但少被引用的内容」）后直接跟普通结果卡列表
（间距 10px），与主结果区同构，仅靠标题行区分。

### 搜索补全

输入框下方的下拉 `.suggest-panel`：`--surface` 底、1px `--line` 边、`--r-md` 圆角、
`--shadow-md` 阴影，与搜索框同宽（相对整个 `.searchbar` 定位，所以 `.searchbar` 需要
`position: relative`）。选项 `.suggest-item` 高 32px 左右、左对齐、无图标；
键盘高亮与 hover 都用 `--sunken` 底。最多 8 条，取不到就整块不渲染（不留空框）。

### 加载态

不用居中转圈：转圈既占位又暗示「要等很久」，骨架更接近最终形态，数据到了不跳版。

- **首查 / 无任何结果时**：3 张**骨架卡**（`.skeleton-card`），形状对齐真实结果卡
  ——标题 1 行（58%、17px）、meta 1 行（34%、10px）、摘要 2 行（100% / 68%）。
  块色 `--sunken`，`sk-pulse` 1.3s 呼吸；`prefers-reduced-motion` 下静止。
- **翻页 / 改过滤**：已有结果**不换成骨架**，整列加 `.is-loading`（opacity .55 +
  `pointer-events: none`，160ms 过渡）。把已经看到的内容抽走比变淡更晃眼。
- **路由切换**：顶部 2px `--brand` 进度条（`.route-progress`，宽 0→72% 走 900ms）
  垫在「点击 → 目标页渲染完成」这段白屏上，`NavigationEnd/Cancel/Error` 立即收起。
  它只负责 chunk 下载那一段，目标页渲染后由页面自己的骨架接力。

### 每日用量（后台）

四个统计卡（今日检索 / 抓取 / 入库 / 摘要，`24px` 等宽数字）+ 纯 CSS 柱状趋势 +
按日明细表（`.table` 复用）。柱子 `--brand`、零值退回 `--line` 的 2px 底线，
容器高 120px、等距分列。**不引图表库**——三十根柱子不值得为此多 100KB 依赖。

### Notice
subtle 底 + 1px 语义边线 + 语义文字色，圆角 `--r-md`，可关闭（× 有 44px 热区）。

### 空状态
48px 上下留白、`--text-3`、一句话；引导型空状态（空索引）例外：卡片 + 主按钮。

### Tabs / 后台侧边栏
后台页签顺序按使用频率：审核队列 / 白名单 / 黑名单 / 网站提交 / 标签反馈 / 功能开关。
**桌面（≥860px）**：208px 常驻左侧栏（Lucide 线型图标 + 文字），激活态 `--brand-subtle` 底 +
2px 渐变左条（`--grad-brand`）；审核队列带待审数徽标（`--viz-2` 粉底白字药丸，0 条时隐藏），
「退出」在侧栏底部。**<860px**：导航**收进抽屉**——标题行左侧出现「☰ 菜单」钮
（`aria-expanded`），点开侧栏从左滑出（240px、`--shadow-lg`、顶部留安全区），
遮罩模糊 + Esc + 选中/退出后关闭；同一份侧栏 DOM，桌面静止、移动端 off-canvas。

### 分页
数字页码（窗口最多 5 页）+ 前后翻页**纯文字**扁平按钮（无箭头图标）；当前页用原型强调色
**紫罗兰实色 `--viz-1`**（白字 6.2:1），其余页码 1px 边框盒，hover 边框转紫 35% 混合；
等宽字体页码，按钮 36px（触屏 40px）。桌面居中排版；后台列表在左侧带「共 N 条」
（结果页的总数已在计数行展示，不重复）。

### 过滤与时间筛选（结果页工具行）
常规流一行（原计数行位置，随页面滚动，不吸顶），底部细线分隔：**「过滤」「时间」两个
同款下拉 chip**，无图标钮、无内容标签。
- **过滤**：多选。收起时「过滤」chip（有已选项带数量徽标、激活态）；展开 216px 浮层，
  每项 = 勾选列（16px 恒占位，选中 `--brand` 对勾）+ 名称 + 一行防错提示。多选切换
  **不关闭菜单**；改动即重搜回第 1 页，选择存本机偏好。
- **时间**：单选。收起时「时间」chip（非「全部」时显示 `时间 · {选中档}` 并激活态）；
  菜单为 radio 语义，**选中即关闭**。
- 两个菜单共用关闭逻辑：点外部 / Esc 收起（host document 监听）。
- 结果列容器 `page-results`：左缘**贴近视口左侧**（百度式小间隙）——
  `margin-left: clamp(16px, 3vw, 56px)`，宽 `min(744px, 可用宽)`，右侧留白。
- 头部在 /search 时左侧为紧凑搜索框，且头部容器取消 page-wide 居中
  （`header-inner-results`：`max-width: none` + 同一 `clamp` 左缘）——
  **搜索框、过滤行、结果列三条左缘同一条竖线**；其他页面头部仍是 page-wide 居中。

---

## 11. 主题（浅色 / 深色）

- 默认跟随系统（`prefers-color-scheme`）；设置页提供 跟随系统 / 浅色 / 深色 三选一，
  存 `localStorage: voidxo.theme`，写到 `<html data-theme>`。
- `index.html` 内联 3 行脚本在首帧前设好 `data-theme`，避免闪白。
- `<meta name="theme-color">` 双份（light/dark media）。
- 主题切换只允许换 1.4 节语义令牌，组件零改动。

---

## 12. 无障碍（WCAG AA，CRITICAL）

- 全键盘可达：focus-visible 环（2px `--focus-ring`，offset 2px）永不删除。
- 语义地标：header/nav/main/footer；搜索框 `role="search"`。
- 图片 alt、图标钮 aria-label、开关 role=switch、弹层 role=dialog + aria-modal + 焦点圈定。
- 结果数与「检索中」状态用 `aria-live="polite"` 播报。
- 颜色不作为唯一信息载体（chip 语义都带文字）。
- 触控目标 ≥44px、间距 ≥8px（§6）。

---

## 13. Angular 实现约定

基于 **Angular CLI MCP server（`npx @angular/cli mcp`）的 get_best_practices** 与
angular.dev/ai 规范（Angular 22）：

- **zoneless + signals-first**：状态一律 `signal()` / `computed()`；不写 Zone 依赖的代码。
- 组件全部 standalone（不写 `standalone: true`，v20+ 默认）；OnPush 是默认，不显式声明。
- 模板用原生控制流 `@if / @for / @switch`；禁止 `ngClass / ngStyle / CommonModule`，
  类名用 `class` 绑定拼条件。
- `input() / output()` 函数式 API；`inject()` 取依赖；host 绑定写进 `host` 对象。
- 路由：`withComponentInputBinding()` 把 `?q=&page=` 直接绑成组件 input；
  管理后台按页 `loadComponent` 懒加载；标题用自定义 `TitleStrategy`（`q` 进标题）。
- HTTP：`provideHttpClient(withFetch())` + 函数式拦截器统一附加 `X-Admin-Token`；
  API 类型集中在 `core/api.ts`，与 Go 侧字段一一对应（snake_case 保持原样，不做转换层）。
- 表单刻意**不用** Signal Forms / FormsModule：本产品表单最多 3 个输入、单条非空校验，
  信号 + 模板绑定最短路径；等校验规则超过「必填」再引入 `@angular/forms/signals`。
- 文件组织：`core/`（api 类型 + 服务）、`shared/ui/`（无业务组件）、`layout/`、`pages/`；
  组件模板内联（小组件）或相对路径外置（页面）。
- 构建产物经 `npm run emit-dist` 同步到 `internal/webui/dist`，由 go:embed 打包；
  Go 侧 `embed.go` 已含 SPA 回退，无需逐路由预渲染。

### Angular CLI MCP server（本仓库的接入方式）

```jsonc
// .mcp.json（仓库根，MCP 客户端通用格式）
{ "mcpServers": { "angular": { "command": "npx", "args": ["-y", "@angular/cli", "mcp"] } } }
```

- 手动运行：`npx @angular/cli mcp`（加 `--read-only` 只开查询类工具）。
- 提供工具：`get_best_practices`（本文 §13 的来源）、`search_documentation`、
  `list_projects`、`devserver_start/wait_for_build/stop`、`onpush_zoneless_migration`、`ai_tutor`。
- AI 会话开始时先调 `get_best_practices`；不确定的 API 用 `search_documentation` 查证后再写。
