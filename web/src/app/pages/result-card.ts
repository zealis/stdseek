import { Component, computed, inject, input, signal } from '@angular/core';
import { DomSanitizer, type SafeHtml, type SafeResourceUrl } from '@angular/platform-browser';
import { HttpErrorResponse } from '@angular/common/http';
import { errMsg } from '../core/http';
import { PublicApi } from '../core/public-api.service';
import { resultTime, truncate } from '../shared/format';
import { labelBadge, labelText, reasonText, type Hit, type SummaryResp } from '../core/api';
import type { IconName } from '../shared/ui/icon';

/** 页级标签的图标。key 与后端 labels 一致（见 core/api 的 LABEL_TEXT）。
    广告较多走 AD 字母徽标（labelBadge），不在这里。 */
const LABEL_ICON: Record<string, IconName> = {
  ai_suspect: 'bot',
  tracker: 'spy',
  personal: 'user',
  ugc_platform: 'users',
};
import { Icon } from '../shared/ui/icon';
import { Modal } from '../shared/ui/modal';
import { LabelFeedbackModal } from './label-feedback-modal';

// 在原文快照里把引用片段包成 <mark>。只做一次精确子串替换，替换不到就只打开
// 快照不高亮——不为了"一定要高亮"去做模糊匹配，那会把高亮打到错误的段落上。
//
// 定位靠 id + URL fragment（见 buildSnapshotSrc）：快照动辄两万像素高，
// 点开停在第 0 屏等于没定位。
const CITE_ANCHOR = 'voidxo-cite';

// iframe 内拿不到宿主页面的 CSS 变量，高亮色值只能内联（amber 亮底深字，见 DESIGN.md）
const MARK_BG = '#ffee9c';
const MARK_FG = '#4f3422';

function highlightQuote(html: string, quote: string): { html: string; hit: boolean } {
  if (!quote) return { html, hit: false };
  const esc = quote.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] as string);
  const idx = html.indexOf(esc);
  if (idx < 0) return { html, hit: false };
  const mark = `<mark id="${CITE_ANCHOR}" style="background:${MARK_BG};color:${MARK_FG};padding:2px;border-radius:2px;scroll-margin-top:24px">${esc}</mark>`;
  return { html: html.slice(0, idx) + mark + html.slice(idx + esc.length), hit: true };
}

// buildSnapshotSrc 决定快照怎么喂给 iframe。
//
// 有命中片段时用 data: URL + #fragment：fragment 能让浏览器直接滚到高亮处，
// 而 srcdoc 不支持 fragment 跳转。data: URL 不是没有代价——超过约 2MB 会被浏览器拒绝，
// 所以留了长度上限，超了退回 srcdoc（内容完整、只是不自动定位）。
const DATA_URL_LIMIT = 1_200_000;

function buildSnapshotSrc(html: string, hit: boolean): string {
  if (!hit) return '';
  const url = 'data:text/html;charset=utf-8,' + encodeURIComponent(html);
  if (url.length > DATA_URL_LIMIT) return '';
  return url + '#' + CITE_ANCHOR;
}

type SnapState = { open: boolean; loading: boolean; html: string; src: string; err: string; notFound: boolean };type SumState = { open: boolean; loading: boolean; data: SummaryResp | null; err: string };
/** 补抓按钮的状态机：idle → busy → done / fail（fail 可重试）。 */
type FetchState = { state: 'idle' | 'busy' | 'done' | 'fail'; err: string };

const SNAP_CLOSED: SnapState = { open: false, loading: false, html: '', src: '', err: '', notFound: false };
const SUM_CLOSED: SumState = { open: false, loading: false, data: null, err: '' };
const FETCH_IDLE: FetchState = { state: 'idle', err: '' };

