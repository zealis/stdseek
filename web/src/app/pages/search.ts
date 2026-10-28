import {
  Component,
  ElementRef,
  computed,
  effect,
  inject,
  input,
  signal,
  viewChild,
} from '@angular/core';
import { Router } from '@angular/router';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { PrefsService } from '../core/prefs.service';
import { SearchSession } from '../core/search-session';
import { FILTERS, TIME_WINDOWS, type Hit, type QuerySyntax } from '../core/api';
import { RouterLink } from '@angular/router';
import { Icon } from '../shared/ui/icon';
import { Pager } from '../shared/ui/pager';
import { ResultCard } from './result-card';

/** 用户偏好：是否显示相关搜索（'0' 关闭，缺省开启）。 */
const PREF_RELATED = 'voidxo.pref.related';

const PAGE_SIZE = 10;

/**
 * 结果页。?q 与 ?page 通过 withComponentInputBinding 直接绑成 input；
 * 过滤条件存本机偏好，改动后从第 1 页重新搜。弱相关折叠只在第 1 页生效：
 * 用户主动翻页说明他愿意看更多。
 */
@Component({
  selector: 'vx-search-page',
  imports: [Icon, Pager, ResultCard, RouterLink],
  host: {
    '(document:keydown.escape)': 'closeMenusOnEsc()',
    '(document:click)': 'closeMenus($event)',
  },
  template: `
    <div class="page page-results" style="padding-top: 0; padding-bottom: 48px" [class.has-aside]="hasAside()">
      <div class="row wrap results-filters">
        <span #filterBox class="dropdown">
          <button
            type="button"
            class="chip dropdown-toggle"
            [class.is-active]="exclude().length > 0"
            [attr.aria-expanded]="filterMenuOpen()"
            aria-label="过滤选项"
            (click)="filterMenuOpen.set(!filterMenuOpen())"
          >
            过滤 @if (exclude().length > 0) {
              <span class="dropdown-count mono">{{ exclude().length }}</span>
            }
            <vx-icon name="chevron-down" [size]="13" />
          </button>
          @if (filterMenuOpen()) {
            <div class="dropdown-menu" role="menu" aria-label="过滤选项">
              @for (f of FILTERS; track f.key) {
                <button
                  type="button"
                  class="dropdown-item"
                  role="menuitemcheckbox"
                  [attr.aria-checked]="exclude().includes(f.key)"
                  (click)="toggleExclude(f.key)"
                >
                  <span class="dd-check">
                    @if (exclude().includes(f.key)) {
                      <vx-icon name="check" [size]="14" />
                    }
                  </span>
                  <span class="stack" style="gap: 1px">
                    <span>{{ f.label }}</span>
                    <span class="faint small">{{ f.hint }}</span>
                  </span>
                </button>
              }
            </div>
          }
        </span>
        <span #timeBox class="dropdown">
          <button
            type="button"
            class="chip dropdown-toggle"
            [class.is-active]="since() > 0"
            [attr.aria-expanded]="timeMenuOpen()"
            aria-label="时间范围"
            (click)="timeMenuOpen.set(!timeMenuOpen())"
          >
            时间 @if (since() > 0) {
              · {{ timeLabel() }}
            }
            <vx-icon name="chevron-down" [size]="13" />
          </button>
          @if (timeMenuOpen()) {
            <div class="dropdown-menu" role="menu" aria-label="时间范围">
              @for (t of TIME_WINDOWS; track t.since) {
                <button
                  type="button"
                  class="dropdown-item"
                  role="menuitemradio"
                  [attr.aria-checked]="since() === t.since"
                  (click)="setSince(t.since); timeMenuOpen.set(false)"
                >
                  <span class="dd-check">
                    @if (since() === t.since) {
                      <vx-icon name="check" [size]="14" />
                    }
                  </span>
                  <span>{{ t.label }}</span>
                </button>
              }
            </div>
          }
        </span>

        <!-- 识别到的查询语法：让人确认 site:/+/-/引号 真的生效了，
             而不是被当成普通词搜了一遍 -->
        @if (syntax(); as s) {
          @for (r of s.required ?? []; track r) {
            <span class="chip chip-accent mono">+{{ r }}</span>
          }
          @for (e of s.excluded ?? []; track e) {
            <span class="chip chip-accent mono">-{{ e }}</span>
          }
          @for (p of s.phrases ?? []; track p) {
            <span class="chip chip-accent mono">“{{ p }}”</span>
          }
          @if (s.site) {
            <span class="chip chip-accent mono">site:{{ s.site }}</span>
          }
        }
      </div>

      <div class="results-main">
      @if (!loading() && freshIntent()) {
        <div class="count-row" role="status">
          <span class="chip chip-accent" title="查询含时效意图，结果已按发布时间加权">
            <vx-icon name="clock" [size]="12" />
            按发布时间优先
          </span>
        </div>
      }

      @if (error()) {
        <div class="notice notice-error" style="margin-bottom: 12px">{{ error() }}</div>
      }

      @if (showSkeleton()) {
        <div class="stack" style="gap: 12px" aria-busy="true" aria-label="正在检索">
          @for (i of SKELETONS; track i) {
            <div class="card skeleton-card">
              <span class="sk-line sk-title"></span>
              <span class="sk-line sk-meta"></span>
              <span class="sk-line sk-desc"></span>
              <span class="sk-line sk-desc sk-desc-2"></span>
            </div>
          }
        </div>
      }

      @if (!loading() && !error() && hits().length === 0 && query()) {
        <div class="card" style="text-align: center; padding: 32px">
          <p class="muted">没有找到结果</p>
          <p class="faint small" style="margin-top: 4px">
            索引只覆盖白名单站点，试着去掉过滤或缩短关键词
          </p>
        </div>
      }

      <div class="stack" style="gap: 12px" [class.is-loading]="loading() && !showSkeleton()">
        @for (h of strong(); track h.url) {
          <vx-result-card [hit]="h" [q]="query()" [aiSummary]="aiSummary()" />
        }
      </div>

      @if (weak().length > 0) {
        <div style="margin-top: 12px">
          <button type="button" class="btn btn-ghost btn-sm" (click)="showWeak.set(!showWeak())">
            {{ showWeak() ? '收起弱相关结果' : '还有 ' + weak().length + ' 条相关性较低的结果' }}
          </button>
          @if (showWeak()) {
            <div class="stack" style="gap: 12px; margin-top: 12px">
              @for (h of weak(); track h.url) {
                <vx-result-card [hit]="h" [q]="query()" [aiSummary]="aiSummary()" />
              }
            </div>
          }
        </div>
      }

      @if (discover().length > 0) {
        <div class="discover-head">
          <span class="discover-icon">
            <vx-icon name="radar" [size]="16" />
          </span>
          <span class="discover-title">深网发现</span>
          <span class="discover-hint">相关但少被引用的内容</span>
        </div>
        <div class="stack" style="gap: 10px; margin-top: 8px">
          @for (h of discover(); track h.url) {
            <vx-result-card [hit]="h" [q]="query()" [aiSummary]="aiSummary()" />
          }
        </div>
      }

      </div>

      <aside class="results-aside">
        @if (related().length > 0) {
          <div class="related">
            <div class="discover-head related-head">
              <span class="discover-icon">
                <vx-icon name="tag" [size]="16" />
              </span>
              <span class="related-title">深入了解 <span class="related-term">{{ query() }}</span></span>
            </div>
            @for (it of related(); track it) {
              <a
                class="related-item"
                [routerLink]="['/search']"
                [queryParams]="{ q: it }"
                [title]="it"
              >
                <vx-icon name="search" [size]="15" />
                <span class="related-text">
                  @for (p of parts(it, query()); track $index) {
                    @if (p.hit) {
                      <mark class="related-hit">{{ p.text }}</mark>
                    } @else {
                      {{ p.text }}
                    }
                  }
                </span>
              </a>
            }
          </div>
        }

        @if (wiki(); as w) {
          <div class="wiki-card">
            <div class="wiki-head">
              <span class="wiki-mark" aria-hidden="true">W</span>
              <span class="wiki-source">维基百科</span>
            </div>
            <a class="wiki-title" [href]="w.url" target="_blank" rel="noreferrer">{{ w.title }}</a>
            <p class="wiki-extract">{{ wikiShort(w.extract) }}</p>
            <a class="wiki-more" [href]="w.url" target="_blank" rel="noreferrer">全部显示</a>
          </div>
        }
      </aside>

      @if (pages() > 1) {
        <div class="results-pager">
          <vx-pager [page]="pageIndex()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />
        </div>
      }
    </div>
  `,
})
export class SearchPage {
  private readonly api = inject(PublicApi);
  private readonly router = inject(Router);
  private readonly prefsService = inject(PrefsService);
  private readonly session = inject(SearchSession);

