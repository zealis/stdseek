import { Component, computed, inject, signal } from '@angular/core';
import { NgTemplateOutlet } from '@angular/common';
import { ActivatedRoute, NavigationEnd, Router } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { CATEGORIES, type SubmissionStatus } from '../core/api';
import { fullTime } from '../shared/format';
import { Icon } from '../shared/ui/icon';
import { CaptchaDialog } from '../shared/ui/captcha-dialog';
import { Modal } from '../shared/ui/modal';
import { AuthService, type CaptchaChallenge } from '../core/auth.service';

// 用户提交站点：进审核队列，不进白名单。管理员在「管理后台 → 审核队列」逐条处理。
const STEPS = ['已提交', '人工审核', '抓取入库'];

function stepIndex(status: string): number {
  if (status === 'approved') return 2; // 已审核通过，等下一次抓取
  if (status === 'rejected') return 1; // 卡在审核这一步
  return 0; // pending：只完成「已提交」
}

const STATUS_TONE: Record<string, string> = {
  pending: 'chip-warning',
  approved: 'chip-success',
  rejected: 'chip-danger',
};

/** 提交站点 + 查询审核进度。支持带 ?ticket= 的分享链接，刷新后仍能看到进度。 */
@Component({
  selector: 'vx-contribute-page',
  imports: [Icon, NgTemplateOutlet, CaptchaDialog, Modal],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">提交站点</h1>
      <p class="page-desc">提交后进入审核队列，通过后可被搜索；同一域名重复提交会合并。</p>

      @if (ticket(); as t) {
        <div class="notice notice-success" style="margin-top: 16px">
          <div class="notice-body">已进入审核队列。请保存下方查询码，之后可随时查看进度。</div>
        </div>

        <div class="card" style="margin-top: 12px">
          <span class="faint small">查询码</span>
          <div class="row wrap" style="margin-top: 6px">
            <span class="mono" style="font-size: var(--fs-lg); color: var(--brand); font-weight: 500">
              {{ t }}
            </span>
            <button type="button" class="btn btn-sm" (click)="copyTicket()">
              <vx-icon [name]="copied() ? 'check' : 'copy'" [size]="14" />
              {{ copied() ? '已复制' : '复制' }}
            </button>
          </div>
          <p class="faint small" style="margin-top: 8px">
            查询码只对应这一次提交，凭它可查看审核状态，不需要注册账号。
          </p>
        </div>

        <ng-container [ngTemplateOutlet]="statusCard" />
        <ng-container [ngTemplateOutlet]="againTpl" />
      } @else if (!agreed()) {
        <div class="card" style="margin-top: 16px; text-align: center; padding: 28px">
          <p class="muted">提交前请先看一遍提交要求。</p>
          <button type="button" class="btn btn-solid btn-sm" style="margin-top: 10px"
            (click)="agreeOpen.set(true)">查看提交要求</button>
        </div>
      } @else {
        <div class="card" style="margin-top: 16px">
          <div class="form-grid">
            <label class="field">
              <span class="field-label">网址</span>
              <input
                class="input"
                type="url"
                inputmode="url"
                autocomplete="url"
                placeholder="https://example.com 或 example.com"
                [value]="form().url"
                (input)="patchForm('url', urlEl.value)"
                #urlEl
              />
              <span class="field-hint">填站点首页即可，具体抓取入口由管理员按站点结构配置</span>
            </label>
            <!-- 分类只在「没开 AI 打标」时才让人选：AI 开着时分类由模型判定，
                 让用户手填会互相打架（提交里那份也不作数，服务端会覆盖成「其他」）。 -->
            @if (aiCategory()) {
              <div class="field">
                <span class="field-label">分类</span>
                <p class="field-hint" style="margin: 0">
                  由 AI 自动判定，你不需要选。审核通过后系统会按站点内容归类。
                </p>
              </div>
            } @else {
              <label class="field">
                <span class="field-label">分类</span>
                <select
                  class="input select"
                  [value]="form().category"
                  (change)="patchForm('category', catEl.value)"
                  #catEl
                >
                  @for (c of categories; track c.value) {
                    <option [value]="c.value" [selected]="c.value === form().category">
                      {{ c.label }}
                    </option>
                  }
                </select>
              </label>
            }
            <label class="field">
              <span class="field-label">理由（可选）</span>
              <textarea
                class="input textarea"
                rows="2"
                maxlength="300"
                placeholder="为什么这个站点值得收录？例如「Redis 官方命令参考」"
                [value]="form().reason"
                (input)="patchForm('reason', reasonEl.value)"
                #reasonEl
              ></textarea>
            </label>
            @if (err()) {
              <div class="notice notice-error">{{ err() }}</div>
            }
            <div>
              <button
                type="button"
                class="btn btn-solid"
                [disabled]="!form().url || busy()"
                (click)="submit()"
              >
                {{ busy() ? '提交中' : '提交' }}
              </button>
            </div>
          </div>
        </div>

        <h2 class="section-title" style="margin-top: 24px">查询已有提交</h2>
        <p class="faint small" style="margin-top: 4px; margin-bottom: 10px">
          查询码没保存无法找回。
        </p>
        <form class="row wrap" (submit)="queryStatus($event)">
          <input
            class="input"
            type="text"
            style="max-width: 320px"
            placeholder="vx…"
            [value]="query()"
            (input)="query.set(qEl.value.trim())"
            #qEl
          />
          <button type="submit" class="btn" [disabled]="!query().trim() || stBusy()">
            {{ stBusy() ? '查询中' : '查询' }}
          </button>
        </form>
        @if (stData() || stErr()) {
          <ng-container [ngTemplateOutlet]="statusCard" />
        }
      }

      <ng-template #statusCard>
        <div class="card" style="margin-top: 12px">
          @if (stErr()) {
            <div class="notice notice-error">{{ stErr() }}</div>
          }
          @if (stData(); as d) {
            <div class="row wrap">
              <span class="section-title">{{ d.domain }}</span>
              <span class="chip {{ STATUS_TONE[d.status] || '' }}">{{ d.status_text }}</span>
            </div>
            <div class="steps" style="margin: 12px 0">
              @for (s of STEPS; track s; let i = $index) {
                <span class="row" style="gap: 8px">
                  <span class="step" [class]="stepClass(d.status, i)">
                    <span class="step-dot"></span>
                    {{ d.status === 'rejected' && i === 1 ? '未通过审核' : s }}
                  </span>
                  @if (i < STEPS.length - 1) {
                    <span class="step-line"></span>
                  }
                </span>
              }
            </div>
            <p class="faint small">
              提交于 {{ fullTime(d.created_at) }}{{ d.status === 'approved' ? '，已加入抓取白名单，下一次抓取后可搜索' : '' }}
            </p>
          }
          @if (!stData() && !stErr()) {
            <p class="faint small">{{ stBusy() ? '查询中' : '暂无进度信息' }}</p>
          }
          <div style="margin-top: 12px">
            <button
              type="button"
              class="btn btn-sm"
              [disabled]="stBusy() || !query().trim()"
              (click)="loadStatus()"
            >
              刷新进度
            </button>
          </div>
        </div>
      </ng-template>

      <ng-template #againTpl>
        <div style="margin-top: 12px">
          <button type="button" class="btn btn-ghost btn-sm" (click)="startOver()">再提交一个</button>
        </div>
      </ng-template>
    </div>

    @if (agreeOpen()) {
      <vx-modal [title]="greet()" [width]="760" (closed)="agreeOpen.set(false)">
        <p class="guide-lead">感谢你为 voidxo 做出贡献！你太棒了。</p>
        <p class="guide-sub">为了节省你的时间、也让通过的机会更大，提交前请确认站点符合这三条：</p>

        <div class="guide-grid">
          <div class="guide-item">
            <vx-icon name="globe" [size]="18" />
            <p><strong>网站符合国际法规。</strong>内容合法，无病毒、网络钓鱼、强力跟踪等违规内容。</p>
          </div>
          <div class="guide-item">
            <vx-icon name="ban" [size]="18" />
            <p><strong>不是采集站或 AI 生成站。</strong>搬运、洗稿、批量生成的内容会被直接拒绝。</p>
          </div>
          <div class="guide-item">
            <vx-icon name="shield-check" [size]="18" />
            <p><strong>内容公开可访问。</strong>需要登录才能看的、或正文里全是广告的，收录价值很低。</p>
          </div>
        </div>

        <p class="guide-note">
          提交后进入人工审核，通过前不会出现在搜索里；同一域名重复提交会自动合并，不用反复试。
        </p>

        <!-- 勾选与按钮同一组、右对齐：原来一个贴左一个贴右，隔了整个弹窗宽度，
             视觉上是两件事，点完勾还得去找按钮在哪。 -->
        <div footer class="row" style="gap: 12px; width: 100%; justify-content: flex-end">
          <label class="row" style="gap: 8px; cursor: pointer; min-width: 0">
            <input
              type="checkbox"
              [checked]="agreed()"
              (change)="agreed.set($any($event.target).checked)"
            />
            <span class="small">我已阅读并同意上述提交要求</span>
          </label>
          <button type="button" class="btn btn-solid" [disabled]="!agreed()" (click)="startSubmit()">
            开始提交
          </button>
        </div>
      </vx-modal>
    }

    @if (capOpen()) {
      <vx-captcha-dialog
        [provider]="capProvider()"
        [siteKey]="tsSiteKey()"
        [endpoint]="capEndpoint()"
        [challenge]="cap()"
        action="submit"
        (solved)="onCaptchaSolved($event)"
        (closed)="capOpen.set(false)"
      />
    }
  `,
})
export class ContributePage {
  private readonly api = inject(PublicApi);
  private readonly authApi = inject(AuthService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router);

  protected readonly categories = CATEGORIES;
  protected readonly STEPS = STEPS;
  protected readonly STATUS_TONE = STATUS_TONE;
  protected readonly fullTime = fullTime;

  /** 提交前的同意闸：没勾同意就不放表单出来，避免「不看要求直接提交」。 */
  protected readonly agreed = signal(false);
  protected readonly agreeOpen = signal(false);

  /** 标题：登录了就叫邮箱前缀，没登录就是「你好」——照 Unsplash 那句 Hi xx 👋 的写法。 */
  protected readonly greet = computed(() => {
    const email = this.authApi.user()?.email ?? '';
    const name = email.includes('@') ? email.split('@')[0] : email;
    return name ? `Hi ${name} 👋` : '你好 👋';
  });

  protected readonly form = signal({ url: '', category: 'blog', reason: '' });
  protected readonly busy = signal(false);
  protected readonly err = signal('');
  protected readonly ticket = signal('');
  protected readonly copied = signal(false);

  protected startSubmit(): void {
    this.agreed.set(true);
    this.agreeOpen.set(false);
  }

  protected readonly query = signal('');
  protected readonly stData = signal<SubmissionStatus | null>(null);
  /** 后台开了「AI 站点分类」：提交页不再让用户选分类（模型来判）。 */
  protected readonly aiCategory = signal(false);
  protected readonly stErr = signal('');
  protected readonly stBusy = signal(false);

  constructor() {
    // 支持把带查询码的链接直接发给用户（?ticket=vx...），刷新后仍能看到进度。
    const t = this.route.snapshot.queryParamMap.get('ticket');
    // 只有「来提交」时才弹须知；带查询码回来查进度的老用户不该再被拦一次。
    if (!t) this.agreeOpen.set(true);
    // 「进入提交页就弹」不只在首次创建时成立：人已经在这一页、再点一次导航时
    // Angular 不会重建组件，构造函数不会再跑，弹窗自然不出现。所以跟着路由走。
    this.router.events.pipe(takeUntilDestroyed()).subscribe((e) => {
      if (!(e instanceof NavigationEnd)) return;
      if (!e.urlAfterRedirects.startsWith('/contribute')) return;
      if (this.route.snapshot.queryParamMap.get('ticket')) return;
      this.agreeOpen.set(true);
    });
    if (t) {
      this.query.set(t);
      void this.loadStatus();
    }
    void this.loadCaptcha();
    void this.loadFeatures();
  }

  private async loadFeatures(): Promise<void> {
    try {
      const r = await this.api.publicFeatures();
      this.aiCategory.set(Boolean(r.features?.['ai_labeling']));
    } catch {
      // 读不到就按「没开 AI」处理：让用户能选分类，总比把选项整块藏掉好。
    }
  }

  protected patchForm(key: 'url' | 'category' | 'reason', value: string): void {
    this.form.update((f) => ({ ...f, [key]: value.trim() }));
  }

  // ---- 人机验证（管理员已登录时不弹） ----
  protected readonly capOpen = signal(false);
  protected readonly capEnabled = signal(false);
  protected readonly capProvider = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly tsSiteKey = signal('');
  protected readonly cap = signal<CaptchaChallenge | null>(null);
  protected readonly capAnswer = signal('');
  protected readonly tsToken = signal('');

  private async loadCaptcha(): Promise<void> {
    if (this.authApi.isAdmin()) return;
    try {
      const r = await this.authApi.captcha();
      this.capEnabled.set(!!r.enabled);
      if (!r.enabled) return;
      if (r.provider && r.provider !== 'math') {
        this.capProvider.set(r.provider);
        this.tsSiteKey.set(r.site_key ?? '');
        this.capEndpoint.set((r as { endpoint?: string }).endpoint ?? '');
      } else {
        this.capProvider.set('math');
        this.cap.set(r.challenge ?? null);
      }
    } catch {
      /* 忽略 */
    }
  }

  private capPassed(): boolean {
    if (this.capProvider() === '' || this.capProvider() === 'math') return this.capAnswer() !== '';
    return this.tsToken() !== '';
  }

  private async openCaptcha(): Promise<void> {
    if (this.capProvider() === '' || this.capProvider() === 'math') {
      try {
        const r = await this.authApi.captcha();
        this.cap.set(r.challenge ?? null);
      } catch {
        /* 忽略 */
      }
      this.capAnswer.set('');
    } else {
      this.tsToken.set('');
    }
    this.capOpen.set(true);
  }

  protected onCaptchaSolved(r: { token?: string; answer?: string }): void {
    if (r.token) this.tsToken.set(r.token);
    if (r.answer) this.capAnswer.set(r.answer);
    this.capOpen.set(false);
    void this.doSubmit();
  }

  protected async submit(): Promise<void> {
    this.err.set('');
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha();
      return;
    }
    await this.doSubmit();
  }

  private async doSubmit(): Promise<void> {
    this.busy.set(true);
    try {
      const r = await this.api.submitSite({
        url: this.form().url,
        category: this.aiCategory() ? 'other' : this.form().category,
        reason: this.form().reason || undefined,
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstile_token: this.tsToken(),
      });
      this.ticket.set(r.ticket);
      this.query.set(r.ticket);
      this.stData.set(null);
      this.stErr.set('');
      this.form.set({ url: '', category: 'blog', reason: '' });
      await this.loadStatus();
    } catch (e) {
      this.err.set(errMsg(e));
      // token 已消费 / 题已过期：清掉，下次提交重新弹窗
      this.tsToken.set('');
      this.capAnswer.set('');
      void this.loadCaptcha();
    } finally {
      this.busy.set(false);
    }
  }

  protected async loadStatus(): Promise<void> {
    const code = this.query().trim();
    if (!code) return;
    this.stBusy.set(true);
    this.stErr.set('');
    try {
      this.stData.set(await this.api.getSubmission(code));
    } catch (e) {
      this.stData.set(null);
      this.stErr.set(errMsg(e));
    } finally {
      this.stBusy.set(false);
    }
  }

  protected queryStatus(event: Event): void {
    event.preventDefault();
    void this.loadStatus();
  }

  protected stepClass(status: string, i: number): string {
    const rejected = status === 'rejected' && i === 1;
    if (rejected) return 'is-blocked';
    return i <= stepIndex(status) && status !== 'rejected' ? 'is-reached' : '';
  }

  protected async copyTicket(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.ticket());
      this.copied.set(true);
      setTimeout(() => this.copied.set(false), 2000);
    } catch {
      this.copied.set(false);
    }
  }

  protected startOver(): void {
    this.ticket.set('');
    this.stData.set(null);
    this.stErr.set('');
    // 从「查进度」回到「再交一个」等于重新进一次提交页，须知要再看一遍：
    // 直接把表单放出来会让这道同意闸形同虚设。
    this.agreed.set(false);
    this.agreeOpen.set(true);
  }
}
