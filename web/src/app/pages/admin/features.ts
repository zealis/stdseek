import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi, type AIConfig } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { bytes, fullTime } from '../../shared/format';
import { type FeatureFlag, type SnapshotStats } from '../../core/api';
import { Modal } from '../../shared/ui/modal';
import { Confirm } from '../../shared/ui/confirm';
import { BrandingService } from '../../core/branding.service';

/**
 * 功能开关：只做「改这个开关」一件事。刻意不做「保存」按钮——开关这类东西，
 * 多一次点击就多一次"以为改了其实没改"，点下去即生效，状态以服务端返回为准。
 */
@Component({
  selector: 'vx-features-page',
  imports: [Modal, Confirm],
  template: `
    @if (err()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ err() }}</div>
    }
    @if (msg()) {
      <div class="notice notice-success" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <div class="card" style="padding: 0">
      @if (loading()) {
        <div class="loading"><span class="spinner spinner-lg"></span></div>
      }
      @if (!loading() && items().length === 0) {
        <div style="padding: 32px; text-align: center" class="faint">没有可配置的开关</div>
      }
      @for (f of items(); track f.key) {
        <div class="flag">
          <div class="flag-body">
            <div class="row wrap">
              <span class="section-title">{{ f.label }}</span>
              @if (f.danger) {
                <span class="chip chip-warning">会产生调用成本</span>
              }
              <span class="chip" [class]="f.enabled ? 'chip-success' : ''">
                {{ f.enabled ? '运行中' : '已关闭' }}
              </span>
            </div>
            <p class="flag-desc">{{ f.desc }}</p>
            <p class="flag-foot">
              {{ f.updated_at ? '最后修改 ' + fullTime(f.updated_at) : '从未修改过，当前是默认值' }}
            </p>
          </div>
          <button
            type="button"
            class="switch"
            role="switch"
            [attr.aria-checked]="f.enabled"
            [attr.aria-label]="f.label"
            [class.is-on]="f.enabled"
            [disabled]="busy() === f.key"
            (click)="toggle(f, !f.enabled)"
          ></button>
        </div>
      }
    </div>

    <!-- 配置类项：一行一个，点「配置」开弹窗。表单常驻会把开关列表淹掉。 -->
    <div class="card" style="margin-top: 12px; padding: 0">
      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">邮件（SMTP）</span>
            <span class="chip" [class.chip-success]="mailEnabled()">
              {{ mailEnabled() ? '已配置' : '未配置' }}
            </span>
          </div>
          <p class="flag-foot">
            {{ mailEnabled() ? mailHost() : '未配置时验证与重置邮件只写进服务端日志' }}
          </p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="mailOpen.set(true)">
          配置
        </button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">人机验证方式</span>
            <span
              class="chip"
              [class.chip-success]="capProvider() !== 'none' && capProvider() !== 'math'"
            >
              {{ capLabel() }}
            </span>
          </div>
          <p class="flag-foot">{{ capFoot() }}</p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="capOpen.set(true)">
          配置
        </button>
      </div>
      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">AI 接口</span>
            <span class="chip" [class.chip-success]="ai()?.ready">
              {{ ai()?.ready ? ai()?.model : '未配置' }}
            </span>
            @if (ai()?.has_key) {
              <span class="chip">已存密钥</span>
            }
            @if (ai()?.backup_ready) {
              <span class="chip">备用 {{ ai()?.backup_model }}</span>
            }
            @if (ai()?.backup2_ready) {
              <span class="chip">二备 {{ ai()?.backup2_model }}</span>
            }
            @if (ai()?.failover) {
              <span class="chip chip-danger">主接口故障中</span>
            }
          </div>
          <p class="flag-foot">{{ aiFoot() }}</p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="openAI()">配置</button>
      </div>

      <!-- WHOIS 与蒸馏都是独立的一行：.flag 是「flex 行 + 下边框」，
           放进另一张卡的 .row 里会被压成一行里的色块（曾经就是这么错的）。 -->
      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">域名 WHOIS 查询</span>
            <span class="chip" [class.chip-success]="!!whoisCustom()">
              {{ whoisCustom() ? '自定义接口' : '默认接口' }}
            </span>
          </div>
          <p class="flag-foot">
            {{ whoisCustom() ? whoisCustom() : '默认 xxapi.cn；注册时间用于排序的域名信任度信号' }}
          </p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="whoisOpen.set(true)">
          配置
        </button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">蒸馏通道</span>
            <span class="chip" [class.chip-success]="distillOn()">
              {{ distillOn() ? '已开启' : '未开启' }}
            </span>
            <span class="chip">{{ distillCustom() ? '自定义阅读器' : '默认阅读器' }}</span>
          </div>
          <p class="flag-foot">
            开关在下面「蒸馏抓取」里，这里只配用哪个阅读器与代理。抓取时只发目标页
            URL，不发搜索词与用户 IP。
          </p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="openDistill()">配置</button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">原文快照</span>
            <span class="chip" [class.chip-success]="snap()?.enabled">
              {{ snap()?.enabled ? '保存中' : '已停止' }}
            </span>
            <span class="chip mono">{{ snapBytes() }}</span>
          </div>
          <p class="flag-foot">{{ snapFoot() }}</p>
        </div>
        <button
          type="button"
          class="btn btn-outline btn-sm"
          [disabled]="!snap()?.available"
          (click)="openSnap()"
        >
          管理
        </button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">站点标识</span>
            <span class="chip" [class.chip-success]="brand()?.has_icon">
              {{ brand()?.has_icon ? '已自定义' : '默认图标' }}
            </span>
          </div>
          <p class="flag-foot">顶栏品牌章与浏览器标签页图标。只换图标，不换站名文字。</p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="openBrand()">配置</button>
      </div>
    </div>

    @if (brandOpen()) {
      <vx-modal title="站点标识" [width]="440" (closed)="closeBrand()">
        <div class="row" style="gap: 12px; align-items: center">
          @if (preview()) {
            <img class="brand-preview" [src]="preview()" alt="图标预览" />
          } @else {
            <span class="brand-preview brand-preview-default"></span>
          }
          <span class="faint small">
            {{
              brand()?.has_icon ? '当前使用自定义图标' : '当前是自带图标（渐变方块 + &lt;/&gt;）'
            }}
          </span>
        </div>

        <div class="field" style="margin-top: 14px">
          <span>图片（PNG / ICO / SVG / WebP，≤ 512 KB，建议正方形）</span>
          <!-- 原生的 <input type="file"> 会带一颗系统按钮，字体和圆角都不受控，
               跟这里的其他控件不是一个东西。改成隐藏原生控件 + 自己的按钮。 -->
          <div class="row wrap" style="gap: 10px; align-items: center">
            <button type="button" class="btn btn-outline btn-sm" (click)="brandPicker.click()">
              选择图片
            </button>
            <span class="faint small">{{ brandFileName() || '未选择文件' }}</span>
            <input
              #brandPicker
              type="file"
              hidden
              accept="image/png,image/x-icon,image/svg+xml,image/webp,image/jpeg"
              (change)="pickBrand($event)"
            />
          </div>
        </div>
        <p class="faint small" style="margin-top: 8px">
          这个文件同时用作浏览器标签页图标；顶栏会立刻更新，标签页可能要刷新一次才认。
        </p>

        <div
          class="row wrap"
          style="margin-top: 14px; gap: 8px; align-items: center; justify-content: flex-end"
        >
          <button
            type="button"
            class="btn btn-solid btn-sm"
            [disabled]="brandBusy() || !brandFileSel()"
            (click)="uploadBrand()"
          >
            @if (brandBusy()) {
              <span class="spinner"></span>
            } @else {
              上传并启用
            }
          </button>
          @if (brand()?.has_icon) {
            <button
              type="button"
              class="btn btn-outline btn-sm"
              [disabled]="brandBusy()"
              (click)="resetBrand()"
            >
              恢复默认
            </button>
          }
          @if (brandMsg()) {
            <span class="small" style="color: var(--success)">{{ brandMsg() }}</span>
          }
          @if (brandErr()) {
            <span class="small" style="color: var(--danger)">{{ brandErr() }}</span>
          }
        </div>
      </vx-modal>
    }

    @if (mailOpen()) {
      <vx-modal title="邮件（SMTP）" [width]="520" (closed)="mailOpen.set(false)">
        <div class="row wrap" style="gap: 10px">
          <label class="field" style="flex: 2; min-width: 220px">
            <span>SMTP 服务器</span>
            <input
              class="input"
              placeholder="smtp.qq.com"
              [value]="mailHost()"
              (input)="mailHost.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="width: 110px">
            <span>端口</span>
            <input
              class="input mono"
              inputmode="numeric"
              [value]="mailPort()"
              (input)="mailPort.set($any($event.target).value)"
            />
          </label>
        </div>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 180px">
            <span>用户名</span>
            <input
              class="input"
              autocomplete="off"
              [value]="mailUser()"
              (input)="mailUser.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="flex: 1; min-width: 180px">
            <span>密码{{ mailHasPass() ? '（留空则不修改）' : '' }}</span>
            <input
              class="input"
              type="password"
              autocomplete="off"
              placeholder="{{ mailHasPass() ? '••••••••' : '' }}"
              [value]="mailPass()"
              (input)="mailPass.set($any($event.target).value)"
            />
          </label>
        </div>

        <label class="field" style="margin-top: 10px">
          <span>发件地址</span>
          <input
            class="input"
            placeholder="留空则用用户名"
            [value]="mailFrom()"
            (input)="mailFrom.set($any($event.target).value)"
          />
        </label>

        <label class="row" style="gap: 6px; align-items: center; margin-top: 10px; cursor: pointer">
          <input
            type="checkbox"
            [checked]="mailSecure()"
            (change)="mailSecure.set($any($event.target).checked)"
          />
          <span class="small">使用隐式 TLS（465 端口）</span>
        </label>

        <div
          class="row wrap"
          style="margin-top: 14px; gap: 8px; align-items: center; justify-content: flex-end"
        >
          <button
            type="button"
            class="btn btn-solid btn-sm"
            [disabled]="mailBusy()"
            (click)="saveMail()"
          >
            @if (mailBusy()) {
              <span class="spinner"></span>
            } @else {
              保存
            }
          </button>
          @if (mailMsg()) {
            <span class="small" style="color: var(--success); margin-right: auto">{{
              mailMsg()
            }}</span>
          }
          @if (mailErr()) {
            <span class="small" style="color: var(--danger); margin-right: auto">{{
              mailErr()
            }}</span>
          }
        </div>

        <div
          class="row wrap"
          style="margin-top: 14px; gap: 8px; align-items: flex-end;
             border-top: 1px solid var(--line); padding-top: 14px"
        >
          <label class="field" style="flex: 1; min-width: 200px">
            <span>测试收件地址</span>
            <input
              class="input"
              type="email"
              placeholder="you@example.com"
              [value]="testTo()"
              (input)="testTo.set($any($event.target).value)"
            />
          </label>
          <button
            type="button"
            class="btn btn-outline"
            [disabled]="testBusy()"
            (click)="sendTestMail()"
          >
            @if (testBusy()) {
              <span class="spinner"></span>
            } @else {
              发送测试邮件
            }
          </button>
        </div>
        @if (testMsg()) {
          <p class="small" style="margin-top: 8px; color: var(--success)">{{ testMsg() }}</p>
        }
        @if (testErr()) {
          <p class="small" style="margin-top: 8px; color: var(--danger)">{{ testErr() }}</p>
        }
      </vx-modal>
    }

    @if (aiOpen()) {
      <vx-modal title="AI 接口" [width]="520" (closed)="aiOpen.set(false)">
        <span class="faint small">OpenAI 兼容接口，站点分类与 AI 概览共用。</span>

        <label class="field" style="margin-top: 14px">
          <span>接口地址</span>
          <input
            class="input mono"
            placeholder="https://api.deepseek.com/v1"
            [value]="aiBase()"
            (input)="aiBase.set($any($event.target).value)"
          />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input
              class="input mono"
              placeholder="deepseek-chat"
              [value]="aiModel()"
              (input)="aiModel.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiHasKey() ? '（留空则不修改）' : '' }}</span>
            <input
              class="input"
              type="password"
              autocomplete="off"
              placeholder="{{ aiHasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiKey()"
              (input)="aiKey.set($any($event.target).value)"
            />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">备用接口</span>
          @if (ai()?.failover) {
            <span class="chip chip-danger">正在使用</span>
          }
        </div>
        <span class="faint small">主接口超时或 5xx 时顶上，不填不启用。</span>

        <label class="field" style="margin-top: 10px">
          <span>接口地址</span>
          <input
            class="input mono"
            placeholder="https://api.moonshot.cn/v1"
            [value]="aiBackupBase()"
            (input)="aiBackupBase.set($any($event.target).value)"
          />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input
              class="input mono"
              placeholder="moonshot-v1-8k"
              [value]="aiBackupModel()"
              (input)="aiBackupModel.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiBackupHasKey() ? '（留空则不修改）' : '' }}</span>
            <input
              class="input"
              type="password"
              autocomplete="off"
              placeholder="{{ aiBackupHasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiBackupKey()"
              (input)="aiBackupKey.set($any($event.target).value)"
            />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">第二备用接口</span>
        </div>
        <span class="faint small">前两个都不可用时才用，不填不启用。</span>

        <label class="field" style="margin-top: 10px">
          <span>接口地址</span>
          <input
            class="input mono"
            placeholder="https://api.example.com/v1"
            [value]="aiBackup2Base()"
            (input)="aiBackup2Base.set($any($event.target).value)"
          />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input
              class="input mono"
              placeholder="model-name"
              [value]="aiBackup2Model()"
              (input)="aiBackup2Model.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiBackup2HasKey() ? '（留空则不修改）' : '' }}</span>
            <input
              class="input"
              type="password"
              autocomplete="off"
              placeholder="{{ aiBackup2HasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiBackup2Key()"
              (input)="aiBackup2Key.set($any($event.target).value)"
            />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">AI 概览优先接口</span>
        </div>
        <select
          class="input select"
          style="width: 100%"
          [value]="aiSummaryEndpoint()"
          (change)="aiSummaryEndpoint.set($any($event.target).value)"
        >
          <option value="primary" [selected]="aiSummaryEndpoint() === 'primary'">主接口</option>
          <option value="backup" [selected]="aiSummaryEndpoint() === 'backup'">备用接口</option>
          <option value="backup2" [selected]="aiSummaryEndpoint() === 'backup2'">
            第二备用接口
          </option>
        </select>
        <span class="faint small">只影响 AI 概览的尝试顺序。</span>

        <label class="field" style="margin-top: 14px; width: 180px">
          <span>概览配额（次 / IP / 天）</span>
          <input
            class="input mono"
            inputmode="numeric"
            maxlength="4"
            [value]="aiQuota()"
            (input)="aiQuota.set($any($event.target).value)"
          />
        </label>
        <span class="faint small">填 0 表示不限制。</span>

        <div
          class="row wrap"
          style="margin-top: 16px; gap: 8px; align-items: center; justify-content: flex-end"
        >
          <button
            type="button"
            class="btn btn-solid btn-sm"
            [disabled]="aiBusy()"
            (click)="saveAI()"
          >
            @if (aiBusy()) {
              <span class="spinner"></span>
            } @else {
              保存
            }
          </button>
          <button
            type="button"
            class="btn btn-outline btn-sm"
            [disabled]="aiTestBusy()"
            (click)="testAI()"
          >
            @if (aiTestBusy()) {
              <span class="spinner"></span>
            } @else {
              测试连通性
            }
          </button>
          @if (aiMsg()) {
            <span class="small" style="color: var(--success); margin-right: auto">{{
              aiMsg()
            }}</span>
          }
          @if (aiErr()) {
            <span class="small" style="color: var(--danger); margin-right: auto">{{
              aiErr()
            }}</span>
          }
        </div>
      </vx-modal>
    }

    @if (whoisOpen()) {
      <vx-modal title="域名 WHOIS 查询接口" [width]="520" (closed)="whoisOpen.set(false)">
        <label class="field">
          <span>自定义接口地址（URL 里用 {{ '{domain}' }} 占位）</span>
          <input
            class="input mono"
            placeholder="https://v2.xxapi.cn/api/whois?domain={{ '{domain}' }}"
            [value]="whoisURL()"
            (input)="whoisURL.set($any($event.target).value)"
          />
        </label>
        <span class="faint small">
          留空使用默认接口（xxapi.cn）。约定：返回 JSON、任意字段带 ISO 格式的注册时间即可。
          自定义接口失效时自动退回默认；查询结果写进白名单，90 天复查一次。
        </span>
        @if (whoisMsg()) {
          <div class="notice notice-success">{{ whoisMsg() }}</div>
        }
        @if (whoisErr()) {
          <div class="notice notice-error">{{ whoisErr() }}</div>
        }
        <div footer class="foot-row">
          <button type="button" class="btn" (click)="whoisOpen.set(false)">关闭</button>
          <button
            type="button"
            class="btn btn-solid"
            [disabled]="whoisBusy()"
            (click)="saveWhois()"
          >
            @if (whoisBusy()) {
              保存中
            } @else {
              保存
            }
          </button>
        </div>
      </vx-modal>
    }

    @if (distillOpen()) {
      <vx-modal title="蒸馏抓取" [width]="560" (closed)="distillOpen.set(false)">
        <span class="faint small">
          直连被拦（403 / 451 / 连不上）的页面，交给阅读器代取：把目标 URL
          拼在阅读器地址后面请求一次， 由它替我们访问并回整页 HTML，然后照常提取入库。
        </span>
        <span class="faint small" style="display: block; margin-top: 8px">
          与「爬搜索结果」的区别：搜索只给一两百字摘要，而且常常不包含要的那一页；阅读器给的是整页正文。
          发出去的只有目标页 URL，不含用户搜索词与用户 IP。404
          不蒸馏：页面真没了，换个通道也是没有。
        </span>
        <span class="faint small" style="display: block; margin-top: 8px">
          阅读器在墙外，所以这里可以单独给它配一个代理——抓取仍然直连，不会被代理掩盖站点本身的可访问性。
        </span>
        <label class="field" style="margin-top: 12px">
          <span>阅读器地址（留空用默认）</span>
          <input
            class="input mono"
            placeholder="{{ distillDefault() }}"
            [value]="distillURL()"
            (input)="distillURL.set($any($event.target).value)"
          />
        </label>
        <p class="faint small" style="margin-top: 8px">
          协议要对得上：<code class="mono">GET 基地址 + 目标URL</code>，返回整页
          HTML（自建反代也照这个来）。
        </p>
        <label class="field">
          <span>代理（可选，只给蒸馏用）</span>
          <input
            class="input mono"
            placeholder="http://127.0.0.1:7890 或 socks5://host:1080"
            [value]="distillProxy()"
            (input)="distillProxy.set($any($event.target).value)"
          />
        </label>
        <p class="faint small" style="margin-top: 8px">
          留空则直连（忽略系统 HTTP_PROXY）。抓取不走这条代理——那是被收录站点自己的可访问性，
          不能用代理掩盖。
        </p>

        <div style="margin-top: 14px; border-top: 1px solid var(--line); padding-top: 12px">
          <span class="small">试抓一页（不写库）</span>
          <div class="row wrap" style="gap: 8px; margin-top: 8px">
            <input
              class="input mono"
              style="flex: 1; min-width: 220px"
              placeholder="https://被拦的站点/文章"
              [value]="distillTestURL()"
              (input)="distillTestURL.set($any($event.target).value)"
            />
            <button
              type="button"
              class="btn btn-outline btn-sm"
              [disabled]="distillTestBusy()"
              (click)="runDistillTest()"
            >
              @if (distillTestBusy()) {
                抓取中
              } @else {
                试抓
              }
            </button>
          </div>
          @if (distillTestResult(); as r) {
            <div class="notice notice-success" style="margin-top: 10px">
              <strong>{{ r.title }}</strong
              >（{{ r.chars }} 字）
              <p class="small" style="margin: 4px 0 0">{{ r.preview }}</p>
            </div>
          }
          @if (distillTestErr()) {
            <div class="notice notice-error" style="margin-top: 10px">{{ distillTestErr() }}</div>
          }
        </div>

        @if (distillMsg()) {
          <div class="notice notice-success">{{ distillMsg() }}</div>
        }
        @if (distillErr()) {
          <div class="notice notice-error">{{ distillErr() }}</div>
        }
        <div footer class="foot-row">
          <button type="button" class="btn" (click)="distillOpen.set(false)">关闭</button>
          <button
            type="button"
            class="btn btn-solid"
            [disabled]="distillBusy()"
            (click)="saveDistill()"
          >
            @if (distillBusy()) {
              保存中
            } @else {
              保存
            }
          </button>
        </div>
      </vx-modal>
    }

    @if (snapOpen()) {
      <vx-modal title="原文快照" [width]="560" (closed)="snapOpen.set(false)">
        <div class="row-between" style="gap: 14px; align-items: flex-start">
          <div class="stack" style="gap: 2px">
            <span class="small">抓取时保存页面原始 HTML，单页上限 2MB</span>
            <span class="faint small">关闭后只停止新增，已有快照仍可查看</span>
          </div>
          <button
            type="button"
            class="switch"
            role="switch"
            aria-label="保存原文快照"
            [attr.aria-checked]="snap()?.enabled"
            [class.is-on]="snap()?.enabled"
            [disabled]="snapBusy() === 'switch'"
            (click)="toggleSnap(!snap()?.enabled)"
          ></button>
        </div>

        <div class="snap-stats">
          <div class="card stat">
            <span class="faint small">磁盘占用</span>
            <strong class="stat-num mono">{{ snapBytes() }}</strong>
            <span class="faint small">{{ snapRatio() }}</span>
          </div>
          <div class="card stat">
            <span class="faint small">快照条数</span>
            <strong class="stat-num mono">{{ snap()?.pages ?? 0 }}</strong>
            <span class="faint small">索引文档 {{ snap()?.documents ?? 0 }} 条</span>
          </div>
          <div class="card stat">
            <span class="faint small">孤儿快照</span>
            <strong class="stat-num mono">{{ snap()?.orphans ?? 0 }}</strong>
            <span class="faint small">索引里已无对应页面</span>
          </div>
        </div>

        <div
          class="row wrap"
          style="margin-top: 16px; gap: 8px; align-items: center; justify-content: flex-end"
        >
          <button
            type="button"
            class="btn btn-outline btn-sm"
            [disabled]="!!snapBusy() || (snap()?.orphans ?? 0) === 0"
            (click)="askCleanup('orphan')"
          >
            清理孤儿
          </button>
          <button
            type="button"
            class="btn btn-danger btn-sm"
            [disabled]="!!snapBusy() || (snap()?.pages ?? 0) === 0"
            (click)="askCleanup('all')"
          >
            清空全部
          </button>
          @if (snapBusy() === 'orphan' || snapBusy() === 'all') {
            <span class="spinner"></span>
          }
          @if (snapMsg()) {
            <span class="small" style="color: var(--success)">{{ snapMsg() }}</span>
          }
          @if (snapErr()) {
            <span class="small" style="color: var(--danger)">{{ snapErr() }}</span>
          }
        </div>
      </vx-modal>
    }

    @if (cleanupMode(); as mode) {
      <vx-confirm
        [title]="mode === 'all' ? '清空全部快照' : '清理孤儿快照'"
        [desc]="cleanupDesc(mode)"
        [confirmText]="mode === 'all' ? '全部删除' : '删除'"
        [danger]="true"
        (confirmed)="runCleanup(mode)"
        (cancelled)="cleanupMode.set('')"
      />
    }

    @if (capOpen()) {
      <vx-modal title="人机验证方式" [width]="480" (closed)="capOpen.set(false)">
        <div class="row wrap" style="gap: 6px">
          @for (p of PROVIDERS; track p.value) {
            <button
              type="button"
              class="chip"
              [class.is-active]="capProvider() === p.value"
              (click)="capProvider.set(p.value)"
            >
              {{ p.label }}
            </button>
          }
        </div>

        @if (needsKeys()) {
          <label class="field" style="margin-top: 14px">
            <span>{{ siteKeyLabel() }}</span>
            <input
              class="input"
              [value]="capSiteKey()"
              (input)="capSiteKey.set($any($event.target).value)"
            />
          </label>
          <label class="field" style="margin-top: 10px">
            <span>{{ secretLabel() }}{{ capHasSecret() ? '（留空则不修改）' : '' }}</span>
            <input
              class="input"
              type="password"
              autocomplete="off"
              placeholder="{{ capHasSecret() ? '••••••••' : '' }}"
              [value]="capSecret()"
              (input)="capSecret.set($any($event.target).value)"
            />
          </label>
        }

        @if (capProvider() === 'cap') {
          <label class="field" style="margin-top: 10px">
            <span>Cap 服务地址</span>
            <input
              class="input"
              [value]="capEndpoint()"
              (input)="capEndpoint.set($any($event.target).value)"
            />
          </label>
        }

        <div
          class="row wrap"
          style="margin-top: 16px; gap: 8px; align-items: center; justify-content: flex-end"
        >
          <button
            type="button"
            class="btn btn-solid btn-sm"
            [disabled]="capBusy()"
            (click)="saveCaptcha()"
          >
            @if (capBusy()) {
              <span class="spinner"></span>
            } @else {
              保存
            }
          </button>
          <button
            type="button"
            class="btn btn-outline btn-sm"
            [disabled]="capTestBusy()"
            (click)="testCaptcha()"
          >
            @if (capTestBusy()) {
              <span class="spinner"></span>
            } @else {
              测试连通性
            }
          </button>
          @if (capMsg()) {
            <span class="small" style="color: var(--success); margin-right: auto">{{
              capMsg()
            }}</span>
          }
          @if (capErr()) {
            <span class="small" style="color: var(--danger); margin-right: auto">{{
              capErr()
            }}</span>
          }
        </div>
      </vx-modal>
    }
  `,
})
export class FeaturesPage {
  // ---- AI 接口 ----
  protected readonly ai = signal<AIConfig | null>(null);
  protected readonly aiOpen = signal(false);
  protected readonly aiBase = signal('');
  protected readonly aiModel = signal('');
  protected readonly aiKey = signal('');
  protected readonly aiHasKey = signal(false);
  protected readonly aiBackupBase = signal('');
  protected readonly aiBackupModel = signal('');
  protected readonly aiBackupKey = signal('');
  protected readonly aiBackupHasKey = signal(false);
  protected readonly aiBackup2Base = signal('');
  protected readonly aiBackup2Model = signal('');
  protected readonly aiBackup2Key = signal('');
  protected readonly aiBackup2HasKey = signal(false);
  protected readonly aiSummaryEndpoint = signal('primary');
  /** 每 IP 每天的概览次数上限（字符串形态，便于输入框直接绑定）。 */
  protected readonly aiQuota = signal('20');
  protected readonly aiBusy = signal(false);
  protected readonly aiMsg = signal('');
  protected readonly aiErr = signal('');
  protected readonly aiTestBusy = signal(false);