/**
 * 直达链接显示的最大字符数。
 *
 * 按字符夹断，不按路径层级。早期版本是「最多显示 3 段路径」，问题是段数不含信息量：
 * `…/sdcb/p/23378501/…` 里三段全是数字和短词，真正能定位页面的 slug 反而被整段丢掉。
 * 同一个宽度下按字符截，用户至少能认出这是哪一篇。
 *
 * 48 是「够认出是哪篇、又不把时间与得分条挤到边上」的量：等宽 12px 下约 340px，
 * 在 720px 的结果卡里占不到一半（64 会占到三分之二，整行看起来只有链接）。
 */
const URL_DISPLAY_MAX = 48;

// 中文路径在 API 里是 percent-encoded 的，直接显示会是一串 %E4%B8%AD。
// 解码失败（非法转义）就保持原样——不为了好看把链接显示改坏。
function decodeUrl(s: string): string {
  try {
    return decodeURI(s);
  } catch {
    return s;
  }
}

// 结果卡第二行显示「直达链接」而不是域名：用户要确认的是点下去到哪一页。
// 去协议保留 www（与白名单登记写法一致），超长时直接按字符数截——
// 窄屏还有 CSS 的 text-overflow 兜底，不必在这里再按段裁剪。
function shortUrl(url: string): string {
  const bare = decodeUrl(url.replace(/^https?:\/\//i, '').replace(/\/$/, ''));
  if (bare.length <= URL_DISPLAY_MAX) return bare;
  let cut = bare.slice(0, URL_DISPLAY_MAX - 1);
  // 别把代理对切成半个字符（emoji 域名罕见，中文路径走的是单个码点，这里只防万一）
  const last = cut.charCodeAt(cut.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) cut = cut.slice(0, -1);
  return `${cut}…`;
}

/**
 * 搜索结果卡。可解释性是这个产品的差异化：排序理由 chips 用强调底色，
 * 原文摘要每条结论挂原文引用编号，点击在沙箱快照里定位原文。
 */
@Component({
  selector: 'vx-result-card',
  imports: [Icon, Modal, LabelFeedbackModal],
  template: `
    <article class="card result">
      <a [href]="hit().url" target="_blank" rel="noreferrer" class="result-title">
        {{ hit().title || hit().url }}
      </a>

      <div class="result-meta">
        <span class="result-domain" [title]="hit().url">{{ displayUrl() }}</span>
        @if (time()) {
          <!-- 时间和它前面的分隔点绑在一个盒子里：窄屏整块隐藏时，
               不会留下一个孤零零的点。 -->
          <span class="result-time">
            <span class="result-dot" aria-hidden="true"></span>
            <span>{{ time() }}</span>
          </span>
        }
        <!-- 得分条不在 meta 行：挪进「排序理由」展开区最前（见下方 result-reasons），
             meta 行只留域名 / 时间 / 标签 / 反馈。 -->
        <!-- 标签默认只画图标（AI=机器人、独立网站=单人、UGC=多人），
             文字放进 title 与 aria-label：一排标签全用中文会把结果卡的元信息挤到换行，
             而含义本身又能用图标直接看懂。「广告较多」是例外，用 AD 字母徽标——
             喇叭表达的是「宣传/公告」，读不出「这页广告多」。 -->
        @for (l of labels(); track l) {
          <span
            class="chip chip-ico"
            [title]="labelText(l) + (hit().label_reasons?.[l] ? '：' + hit().label_reasons?.[l] : '')"
            [attr.aria-label]="labelText(l)"
            role="img"
          >
            @if (labelBadge(l); as b) {
              <span class="chip-ad">{{ b }}</span>
            } @else {
              <vx-icon [name]="labelIcon(l)" [size]="15" />
            }
          </span>
        }
        @if (labels().length > 0 || hit().category) {
          <button
            type="button"
            class="btn btn-text"
            aria-label="反馈"
            title="标签或分类不准？反馈给我们"
            (click)="fbOpen.set(true)"
          >
            <vx-icon name="sliders" [size]="14" />
          </button>
        }
      </div>

      <p class="result-desc">{{ desc() || '（无摘要）' }}</p>

      <div class="result-foot">
        @if (hit().site_name) {
          <span class="result-site">{{ hit().site_name }}</span>
        }
        <span class="result-actions">
          @if ((hit().reasons ?? []).length > 0 || scoreInfo()) {
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              [class.is-on]="reasonsOpen()"
              [attr.aria-label]="reasonsOpen() ? '收起排序理由' : '查看排序理由'"
              title="排序理由"
              (click)="toggleReasons()"
            >
              <vx-icon name="list-checks" [size]="15" />
            </button>
          }
          @if (aiSummary() && q()) {
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              [class.is-on]="sum().open"
              [attr.aria-label]="sum().open ? '收起原文摘要' : '生成原文摘要'"
              title="原文摘要"
              (click)="toggleSummary()"
            >
              @if (sum().loading) {
                <span class="spinner"></span>
              } @else {
                <vx-icon name="sparkles" [size]="15" />
              }
            </button>
          }
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            aria-label="查看原文快照"
            title="查看快照"
            (click)="openSnapshot('')"
          >
            <vx-icon name="history" [size]="15" />
          </button>
        </span>
      </div>

      @if (reasonsOpen()) {
        <div class="result-reasons">
          <!-- 得分条放在最前：先看量化占比，再看定性理由。悬浮/点按仍有分解说明。 -->
          @if (scoreInfo(); as s) {
            <button
              type="button"
              class="result-score vx-tip"
              [class.is-open]="scoreTipOpen()"
              [attr.data-tip]="s.tip"
              [attr.aria-label]="s.tip"
              (click)="scoreTipOpen.set(!scoreTipOpen())"
            >
              <span class="score-bar" aria-hidden="true">
                <i [style.width.%]="s.rel"></i>
                <i [style.width.%]="s.fresh"></i>
                <i [style.width.%]="s.qual"></i>
              </span>
              <span class="score-val">{{ s.value }}</span>
            </button>
          }
          @for (r of hit().reasons ?? []; track r) {
            <span class="chip chip-accent">{{ reasonText(r) }}</span>
          }
        </div>
      }

      @if (sum().open) {
        <div class="summary">
          @if (sum().loading) {
            <div class="row faint small" style="gap: 8px">
              <span class="spinner"></span>
              <span>正在从正文里摘录证据</span>
            </div>
          }
          @if (sum().err) {
            <div class="notice notice-warning" style="margin-bottom: 8px">{{ sum().err }}</div>
          }
          @if (sum().data; as data) {
            @if (data.empty) {
              <p style="color: var(--warning); font-weight: 500">未找到可靠来源，不生成摘要</p>
              @if (data.reason) {
                <p class="faint small">{{ data.reason }}</p>
              }
            } @else {
              <div class="stack" style="gap: 8px">
                @for (c of data.conclusions ?? []; track $index) {
                  <p class="summary-line">
                    {{ c.text }}
                    @for (ci of c.citations ?? []; track ci.id) {
                      <button
                        type="button"
                        class="cite"
                        title="点击在原文快照中定位这段"
                        (click)="openSnapshot(ci.text)"
                      >
                        [{{ ci.id }}]
                      </button>
                    }
                    @if ((c.citations ?? []).length === 0) {
                      <span class="chip chip-warning" title="这条结论没有对应的原文片段">未验证</span>
                    }
                  </p>
                }
              </div>
              <p class="summary-note">
                {{ data.provider === 'extractive' ? '抽取式摘要，直接摘录原文句子，未经模型改写' : '由 ' + data.provider + ' 生成' }}{{
                  (data.conclusions?.length ?? 0) > 0 ? '，点击编号可在原文中定位' : ''
                }}
              </p>
              @if (data.reason) {
                <p class="summary-note">{{ data.reason }}</p>
              }
            }
          }
        </div>
      }

      @if (snap().open) {
        <vx-modal
          title="原文快照"
          desc="抓取当时保存的页面，用于核对索引内容是否与原文一致"
          [width]="900"
          (closed)="snap.set(SNAP_CLOSED)"
        >
          <div class="snap-body">
            @if (snap().loading) {
              <div class="loading"><span class="spinner spinner-lg"></span></div>
            }
            @if (!snap().loading && snap().err) {
              <div class="notice notice-warning" style="margin: 16px">{{ snap().err }}</div>
              @if (snap().notFound) {
                <div class="row" style="margin: 0 16px 12px">
                  <button
                    type="button"
                    class="btn btn-sm"
                    [disabled]="fetchState().state === 'busy'"
                    (click)="fetchPage()"
                  >
                    {{ fetchState().state === 'busy' ? '抓取中…' : '抓取这一页' }}
                  </button>
                  @if (fetchState().state === 'fail') {
                    <span class="small" style="color: var(--danger)">{{ fetchState().err }}</span>
                  }
                </div>
              }
            }
            @if (!snap().loading && (snap().html || snap().src)) {
              @if (snapSrc(); as src) {
                <iframe class="snapshot-frame" sandbox="" title="原文快照" [src]="src"></iframe>
              } @else if (snapHtml(); as html) {
                <iframe class="snapshot-frame" sandbox="" title="原文快照" [srcdoc]="html"></iframe>
              }
            }
          </div>
          <div footer class="foot-row">
            <a class="btn btn-outline" [href]="hit().url" target="_blank" rel="noreferrer">
              打开原网页
            </a>
            <button type="button" class="btn btn-solid" (click)="snap.set(SNAP_CLOSED)">关闭</button>
          </div>
        </vx-modal>
      }

      @if (fbOpen()) {
        <vx-label-feedback
          [url]="hit().url"
          [labels]="labels()"
          [category]="hit().category ?? ''"
          [reasons]="hit().label_reasons ?? null"
          (closed)="fbOpen.set(false)"
        />
      }
    </article>
  `,
  styles: `
    .result-title {
      display: block;
      min-width: 0;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      font-size: var(--fs-lg);
      font-weight: 500;
      color: var(--text-1);
      line-height: 1.4;
      transition: color var(--dur-fast) var(--ease-out);
    }

    .result-title:hover {
      color: var(--brand);
    }

    .result-reasons {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: 6px;
      margin-top: 8px;
      padding-top: 8px;
      border-top: 1px solid var(--line);
    }

    /* 补抓入口：比正常按钮矮一号，跟域名/时间同一行的视觉重量 */
    .result-fetch {
      height: auto;
      padding: 0 2px;
      font-size: var(--fs-xs);
      line-height: 1.2;
      color: var(--brand);
    }

    .result-site {
      align-self: center;
      color: var(--text-3);
      font-size: var(--fs-xs);
      /* 站点名可以很长（有的站用整句做站名）。只写 ellipsis 不够：
         作为 flex 子项，默认 min-width 是内容宽度，撑开后就收不回来，
         右侧操作钮会被顶到第二行。所以必须允许它收缩。 */
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* 时间与分隔点同盒：窄屏隐藏时不会留下孤立的分隔点 */
    .result-time {
      display: inline-flex;
      align-items: center;
      gap: var(--sp-2);
      flex: none;
      white-space: nowrap;
    }

    /* 操作钮图标化：方钮 + 展开态高亮 */
    .result-actions .btn {
      width: var(--h-sm);
      padding: 0;
    }

    .result-actions .btn.is-on {
      background: var(--brand-subtle);
      color: var(--brand);
    }

    .snap-body {
      margin: 0 -20px;
      border-top: 1px solid var(--line);
    }

    .foot-row {
      display: flex;
      gap: 8px;
    }

    /* 窄屏（≤767px，与全站移动断点一致）：
       - 时间在手机上信息价值低，整块让位给正文；
       - 站点名与右侧按钮、域名与标签各锁一行，超长部分用省略号收口，
         而不是换行把卡片撑高。 */
    @media (max-width: 767.98px) {
      .result-time {
        display: none;
      }

      .result-foot {
        flex-wrap: nowrap;
      }

      .result-actions {
        flex: none;
      }

      .result-meta {
        flex-wrap: nowrap;
      }

      .result-domain {
        flex: 1 1 auto;
        min-width: 0;
      }

      /* 评分条与标签是定宽的，不能参与收缩，否则会被压成看不见 */
      .result-score,
      .result-meta > .chip,
      .result-meta > .btn {
        flex: none;
      }
    }

    @media (max-width: 639.98px) {
      .foot-row .btn {
        flex: 1;
      }
    }
  `,
})
export class ResultCard {
  private readonly api = inject(PublicApi);
  private readonly sanitizer = inject(DomSanitizer);

  /** hit 为本条结果；q 用于给摘要定位原文，没有查询词时不显示摘要入口。 */
  readonly hit = input.required<Hit>();
  readonly q = input('');
  /** aiSummary 由 /api/features 决定，后台关闭时整块 UI 都不出现。 */
  readonly aiSummary = input(false);

  protected readonly SNAP_CLOSED = SNAP_CLOSED;
  protected readonly labelText = labelText;

  /** 标签 → 图标。认不出来的标签退回 tag，至少还有个形状，不会空一格。 */
  protected labelIcon(label: string): IconName {
    return LABEL_ICON[label] ?? 'tag';
  }
  protected readonly labelBadge = labelBadge;
  protected readonly reasonText = reasonText;

  protected readonly snap = signal<SnapState>(SNAP_CLOSED);
  protected readonly sum = signal<SumState>(SUM_CLOSED);
  /** 摘要请求代数：收起动作 +1，让还在路上的响应自己作废（不能只靠 open 开关）。 */
  private sumSeq = 0;
  protected readonly fetchState = signal<FetchState>(FETCH_IDLE);
  protected readonly fbOpen = signal(false);
  /** 排序理由默认收进按钮，点击展开（DESIGN.md §10）。 */
  protected readonly reasonsOpen = signal(false);
  /** 得分分解说明：触屏没有 hover，点按切换显示（桌面仍走 hover）。 */
  protected readonly scoreTipOpen = signal(false);

  /** 展开收起排序理由；收起时把打开的得分说明一起复位，下次展开是干净的默认态。 */
  protected toggleReasons(): void {
    const next = !this.reasonsOpen();
    this.reasonsOpen.set(next);
    if (!next) this.scoreTipOpen.set(false);
  }

  protected readonly labels = computed(() => this.hit().labels ?? []);
  protected readonly time = computed(() => resultTime(this.hit().sort_time, this.hit().time_by_fetch));
  // 有些站把整篇正文塞进 meta description（实测单条 1600 字）。
  // 展示层再截一次：CSS clamp 只管视觉，DOM 里不该留这么长的文本。
  protected readonly desc = computed(() => truncate(this.hit().description ?? '', 200));

  // 第二行显示「直达链接」而不是域名：用户要确认的是点下去到哪一页
  // （/releases/8.5/en.php 和站点首页完全是两回事），路径超过 3 段截断。
  protected readonly displayUrl = computed(() => shortUrl(this.hit().url));

  // 排序得分分解。breakdown 各分量都是 0~1 归一化值，按默认权重聚合为三段
  // （rank.go：relPart=BM25+Exact(+Vector)，质量项统一乘 rel，算占比时约掉；
  // Freshness 权重 1.0，Prosperity 0.25 + Quality 0.2）。与 Go 侧权重改动脱钩时
  // 只是条形比例的近似，总分仍以 hit.score 为准。
  protected readonly scoreInfo = computed(() => {
    const b = this.hit().breakdown ?? {};
    const rel = (b['bm25'] ?? 0) + (b['exact'] ?? 0) + (b['vector'] ?? 0);
    const fresh = b['freshness'] ?? 0;
    const qual = 0.25 * (b['prosperity'] ?? 0) + 0.2 * (b['quality'] ?? 0);
    const total = rel + fresh + qual;
    const show = total > 0 && this.hit().score > 0;
    if (!show) return null;
    const pct = (v: number) => Math.round((v / total) * 100);
    const value = this.hit().score.toFixed(2);
    return {
      value,
      rel: pct(rel),
      fresh: pct(fresh),
      qual: pct(qual),
      tip: `排序得分 ${value}：相关 ${pct(rel)}% · 时效 ${pct(fresh)}% · 站点权重 ${pct(qual)}%`,
    };
  });

  protected readonly snapSrc = computed<SafeResourceUrl | null>(() => {
    const src = this.snap().src;
    return src ? this.sanitizer.bypassSecurityTrustResourceUrl(src) : null;
  });

  protected readonly snapHtml = computed<SafeHtml | null>(() => {
    const html = this.snap().html;
    // 走 srcdoc 分支的前提是没有可用的 data URL（超长或无命中片段）
    return this.snap().src || !html ? null : this.sanitizer.bypassSecurityTrustHtml(html);
  });

  protected async fetchPage(): Promise<void> {
    if (this.fetchState().state === 'busy') return;
    this.fetchState.set({ state: 'busy', err: '' });
    try {
      await this.api.fetchPage(this.hit().url);
      this.fetchState.set({ state: 'done', err: '' });
      // 刚抓完就能看，不用用户再点一次
      void this.openSnapshot('');
    } catch (e) {
      this.fetchState.set({ state: 'fail', err: (e as Error).message });
    }
  }

  // 证据链：抓取时存进 KV 的原文快照。接口返回的是不可信 HTML，
  // 只能用 sandbox iframe 渲染——sandbox="" 会禁掉脚本/表单/同源访问，
  // 页面里的 JS 无法碰到宿主页面。相对资源靠注入 <base> 还原，否则图片和样式全丢。
  protected async openSnapshot(quote: string): Promise<void> {
    const url = this.hit().url;
    this.snap.set({ open: true, loading: true, html: '', src: '', err: '', notFound: false });
    try {
      const r = await this.api.pageSnapshot(url);
      const withBase = r.html.replace(/<head([^>]*)>/i, `<head$1><base href="${url}">`);
      const { html: marked, hit: found } = highlightQuote(withBase, quote);
      this.snap.set({
        open: true,
        loading: false,
        html: marked,
        src: buildSnapshotSrc(marked, found),
        err: '',
        notFound: false,
      });
    } catch (e) {
      // 404 = 这页没抓过，可以现场补抓；其它错误（云端读失败等）只报错。
      const notFound = e instanceof HttpErrorResponse && e.status === 404;
      this.snap.set({ open: true, loading: false, html: '', src: '', err: errMsg(e), notFound });
    }
  }

  protected async toggleSummary(): Promise<void> {
    const cur = this.sum();
    // 已经展开就收起——不管上次成功、失败，还是请求还在路上。
    // （旧逻辑只在拿到 data 时才允许收起，失败后按钮就卡在展开状态出不去）
    if (cur.open) {
      // 作废在途请求：否则它回来后会把面板重新顶开，表现为"点了也没用"
      this.sumSeq++;
      this.sum.set({ ...cur, open: false, loading: false });
      return;
    }
    if (cur.data) {
      this.sum.set({ ...cur, open: true });
      return;
    }
    const seq = ++this.sumSeq;
    this.sum.set({ open: true, loading: true, data: null, err: '' });
    try {
      const data = await this.api.summarize(this.hit().url, this.q());
      if (seq !== this.sumSeq) return; // 已被用户收起，结果丢弃
      this.sum.set({ open: true, loading: false, data, err: '' });
    } catch (e) {
      if (seq !== this.sumSeq) return;
      // 不直接抛 Angular 的 HttpErrorResponse.message（是 "Http failure response for ..." 这种）
      this.sum.set({ open: true, loading: false, data: null, err: errMsg(e) });
    }
  }
}
