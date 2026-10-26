import { Component, computed, inject, input, signal } from '@angular/core';
import { DomSanitizer, type SafeHtml, type SafeResourceUrl } from '@angular/platform-browser';
import { PublicApi } from '../core/public-api.service';
import { resultTime, truncate } from '../shared/format';
import { labelText, reasonText, type Hit, type SummaryResp } from '../core/api';
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

type SnapState = { open: boolean; loading: boolean; html: string; src: string; err: string };type SumState = { open: boolean; loading: boolean; data: SummaryResp | null; err: string };
/** 补抓按钮的状态机：idle → busy → done / fail（fail 可重试）。 */
type FetchState = { state: 'idle' | 'busy' | 'done' | 'fail'; err: string };

const SNAP_CLOSED: SnapState = { open: false, loading: false, html: '', src: '', err: '' };
const SUM_CLOSED: SumState = { open: false, loading: false, data: null, err: '' };
const FETCH_IDLE: FetchState = { state: 'idle', err: '' };

/** 直达链接的显示长度上限：再长会把整行 meta 挤掉（窄屏尤其明显）。 */
const URL_DISPLAY_MAX = 64;

// 中文路径在 API 里是 percent-encoded 的，直接显示会是一串 %E4%B8%AD。
// 解码失败（非法转义）就保持原样——不为了好看把链接显示改坏。
function decodeUrl(s: string): string {
  try {
    return decodeURI(s);
  } catch {
    return s;
  }
}

/**
 * 搜索结果卡。可解释性是这个产品的差异化：排序理由 chips 用强调底色，
 * AI 摘要每条结论挂原文引用编号，点击在沙箱快照里定位原文。
 */
@Component({
  selector: 'vx-result-card',
  imports: [Icon, Modal, LabelFeedbackModal],
  template: `
    <article class="card card-hover result">
      <a [href]="hit().url" target="_blank" rel="noreferrer" class="result-title">
        {{ hit().title || hit().url }}
      </a>

      <div class="result-meta">
        <span class="result-domain" [title]="hit().url">{{ displayUrl() }}</span>
        @if (hit().has_snapshot === false && fetchState().state !== 'done') {
          <button
            type="button"
            class="btn btn-text result-fetch"
            [disabled]="fetchState().state === 'busy'"
            [attr.title]="fetchState().state === 'fail' ? fetchState().err : '这页还没抓过，点击抓取'"
            (click)="fetchPage()"
          >
            {{ fetchState().state === 'busy' ? '抓取中…' : fetchState().state === 'fail' ? '重试抓取' : '抓取' }}
          </button>
        }
        @if (time()) {
          <span class="result-dot" aria-hidden="true"></span>
          <span>{{ time() }}</span>
        }
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
        @for (l of labels(); track l) {
          <span class="chip" [title]="hit().label_reasons?.[l] || '按站点特征自动归类'">
            {{ labelText(l) }}
          </span>
        }
        @if (labels().length > 0) {
          <button
            type="button"
            class="btn btn-text"
            aria-label="标签反馈"
            title="标签不准？反馈给我们"
            (click)="fbOpen.set(true)"
          >
            <vx-icon name="thumbs-down" [size]="14" />
          </button>
        }
      </div>

      <p class="result-desc">{{ desc() || '（无摘要）' }}</p>

      <div class="result-foot">
        @if (hit().site_name) {
          <span class="result-site">{{ hit().site_name }}</span>
        }
        <span class="result-actions">
          @if ((hit().reasons ?? []).length > 0) {
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              [class.is-on]="reasonsOpen()"
              [attr.aria-label]="reasonsOpen() ? '收起排序理由' : '查看排序理由'"
              title="排序理由"
              (click)="reasonsOpen.set(!reasonsOpen())"
            >
              <vx-icon name="list-checks" [size]="15" />
            </button>
          }
          @if (aiSummary() && q()) {
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              [class.is-on]="sum().open"
              [disabled]="sum().loading"
              [attr.aria-label]="sum().open ? '收起 AI 摘要' : '生成 AI 摘要'"
              title="AI 摘要"
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
      font-size: var(--fs-md);
      font-weight: 500;
      color: var(--brand);
      line-height: 1.5;
    }

    .result-reasons {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin-top: 12px;
      padding-top: 12px;
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
      overflow: hidden;
      text-overflow: ellipsis;
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
  protected readonly reasonText = reasonText;

  protected readonly snap = signal<SnapState>(SNAP_CLOSED);
  protected readonly sum = signal<SumState>(SUM_CLOSED);
  protected readonly fetchState = signal<FetchState>(FETCH_IDLE);
  protected readonly fbOpen = signal(false);
  /** 排序理由默认收进按钮，点击展开（DESIGN.md §10）。 */
  protected readonly reasonsOpen = signal(false);
  /** 得分分解说明：触屏没有 hover，点按切换显示（桌面仍走 hover）。 */
  protected readonly scoreTipOpen = signal(false);

  protected readonly labels = computed(() => this.hit().labels ?? []);
  protected readonly time = computed(() => resultTime(this.hit().sort_time, this.hit().time_by_fetch));
  // 有些站把整篇正文塞进 meta description（实测单条 1600 字）。
  // 展示层再截一次：CSS clamp 只管视觉，DOM 里不该留这么长的文本。
  protected readonly desc = computed(() => truncate(this.hit().description ?? '', 200));

  // 第二行显示「直达链接」而不是域名：用户要确认的是点下去到哪一页
  // （/releases/8.5/en.php 和站点首页完全是两回事）。
  // 去协议保留 www——与白名单里登记的写法一致，便于核对来源。
  protected readonly displayUrl = computed(() => {
    const bare = this.hit().url.replace(/^https?:\/\//i, '').replace(/\/$/, '');
    const text = decodeUrl(bare);
    return text.length > URL_DISPLAY_MAX ? text.slice(0, URL_DISPLAY_MAX - 1) + '…' : text;
  });

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
    } catch (e) {
      this.fetchState.set({ state: 'fail', err: (e as Error).message });
    }
  }

  // 证据链：抓取时存进 KV 的原文快照。接口返回的是不可信 HTML，
  // 只能用 sandbox iframe 渲染——sandbox="" 会禁掉脚本/表单/同源访问，
  // 页面里的 JS 无法碰到宿主页面。相对资源靠注入 <base> 还原，否则图片和样式全丢。
  protected async openSnapshot(quote: string): Promise<void> {
    const url = this.hit().url;
    this.snap.set({ open: true, loading: true, html: '', src: '', err: '' });
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
      });
    } catch (e) {
      this.snap.set({ open: true, loading: false, html: '', src: '', err: (e as Error).message });
    }
  }

  protected async toggleSummary(): Promise<void> {
    const cur = this.sum();
    if (cur.data) {
      this.sum.set({ ...cur, open: !cur.open });
      return;
    }
    this.sum.set({ open: true, loading: true, data: null, err: '' });
    try {
      const data = await this.api.summarize(this.hit().url, this.q());
      this.sum.set({ open: true, loading: false, data, err: '' });
    } catch (e) {
      this.sum.set({ open: true, loading: false, data: null, err: (e as Error).message });
    }
  }
}