  /** 行内说明：说清现在能不能用，而不是解释规则。 */
  protected aiFoot(): string {
    const a = this.ai();
    if (!a) return '读取配置失败';
    if (!a.ready) return '填好接口地址与模型后，AI 分类与总结才能用';
    if (!a.enabled) return '已配置，但「AI 站点分类」开关还没打开';
    if (a.failover) return '主接口调用失败，正在用备用接口，恢复后自动切回';
    return '分类用于白名单站点打标，AI 概览在搜索页按需生成';
  }

  protected openAI(): void {
    const a = this.ai();
    this.aiBase.set(a?.base_url ?? '');
    this.aiModel.set(a?.model ?? '');
    this.aiKey.set('');
    this.aiHasKey.set(Boolean(a?.has_key));
    this.aiBackupBase.set(a?.backup_base_url ?? '');
    this.aiBackupModel.set(a?.backup_model ?? '');
    this.aiBackupKey.set('');
    this.aiBackupHasKey.set(Boolean(a?.backup_has_key));
    this.aiBackup2Base.set(a?.backup2_base_url ?? '');
    this.aiBackup2Model.set(a?.backup2_model ?? '');
    this.aiBackup2Key.set('');
    this.aiBackup2HasKey.set(Boolean(a?.backup2_has_key));
    this.aiSummaryEndpoint.set(a?.summary_endpoint || 'primary');
    this.aiQuota.set(String(a?.overview_daily_quota ?? 20));
    this.aiMsg.set('');
    this.aiOpen.set(true);
  }

