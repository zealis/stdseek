import { Component, inject, signal } from '@angular/core';
import { PrefsService, type Theme } from '../core/prefs.service';
import { FILTERS, TIME_WINDOWS } from '../core/api';
import { Icon } from '../shared/ui/icon';

/** 搜索偏好 + 外观。偏好保存在本机，结果页默认按此过滤；主题三选一（DESIGN.md §11）。 */
@Component({
  selector: 'vx-settings-page',
  imports: [Icon],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">设置</h1>
      <p class="page-desc">搜索偏好保存在本机，结果页默认按此过滤。</p>

      <div class="card" style="margin-top: 16px; padding: 0">
        @for (f of FILTERS; track f.key) {
          <div class="flag">
            <div class="flag-body">
              <div class="section-title">{{ f.label }}</div>
              <p class="faint small">{{ f.hint }}</p>
            </div>
            <button
              type="button"
              class="switch"
              role="switch"
              [attr.aria-checked]="exclude().includes(f.key)"
              [attr.aria-label]="f.label"
              [class.is-on]="exclude().includes(f.key)"
              (click)="toggleExclude(f.key)"
            ></button>
          </div>
        }
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">默认时间范围</div>
        <div class="row wrap" style="margin-top: 10px">
          @for (t of TIME_WINDOWS; track t.since) {
            <button
              type="button"
              class="chip"
              [class.is-active]="since() === t.since"
              (click)="since.set(t.since)"
            >
              {{ t.label }}
            </button>
          }
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">外观</div>
        <div class="row wrap" style="margin-top: 10px">
          @for (t of THEMES; track t.value) {
            <button
              type="button"
              class="chip"
              [class.is-active]="theme() === t.value"
              (click)="setTheme(t.value)"
            >
              {{ t.label }}
            </button>
          }
        </div>
      </div>

      <div class="row" style="margin-top: 16px">
        <button type="button" class="btn btn-solid" (click)="save()">
          <vx-icon name="check" [size]="16" />
          保存偏好
        </button>
        @if (saved()) {
          <span class="small" style="color: var(--success)" role="status">已保存到本机</span>
        }
      </div>
    </div>
  `,
})
export class SettingsPage {
  private readonly prefsService = inject(PrefsService);

  protected readonly FILTERS = FILTERS;
  protected readonly TIME_WINDOWS = TIME_WINDOWS;

  protected readonly THEMES: { value: Theme; label: string }[] = [
    { value: 'system', label: '跟随系统' },
    { value: 'light', label: '浅色' },
    { value: 'dark', label: '深色' },
  ];

  protected readonly exclude = signal<string[]>([]);
  protected readonly since = signal(0);
  protected readonly theme = signal<Theme>('system');
  protected readonly saved = signal(false);

  constructor() {
    this.prefsService.load();
    const p = this.prefsService.prefs();
    this.exclude.set([...p.exclude]);
    this.since.set(p.since);
    this.theme.set(this.prefsService.theme());
  }

  protected toggleExclude(key: string): void {
    this.exclude.update((list) =>
      list.includes(key) ? list.filter((x) => x !== key) : [...list, key],
    );
    this.saved.set(false);
  }

  protected setTheme(theme: Theme): void {
    this.theme.set(theme);
    this.prefsService.setTheme(theme);
  }

  protected save(): void {
    this.prefsService.save({ exclude: this.exclude(), since: this.since() });
    this.saved.set(true);
  }
}