  /** 由 withComponentInputBinding 从 ?q= / ?page= 注入 */
  readonly q = input('');
  readonly page = input(1);

  // 路由绑定的坑：?page= 缺省时输入被置为 undefined（覆盖默认值），有值时是字符串。
  // 所有消费点统一走这个归一化后的数字页码。
  private readonly routePage = computed(() => {
    const n = Number(this.page());
    return Number.isFinite(n) && n >= 1 ? Math.floor(n) : 1;
  });

  /**
   * 当前生效的查询词。
   *
   * GET 模式（默认）从路由参数取——刷新、分享、前进后退都能还原；
   * POST 模式取内存会话，地址栏里不带搜索词，代价是刷新会丢。
   * 两个来源合成一个 computed，下游（effect / 卡片 / 空态）不用关心模式。
   */
  protected readonly query = computed(() =>
    this.prefsService.getRequest() ? this.q().trim() : this.session.q().trim(),
  );

  protected readonly pageIndex = computed(() =>
    this.prefsService.getRequest() ? this.routePage() : this.session.page(),
  );

  protected readonly FILTERS = FILTERS;
  protected readonly TIME_WINDOWS = TIME_WINDOWS;

  protected readonly exclude = signal<string[]>([...this.prefsService.prefs().exclude]);
  protected readonly since = signal(this.prefsService.prefs().since);
  /** 过滤下拉：点外部 / Esc 关闭（多选切换不关闭）。 */
  protected readonly filterMenuOpen = signal(false);
  private readonly filterBox = viewChild<ElementRef<HTMLElement>>('filterBox');
  /** 时间下拉（工具库）：单选，选中即关。 */
  protected readonly timeMenuOpen = signal(false);
  private readonly timeBox = viewChild<ElementRef<HTMLElement>>('timeBox');
  protected readonly timeLabel = computed(
    () => TIME_WINDOWS.find((t) => t.since === this.since())?.label ?? '全部',
  );