  /** 配额输入框的值。不是数字就当 0（不限制）——服务端会再校验范围。 */
  private quotaValue(): number {
    const n = Number(this.aiQuota().trim());
    return Number.isFinite(n) && n >= 0 ? Math.trunc(n) : 0;
  }

  private async loadAI(): Promise<void> {
    try {
      this.ai.set(await this.api.aiConfig());
    } catch (e) {
      // 读不到就静默会变成「界面说没配置、其实配好了」这种最难查的 bug，
      // 所以留一条控制台痕迹，同时让行内说明指向"读取失败"而不是"未配置"。
      console.warn('[features] ai config load failed', e);
      this.aiErr.set(errMsg(e));
    }
  }

  protected async saveAI(): Promise<void> {
    this.aiMsg.set('');
    this.aiErr.set('');
    this.aiBusy.set(true);
    try {
      const r = await this.api.saveAIConfig({
        base_url: this.aiBase().trim(),
        model: this.aiModel().trim(),
        api_key: this.aiKey().trim(),
        backup_base_url: this.aiBackupBase().trim(),
        backup_model: this.aiBackupModel().trim(),
        backup_api_key: this.aiBackupKey().trim(),
        backup2_base_url: this.aiBackup2Base().trim(),
        backup2_model: this.aiBackup2Model().trim(),
        backup2_api_key: this.aiBackup2Key().trim(),
        summary_endpoint: this.aiSummaryEndpoint(),
        overview_daily_quota: this.quotaValue(),
      });
      this.aiKey.set('');
      this.aiBackupKey.set('');
      this.aiBackup2Key.set('');
      await this.loadAI();
      this.aiMsg.set(r.ready ? '已保存并生效' : '已保存，但地址或模型为空，暂不生效');
    } catch (e) {
      this.aiErr.set(errMsg(e));
    } finally {
      this.aiBusy.set(false);
    }
  }

