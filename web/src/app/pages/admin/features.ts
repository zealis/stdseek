import { Component, computed, effect, inject, signal } from '@angular/core';
import { AdminApi, type AIConfig } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { bytes, fullTime, shortTime } from '../../shared/format';
import {
  type FeatureFlag,
  type SnapshotBackup,
  type SnapshotStats,
  type SnapshotStorage,
} from '../../core/api';
import { Modal } from '../../shared/ui/modal';
import { Confirm } from '../../shared/ui/confirm';

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
        <button type="button" class="btn btn-outline btn-sm" (click)="mailOpen.set(true)">配置</button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">人机验证方式</span>
            <span class="chip" [class.chip-success]="capProvider() !== 'none' && capProvider() !== 'math'">
              {{ capLabel() }}
            </span>
          </div>
          <p class="flag-foot">{{ capFoot() }}</p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="capOpen.set(true)">配置</button>
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
        <button type="button" class="btn btn-outline btn-sm" [disabled]="!snap()?.available"
          (click)="openSnap()">管理</button>
      </div>
    </div>

    @if (mailOpen()) {
      <vx-modal title="邮件（SMTP）" [width]="520" (closed)="mailOpen.set(false)">
        <div class="row wrap" style="gap: 10px">
          <label class="field" style="flex: 2; min-width: 220px">
            <span>SMTP 服务器</span>
            <input class="input" placeholder="smtp.qq.com"
              [value]="mailHost()" (input)="mailHost.set($any($event.target).value)" />
          </label>
          <label class="field" style="width: 110px">
            <span>端口</span>
            <input class="input mono" inputmode="numeric"
              [value]="mailPort()" (input)="mailPort.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 180px">
            <span>用户名</span>
            <input class="input" autocomplete="off"
              [value]="mailUser()" (input)="mailUser.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 180px">
            <span>密码{{ mailHasPass() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ mailHasPass() ? '••••••••' : '' }}"
              [value]="mailPass()" (input)="mailPass.set($any($event.target).value)" />
          </label>
        </div>

        <label class="field" style="margin-top: 10px">
          <span>发件地址</span>
          <input class="input" placeholder="留空则用用户名"
            [value]="mailFrom()" (input)="mailFrom.set($any($event.target).value)" />
        </label>

        <label class="row" style="gap: 6px; align-items: center; margin-top: 10px; cursor: pointer">
          <input type="checkbox" [checked]="mailSecure()"
            (change)="mailSecure.set($any($event.target).checked)" />
          <span class="small">使用隐式 TLS（465 端口）</span>
        </label>

        <div class="row wrap" style="margin-top: 14px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="mailBusy()" (click)="saveMail()">
            @if (mailBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
          @if (mailMsg()) { <span class="small" style="color: var(--success)">{{ mailMsg() }}</span> }
          @if (mailErr()) { <span class="small" style="color: var(--danger)">{{ mailErr() }}</span> }
        </div>

        <div class="row wrap" style="margin-top: 14px; gap: 8px; align-items: flex-end;
             border-top: 1px solid var(--line); padding-top: 14px">
          <label class="field" style="flex: 1; min-width: 200px">
            <span>测试收件地址</span>
            <input class="input" type="email" placeholder="you@example.com"
              [value]="testTo()" (input)="testTo.set($any($event.target).value)" />
          </label>
          <button type="button" class="btn btn-outline" [disabled]="testBusy()" (click)="sendTestMail()">
            @if (testBusy()) { <span class="spinner"></span> } @else { 发送测试邮件 }
          </button>
        </div>
        @if (testMsg()) { <p class="small" style="margin-top: 8px; color: var(--success)">{{ testMsg() }}</p> }
        @if (testErr()) { <p class="small" style="margin-top: 8px; color: var(--danger)">{{ testErr() }}</p> }
      </vx-modal>
    }

    @if (aiOpen()) {
      <vx-modal title="AI 接口" [width]="520" (closed)="aiOpen.set(false)">
        <span class="faint small">OpenAI 兼容接口，供「AI 站点分类」与「AI 概览」共用；分类每判定一个站点一次调用，概览按需调用。</span>

        <label class="field" style="margin-top: 14px">
          <span>接口地址</span>
          <input class="input mono" placeholder="https://api.deepseek.com/v1"
            [value]="aiBase()" (input)="aiBase.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input class="input mono" placeholder="deepseek-chat"
              [value]="aiModel()" (input)="aiModel.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiHasKey() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ aiHasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiKey()" (input)="aiKey.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">备用接口</span>
          @if (ai()?.failover) {
            <span class="chip chip-danger">正在使用</span>
          }
        </div>
        <span class="faint small">主接口超时或返回 5xx 时自动顶上，恢复后自动切回；切换与恢复都会发邮件通知管理员。不填则不启用。</span>

        <label class="field" style="margin-top: 10px">
          <span>接口地址</span>
          <input class="input mono" placeholder="https://api.moonshot.cn/v1"
            [value]="aiBackupBase()" (input)="aiBackupBase.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input class="input mono" placeholder="moonshot-v1-8k"
              [value]="aiBackupModel()" (input)="aiBackupModel.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiBackupHasKey() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ aiBackupHasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiBackupKey()" (input)="aiBackupKey.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">第二备用接口</span>
        </div>
        <span class="faint small">主接口与备用都不可用时才使用。不填则不启用。</span>

        <label class="field" style="margin-top: 10px">
          <span>接口地址</span>
          <input class="input mono" placeholder="https://api.example.com/v1"
            [value]="aiBackup2Base()" (input)="aiBackup2Base.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>模型</span>
            <input class="input mono" placeholder="model-name"
              [value]="aiBackup2Model()" (input)="aiBackup2Model.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>API Key{{ aiBackup2HasKey() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ aiBackup2HasKey() ? '••••••••' : 'sk-...' }}"
              [value]="aiBackup2Key()" (input)="aiBackup2Key.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">AI 概览优先接口</span>
        </div>
        <select class="input select" style="width: 100%"
          [value]="aiSummaryEndpoint()" (change)="aiSummaryEndpoint.set($any($event.target).value)">
          <option value="primary" [selected]="aiSummaryEndpoint() === 'primary'">主接口</option>
          <option value="backup" [selected]="aiSummaryEndpoint() === 'backup'">备用接口</option>
          <option value="backup2" [selected]="aiSummaryEndpoint() === 'backup2'">第二备用接口</option>
        </select>
        <span class="faint small">只影响 AI 概览的尝试顺序；站点分类仍按主 → 备用 → 第二备用自动切换。</span>

        <div class="row wrap" style="margin-top: 16px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="aiBusy()" (click)="saveAI()">
            @if (aiBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="aiTestBusy()" (click)="testAI()">
            @if (aiTestBusy()) { <span class="spinner"></span> } @else { 测试连通性 }
          </button>
          @if (aiMsg()) { <span class="small" style="color: var(--success)">{{ aiMsg() }}</span> }
          @if (aiErr()) { <span class="small" style="color: var(--danger)">{{ aiErr() }}</span> }
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
            <span class="faint small">{{
              bk()?.prune_local
                ? '云端已归档 ' + (bk()?.keys ?? 0) + ' 页'
                : '索引文档 ' + (snap()?.documents ?? 0) + ' 条'
            }}</span>
          </div>
          <div class="card stat">
            <span class="faint small">孤儿快照</span>
            <strong class="stat-num mono">{{ snap()?.orphans ?? 0 }}</strong>
            <span class="faint small">索引里已无对应页面</span>
          </div>
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">云存储</span>
          <span class="chip">当前：本地</span>
        </div>
        <span class="faint small">热层仍在本地；开启下方云冷备后才按包上传。</span>

        <label class="field" style="margin-top: 10px">
          <span>接口地址</span>
          <input class="input mono" placeholder="https://s3.us-west-004.backblazeb2.com"
            [value]="s3Endpoint()" (input)="s3Endpoint.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>桶名</span>
            <input class="input mono" placeholder="voidxo-snapshots"
              [value]="s3Bucket()" (input)="s3Bucket.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>区域</span>
            <input class="input mono" placeholder="us-west-004 / auto"
              [value]="s3Region()" (input)="s3Region.set($any($event.target).value)" />
          </label>
        </div>

        <label class="field" style="margin-top: 10px">
          <span>前缀</span>
          <input class="input mono" placeholder="snapshots/"
            [value]="s3Prefix()" (input)="s3Prefix.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span>Access Key{{ s3HasAccessKey() ? '（留空则不修改）' : '' }}</span>
            <input class="input" autocomplete="off"
              placeholder="{{ s3HasAccessKey() ? '已保存' : '' }}"
              [value]="s3AccessKey()" (input)="s3AccessKey.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span>Secret{{ s3HasSecret() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ s3HasSecret() ? '••••••••' : '' }}"
              [value]="s3SecretKey()" (input)="s3SecretKey.set($any($event.target).value)" />
          </label>
        </div>

        <label class="row" style="gap: 8px; align-items: center; margin-top: 10px">
          <input type="checkbox" [checked]="s3PathStyle()"
            (change)="s3PathStyle.set($any($event.target).checked)" />
          <span class="small">Path-style（endpoint/bucket；B2/R2 常用）</span>
        </label>

        <div class="row wrap" style="margin-top: 12px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="!!s3Busy()" (click)="saveStorage()">
            @if (s3Busy() === 'save') { <span class="spinner"></span> } @else { 保存配置 }
          </button>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="!!s3Busy()" (click)="testStorage()">
            @if (s3Busy() === 'test') { <span class="spinner"></span> } @else { 测试连接 }
          </button>
          @if (s3Msg()) { <span class="small" style="color: var(--success)">{{ s3Msg() }}</span> }
          @if (s3Err()) { <span class="small" style="color: var(--danger)">{{ s3Err() }}</span> }
        </div>

        <div class="row wrap" style="margin-top: 18px; gap: 6px; align-items: center">
          <span class="section-title">云冷备</span>
          <span class="chip" [class.chip-success]="bk()?.enabled">
            {{ bk()?.enabled ? '已开启' : '已关闭' }}
          </span>
          <span class="chip mono">{{ bk()?.keys ?? 0 }} 页 · {{ bk()?.parts ?? 0 }} 包</span>
        </div>

        <div class="row-between" style="gap: 12px; margin-top: 8px; align-items: center">
          <span class="small">自动打包上传</span>
          <button
            type="button"
            class="switch"
            role="switch"
            aria-label="开启云冷备"
            [attr.aria-checked]="bk()?.enabled"
            [class.is-on]="bk()?.enabled"
            [disabled]="!!bkBusy() || !snap()?.storage?.s3_configured"
            (click)="toggleBackup(!bk()?.enabled)"
          ></button>
        </div>

        <div class="row wrap" style="gap: 8px; align-items: flex-end; margin-top: 10px">
          <label class="field" style="width: 132px">
            <span>单包大小（MB）</span>
            <input class="input mono" inputmode="numeric"
              [value]="bkPartMB()" (input)="bkPartMB.set($any($event.target).value)" />
          </label>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="!!bkBusy()" (click)="saveBackupSize()">
            保存
          </button>
          @if (bk()?.last_run) {
            <span class="faint small">上次 {{ shortTime(bk()!.last_run) }}</span>
          }
          @if (bk()?.exported_at) {
            <span class="faint small">导出 {{ shortTime(bk()!.exported_at) }} · {{ bk()?.export_docs ?? 0 }} 条</span>
          }
        </div>

        <div class="row wrap" style="gap: 6px; align-items: center; margin-top: 10px">
          <span class="small">快照存放</span>
          <button type="button" class="chip" [class.is-active]="!bk()?.prune_local"
            [disabled]="!!bkBusy()" (click)="setMode('local')">本地 + 云备份</button>
          <button type="button" class="chip" [class.is-active]="bk()?.prune_local"
            [disabled]="!!bkBusy() || !bk()?.enabled" (click)="setMode('cloud')">只留云端</button>
          @if (bk()?.prune_local) {
            <span class="faint small">本地只做暂存，读取从云端回源</span>
          }
        </div>
        @if (bk()?.last_error) {
          <div class="small" style="color: var(--danger); margin-top: 6px">{{ bk()?.last_error }}</div>
        }

        <div class="row wrap" style="gap: 8px; align-items: center; margin-top: 10px">
          <button type="button" class="btn btn-solid btn-sm"
            [disabled]="!!bkBusy() || !snap()?.storage?.s3_configured" (click)="runBackup()">
            @if (bkBusy() === 'run') { <span class="spinner"></span> } @else { 立即冷备 }
          </button>
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!bkBusy() || !snap()?.storage?.s3_configured" (click)="exportDocs()">
            @if (bkBusy() === 'export') { <span class="spinner"></span> } @else { 导出文档 }
          </button>
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!bkBusy() || !bk()?.last_key" (click)="verifyArchive()">
            @if (bkBusy() === 'verify') { <span class="spinner"></span> } @else { 校验归档 }
          </button>
          @if (bkMsg()) { <span class="small" style="color: var(--success)">{{ bkMsg() }}</span> }
          @if (bkErr()) { <span class="small" style="color: var(--danger)">{{ bkErr() }}</span> }
        </div>

        <div class="row wrap" style="margin-top: 16px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!snapBusy() || (snap()?.orphans ?? 0) === 0"
            (click)="askCleanup('orphan')">
            清理孤儿
          </button>
          <button type="button" class="btn btn-danger btn-sm"
            [disabled]="!!snapBusy() || (snap()?.pages ?? 0) === 0"
            (click)="askCleanup('all')">
            清空全部
          </button>
          @if (snapBusy() === 'orphan' || snapBusy() === 'all') {
            <span class="spinner"></span>
          }
          @if (snapMsg()) { <span class="small" style="color: var(--success)">{{ snapMsg() }}</span> }
          @if (snapErr()) { <span class="small" style="color: var(--danger)">{{ snapErr() }}</span> }
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
            <input class="input"
              [value]="capSiteKey()" (input)="capSiteKey.set($any($event.target).value)" />
          </label>
          <label class="field" style="margin-top: 10px">
            <span>{{ secretLabel() }}{{ capHasSecret() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ capHasSecret() ? '••••••••' : '' }}"
              [value]="capSecret()" (input)="capSecret.set($any($event.target).value)" />
          </label>
        }

        @if (capProvider() === 'cap') {
          <label class="field" style="margin-top: 10px">
            <span>Cap 服务地址</span>
            <input class="input"
              [value]="capEndpoint()" (input)="capEndpoint.set($any($event.target).value)" />
          </label>
        }

        <div class="row wrap" style="margin-top: 16px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="capBusy()" (click)="saveCaptcha()">
            @if (capBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="capTestBusy()" (click)="testCaptcha()">
            @if (capTestBusy()) { <span class="spinner"></span> } @else { 测试连通性 }
          </button>
          @if (capMsg()) { <span class="small" style="color: var(--success)">{{ capMsg() }}</span> }
          @if (capErr()) { <span class="small" style="color: var(--danger)">{{ capErr() }}</span> }
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
    this.aiMsg.set('');
    this.aiOpen.set(true);
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

  // ---- 原文快照 ----
  protected readonly snap = signal<SnapshotStats | null>(null);
  protected readonly snapOpen = signal(false);
  protected readonly snapBusy = signal('');
  protected readonly snapMsg = signal('');
  protected readonly snapErr = signal('');
  protected readonly cleanupMode = signal<'' | 'orphan' | 'all'>('');
  // ---- 快照云存储（本阶段只保存配置/测试连接，读写仍本地） ----
  protected readonly s3Endpoint = signal('');
  protected readonly s3Bucket = signal('');
  protected readonly s3Region = signal('');
  protected readonly s3Prefix = signal('snapshots/');
  protected readonly s3AccessKey = signal('');
  protected readonly s3SecretKey = signal('');
  protected readonly s3HasAccessKey = signal(false);
  protected readonly s3HasSecret = signal(false);
  protected readonly s3PathStyle = signal(true);
  protected readonly s3Busy = signal('');
  protected readonly s3Msg = signal('');
  protected readonly s3Err = signal('');
  // ---- 云冷备（按包上传；默认关，开启才会向外发数据） ----
  protected readonly bk = signal<SnapshotBackup | null>(null);
  protected readonly bkPartMB = signal('128');
  protected readonly bkBusy = signal('');
  protected readonly bkMsg = signal('');
  protected readonly bkErr = signal('');
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
    this.s3Msg.set('');
    this.s3Err.set('');
    this.bkMsg.set('');
    this.bkErr.set('');
    this.snapOpen.set(true);
    void this.loadSnap();
  }

  private async loadSnap(): Promise<void> {
    try {
      this.snap.set(await this.api.snapshotStats());
      this.applyStorage(this.snap()?.storage);
      this.applyBackup(this.snap()?.backup);
    } catch (e) {
      this.snapErr.set(errMsg(e));
    }
  }

  /** 云冷备状态回填；单包大小跟着服务端的值走，不在前端留第二份默认值。 */
  private applyBackup(b?: SnapshotBackup): void {
    if (!b) return;
    this.bk.set(b);
    this.bkPartMB.set(String(b.part_mb || 128));
  }

  /** 回填云存储配置；密钥永远不回显，只保留「已保存」状态。 */
  private applyStorage(st?: SnapshotStorage): void {
    if (!st) return;
    this.s3Endpoint.set(st.endpoint ?? '');
    this.s3Bucket.set(st.bucket ?? '');
    this.s3Region.set(st.region ?? '');
    this.s3Prefix.set(st.prefix || 'snapshots/');
    this.s3AccessKey.set('');
    this.s3SecretKey.set('');
    this.s3HasAccessKey.set(Boolean(st.has_access_key));
    this.s3HasSecret.set(Boolean(st.has_secret));
    this.s3PathStyle.set(st.path_style !== false);
  }

  private storageBody() {
    return {
      endpoint: this.s3Endpoint().trim(),
      bucket: this.s3Bucket().trim(),
      region: this.s3Region().trim(),
      prefix: this.s3Prefix().trim(),
      access_key: this.s3AccessKey().trim(),
      secret_key: this.s3SecretKey().trim(),
      path_style: this.s3PathStyle(),
    };
  }

  protected async saveStorage(): Promise<void> {
    this.s3Busy.set('save');
    this.s3Msg.set('');
    this.s3Err.set('');
    try {
      const r = await this.api.saveSnapshotStorage(this.storageBody());
      this.applyStorage(r.storage);
      this.s3Msg.set('已保存');
    } catch (e) {
      this.s3Err.set(errMsg(e));
    } finally {
      this.s3Busy.set('');
    }
  }

  protected async testStorage(): Promise<void> {
    this.s3Busy.set('test');
    this.s3Msg.set('');
    this.s3Err.set('');
    try {
      const r = await this.api.testSnapshotStorage(this.storageBody());
      this.s3Msg.set(r.message || '连接成功');
    } catch (e) {
      this.s3Err.set(errMsg(e));
    } finally {
      this.s3Busy.set('');
    }
  }

  // ---- 云冷备 ----

  protected async toggleBackup(on: boolean): Promise<void> {
    this.bkBusy.set('switch');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      const r = await this.api.setSnapshotBackup(on, this.partMB());
      this.applyBackup(r.backup);
      this.bkMsg.set(on ? '已开启，每小时检查一次新页面' : '已关闭，不再上传');
    } catch (e) {
      this.bkErr.set(errMsg(e));
    } finally {
      this.bkBusy.set('');
    }
  }

  protected async saveBackupSize(): Promise<void> {
    const mb = this.partMB();
    if (mb < 16 || mb > 512) {
      this.bkErr.set('单包大小需在 16–512 MB 之间');
      return;
    }
    this.bkBusy.set('size');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      const r = await this.api.setSnapshotBackup(this.bk()?.enabled ?? false, mb);
      this.applyBackup(r.backup);
      this.bkMsg.set('已保存');
    } catch (e) {
      this.bkErr.set(errMsg(e));
    } finally {
      this.bkBusy.set('');
    }
  }

  private partMB(): number {
    const n = Number(this.bkPartMB());
    return Number.isFinite(n) ? Math.round(n) : 0;
  }

  protected async runBackup(): Promise<void> {
    this.bkBusy.set('run');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      await this.api.runSnapshotBackup();
      await this.pollJob('run');
    } catch (e) {
      this.bkErr.set(errMsg(e));
      this.bkBusy.set('');
    }
  }

  /** 上传是分钟级操作，服务端异步跑，这里轮询到任务结束再显示结果。 */
  private async pollJob(kind: string): Promise<void> {
    for (let i = 0; i < 400; i++) {
      await new Promise((resolve) => setTimeout(resolve, 1500));
      await this.loadSnap();
      const job = this.snap()?.job;
      if (!job?.job) {
        const msg = job?.message || '';
        if (msg.includes('失败')) this.bkErr.set(msg);
        else this.bkMsg.set(msg);
        this.bkBusy.set('');
        return;
      }
    }
    this.bkErr.set('任务仍在后台运行，稍后刷新页面查看结果');
    this.bkBusy.set('');
    void kind;
  }

  /** 快照存放方式。切到「只留云端」要先确认归档开着，否则本地删了就真没了。 */
  protected async setMode(mode: 'local' | 'cloud'): Promise<void> {
    this.bkBusy.set('mode');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      const r = await this.api.setSnapshotMode(mode);
      this.applyBackup(r.backup);
      this.bkMsg.set(
        mode === 'cloud' ? '已切换：下次归档后删除本地副本' : '已切换：本地保留全量',
      );
      await this.loadSnap();
    } catch (e) {
      this.bkErr.set(errMsg(e));
    } finally {
      this.bkBusy.set('');
    }
  }

  protected async exportDocs(): Promise<void> {
    this.bkBusy.set('export');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      await this.api.exportDocuments();
      await this.pollJob('export');
    } catch (e) {
      this.bkErr.set(errMsg(e));
      this.bkBusy.set('');
    }
  }

  /** 校验归档：从桶里按 manifest 范围读回最近归档的一页，与本地比长度。 */
  protected async verifyArchive(): Promise<void> {
    const key = this.bk()?.last_key;
    if (!key) return;
    this.bkBusy.set('verify');
    this.bkMsg.set('');
    this.bkErr.set('');
    try {
      const r = await this.api.restoreSnapshot(key);
      if (r.match === false) {
        this.bkErr.set(`取回 ${r.bytes} 字节，与本地 ${r.local_bytes ?? 0} 字节不一致`);
        return;
      }
      this.bkMsg.set(
        r.local_absent
          ? `已从云端取回 ${r.bytes} 字节（本地无副本）`
          : `取回 ${r.bytes} 字节，与本地一致`,
      );
    } catch (e) {
      this.bkErr.set(errMsg(e));
    } finally {
      this.bkBusy.set('');
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
      this.snapMsg.set(
        `已删除 ${r.removed} 条，回收 ${bytes(r.freed)}${extra}`,
      );
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
  protected readonly shortTime = shortTime;

  constructor() {
    effect(() => {
      void this.load();
    });
    void this.loadCaptcha();
    void this.loadMail();
    void this.loadSnap();
    void this.loadAI();
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