  protected readonly hits = signal<Hit[]>([]);
  protected readonly discover = signal<Hit[]>([]);
  protected readonly total = signal(0);
  // 初值 true：组件刚挂载、effect 还没跑完的第一帧不该先闪一下「没有找到结果」
  protected readonly loading = signal(true);
  protected readonly error = signal('');

  // ---- 维基百科卡片（懒加载，仅单个词）----
  protected readonly wiki = signal<{ title: string; extract: string; url: string } | null>(null);
  /** 维基面板开关（后台控制，默认关闭）。 */
  protected readonly wikiOn = signal(false);

  // ---- 相关搜索（Bing 联想）----
  /** 后台开关（related_links）。 */
  protected readonly relatedOn = signal(false);
  /** 用户偏好：设置页可关（localStorage，默认开）。 */
  protected readonly relatedPref = signal(localStorage.getItem(PREF_RELATED) !== '0');
  protected readonly related = signal<string[]>([]);
  private relatedQ = '';

  /** 侧栏是否有内容（wiki 卡片或相关搜索）。 */
  protected readonly hasAside = computed(() => !!this.wiki() || this.related().length > 0);

  private loadRelated(q: string): void {
    if (!this.relatedOn() || !this.relatedPref()) {
      this.related.set([]);
      return;
    }
    const term = q.trim();
    if (!term) {
      this.relatedQ = '';
      this.related.set([]);
      return;
    }
    if (term === this.relatedQ) return;
    this.relatedQ = term;
    this.related.set([]);
    void this.api.related(term).then((r) => {
      if (this.relatedQ === term) this.related.set((r.items ?? []).slice(0, 5));
    });
  }

