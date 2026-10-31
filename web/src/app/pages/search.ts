import {
  Component,
  ElementRef,
  computed,
  DestroyRef,
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
import { MapLogo } from '../shared/ui/map-logo';
import { Pager } from '../shared/ui/pager';
import { ResultCard } from './result-card';
import { AiOverview } from './ai-overview';

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
  imports: [Icon, MapLogo, Pager, ResultCard, RouterLink, AiOverview],
  host: {
    '(document:keydown.escape)': 'closeMenusOnEsc()',
    '(document:click)': 'closeMenus($event)',
  },
  template: `
    <div
      class="page page-results"
      style="padding-top: 0"
      [style.padding-bottom]="mapProvider() ? '0' : '48px'"
      [class.has-aside]="hasAside()"
      [class.has-map]="mapProvider() !== ''"
    >
      <div class="results-filters">
        <!-- 分类快捷 tab（图片/新闻/文档）+ 地图 + 更多 + 过滤 + 时间。
             tab 是单选快捷方式，选中即 cats=[key]；再点一次回到不限。
             「更多」展开全部分类（多选）；「过滤」只放排除标签（分类已有两个入口）。
             三个入口改的是同一个 cats 状态，不会各说各话。
             顺序按使用频率：日常切换的分类在前，过滤与时间这两个低频控件排在后。 -->
        <!-- 移动端：tabs 行的四个下拉改成底部弹窗，这条遮罩负责视觉隔离与「点空白关闭」。
             桌面端它 display:none，收起点由 document:click 处理（见 closeMenus）。 -->
        @if (anyMenuOpen()) {
          <div class="dd-scrim" aria-hidden="true" (click)="closeAllMenus()"></div>
        }

        <div class="tabs-row">
          <!-- 「全部」= 不限分类。放在分类 tab 最前面，等于给这组 tab 一个「归零」入口：
               选了某个分类后不必再点那个 tab 才取消，直接点全部。 -->
          <button
            type="button"
            class="stab"
            [class.is-active]="cats().length === 0"
            [attr.aria-pressed]="cats().length === 0"
            (click)="showAll()"
          >
            全部
          </button>

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
                    <vx-map-logo [name]="m.key" />
                    <span>{{ m.label }}</span>
                  </button>
                }
                <button type="button" class="dd-done" (click)="closeAllMenus()">完成</button>
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
                <button type="button" class="dd-done" (click)="closeAllMenus()">完成</button>
              </div>
            }
          </span>

          <!-- 过滤放在最后（紧跟「时间」之前）：它是最少用的入口，
               而「全部/图片/新闻/文档」是日常切换项，应该先看到。 -->
          <span #filterBox class="dropdown dropdown-end">
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
                <button type="button" class="dd-done" (click)="closeAllMenus()">完成</button>
              </div>
            }
          </span>

          <span #timeBox class="dropdown dropdown-end">
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
                <button type="button" class="dd-done" (click)="closeAllMenus()">完成</button>
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

      <!-- 地图：选中服务商后占满整块结果区（连过滤行和左右边距都让出来）。
           做法是把关键词直接拼进地图网站自己的搜索地址，等于把这个页面原样装进来——
           不是嵌地图组件的 iframe API，所以地点、结果列表、缩放都和直接打开一样。
           referrerpolicy 是**按服务商分开**的，两家要求相反：
           高德要 Referer（去掉后瓦片全灰），百度反之（带 Referer 会弹安全验证、
           并停在你所在的城市）。别图省事统一设一个值。 -->
      @if (mapProvider()) {
        <div class="map-panel">
          <!-- referrerpolicy 用两个固定分支而不是绑属性：Angular 对 iframe 的
               referrerpolicy 属性绑定会在运行时报错（NG0910）、整块视图不渲染。
               静态属性各写一遍最稳，两行重复换一个不会白屏的 iframe。 -->
          @if (mapProvider() === 'baidu') {
            <iframe class="map-frame" referrerpolicy="no-referrer" [src]="mapSrcSafe()" [title]="mapLabel() + '搜索'"></iframe>
          } @else {
            <iframe class="map-frame" [src]="mapSrcSafe()" [title]="mapLabel() + '搜索'"></iframe>
          }
          <div class="map-actions">
            <a class="map-act" [href]="mapLink()" target="_blank" rel="noopener" aria-label="在新窗口打开">
              <vx-icon name="external-link" [size]="15" />
            </a>
            <button type="button" class="map-act" (click)="closeMap()" aria-label="关闭地图">
              <vx-icon name="x" [size]="16" />
            </button>
          </div>
        </div>
      }

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
        <div class="results-list" aria-busy="true" aria-label="正在检索">
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

      <vx-ai-overview [q]="query()" [enabled]="aiOverview()" />

      <div class="results-list" [class.is-loading]="loading() && !showSkeleton()">
        @for (h of strong(); track h.url) {
          <vx-result-card [hit]="h" [q]="query()" [aiSummary]="aiSummary()" />
        }
      </div>

      @if (weak().length > 0) {
        <!-- 收起钮在展开列表的末尾：读完最后一条再收，视线不用跳回顶部。 -->
        <div class="weak-toggle">
          @if (showWeak()) {
            <div class="results-list">
              @for (h of weak(); track h.url) {
                <vx-result-card [hit]="h" [q]="query()" [aiSummary]="aiSummary()" />
              }
            </div>
          }
          <button type="button" (click)="showWeak.set(!showWeak())">
            {{ showWeak() ? '收起' : '更多结果' }}
          </button>
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

  /** 地图服务商。高德 / 百度排前面——谷歌在国内网络打不开，实际能用的是这两家。
   *  每家都是「把关键词拼进它自己的搜索地址」，不调用任何地图 API，也不需要 Key。
   *  必应不在这里：它的搜索页带 X-Frame-Options: DENY，浏览器根本不允许别人嵌；
   *  唯一能嵌的 /maps/embed 只认经纬度、忽略 q，等于得自己先解析地名。 */
  protected readonly MAPS = [
    { key: 'amap', label: '高德地图' },
    { key: 'baidu', label: '百度地图' },
    { key: 'google', label: '谷歌地图' },
  ] as const;

  /** 地图服务商：空串 = 不显示。 */
  protected readonly mapProvider = signal<'' | 'amap' | 'baidu' | 'google'>('');
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
    const q = encodeURIComponent(this.query().trim());
    const p = this.mapProvider();
    if (p === 'amap') {
      // 高德网页版自己的搜索地址：关键词直接交给它，定位、结果列表、缩放都由它负责。
      return q ? `https://ditu.amap.com/search?query=${q}` : 'https://ditu.amap.com/';
    }
    if (p === 'baidu') {
      // querytype=s 是「搜索」；wd 就是关键词。百度会自己把它解析成城市中心。
      return q ? `https://map.baidu.com/search/?querytype=s&wd=${q}` : 'https://map.baidu.com/';
    }
    // 谷歌只允许用 output=embed 这种方式嵌（直接开搜索页会被它拦）。
    return q
      ? `https://maps.google.com/maps?q=${q}&z=12&output=embed`
      : 'https://maps.google.com/maps?z=2&output=embed';
  }

  /** 「在新窗口打开」用人类页面，不用嵌入地址（谷歌的 embed 单独打开会提示必须放在 iframe 里）。 */
  protected mapLink(): string {
    const q = encodeURIComponent(this.query().trim());
    const p = this.mapProvider();
    if (p === 'amap') return `https://ditu.amap.com/search?query=${q}`;
    if (p === 'baidu') return `https://map.baidu.com/search/?querytype=s&wd=${q}`;
    return `https://www.google.com/maps/search/${q}`;
  }

  protected setMap(key: 'amap' | 'baidu' | 'google'): void {
    this.mapProvider.set(key);
    this.mapMenuOpen.set(false);
  }

  /** 关掉地图回到结果。地点留着，下次再开还是上次那个地方。 */
  protected closeMap(): void {
    this.mapProvider.set('');
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

  /** 「全部」= 不限分类。本来就是全部时不重搜——点了没反应总比白刷一次结果好。 */
  protected showAll(): void {
    if (this.cats().length === 0) return;
    this.cats.set([]);
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
  protected readonly aiOverview = signal(false);

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

    // 地图铺满整屏时页脚会把文档撑高（多出一条可滚动的空白），所以挂在 body 上关掉它。
    // 用 body 类而不是 :has()：这条状态要能被测试与排查看见，也不依赖浏览器对 :has 的支持。
    effect(() => {
      document.body.classList.toggle('is-map-open', this.mapProvider() !== '');
    });
    inject(DestroyRef).onDestroy(() => document.body.classList.remove('is-map-open'));

    this.api.publicFeatures().then((r) => {
      this.aiSummary.set(Boolean(r.features?.['ai_summary']));
      this.aiOverview.set(Boolean(r.features?.['ai_overview']));
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
    this.closeAllMenus();
  }

  /** 任一浮层打开（移动端据此显示遮罩）。 */
  protected readonly anyMenuOpen = computed(
    () =>
      this.filterMenuOpen() || this.timeMenuOpen() || this.mapMenuOpen() || this.moreMenuOpen(),
  );

  /** 收起全部浮层：Esc、点遮罩、点「完成」共用这一条。 */
  protected closeAllMenus(): void {
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
