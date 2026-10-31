import { Component, computed, inject, input, output, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { CATEGORIES, KNOWN_LABELS, categoryText, labelText } from '../core/api';
import { Modal } from '../shared/ui/modal';
import { CaptchaDialog } from '../shared/ui/captcha-dialog';
import { AuthService, type CaptchaChallenge } from '../core/auth.service';

/**
 * 标签误判反馈弹窗。规划书红线：概率性标签必须可解释 + 可反馈，
 * 所以把判定理由一起摊开，用户看着理由判断，而不是只看结果猜。
 */
@Component({
  selector: 'vx-label-feedback',
  imports: [Modal, CaptchaDialog],
  template: `
    <vx-modal
      title="用户反馈"
      [width]="480"
      (closed)="reset(); closed.emit()"
    >
      <div class="stack" style="gap: 12px">
        @if (labels().length === 0) {
          <span class="faint small">这个页面没有标签</span>
        } @else {
          @for (l of labels(); track l) {
            <label class="fb-row">
              <input
                type="checkbox"
                [checked]="wrong().includes(l)"
                (change)="toggleWrong(l, $event)"
              />
              <span>
                <span class="small">{{ labelText(l) }} 不成立</span>
                <span class="faint small" style="display: block">
                  判定理由 {{ reasons()?.[l] || '按站点特征归类' }}
                </span>
              </span>
            </label>
          }
        }

        @if (category()) {
          <div class="stack" style="gap: 8px">
            <label class="fb-row" style="align-items: center">
              <input
                type="checkbox"
                [checked]="catWrong()"
                (change)="catWrong.set($any($event.target).checked)"
              />
              <span class="small">站点分类「{{ categoryText(category()) }}」不对</span>
            </label>
            @if (catWrong()) {
              <div class="row" style="gap: 6px; padding-left: 24px">
                <span class="faint small">应该是</span>
                <select
                  class="input select"
                  style="width: 160px; height: 30px; font-size: var(--fs-xs)"
                  aria-label="认为正确的分类"
                  [value]="catSuggest()"
                  (change)="catSuggest.set($any($event.target).value)"
                >
                  <option value="" [selected]="catSuggest() === ''">不确定</option>
                  @for (c of categories; track c.value) {
                    <option [value]="c.value" [selected]="c.value === catSuggest()">{{ c.label }}</option>
                  }
                </select>
              </div>
            }
          </div>
        }

        <!-- 漏报报告入口：反色药丸（分类反馈下方）。点开独立弹窗选标签提交，
             不与主表单合并——漏报是独立的一次请求，自带人机验证流程。 -->
        @if (suggestable().length > 0) {
          <div class="row" style="gap: 10px; align-items: center">
            <button type="button" class="fb-report" (click)="openReport()">报告</button>
            <span class="faint small">这个页面应该有的标签没有被打出来？点此报告</span>
          </div>
        }

        <textarea
          class="input textarea"
          rows="2"
          maxlength="300"
          placeholder="补充说明，例如：这是原创文章，没有用 AI"
          [value]="note()"
          (input)="note.set(noteEl.value)"
          #noteEl
        ></textarea>
        @if (err()) {
          <div class="notice notice-error">{{ err() }}</div>
        }
        @if (msg()) {
          <div class="notice notice-success">{{ msg() }}</div>
        }
      </div>

      <div footer class="foot-row">
        <button type="button" class="btn" (click)="reset(); closed.emit()">关闭</button>
        <button type="button" class="btn btn-solid" [disabled]="busy()" (click)="send()">
          @if (busy()) {
            提交中
          } @else {
            {{ submitText() }}
          }
        </button>
      </div>
    </vx-modal>

    @if (reportOpen()) {
      <vx-modal title="报告此页面应有的标签" [width]="420" (closed)="reportOpen.set(false)">
        <span class="faint small">
          勾选这个页面应该有、但没有被打出的标签（可多选）。
          报告会用于放宽判定规则，不会被直接写进结果。
        </span>
        <div class="row wrap" style="gap: 6px; margin-top: 12px">
          @for (l of suggestable(); track l) {
            <button
              type="button"
              class="fb-suggest"
              [class.is-on]="missing().includes(l)"
              [attr.aria-pressed]="missing().includes(l)"
              (click)="toggleMissing(l)"
            >
              {{ labelText(l) }}
            </button>
          }
        </div>
        @if (reportErr()) {
          <div class="notice notice-error" style="margin-top: 10px">{{ reportErr() }}</div>
        }
        @if (reportMsg()) {
          <div class="notice notice-success" style="margin-top: 10px">{{ reportMsg() }}</div>
        }
        <div footer class="foot-row">
          <button type="button" class="btn" (click)="reportOpen.set(false)">关闭</button>
          <button
            type="button"
            class="btn btn-solid"
            [disabled]="reportBusy() || missing().length === 0"
            (click)="sendReport()"
          >
            @if (reportBusy()) {
              提交中
            } @else {
              提交报告
            }
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
        action="feedback"
        (solved)="onCaptchaSolved($event)"
        (closed)="capOpen.set(false)"
      />
    }
  `,
  styles: `
    .fb-row {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      cursor: pointer;
    }

    .fb-row input {
      margin-top: 4px;
      accent-color: var(--brand-solid);
      width: 16px;
      height: 16px;
    }

    /* 漏报 chips：默认中性，选中转品牌底——与「误判」勾选框在视觉上区分两种表态 */
    .fb-suggest {
      border: 1px solid var(--line);
      border-radius: var(--r-full);
      background: none;
      color: var(--text-2);
      font-size: var(--fs-xs);
      padding: 3px 10px;
      cursor: pointer;
      transition: background-color var(--dur-fast) var(--ease-out),
        border-color var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
    }

    .fb-suggest.is-on {
      border-color: var(--brand-line);
      background: var(--brand-subtle);
      color: var(--brand);
    }

    /* 漏报报告钮：反色药丸（白底深字），与弹窗里的 ghost 按钮拉开主次 */
    .fb-report {
      flex: none;
      border: 0;
      border-radius: var(--r-full);
      background: var(--text-1);
      color: var(--bg);
      font-size: var(--fs-sm);
      font-weight: 500;
      padding: 7px 24px;
      cursor: pointer;
      transition: opacity var(--dur-fast) var(--ease-out);
    }

    .fb-report:hover {
      opacity: 0.88;
    }

    .foot-row {
      display: flex;
      gap: 8px;
    }
  `,
})
export class LabelFeedbackModal {
  private readonly api = inject(PublicApi);
  private readonly authApi = inject(AuthService);

  readonly url = input.required<string>();

  constructor() {
    void this.loadCaptcha();
  }
  readonly labels = input.required<string[]>();
  readonly reasons = input<Record<string, string> | null>(null);
  /** 站点分类（白名单里那份）。为空时不显示分类那一段。 */
  readonly category = input('');

  readonly closed = output<void>();

  protected readonly labelText = labelText;
  protected readonly categoryText = categoryText;
  protected readonly categories = CATEGORIES;
  protected readonly wrong = signal<string[]>([]);
  /** 漏报：用户认为该页面应该有的标签（页面上没有的那几个）。 */
  protected readonly missing = signal<string[]>([]);
  protected readonly catWrong = signal(false);
  protected readonly catSuggest = signal('');
  protected readonly note = signal('');
  protected readonly busy = signal(false);
  protected readonly msg = signal('');
  protected readonly err = signal('');
  /** 漏报报告：独立弹窗、独立请求（自带人机验证），不与主表单合并。 */
  protected readonly reportOpen = signal(false);
  protected readonly reportBusy = signal(false);
  protected readonly reportErr = signal('');
  protected readonly reportMsg = signal('');
  /** 验证码解完后要提交哪一份：主表单（误报/分类）还是漏报报告。 */
  private readonly pending = signal<'main' | 'report'>('main');

  /** 可补报的标签 = 全部词表 − 页面已有的。已有的再报「应该是」自相矛盾。 */
  protected readonly suggestable = computed(() =>
    KNOWN_LABELS.filter((l) => !this.labels().includes(l)),
  );

  /** 按钮要说得清这次会报什么：只写「提交」用户不知道自己勾了几个。 */
  protected submitText(): string {
    const parts: string[] = [];
    if (this.wrong().length > 0) parts.push(`${this.wrong().length} 个误报`);
    if (this.catWrong()) parts.push('分类');
    return parts.length > 0 ? `提交 ${parts.join(' + ')}` : '确认无误';
  }

  protected toggleWrong(label: string, event: Event): void {
    const on = (event.target as HTMLInputElement).checked;
    this.wrong.update((list) =>
      on ? (list.includes(label) ? list : [...list, label]) : list.filter((x) => x !== label),
    );
  }

  protected toggleMissing(label: string): void {
    this.missing.update((list) =>
      list.includes(label) ? list.filter((x) => x !== label) : [...list, label],
    );
  }

  protected reset(): void {
    this.wrong.set([]);
    this.missing.set([]);
    this.catWrong.set(false);
    this.catSuggest.set('');
    this.note.set('');
    this.msg.set('');
    this.err.set('');
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
    if (this.pending() === 'report') {
      void this.doReportSubmit();
    } else {
      void this.doSubmit();
    }
  }

  protected async send(): Promise<void> {
    this.err.set('');
    this.msg.set('');
    this.pending.set('main');
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha();
      return;
    }
    await this.doSubmit();
  }

  // ---- 漏报报告：独立弹窗流程（自带人机验证，不与主表单合并提交） ----
  protected openReport(): void {
    this.reportErr.set('');
    this.reportMsg.set('');
    this.reportOpen.set(true);
  }

  protected async sendReport(): Promise<void> {
    this.reportErr.set('');
    this.reportMsg.set('');
    this.pending.set('report');
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha();
      return;
    }
    await this.doReportSubmit();
  }

  private async doReportSubmit(): Promise<void> {
    this.reportBusy.set(true);
    this.reportErr.set('');
    try {
      await this.api.submitLabelFeedback({
        url: this.url(),
        labels: [],
        verdict: 'confirmed',
        should: this.missing(),
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstile_token: this.tsToken(),
      });
      this.reportMsg.set('报告已提交，规则调整时会一并考虑。');
      this.missing.set([]);
      // 验证码凭证一次性：报告提交成功即作废，主表单再提交要重新验证。
      this.tsToken.set('');
      this.capAnswer.set('');
      void this.loadCaptcha();
    } catch (e) {
      this.reportErr.set(errMsg(e));
      this.tsToken.set('');
      this.capAnswer.set('');
      void this.loadCaptcha();
    } finally {
      this.reportBusy.set(false);
    }
  }

  private async doSubmit(): Promise<void> {
    this.busy.set(true);
    this.err.set('');
    this.msg.set('');
    try {
      // 没勾任何误报 = 对现有标签全部认可，记一条 confirmed 用于看规则准确率；
      // 勾了就只对勾选的那些报误判，没勾的不表态——不该替用户说「没问题」。
      const targets = this.wrong().length > 0 ? this.wrong() : this.labels();
      const verdict = this.wrong().length > 0 ? ('false_positive' as const) : ('confirmed' as const);
      // 分类只在用户明确表态时才写：只勾了标签不代表认可分类，反之亦然。
      const category = this.catWrong()
        ? ({ verdict: 'false_positive', suggest: this.catSuggest() || undefined } as const)
        : this.category()
          ? ({ verdict: 'confirmed' } as const)
          : undefined;
      await this.api.submitLabelFeedback({
        url: this.url(),
        labels: targets,
        verdict,
        category,
        note: this.note().trim() || undefined,
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstile_token: this.tsToken(),
      });
      this.msg.set(
        this.wrong().length > 0 || this.catWrong()
          ? '已记录反馈，规则调整后会重新计算。'
          : '已记录为「判定正确」。',
      );
      this.wrong.set([]);
      this.catWrong.set(false);
      this.catSuggest.set('');
      this.note.set('');
    } catch (e) {
      this.err.set(errMsg(e));
      this.tsToken.set('');
      this.capAnswer.set('');
      void this.loadCaptcha();
    } finally {
      this.busy.set(false);
    }
  }
}