  /** 把联想词按搜索词切成高亮片段。 */
  protected parts(item: string, q: string): { text: string; hit: boolean }[] {
    const term = q.trim();
    if (!term) return [{ text: item, hit: false }];
    const lower = item.toLowerCase();
    const t = term.toLowerCase();
    const out: { text: string; hit: boolean }[] = [];
    let i = 0;
    while (i < item.length) {
      const at = lower.indexOf(t, i);
      if (at < 0) {
        out.push({ text: item.slice(i), hit: false });
        break;
      }
      if (at > i) out.push({ text: item.slice(i, at), hit: false });
      out.push({ text: item.slice(at, at + term.length), hit: true });
      i = at + term.length;
    }
    return out;
  }
  private wikiQ = '';

  private loadWiki(q: string): void {
    if (!this.wikiOn()) {
      this.wiki.set(null);
      return;
    }
    const term = q.trim();
    // 只对「一个词」展示：句子、多词组合、空查询都不请求
    if (!term || /\s/.test(term) || term.length > 30) {
      this.wikiQ = '';
      this.wiki.set(null);
      return;
    }
    if (term === this.wikiQ) return; // 同一个词不重复请求
    this.wikiQ = term;
    this.wiki.set(null);
    void this.api.wiki(term)
      .then((w) => { if (this.wikiQ === term) this.wiki.set(w); })
      .catch(() => { if (this.wikiQ === term) this.wiki.set(null); });
  }

  protected wikiShort(t: string): string {
    const clean = t.replace(/\s+/g, ' ').trim();
    return clean.length > 150 ? clean.slice(0, 150) + '…' : clean;
  }

  // 查询词变化时懒加载维基卡片（字段级 effect 在注入上下文内合法）
  private readonly wikiEffect = effect(() => this.loadWiki(this.query()));
  private readonly relatedEffect = effect(() => this.loadRelated(this.query()));
  protected readonly showWeak = signal(false);
  protected readonly freshIntent = signal(false);
  /** 查询语法的解析回显；没用语法时为 null，不渲染任何 chip。 */
  protected readonly syntax = signal<QuerySyntax | null>(null);
  // 骨架屏只在「还没有任何结果」时出场。翻页/改过滤时旧结果仍在，变淡就够——
  // 把已看到的内容换成骨架反而更晃眼。
  protected readonly showSkeleton = computed(
    () => this.loading() && this.hits().length === 0 && this.query() !== '',
  );
  protected readonly SKELETONS = [0, 1, 2];
  // 原文摘要入口由后台开关决定；默认 false，接口失败也保持 false
  protected readonly aiSummary = signal(false);

  // 弱相关折叠：召回为了保住中文查询命中英文文档而放宽到「命中任一词即召回」，
  // 排序只能保证最相关的在前，保证不了弱相关不出现——「单元测试」首条 3.96 分、
  // 第 2 条 0.37 分，其后全是噪声。数据不删（翻页/展开仍可取），只收进折叠区。
  protected readonly strong = computed(() => {
    const list = this.hits();
    if (this.pageIndex() !== 1 || list.length <= 2) return list;
    const top = list[0]?.score ?? 0;
    if (top <= 0) return list;
    const cut = top * 0.2;
    return list.filter((h) => h.score >= cut);
  });

