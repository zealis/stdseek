import { Component, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { BrandingService } from '../../core/branding.service';

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
    @if (err()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ err() }}</div>
    }
    @if (ok()) {
      <div class="notice notice-success" style="margin-bottom: 12px">{{ ok() }}</div>
    }

    <div class="card">
      <div class="section-title">站点信息</div>

      <label class="field" style="margin-bottom: 16px">
        <span class="field-label">网站名称</span>
        <input class="input" placeholder="voidxo 维空" [value]="name()" (input)="name.set($any($event.target).value)" />
      </label>

      <label class="field" style="margin-bottom: 16px">
        <span class="field-label">站点地址</span>
        <input class="input mono" placeholder="https://voidxo.com（留空按请求推断）"
          [value]="baseUrl()" (input)="baseUrl.set($any($event.target).value)" />
      </label>

      <label class="field" style="margin-bottom: 16px">
        <span class="field-label">描述</span>
        <textarea
          class="input textarea"
          rows="2"
          placeholder="一句话说明这个站点是什么（留空自动生成）"
          [value]="desc()"
          (input)="desc.set($any($event.target).value)"
        ></textarea>
      </label>

      <label class="field" style="margin-bottom: 16px">
        <span class="field-label">关键词</span>
        <input class="input" placeholder="留空自动生成，或用逗号分隔" [value]="kw()" (input)="kw.set($any($event.target).value)" />
      </label>

      <label class="field" style="margin-bottom: 0">
        <span class="field-label">管理员邮箱</span>
        <input class="input" placeholder="admin@example.com" [value]="email()" (input)="email.set($any($event.target).value)" />
      </label>

      <div class="row" style="gap: 8px; justify-content: flex-end; margin-top: 4px">
        <button type="button" class="btn btn-solid" [disabled]="busy()" (click)="save()">
          @if (busy()) { <span class="spinner"></span> } @else { 保存 }
        </button>
      </div>
    </div>

    <!-- 单独一张卡、单独一次保存：这段代码只会被它自己的保存按钮覆盖 -->
    <div class="card" style="margin-top: 16px">
      <div class="section-title">页头与页脚代码</div>

      <label class="field" style="margin-bottom: 14px">
        <span class="field-label">页头（插入 &lt;/head&gt; 前）</span>
        <textarea
          class="input textarea mono"
          rows="6"
          spellcheck="false"
          placeholder="统计脚本、搜索平台验证 meta、额外的样式"
          [value]="headCode()"
          (input)="headCode.set($any($event.target).value)"
        ></textarea>
      </label>

      <label class="field" style="margin-bottom: 10px">
        <span class="field-label">页脚（插入 &lt;/body&gt; 前）</span>
        <textarea
          class="input textarea mono"
          rows="6"
          spellcheck="false"
          placeholder="延迟加载的脚本、客服挂件"
          [value]="footCode()"
          (input)="footCode.set($any($event.target).value)"
        ></textarea>
      </label>

      <span class="faint small">后台与登录页不注入；单段上限 32 KB。</span>

      <div class="row wrap" style="gap: 8px; justify-content: flex-end; align-items: center; margin-top: 12px">
        @if (codeMsg()) { <span class="small" style="color: var(--success)">{{ codeMsg() }}</span> }
        @if (codeErr()) { <span class="small" style="color: var(--danger)">{{ codeErr() }}</span> }
        <button type="button" class="btn btn-solid" [disabled]="codeBusy()" (click)="saveCode()">
          @if (codeBusy()) { <span class="spinner"></span> } @else { 保存 }
        </button>
      </div>
    </div>
  `,
})
export class SitePage {
  private readonly api = inject(AdminApi);
  private readonly branding = inject(BrandingService);

  protected readonly name = signal('');
  protected readonly baseUrl = signal('');
  protected readonly desc = signal('');
  protected readonly kw = signal('');
  protected readonly email = signal('');
  protected readonly headCode = signal('');
  protected readonly footCode = signal('');
  protected readonly busy = signal(false);
  protected readonly err = signal('');
  protected readonly ok = signal('');
  protected readonly codeBusy = signal(false);
  protected readonly codeMsg = signal('');
  protected readonly codeErr = signal('');

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      const s = await this.api.site();
      this.name.set(s['site.name'] ?? '');
      this.baseUrl.set(s['site.base_url'] ?? '');
      this.desc.set(s['site.description'] ?? '');
      this.kw.set(s['site.keywords'] ?? '');
      this.email.set(s['site.admin_email'] ?? '');
      this.headCode.set(s['site.head_html'] ?? '');
      this.footCode.set(s['site.foot_html'] ?? '');
    } catch (e) {
      this.err.set(errMsg(e));
    }
  }

  protected async save(): Promise<void> {
    this.busy.set(true);
    this.err.set('');
    this.ok.set('');
    try {
      await this.api.saveSite({
        'site.name': this.name().trim(),
        'site.base_url': this.baseUrl().trim(),
        'site.description': this.desc().trim(),
        'site.keywords': this.kw().trim(),
        'site.admin_email': this.email().trim(),
      });
      // 回读一次：服务端会 trim，页面要显示真正落库的值，
      // 否则「点了保存」和「看到的还是刚才那串带空格的字」对不上。
      await this.load();
      // 顶栏与首屏的品牌字来自这份数据，不刷新的话要等下次整页加载才变。
      await this.branding.refresh();
      this.ok.set('已保存：顶栏、浏览器标题与分享卡片都已更新。');
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }

  /** 只提交这两段代码：服务端按「请求里出现的键」写入，不会碰站点信息那几项。 */
  protected async saveCode(): Promise<void> {
    this.codeBusy.set(true);
    this.codeMsg.set('');
    this.codeErr.set('');
    try {
      await this.api.saveSite({
        'site.head_html': this.headCode(),
        'site.foot_html': this.footCode(),
      });
      await this.load();
      this.codeMsg.set('已保存，刷新访客页面即可生效');
    } catch (e) {
      this.codeErr.set(errMsg(e));
    } finally {
      this.codeBusy.set(false);
    }
  }
}