  protected async testAI(): Promise<void> {
    this.aiMsg.set('');
    this.aiErr.set('');
    this.aiTestBusy.set(true);
    try {
      const r = await this.api.testAI();
      this.aiMsg.set(r.message);
    } catch (e) {
      this.aiErr.set(errMsg(e));
    } finally {
      this.aiTestBusy.set(false);
    }
  }

  // ---- 域名 WHOIS ----
  protected readonly whoisOpen = signal(false);
  protected readonly whoisURL = signal('');
  protected readonly whoisCustom = signal('');
  protected readonly whoisMsg = signal('');
  protected readonly whoisErr = signal('');
  protected readonly whoisBusy = signal(false);

  private async loadWhois(): Promise<void> {
    try {
      const r = await this.api.whoisConfig();
      this.whoisURL.set(r.api_url);
      this.whoisCustom.set(r.api_url);
    } catch (e) {
      console.warn('[features] whois config load failed', e);
    }
  }

  protected async saveWhois(): Promise<void> {
    this.whoisMsg.set('');
    this.whoisErr.set('');
    this.whoisBusy.set(true);
    try {
      await this.api.saveWhoisConfig(this.whoisURL().trim());
      const r = await this.api.whoisConfig();
      this.whoisCustom.set(r.api_url);
      this.whoisMsg.set('已保存并生效');
    } catch (e) {
      this.whoisErr.set(errMsg(e));
    } finally {
      this.whoisBusy.set(false);
    }
  }

