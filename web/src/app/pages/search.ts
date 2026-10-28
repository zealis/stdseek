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
import { DomSanitizer, type SafeResourceUrl } from '@angular/platform-browser';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { PrefsService } from '../core/prefs.service';
import { SearchSession } from '../core/search-session';
import { CATEGORIES, FILTERS, TIME_WINDOWS, type Hit, type QuerySyntax } from '../core/api';
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
      <div class="results-filters">
        <!-- 分类快捷 tab（图片/新闻/文档）+ 地图 + 更多。
             tab 是单选快捷方式，选中即 cats=[key]；再点一次回到不限。
             「更多」展开全部分类（多选），「过滤」保留原有的分类+标签两段，
             三个入口改的是同一个 cats 状态，不会各说各话。 -->
        <div class="tabs-row">
          <span #filterBox class="dropdown">
            <button
              type="button"
              class="stab"
              [class.is-active]="filterCount() > 0"
              [attr.aria-expanded]="filterMenuOpen()"
              aria-label="过滤选项"
              (click)="filterMenuOpen.set(!filterMenuOpen())"
            >
              过滤
              @if (filterCount() > 0) {
                <span class="dropdown-count mono">{{ filterCount() }}</span>
              }
              <vx-icon name="chevron-down" [size]="13" />
            </button>
            @if (filterMenuOpen()) {
              <div class="dropdown-menu" role="menu" aria-label="过滤选项">
                <!-- 这里只放排除项：分类已经有两个入口（上面的 tab 和「更多」），
                     再列一遍只会让人在这里找分类、找不到就以为功能没了。 -->
                <div class="dd-group">排除这些标签</div>
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

          @for (t of TABS; track t.key) {
            <button
              type="button"
              class="stab"
              [class.is-active]="isTab(t.key)"
              [attr.aria-pressed]="isTab(t.key)"
              (click)="toggleTab(t.key)"
            >
              {{ t.label }}
            </button>
          }

          <span #mapBox class="dropdown">
            <button
              type="button"
              class="stab"
              [class.is-active]="mapProvider() !== ''"
              [attr.aria-expanded]="mapMenuOpen()"
              (click)="mapMenuOpen.set(!mapMenuOpen())"
            >
              地图
              <vx-icon name="chevron-down" [size]="13" />
            </button>
            @if (mapMenuOpen()) {
              <div class="dropdown-menu" role="menu" aria-label="地图服务">
                @for (m of MAPS; track m.key) {
                  <button
                    type="button"
                    class="dropdown-item"
                    role="menuitemradio"
                    [attr.aria-checked]="mapProvider() === m.key"
                    (click)="setMap(m.key)"
                  >
                    <span class="dd-check">
                      @if (mapProvider() === m.key) {
                        <vx-icon name="check" [size]="14" />
                      }
                    </span>
                    <span>{{ m.label }}</span>
                  </button>
                }
              </div>
            }
          </span>

          <span #moreBox class="dropdown">
            <button
              type="button"
              class="stab"
              [class.is-active]="cats().length > 0"
              [attr.aria-expanded]="moreMenuOpen()"
              (click)="moreMenuOpen.set(!moreMenuOpen())"
            >
              更多
              @if (cats().length > 0) {
                <span class="dropdown-count mono">{{ cats().length }}</span>
              }
              <vx-icon name="chevron-down" [size]="13" />
            </button>
            @if (moreMenuOpen()) {
              <div class="dropdown-menu" role="menu" aria-label="全部分类">
                <div class="dd-group">分类</div>
                @for (c of CATEGORIES; track c.value) {
                  <button
                    type="button"
                    class="dropdown-item"
                    role="menuitemcheckbox"
                    [attr.aria-checked]="cats().includes(c.value)"
                    (click)="toggleCat(c.value)"
                  >
                    <span class="dd-check">
                      @if (cats().includes(c.value)) {
                        <vx-icon name="check" [size]="14" />
                      }
                    </span>
                    <span>{{ c.label }}</span>
                  </button>
                }
              </div>
            }
          </span>

          <span class="tabs-spacer"></span>

          <span #timeBox class="dropdown">
            <button
              type="button"
              class="stab"
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
        </div>

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
      <!-- 地图：选服务商后把结果页上方让出一块，用对方的嵌入页展示。
           嵌入页拿到的是搜索词本身，等于把词交给了第三方——隐私页有对应条目。 -->
      @if (mapProvider()) {
        <div class="map-panel">
          <div class="row wrap" style="margin-bottom: 8px; gap: 8px">
            <span class="row" style="gap: 6px; flex: none">
              <vx-icon name="globe" [size]="14" />
              <span class="small">{{ mapLabel() }}</span>
            </span>
            <!-- 地图查的是**地点**，不是搜索词：搜「golang」时地图该显示什么？
                 所以这里给一个可改的地点输入框，默认填用户刚才输入的词，
                 他会自己改成「上海」这类地名。 -->
            <form class="row" style="gap: 6px; flex: 1; min-width: 220px" (submit)="submitMap($event)">
              <input
                class="input"
                type="search"
                placeholder="输入地点，如 上海 / Tokyo"
                aria-label="地图地点"
                [value]="mapInput()"
                (input)="mapInput.set($any($event.target).value)"
              />
              <button type="submit" class="btn btn-outline btn-sm">搜索</button>
            </form>
            <div class="row" style="gap: 6px; flex: none">
              @if (mapProvider() === 'apple') {
                <a class="btn btn-outline btn-sm" [href]="mapLink()" target="_blank" rel="noopener">
                  在新窗口打开
                </a>
              }
              <button type="button" class="btn btn-ghost btn-sm" (click)="mapProvider.set('')">
                关闭
              </button>
            </div>
          </div>
          @if (mapProvider() === 'apple') {
            <!-- Apple 地图的响应头带 X-Frame-Options: SAMEORIGIN，iframe 一定是白屏，
                 与其给一块空白不如说清楚并给出口。 -->
            <p class="small muted" style="margin: 0 0 8px">
              Apple 地图不允许被嵌进其他网站，只能在新窗口打开。
            </p>
          } @else if (!mapQ().trim()) {
            <p class="small muted" style="margin: 0">输入地点后按回车，地图显示该地点。</p>
          } @else {
            <iframe
              class="map-frame"
              [src]="mapSrcSafe()"
              title="地图搜索结果"
              loading="lazy"
              referrerpolicy="no-referrer"
            ></iframe>
            <p class="faint small" style="margin: 6px 0 0">
              地图由{{ mapLabel() }}提供；加载不出来时可直接
              <a [href]="mapSrc()" target="_blank" rel="noopener">在新窗口打开</a>。
            </p>
          }
        </div>
      }

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
  protected readonly CATEGORIES = CATEGORIES;
  protected readonly TIME_WINDOWS = TIME_WINDOWS;

  protected readonly exclude = signal<string[]>([...this.prefsService.prefs().exclude]);
  /** 只看这些站点分类（多选取并集）。 */
  protected readonly cats = signal<string[]>([...(this.prefsService.prefs().cats ?? [])]);
  /** 过滤按钮上的角标只数排除项：分类现在有 tab 和「更多」两个入口，
   *  再在这里重复计数会出现「只点了图片、过滤却亮着 1」。 */
  protected readonly filterCount = computed(() => this.exclude().length);

  /** 分类快捷 tab：只做单选取舍，真正的状态仍然是 cats。 */
  protected readonly TABS = [
    { key: 'image', label: '图片' },
    { key: 'media', label: '新闻' },
    { key: 'docs', label: '文档' },
  ] as const;

  protected readonly MAPS = [
    { key: 'google', label: '谷歌地图' },
    { key: 'bing', label: '必应地图' },
    { key: 'apple', label: 'Apple 地图' },
  ] as const;

  /** 地图服务商：空串 = 不显示。Apple 不支持 iframe，只给外链。 */
  protected readonly mapProvider = signal<'' | 'google' | 'bing' | 'apple'>('');
  /** 输入框里的草稿地点与已提交的地点分开：每敲一个字都重载 iframe 会一直闪。 */
  protected readonly mapInput = signal('');
  protected readonly mapQ = signal('');
  protected readonly mapMenuOpen = signal(false);
  protected readonly moreMenuOpen = signal(false);

  private readonly sanitizer = inject(DomSanitizer);

  /** iframe 的 src 是 RESOURCE_URL 上下文：直接绑字符串会抛 NG0904（整块视图不更新）。
   *  地址由我们自己拼（固定域名 + encodeURIComponent 的词），所以按可信资源放行。 */
  protected readonly mapSrcSafe = computed<SafeResourceUrl>(() =>
    this.sanitizer.bypassSecurityTrustResourceUrl(this.mapSrc()),
  );

  protected mapLabel(): string {
    return this.MAPS.find((m) => m.key === this.mapProvider())?.label ?? '';
  }

  /** 嵌入地址。两家都只接受「地点词」而不需要 API Key。 */
  protected mapSrc(): string {
    const q = encodeURIComponent(this.mapQ().trim());
    return this.mapProvider() === 'bing'
      ? `https://www.bing.com/maps/embed?q=${q}`
      : `https://maps.google.com/maps?q=${q}&z=12&output=embed`;
  }

  protected mapLink(): string {
    const q = encodeURIComponent(this.mapQ().trim());
    return this.mapProvider() === 'apple'
      ? `https://maps.apple.com/?q=${q}`
      : this.mapSrc();
  }

  protected setMap(key: 'google' | 'bing' | 'apple'): void {
    this.mapProvider.set(key);
    this.mapMenuOpen.set(false);
  }

  /** 回车才真正查：输入框逐字写 mapQ 会让 iframe 每敲一下重载一次。 */
  protected submitMap(event: Event): void {
    event.preventDefault();
    this.mapQ.set(this.mapInput().trim());
  }

  protected isTab(key: string): boolean {
    const list = this.cats();
    return list.length === 1 && list[0] === key;
  }

  /** tab 是单选：点同一个再点一次＝取消，点别的＝替换。 */
  protected toggleTab(key: string): void {
    this.cats.set(this.isTab(key) ? [] : [key]);
    this.resetPageIfNeeded();
  }
  protected readonly since = signal(this.prefsService.prefs().since);
  /** 过滤下拉：点外部 / Esc 关闭（多选切换不关闭）。 */
  protected readonly filterMenuOpen = signal(false);
  private readonly filterBox = viewChild<ElementRef<HTMLElement>>('filterBox');
  private readonly mapBox = viewChild<ElementRef<HTMLElement>>('mapBox');
  private readonly moreBox = viewChild<ElementRef<HTMLElement>>('moreBox');
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
      const cats = this.cats();
      const since = this.since();
      this.showWeak.set(false);
      void this.run(query, pg, exclude, since, cats);
    });

    // 打开地图、或换了搜索词时，地点框默认填当前搜索词（可改）。
    // 只依赖 query / provider：用户手打的地点不会被自己敲的字反复覆盖。
    effect(() => {
      const q = this.query();
      if (!this.mapProvider()) return;
      this.mapInput.set(q);
      this.mapQ.set(q);
    });

    this.api.publicFeatures().then((r) => {
      this.aiSummary.set(Boolean(r.features?.['ai_summary']));
      this.wikiOn.set(Boolean(r.features?.['wiki_panel']));
      this.relatedOn.set(r.features?.['related_links'] !== false);
    });
  }

  private async run(
    query: string,
    page: number,
    exclude: string[],
    since: number,
    cats: string[],
  ): Promise<void> {
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
        cats,
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

  protected toggleCat(key: string): void {
    this.cats.update((list) =>
      list.includes(key) ? list.filter((x) => x !== key) : [...list, key],
    );
    this.resetPageIfNeeded();
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
    if (this.mapMenuOpen()) {
      const box = this.mapBox()?.nativeElement;
      if (box && !box.contains(target)) this.mapMenuOpen.set(false);
    }
    if (this.moreMenuOpen()) {
      const box = this.moreBox()?.nativeElement;
      if (box && !box.contains(target)) this.moreMenuOpen.set(false);
    }
  }

  protected closeMenusOnEsc(): void {
    this.filterMenuOpen.set(false);
    this.timeMenuOpen.set(false);
    this.mapMenuOpen.set(false);
    this.moreMenuOpen.set(false);
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
