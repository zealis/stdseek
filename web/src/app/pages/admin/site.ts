import { Component, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';

/**
 * 站点信息：网站名称、描述、关键词、管理员邮箱。
 *
 * 管理员邮箱会展示在隐私政策与服务条款的联系方式里，所以填的是你愿意公开的邮箱，
 * 不是登录账号。
 */
@Component({
  selector: 'vx-site-page',
  imports: [],
  template: `
    @if (msg()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <div class="card">
      <div class="section-title">站点信息</div>

      <label class="field">
        <span>网站名称</span>
        <input class="input" placeholder="voidxo 维空" [value]="name()" (input)="name.set($any($event.target).value)" />
      </label>

      <label class="field">
        <span>描述</span>
        <textarea
          class="input"
          rows="2"
          placeholder="一句话说明这个站点是什么"
          [value]="desc()"
          (input)="desc.set($any($event.target).value)"
        ></textarea>
      </label>

      <label class="field">
        <span>关键词</span>
        <input class="input" placeholder="用逗号分隔" [value]="kw()" (input)="kw.set($any($event.target).value)" />
      </label>

      <label class="field">
        <span>管理员邮箱</span>
        <input class="input" placeholder="admin@example.com" [value]="email()" (input)="email.set($any($event.target).value)" />
      </label>

      <div class="row" style="gap: 8px; justify-content: flex-end; margin-top: 4px">
        <button type="button" class="btn btn-solid" [disabled]="busy()" (click)="save()">
          @if (busy()) { <span class="spinner"></span> } @else { 保存 }
        </button>
      </div>
    </div>
  `,
})
export class SitePage {
  private readonly api = inject(AdminApi);

  protected readonly name = signal('');
  protected readonly desc = signal('');
  protected readonly kw = signal('');
  protected readonly email = signal('');
  protected readonly busy = signal(false);
  protected readonly msg = signal('');

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      const s = await this.api.site();
      this.name.set(s['site.name'] ?? '');
      this.desc.set(s['site.description'] ?? '');
      this.kw.set(s['site.keywords'] ?? '');
      this.email.set(s['site.admin_email'] ?? '');
    } catch (e) {
      this.msg.set(errMsg(e));
    }
  }

  protected async save(): Promise<void> {
    this.busy.set(true);
    this.msg.set('');
    try {
      await this.api.saveSite({
        'site.name': this.name().trim(),
        'site.description': this.desc().trim(),
        'site.keywords': this.kw().trim(),
        'site.admin_email': this.email().trim(),
      });
    } catch (e) {
      this.msg.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }
}