  // ---- 蒸馏抓取 ----
  protected readonly distillOpen = signal(false);
  protected readonly distillURL = signal('');
  protected readonly distillProxy = signal('');
  protected readonly distillCustom = signal('');
  protected readonly distillDefault = signal('');
  protected readonly distillOn = signal(false);
  protected readonly distillMsg = signal('');
  protected readonly distillErr = signal('');
  protected readonly distillBusy = signal(false);
  protected readonly distillTestURL = signal('');
  protected readonly distillTestResult = signal<{
    title: string;
    preview: string;
    chars: number;
  } | null>(null);
  protected readonly distillTestErr = signal('');
  protected readonly distillTestBusy = signal(false);

  private async loadDistill(): Promise<void> {
    try {
      const r = await this.api.distillConfig();
      this.distillURL.set(r.reader);
      this.distillProxy.set(r.proxy);
      this.distillCustom.set(r.reader);
      this.distillDefault.set(r.default_reader);
      this.distillOn.set(r.enabled);
    } catch (e) {
      console.warn('[features] distill config load failed', e);
    }
  }

  protected openDistill(): void {
    this.distillMsg.set('');
    this.distillErr.set('');
    this.distillTestResult.set(null);
    this.distillTestErr.set('');
    this.distillOpen.set(true);
  }

