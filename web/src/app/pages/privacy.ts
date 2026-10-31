import { Component, inject, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';

/**
 * 隐私政策。
 *
 * 正文按代码里真实发生的行为写，不写场面话：收集了什么、怎么用、不收集什么、
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
        <div class="section-title">1. 我们收集什么</div>
        <ul class="privacy-list">
          <li><b>账号</b>：邮箱与密码哈希，不保存明文密码。通行密钥只保存公钥。</li>
          <li>
            <b>登录设备</b>：每次登录记录 IP、浏览器与系统、时间，用于让你查看「谁在用我的账号」。
            设置页可查看并管理，管理员可在后台看到更完整的画像。
          </li>
          <li>
            <b>你提交的内容</b>：站点提交、用户反馈（页面地址 + 你的判断 + 你认为
            正确的分类 + 可选说明），并会记下该站点当时被判成的分类。
            反馈用匿名标识记录，不与账号身份绑定。
          </li>
          <li><b>安全记录</b>：密码是否出现在已知泄露库中（只比对哈希前缀）、IP 封禁规则。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">2. 我们如何使用这些信息</div>
        <ul class="privacy-list">
          <li>
            我们仅临时使用这些信息为您提供内容，并出于安全考虑，确保您不是恶意机器人。
          </li>
          <li>登录设备记录只用于让你核对账号活动，不用于广告或画像推荐。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">3. 我们不做什么</div>
        <ul class="privacy-list">
          <li><b>不记录搜索关键词</b>：查询不写库，也不会与你的账号关联。</li>
          <li><b>本站自身没有广告、没有第三方统计脚本</b>，不出售或共享任何数据。
            管理员若自行加入统计代码，见第 4 节最后一条。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">4. 第三方服务</div>
        <p class="small" style="margin: 0 0 10px">
          以下各项由管理员决定是否启用。未启用时，你的数据不会发送给任何第三方。
          例外：人机验证在<b>提交站点</b>时始终生效；管理员未配置第三方验证时，站点使用
          本地算术题，不向任何第三方发送数据。结果页的<b>站点图标代取</b>也始终生效（见下）。
        </p>
        <ul class="privacy-list">
          <li>
            <b>站点图标代取</b>（Google 图标接口，始终生效）：结果页底部显示的网站图标
            由<b>本站服务器</b>向 Google 的图标服务（google.com/s2/favicons）代为获取，
            发送的是收录站点的<b>域名</b>，不是你的 IP、搜索词或浏览记录；
            你的浏览器只与本站通信，不直连 Google。图标只在本站内存中缓存几天，不落盘。
          </li>
          <li>
            <b>域名 WHOIS 查询</b>（默认 xxapi.cn，管理员可自定义接口，始终生效）：
            为给排序提供「域名历史」信任度信号，收录站点会由本站服务器向 WHOIS 查询接口
            查询域名的注册时间，发送的只有<b>域名本身</b>。查询结果存在本站数据库，
            90 天才复查一次；你的搜索词与 IP 不参与。
          </li>
          <li>
            <b>人机验证</b>（如 Cloudflare Turnstile）：注册、登录与提交站点时使用。
            配置了第三方验证时，验证过程会向该服务提交你的验证响应与 IP。
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
            <b>地图</b>（谷歌地图 / 必应地图 / Apple 地图）：默认不加载任何地图。
            你在结果页点「地图」并选了服务商后，该服务商的嵌入页会直接在你的浏览器里加载，
            它能看到你的 IP 和当前的搜索词，并可能设置它自己的 cookie。
          </li>
          <li>
            <b>站点分类</b>（管理员配置的大模型接口，默认关闭）：管理员在后台对收录站点
            触发 AI 分类时，会把该站点的域名与几条文章标题发给模型接口。
            <b>你的搜索记录不参与</b>。
          </li>
          <li>
            <b>原文摘要</b>（部署方配置的摘要模型，默认关闭）：开启后，你在结果页主动点
            「生成原文摘要」时，若部署方配置了摘要模型，会把这一页的正文片段与查询词发给模型接口；
            未配置时使用抽取式（不发送数据）。<b>不会发送整站内容或其他搜索结果</b>。
          </li>
          <li>
            <b>AI 概览</b>（管理员配置的大模型接口，默认关闭）：开启后，当你输入一个问题时，
            会把查询词与本次搜索前几条结果的标题、摘要/摘录发给模型接口，用于生成顶部概览。
            普通关键词不会触发；你可以随时在设置里关闭。
          </li>
          <li>
            <b>云备份</b>（管理员配置的对象存储，如 Backblaze B2 / Cloudflare R2，默认关闭）：
            开启后，抓取到的页面原文与文档元数据（网址、标题、摘要、标签、字数、抓取时间）
            会按包上传到该存储桶。开启「整库备份」时还会上传站点数据库快照——
            其中包含账号邮箱、密码哈希（不可逆）、站点名单与站点配置，
            <b>但不包含登录会话与验证码</b>。你的搜索记录始终不写入数据库。
          </li>
          <li>
            <b>站点自行加入的代码</b>（管理员在「站点信息 → 页头与页脚代码」里填写）：
            这段代码由管理员自己维护，可能包含访问统计、客服挂件等第三方脚本，
            它们能看到什么由该脚本决定，不属于本站内置能力，因此不在这份清单内。
            <b>后台页面与登录页不会加载这段代码。</b>
          </li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">5. Cookie 与本地存储</div>
        <ul class="privacy-list">
          <li>
            只用一枚<b>会话 cookie</b>（HttpOnly、SameSite=Lax，有效期 30 天），用于保持登录。退出登录即清除。
          </li>
          <li>没有分析、广告或跨站追踪 cookie。</li>
          <li>主题与显示偏好存在浏览器 <b>localStorage</b>，不上传。</li>
          <li>
            浏览器支持时，页面会把<b>本站自身的静态文件</b>（脚本、样式、图标、离线页）缓存在本机，
            断网时用它们打开页面。这份缓存<b>不含搜索词、搜索结果与页面快照</b>，
            也不会上传任何内容；清空浏览器里本站的数据即可删除。
          </li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">6. 保留与删除</div>
        <ul class="privacy-list">
          <li>登录设备记录长期保存，但列表只显示最近一个月。</li>
          <li>会话到期自动清理；IP 封禁按管理员设置的期限失效。</li>
          <li>退出登录可清除本机会话。目前不提供自助注销，需要删除账号数据请联系我们。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">7. 未成年人保护</div>
        <ul class="privacy-list">
          <li>本服务是一款网络工具，并非面向 13 岁以下儿童。</li>
          <li>我们不会在知情的情况下收集儿童的个人信息。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">8. 你的选择</div>
        <ul class="privacy-list">
          <li>退出登录可清除本机会话。</li>
          <li>设置页可查看最近的登录设备，发现异常可立即修改密码。</li>
          <li>第三方相关的功能（维基面板、AI 概览、原文摘要）默认关闭，由站点管理员决定是否开启。</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">9. 关于变更</div>
        <ul class="privacy-list">
          <li>
            我们可通过在本页面发布带有新日期的修订版本来更新本隐私政策。修订自发布之时起适用。
          </li>
        </ul>
      </section>

      <section class="card">
        <div class="section-title">10. 联系方式</div>
        <ul class="privacy-list">
          @if (site()?.admin_email) {
            <li>邮箱：<a [href]="'mailto:' + site()!.admin_email">{{ site()!.admin_email }}</a></li>
          } @else {
            <li>管理员尚未填写联系邮箱。</li>
          }
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
  protected readonly site = signal<{ name: string; admin_email: string } | null>(null);

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      this.meta.set(await this.api.privacy());
    } catch {
      /* 读不到就不显示日期 */
    }
    try {
      this.site.set(await this.api.site());
    } catch {
      /* 读不到就不显示邮箱 */
    }
  }

  protected date(ts: number | undefined): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
}