  protected readonly weak = computed(() => {
    const list = this.hits();
    if (this.pageIndex() !== 1 || list.length <= 2) return [];
    const top = list[0]?.score ?? 0;
    if (top <= 0) return [];
    const cut = top * 0.2;
    return list.filter((h) => h.score < cut);
  });

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / PAGE_SIZE)));

  private seq = 0;

  constructor() {
    effect(() => {
      const query = this.query();
      const pg = this.pageIndex();
      const exclude = this.exclude();
      const since = this.since();
      this.showWeak.set(false);
      void this.run(query, pg, exclude, since);
    });

    this.api.publicFeatures().then((r) => {
      this.aiSummary.set(Boolean(r.features?.['ai_summary']));
      this.wikiOn.set(Boolean(r.features?.['wiki_panel']));
      this.relatedOn.set(r.features?.['related_links'] !== false);
    });
  }

  private async run(query: string, page: number, exclude: string[], since: number): Promise<void> {
    if (!query) {
      this.seq++;
      this.hits.set([]);
      this.discover.set([]);
      this.total.set(0);
      this.loading.set(false);
      this.error.set('');
      this.syntax.set(null);
      return;
    }

    const seq = ++this.seq;
    this.loading.set(true);
    this.error.set('');

    try {
      const r = await this.api.search(query, {
        exclude,
        since,
        page,
        size: PAGE_SIZE,
        post: !this.prefsService.getRequest(),
      });
      if (seq !== this.seq) return; // 已有更新的请求，丢弃旧响应
      this.hits.set(r.hits ?? []);
      this.discover.set(r.discover ?? []);
      this.total.set(r.total);
      this.freshIntent.set(Boolean(r.freshness_intent));
      this.syntax.set(r.syntax ?? null);
    } catch (e) {
      if (seq !== this.seq) return;
      this.error.set(errMsg(e));
      this.hits.set([]);
      this.discover.set([]);
      this.total.set(0);
    } finally {
      if (seq === this.seq) {
        this.loading.set(false);
      }
    }
  }

  protected toggleExclude(key: string): void {
    this.exclude.update((list) =>
      list.includes(key) ? list.filter((x) => x !== key) : [...list, key],
    );
    this.resetPageIfNeeded();
  }

  /** 点在浮层菜单外面时收起（过滤多选不关，时间单选关）。 */
  protected closeMenus(event: Event): void {
    const target = event.target as Node;
    if (this.filterMenuOpen()) {
      const box = this.filterBox()?.nativeElement;
      if (box && !box.contains(target)) this.filterMenuOpen.set(false);
    }
    if (this.timeMenuOpen()) {
      const box = this.timeBox()?.nativeElement;
      if (box && !box.contains(target)) this.timeMenuOpen.set(false);
    }
  }

  protected closeMenusOnEsc(): void {
    this.filterMenuOpen.set(false);
    this.timeMenuOpen.set(false);
  }

  protected setSince(since: number): void {
    this.since.set(since);
    this.resetPageIfNeeded();
  }

  // 偏好改动后从第 1 页重新搜；已在第 1 页时 effect 直接带新过滤重跑
  private resetPageIfNeeded(): void {
    if (this.pageIndex() <= 1 || !this.query()) return;
    if (this.prefsService.getRequest()) {
      void this.router.navigate(['/search'], { queryParams: { q: this.q(), page: 1 } });
      return;
    }
    this.session.setPage(1);
  }

  protected goPage(p: number): void {
    if (this.prefsService.getRequest()) {
      void this.router.navigate(['/search'], { queryParams: { q: this.q(), page: p } });
      return;
    }
    // POST 模式：页码只活在内存里，不写回地址栏
    this.session.setPage(p);
  }
}