  protected async saveDistill(): Promise<void> {
    this.distillMsg.set('');
    this.distillErr.set('');
    this.distillBusy.set(true);
    try {
      await this.api.saveDistillConfig({
        reader: this.distillURL().trim(),
        proxy: this.distillProxy().trim(),
      });
      await this.loadDistill();
      this.distillMsg.set('已保存并生效');
    } catch (e) {
      this.distillErr.set(errMsg(e));
    } finally {
      this.distillBusy.set(false);
    }
  }

  protected async runDistillTest(): Promise<void> {
    const u = this.distillTestURL().trim();
    if (!u) return;
    this.distillTestErr.set('');
    this.distillTestResult.set(null);
    this.distillTestBusy.set(true);
    try {
      this.distillTestResult.set(await this.api.testDistill(u));
    } catch (e) {
      this.distillTestErr.set(errMsg(e));
    } finally {
      this.distillTestBusy.set(false);
    }
  }

  // ---- 站点标识 ----
  private readonly branding = inject(BrandingService);
  protected readonly brand = this.branding.current;
  protected readonly brandOpen = signal(false);
  protected readonly brandBusy = signal(false);
  protected readonly brandMsg = signal('');
  protected readonly brandErr = signal('');
  protected readonly brandFileSel = signal<File | null>(null);
  protected readonly brandFileName = signal('');
  /** 选中文件后的本地预览地址；上传成功后清掉，改用服务端 URL。 */
  private readonly brandLocalURL = signal('');
  protected readonly preview = computed(() => this.brandLocalURL() || this.brand()?.icon_url || '');

  protected openBrand(): void {
    this.brandMsg.set('');
    this.brandErr.set('');
    this.brandOpen.set(true);
  }

  protected closeBrand(): void {
    this.dropLocalPreview();
    this.brandFileSel.set(null);
    this.brandFileName.set('');
    this.brandOpen.set(false);
  }

