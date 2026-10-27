import { Component, inject, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';

/**
 * 隐私政策。
 *
 * 正文按代码里真实发生的行为写，不写场面话：收集了什么、不收集什么、
 * 哪些第三方会在什么条件下收到数据，都可以对着 internal/ 里的实现核对。
 *
 * 生效日与修订日由后端给出（/api/privacy）：
 *   created_at = 最早的管理员账号创建时间（服务从那天起运行并产生数据）
 *   updated_at = settings 里的 privacy.updated_at（每次改政策同步一次）
 */
@Component({
  selector: 'vx-privacy-page',
  imports: [],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="section-title" style="font-size: var(--fs-xl)">隐私政策</h1>
      <p class="small muted" style="margin: 6px 0 20px">
        生效 {{ date(meta()?.created_at) }} · 最后修改 {{ date(meta()?.updated_at) }}
      </p>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">我们收集什么</div>
        <ul class="privacy-list">
          <li><b>账号</b>：邮箱与密码哈希，不保存明文密码。通行密钥只保存公钥。</li>
          <li>
            <b>登录设备</b>：每次登录记录 IP、浏览器与系统、时间，用于让你查看「谁在用我的账号」。
            设置页可查看并管理，管理员可在后台看到更完整的画像。
          </li>
          <li>
            <b>你提交的内容</b>：站点提交、标签反馈（页面地址 + 你的判断 + 可选说明）。
            标签反馈用匿名标识记录，不与账号身份绑定。
          </li>
          <li><b>安全记录</b>：密码是否出现在已知泄露库中（只比对哈希前缀）、IP 封禁规则。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">我们不做什么</div>
        <ul class="privacy-list">
          <li><b>不记录搜索关键词</b>：查询不写库，也不会与你的账号关联。</li>
          <li><b>没有广告、没有第三方统计脚本</b>，不出售或共享任何数据。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">第三方服务</div>
        <p class="small" style="margin: 0 0 10px">
          以下各项由管理员决定是否启用。未启用时，你的数据不会发送给任何第三方。
        </p>
        <ul class="privacy-list">
          <li>
            <b>人机验证</b>（如 Cloudflare Turnstile）：验证时会向该服务提交你的验证响应与 IP。
          </li>
          <li>
            <b>归属地与信誉查询</b>（ip.huzhihui.com）：登录 IP 会发送给它，
            换回地区、运营商、ASN、信誉分与风险标记（数据中心、代理、Tor 等），
            用于判断一个 IP 是否可疑。
          </li>
          <li>
            <b>维基百科知识面板</b>（zh.wikipedia.org，默认关闭）：开启后，
            搜索单个词时会用该词查询维基百科并在结果右栏展示解释。
          </li>
          <li>
            <b>AI 摘要</b>（管理员配置的大模型接口，默认关闭）：开启后，
            搜索结果的文本会发送给该接口以生成摘要。
          </li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">Cookie</div>
        <ul class="privacy-list">
          <li>只用一枚<b>会话 cookie</b>（HttpOnly、SameSite=Lax，有效期 30 天），用于保持登录。退出登录即清除。</li>
          <li>没有分析、广告或跨站追踪 cookie。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">保留与删除</div>
        <ul class="privacy-list">
          <li>登录设备记录长期保存，但列表只显示最近一个月。</li>
          <li>会话到期自动清理；IP 封禁按管理员设置的期限失效。</li>
          <li>退出登录可清除本机会话。目前不提供自助注销，需要删除账号数据请联系管理员。</li>
        </ul>
      </section>
    </div>
  `,
  styles: `
    .privacy-list {
      margin: 8px 0 0;
      padding-left: 18px;
    }

    .privacy-list li {
      font-size: var(--fs-sm);
      line-height: 1.7;
      color: var(--text-2);
      margin-bottom: 6px;
    }

    .privacy-list b {
      color: var(--text-1);
      font-weight: 600;
    }
  `,
})
export class PrivacyPage {
  private readonly api = inject(PublicApi);
  protected readonly meta = signal<{ created_at: number; updated_at: number } | null>(null);

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      this.meta.set(await this.api.privacy());
    } catch {
      /* 读不到就不显示日期 */
    }
  }

  protected date(ts: number | undefined): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
}
