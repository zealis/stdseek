import { Component, inject, input, output, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { labelText } from '../core/api';
import { Modal } from '../shared/ui/modal';

/**
 * 标签误判反馈弹窗。规划书红线：概率性标签必须可解释 + 可反馈，
 * 所以把判定理由一起摊开，用户看着理由判断，而不是只看结果猜。
 */
@Component({
  selector: 'vx-label-feedback',
  imports: [Modal],
  template: `
    <vx-modal
      title="标签反馈"
      desc="标签由规则自动判定，会出错。勾选你认为不成立的标签。"
      [width]="460"
      (closed)="reset(); closed.emit()"
    >
      <div class="stack" style="gap: 12px">
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
          } @else if (wrong().length > 0) {
            提交 {{ wrong().length }} 个误判
          } @else {
            确认标签无误
          }
        </button>
      </div>
    </vx-modal>
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

    .foot-row {
      display: flex;
      gap: 8px;
    }
  `,
})
export class LabelFeedbackModal {
  private readonly api = inject(PublicApi);

  readonly url = input.required<string>();
  readonly labels = input.required<string[]>();
  readonly reasons = input<Record<string, string> | null>(null);

  readonly closed = output<void>();

  protected readonly labelText = labelText;
  protected readonly wrong = signal<string[]>([]);
  protected readonly note = signal('');
  protected readonly busy = signal(false);
  protected readonly msg = signal('');
  protected readonly err = signal('');

  protected toggleWrong(label: string, event: Event): void {
    const on = (event.target as HTMLInputElement).checked;
    this.wrong.update((list) =>
      on ? (list.includes(label) ? list : [...list, label]) : list.filter((x) => x !== label),
    );
  }

  protected reset(): void {
    this.wrong.set([]);
    this.note.set('');
    this.msg.set('');
    this.err.set('');
  }

  protected async send(): Promise<void> {
    this.busy.set(true);
    this.err.set('');
    this.msg.set('');
    try {
      // 没勾任何标签 = 全部认可，记一条 confirmed 用于看规则精确率；
      // 勾了就只对勾选的那些报误判，没勾的不表态——不该替用户说「没问题」。
      const targets = this.wrong().length > 0 ? this.wrong() : this.labels();
      const verdict = this.wrong().length > 0 ? ('false_positive' as const) : ('confirmed' as const);
      for (const label of targets) {
        await this.api.submitLabelFeedback({
          url: this.url(),
          label,
          verdict,
          note: this.note().trim() || undefined,
        });
      }
      this.msg.set(
        this.wrong().length > 0
          ? `已记录 ${this.wrong().length} 个误判，规则调整后会重新计算标签。`
          : '已记录为「标签判定正确」。',
      );
      this.wrong.set([]);
      this.note.set('');
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }
}