  protected pickBrand(e: Event): void {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0] ?? null;
    this.brandMsg.set('');
    this.brandErr.set('');
    this.dropLocalPreview();
    this.brandFileSel.set(file);
    this.brandFileName.set(file?.name ?? '');
    if (file) this.brandLocalURL.set(URL.createObjectURL(file));
    // 清空 value：同一个文件再选一次也要触发 change，否则「重选刚才那张」没反应
    input.value = '';
  }

  protected async uploadBrand(): Promise<void> {
    const file = this.brandFileSel();
    if (!file) return;
    this.brandBusy.set(true);
    this.brandMsg.set('');
    this.brandErr.set('');
    try {
      const r = await this.api.uploadBrandIcon(file);
      // 同步给顶栏：不然要刷新整页才看到新图标，用户会以为没传上去。
      this.branding.apply(r.branding);
      this.dropLocalPreview();
      this.brandFileSel.set(null);
      this.brandFileName.set('');
      this.brandMsg.set('已启用，顶栏已更新');
    } catch (e) {
      this.brandErr.set(errMsg(e));
    } finally {
      this.brandBusy.set(false);
    }
  }

  protected async resetBrand(): Promise<void> {
    this.brandBusy.set(true);
    this.brandMsg.set('');
    this.brandErr.set('');
    try {
      const r = await this.api.clearBrandIcon();
      this.branding.apply(r.branding);
      this.brandMsg.set('已恢复自带图标');
    } catch (e) {
      this.brandErr.set(errMsg(e));
    } finally {
      this.brandBusy.set(false);
    }
  }

  private dropLocalPreview(): void {
    const url = this.brandLocalURL();
    if (url) URL.revokeObjectURL(url);
    this.brandLocalURL.set('');
  }

  // ---- 原文快照 ----
  protected readonly snap = signal<SnapshotStats | null>(null);
  protected readonly snapOpen = signal(false);
  protected readonly snapBusy = signal('');
  protected readonly snapMsg = signal('');
  protected readonly snapErr = signal('');
  protected readonly cleanupMode = signal<'' | 'orphan' | 'all'>('');

  protected readonly snapBytes = computed(() => bytes(this.snap()?.bytes ?? 0));
  /** 占用相对索引体量的直观说法：光是"48.5 MB"看不出是多是少。 */
  protected readonly snapRatio = computed(() => {
    const s = this.snap();
    if (!s || s.pages === 0) return '暂无快照';
    return `平均 ${bytes(Math.round(s.bytes / s.pages))}/条`;
  });

  /** 行内说明：说清当前状态的实际后果。 */
  protected snapFoot(): string {
    const s = this.snap();
    if (!s) return '读取占用失败';
    if (!s.available) return '本次运行没有挂载快照库，开关不生效';
    if (!s.enabled) return `已停止保存新快照，${s.pages} 条旧快照仍可查看`;
    if (s.orphans > 0) return `${s.pages} 条，其中 ${s.orphans} 条索引里已无对应页面`;
    return `${s.pages} 条快照，索引里没有孤儿`;
  }

  protected openSnap(): void {
    this.snapMsg.set('');
    this.snapErr.set('');
    this.snapOpen.set(true);
    void this.loadSnap();
  }

  private async loadSnap(): Promise<void> {
    try {
      this.snap.set(await this.api.snapshotStats());
    } catch (e) {
      this.snapErr.set(errMsg(e));
    }
  }

  protected async toggleSnap(on: boolean): Promise<void> {
    this.snapBusy.set('switch');
    this.snapMsg.set('');
    this.snapErr.set('');
    try {
      await this.api.setSnapshot(on);
      await this.loadSnap();
      this.snapMsg.set(on ? '已开启，下次抓取开始保存快照' : '已关闭，只停止新增');
    } catch (e) {
      this.snapErr.set(errMsg(e));
    } finally {
      this.snapBusy.set('');
    }
  }

  protected askCleanup(mode: 'orphan' | 'all'): void {
    this.snapMsg.set('');
    this.snapErr.set('');
    this.cleanupMode.set(mode);
  }

  /** desc 写「会发生什么」，不是「确定吗」。 */
  protected cleanupDesc(mode: 'orphan' | 'all'): string {
    const s = this.snap();
    if (mode === 'all') {
      return `会删除全部 ${s?.pages ?? 0} 条原文快照，结果页的「查看快照」和引用编号定位随即失效（提示没有快照）。索引本身不受影响，重新抓取可再次生成。`;
    }
    return `会删除 ${s?.orphans ?? 0} 条索引里已不存在的页面的快照。这些页面在搜索结果里已经不出现，删除不影响现有结果；重新收录后会重新生成。`;
  }

  protected async runCleanup(mode: 'orphan' | 'all'): Promise<void> {
    this.cleanupMode.set('');
    this.snapBusy.set(mode);
    this.snapMsg.set('');
    this.snapErr.set('');
    try {
      const r = await this.api.cleanupSnapshot(mode);
      await this.loadSnap();
      const extra = r.truncated ? '（单次有上限，请再点一次继续）' : '';
      this.snapMsg.set(`已删除 ${r.removed} 条，回收 ${bytes(r.freed)}${extra}`);
    } catch (e) {
      this.snapErr.set(errMsg(e));
    } finally {
      this.snapBusy.set('');
    }
  }

  // ---- 邮件（SMTP）----
  protected readonly mailOpen = signal(false);
  protected readonly capOpen = signal(false);
  protected readonly mailEnabled = signal(false);
  protected readonly mailHost = signal('');
  protected readonly mailPort = signal('587');
  protected readonly mailUser = signal('');
  protected readonly mailPass = signal('');
  protected readonly mailFrom = signal('');
  protected readonly mailSecure = signal(false);
  protected readonly mailHasPass = signal(false);
  protected readonly mailBusy = signal(false);
  protected readonly mailMsg = signal('');
  protected readonly mailErr = signal('');
  protected readonly testTo = signal('');
  protected readonly testBusy = signal(false);
  protected readonly testMsg = signal('');
  protected readonly testErr = signal('');

  private async loadMail() {
    try {
      const c = await this.api.getMailConfig();
      this.mailHost.set(c.host ?? '');
      this.mailPort.set(String(c.port || 587));
      this.mailUser.set(c.user ?? '');
      this.mailFrom.set(c.from ?? '');
      this.mailSecure.set(!!c.secure);
      this.mailHasPass.set(!!c.has_pass);
      this.mailEnabled.set(!!c.enabled);
      this.mailPass.set('');
    } catch {
      /* 读不到保持默认 */
    }
  }

  protected async saveMail(): Promise<void> {
    this.mailMsg.set('');
    this.mailErr.set('');
    this.mailBusy.set(true);
    try {
      await this.api.setMailConfig({
        host: this.mailHost().trim(),
        port: Number(this.mailPort()) || 0,
        user: this.mailUser().trim(),
        pass: this.mailPass().trim(),
        from: this.mailFrom().trim(),
        secure: this.mailSecure(),
      });
      this.mailHasPass.update((v) => v || this.mailPass().trim() !== '');
      this.mailPass.set('');
      this.mailEnabled.set(this.mailHost().trim() !== '');
      this.mailMsg.set('已保存');
    } catch (e) {
      this.mailErr.set(errMsg(e));
    } finally {
      this.mailBusy.set(false);
    }
  }

  protected async sendTestMail(): Promise<void> {
    this.testMsg.set('');
    this.testErr.set('');
    this.testBusy.set(true);
    try {
      const r = await this.api.testMail(this.testTo().trim());
      this.testMsg.set(r.message);
    } catch (e) {
      this.testErr.set(errMsg(e));
    } finally {
      this.testBusy.set(false);
    }
  }

  /** 行内展示的 provider 名称。 */
  protected capLabel(): string {
    const hit = this.PROVIDERS.find((p) => p.value === this.capProvider());
    return hit ? hit.label : '关闭';
  }

  /** 行内说明：说清当前状态的实际后果，不是解释规则。 */
  protected capFoot(): string {
    const p = this.capProvider();
    if (p === 'none' || p === '') return '未选择验证方式';
    if (p === 'math') return '内置算术题，零配置';
    return this.capHasSecret() ? '已配置密钥' : '缺少密钥，暂不生效';
  }

  protected readonly PROVIDERS: { value: string; label: string }[] = [
    { value: 'none', label: '关闭' },
    { value: 'math', label: '内置算术题' },
    { value: 'turnstile', label: 'Cloudflare Turnstile' },
    { value: 'geetest', label: '极验 Geetest v4' },
    { value: 'vaptcha', label: 'VAPTCHA' },
    { value: 'cap', label: 'Cap（自托管）' },
  ];

  protected readonly capProvider = signal('none');
  protected readonly capSiteKey = signal('');
  protected readonly capSecret = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly capHasSecret = signal(false);
  protected readonly capBusy = signal(false);
  protected readonly capMsg = signal('');
  protected readonly capTestBusy = signal(false);
  protected readonly capErr = signal('');

  protected needsKeys(): boolean {
    const p = this.capProvider();
    return p !== 'none' && p !== 'math';
  }

  protected siteKeyLabel(): string {
    switch (this.capProvider()) {
      case 'geetest':
        return 'Captcha ID';
      case 'vaptcha':
        return 'VID';
      default:
        return 'Site Key';
    }
  }

  protected secretLabel(): string {
    switch (this.capProvider()) {
      case 'geetest':
        return 'Captcha Key';
      case 'vaptcha':
        return 'Secret Key';
      default:
        return 'Secret Key';
    }
  }

  private async loadCaptcha() {
    try {
      const c = await this.api.getCaptchaConfig();
      this.capProvider.set(c.provider || 'none');
      this.capSiteKey.set(c.site_key ?? '');
      this.capEndpoint.set(c.endpoint ?? '');
      this.capHasSecret.set(!!c.has_secret);
      this.capSecret.set('');
    } catch {
      /* 读不到就保持默认，不打扰 */
    }
  }

  protected async testCaptcha(): Promise<void> {
    this.capMsg.set('');
    this.capErr.set('');
    this.capTestBusy.set(true);
    try {
      const r = await this.api.testCaptcha();
      this.capMsg.set(r.message);
    } catch (e) {
      this.capErr.set(errMsg(e));
    } finally {
      this.capTestBusy.set(false);
    }
  }

  protected async saveCaptcha(): Promise<void> {
    this.capMsg.set('');
    this.capErr.set('');
    this.capBusy.set(true);
    try {
      const r = await this.api.setCaptchaConfig({
        provider: this.capProvider(),
        site_key: this.capSiteKey().trim(),
        secret: this.capSecret().trim(),
        endpoint: this.capEndpoint().trim(),
      });
      this.capHasSecret.update((v) => v || this.capSecret().trim() !== '');
      this.capSecret.set('');
      this.capMsg.set(r.ready ? '已保存并生效' : '已保存，但 key 不完整，暂不生效');
    } catch (e) {
      this.capErr.set(errMsg(e));
    } finally {
      this.capBusy.set(false);
    }
  }

  private readonly api = inject(AdminApi);

  protected readonly items = signal<FeatureFlag[]>([]);
  protected readonly loading = signal(false);
  protected readonly err = signal('');
  protected readonly msg = signal('');
  protected readonly busy = signal('');
  protected readonly fullTime = fullTime;

  constructor() {
    effect(() => {
      void this.load();
    });
    void this.loadCaptcha();
    void this.loadMail();
    void this.loadSnap();
    void this.loadAI();
    void this.loadWhois();
    void this.loadDistill();
    void this.branding.load();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listFeatures();
      this.items.set(r.features ?? []);
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected async toggle(f: FeatureFlag, on: boolean): Promise<void> {
    this.busy.set(f.key);
    this.err.set('');
    this.msg.set('');
    try {
      await this.api.setFeature(f.key, on);
      this.msg.set(`${f.label}已${on ? '开启' : '关闭'}，立即生效。`);
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }
}
