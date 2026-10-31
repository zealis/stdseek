import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { bytes, shortTime } from '../../shared/format';
import { type SnapshotBackup, type SnapshotStats, type SnapshotStorage } from '../../core/api';

/**
 * 备份与恢复：把「页面原文 / 文档元数据 / 整库」推到对象存储，以及取回。
 *
 * 为什么独立成页而不是塞进「原文快照」弹窗：那边只回答「本地这堆快照占了多少、
 * 要不要清」，而这里是四件不同的事（连接、归档、整库、恢复），
 * 挤在一个 560px 的弹窗里既放不下也没有层次，用户找不到重点。
 */
@Component({
  selector: 'vx-backup-page',
  imports: [],
  template: `
    @if (stats() === null) {
      <div class="card"><div class="loading"><span class="spinner spinner-lg"></span></div></div>
    } @else {
      <div class="card">
        <div class="row wrap" style="gap: 8px; align-items: center; margin-bottom: 12px">
          <span class="section-title" style="margin: 0">对象存储</span>
          <span class="chip" [class.chip-success]="storage().s3_configured">
            {{ storage().s3_configured ? '已配置' : '未配置' }}
          </span>
          <span class="faint small">用私有桶：整库快照里有账号邮箱与密码哈希</span>
        </div>

        <label class="field" style="margin-bottom: 12px">
          <span class="field-label">接口地址</span>
          <input class="input mono" placeholder="https://s3.us-west-004.backblazeb2.com"
            [value]="endpoint()" (input)="endpoint.set($any($event.target).value)" />
        </label>

        <div class="row wrap" style="gap: 12px; margin-bottom: 12px">
          <label class="field" style="flex: 1; min-width: 180px">
            <span class="field-label">桶名</span>
            <input class="input mono" placeholder="voidxo-backup"
              [value]="bucket()" (input)="bucket.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 140px">
            <span class="field-label">区域</span>
            <input class="input mono" placeholder="us-west-004 / auto"
              [value]="region()" (input)="region.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="gap: 12px; margin-bottom: 12px">
          <label class="field" style="flex: 1; min-width: 160px">
            <span class="field-label">Access Key{{ hasAccess() ? '（留空则不修改）' : '' }}</span>
            <input class="input" autocomplete="off" placeholder="{{ hasAccess() ? '已保存' : '' }}"
              [value]="accessKey()" (input)="accessKey.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 160px">
            <span class="field-label">Secret{{ hasSecret() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off" placeholder="{{ hasSecret() ? '••••••••' : '' }}"
              [value]="secretKey()" (input)="secretKey.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="gap: 16px; align-items: flex-end; margin-bottom: 16px">
          <label class="field" style="width: 160px">
            <span class="field-label">前缀</span>
            <input class="input mono" placeholder="voidxo/"
              [value]="prefix()" (input)="prefix.set($any($event.target).value)" />
          </label>
          <label class="row" style="gap: 6px; align-items: center; height: 36px; cursor: pointer">
            <input type="checkbox" [checked]="pathStyle()"
              (change)="pathStyle.set($any($event.target).checked)" />
            <span class="small">Path-style（B2/R2 常用）</span>
          </label>
          <label class="row" style="gap: 6px; align-items: center; height: 36px; cursor: pointer">
            <input type="checkbox" [checked]="noProxy()"
              (change)="noProxy.set($any($event.target).checked)" />
            <span class="small">直连（不走系统代理，上传快得多）</span>
          </label>
        </div>

        <!-- 结果和按钮同一行：跑到下面另起一块，看着像「操作失败留下的报错」 -->
        <div class="row wrap" style="gap: 10px; align-items: center">
          @if (connMsg()) {
            <span class="small" style="color: var(--success); margin-right: auto">{{ connMsg() }}</span>
          }
          @if (connErr()) {
            <span class="small" style="color: var(--danger); margin-right: auto">{{ connErr() }}</span>
          }
          <button type="button" class="btn btn-outline btn-sm" [disabled]="!!busy()" (click)="testConn()">
            @if (busy() === 'test') { <span class="spinner"></span> } @else { 测试连接 }
          </button>
          <button type="button" class="btn btn-solid btn-sm" [disabled]="!!busy()" (click)="saveConn()">
            @if (busy() === 'save') { <span class="spinner"></span> } @else { 保存 }
          </button>
        </div>
      </div>

      <div class="card">
        <div class="row-between" style="gap: 12px; align-items: flex-start">
          <div class="stack" style="gap: 2px">
            <span class="section-title" style="margin: 0">页面原文</span>
            <span class="faint small">
              已归档 {{ bk().keys }} 页 · {{ bk().parts }} 个包
              @if (bk().last_run) { · 上次 {{ shortTime(bk().last_run) }} }
            </span>
          </div>
          <button
            type="button"
            class="switch"
            role="switch"
            aria-label="自动打包上传"
            [attr.aria-checked]="bk().enabled"
            [class.is-on]="bk().enabled"
            [disabled]="!!busy() || !storage().s3_configured"
            (click)="toggleBackup(!bk().enabled)"
          ></button>
        </div>

        <div class="row wrap" style="gap: 8px; align-items: center; margin-top: 14px">
          <span class="faint small">存放</span>
          <button type="button" class="chip" [class.is-active]="!bk().prune_local"
            [disabled]="!!busy()" (click)="setMode('local')">本地 + 云备份</button>
          <button type="button" class="chip" [class.is-active]="bk().prune_local"
            [disabled]="!!busy() || !bk().enabled" (click)="setMode('cloud')">只留云端</button>
        </div>

        <div class="row wrap" style="gap: 10px; align-items: flex-end; margin-top: 14px">
          <label class="field" style="width: 132px">
            <span class="field-label">单包大小（MB）</span>
            <input class="input mono" inputmode="numeric"
              [value]="partMB()" (input)="partMB.set($any($event.target).value)" />
          </label>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="!!busy()" (click)="savePartMB()">保存</button>
        </div>

        <div class="row wrap" style="gap: 8px; align-items: center; margin-top: 14px">
          <button type="button" class="btn btn-solid btn-sm"
            [disabled]="!!busy() || !storage().s3_configured" (click)="runBackup()">
            @if (busy() === 'pack') {
              <span class="spinner"></span> {{ progress() || '准备中' }}
            } @else { 立即冷备 }
          </button>
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!busy() || !storage().s3_configured" (click)="exportDocs()">
            @if (busy() === 'export') { <span class="spinner"></span> } @else { 导出文档 }
          </button>
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!busy() || !bk().last_key" (click)="verify()">校验归档</button>
          @if (msg()) { <span class="small" style="color: var(--success)">{{ msg() }}</span> }
          @if (err()) { <span class="small" style="color: var(--danger)">{{ err() }}</span> }
        </div>
        @if (bk().last_error) {
          <div class="small" style="color: var(--danger); margin-top: 8px">{{ bk().last_error }}</div>
        }
      </div>

      <div class="card">
        <div class="row wrap" style="gap: 8px; align-items: center">
          <span class="section-title" style="margin: 0">整库备份</span>
          <span class="chip">{{ bk().db_parts }} 份</span>
          @if (bk().db_last_at) {
            <span class="faint small">上次 {{ shortTime(bk().db_last_at) }} · {{ dbSize() }}</span>
          }
          <span class="faint small">白名单、账号、反馈、设置；保留最近 7 份</span>
        </div>

        <div class="row wrap" style="gap: 12px; align-items: flex-end; margin-top: 14px">
          <label class="field" style="width: 160px">
            <span class="field-label">自动备份频率</span>
            <select class="input select" [value]="dbEvery()" (change)="changeDBEvery($any($event.target).value)">
              <option value="24">每天</option>
              <option value="48">每 2 天</option>
              <option value="72">每 3 天</option>
              <option value="168">每周</option>
              <option value="0">关闭</option>
            </select>
          </label>
          <button type="button" class="btn btn-solid btn-sm"
            [disabled]="!!busy() || !storage().s3_configured" (click)="runDB()">
            @if (busy() === 'db') {
              <span class="spinner"></span> {{ progress() || '准备中' }}
            } @else { 立即备份数据库 }
          </button>
        </div>
      </div>

      <div class="card">
        <div class="row wrap" style="gap: 8px; align-items: center">
          <span class="section-title" style="margin: 0">恢复</span>
          <span class="faint small">只补缺失的页面；整库恢复见自托管文档</span>
        </div>
        <div class="row wrap" style="gap: 8px; align-items: center; margin-top: 12px">
          <button type="button" class="btn btn-outline btn-sm"
            [disabled]="!!busy() || !storage().s3_configured" (click)="restoreAll()">
            @if (busy() === 'restore') {
              <span class="spinner"></span> {{ progress() || '准备中' }}
            } @else { 取回全部快照 }
          </button>
        </div>
      </div>
    }
  `,
})
export class BackupPage {
  private readonly api = inject(AdminApi);
  protected readonly shortTime = shortTime;

  protected readonly stats = signal<SnapshotStats | null>(null);
  protected readonly busy = signal('');
  protected readonly msg = signal('');
  protected readonly err = signal('');
  protected readonly connMsg = signal('');
  protected readonly connErr = signal('');

  protected readonly endpoint = signal('');
  protected readonly bucket = signal('');
  protected readonly region = signal('');
  protected readonly prefix = signal('voidxo/');
  protected readonly accessKey = signal('');
  protected readonly secretKey = signal('');
  protected readonly hasAccess = signal(false);
  protected readonly hasSecret = signal(false);
  protected readonly pathStyle = signal(true);
  protected readonly noProxy = signal(false);
  protected readonly partMB = signal('128');

  protected readonly storage = computed<SnapshotStorage>(
    () =>
      this.stats()?.storage ?? {
        backend: 'local',
        s3_configured: false,
        endpoint: '',
        bucket: '',
        region: '',
        prefix: 'voidxo/',
        has_access_key: false,
        has_secret: false,
        path_style: true,
        no_proxy: false,
      },
  );

  /** 云端状态；没有数据时给一份零值，模板就不用到处写 ?. */
  protected readonly bk = computed<SnapshotBackup>(
    () =>
      this.stats()?.backup ?? {
        enabled: false,
        prune_local: false,
        part_mb: 128,
        parts: 0,
        keys: 0,
        last_run: '',
        last_error: '',
        last_key: '',
        exported_at: '',
        export_key: '',
        export_docs: 0,
        db_every_hours: 24,
        db_last_at: '',
        db_key: '',
        db_size: 0,
        db_parts: 0,
      },
  );

  protected readonly dbEvery = computed(() => String(this.bk().db_every_hours));
  /** 任务进行中的阶段与进度（打包 340/1145 页、上传 12.3/34.0 MB…）。 */
  protected readonly progress = computed(() => this.stats()?.job?.progress || '');
  protected readonly dbSize = computed(() => bytes(this.bk().db_size));

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      const s = await this.api.snapshotStats();
      this.stats.set(s);
      this.applyStorage(s.storage);
      if (!this.partMB()) this.partMB.set(String(s.backup.part_mb || 128));
    } catch (e) {
      this.err.set(errMsg(e));
    }
  }

  /** 回填连接配置；密钥永远不回显，只保留「已保存」状态。 */
  private applyStorage(st?: SnapshotStorage): void {
    if (!st) return;
    this.endpoint.set(st.endpoint ?? '');
    this.bucket.set(st.bucket ?? '');
    this.region.set(st.region ?? '');
    this.prefix.set(st.prefix || 'voidxo/');
    this.accessKey.set('');
    this.secretKey.set('');
    this.hasAccess.set(Boolean(st.has_access_key));
    this.hasSecret.set(Boolean(st.has_secret));
    this.pathStyle.set(st.path_style !== false);
    this.noProxy.set(Boolean(st.no_proxy));
  }

  private connBody() {
    return {
      endpoint: this.endpoint().trim(),
      bucket: this.bucket().trim(),
      region: this.region().trim(),
      prefix: this.prefix().trim(),
      access_key: this.accessKey().trim(),
      secret_key: this.secretKey().trim(),
      path_style: this.pathStyle(),
      no_proxy: this.noProxy(),
    };
  }

  private clearMsgs(): void {
    this.msg.set('');
    this.err.set('');
  }

  protected async saveConn(): Promise<void> {
    this.busy.set('save');
    this.connMsg.set('');
    this.connErr.set('');
    try {
      const r = await this.api.saveSnapshotStorage(this.connBody());
      this.applyStorage(r.storage);
      this.connMsg.set('已保存');
      await this.load();
    } catch (e) {
      this.connErr.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  protected async testConn(): Promise<void> {
    this.busy.set('test');
    this.connMsg.set('');
    this.connErr.set('');
    try {
      const r = await this.api.testSnapshotStorage(this.connBody());
      this.connMsg.set(r.message || '连接成功');
    } catch (e) {
      this.connErr.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  protected async toggleBackup(on: boolean): Promise<void> {
    this.busy.set('switch');
    this.clearMsgs();
    try {
      const r = await this.api.setSnapshotBackup(on, Number(this.partMB()) || 128);
      this.stats.update((s) => (s ? { ...s, backup: r.backup } : s));
      this.msg.set(on ? '已开启，每小时检查一次新页面' : '已关闭，不再上传');
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  protected async savePartMB(): Promise<void> {
    const mb = Number(this.partMB());
    if (!Number.isFinite(mb) || mb < 16 || mb > 512) {
      this.err.set('单包大小需在 16–512 MB 之间');
      return;
    }
    this.busy.set('size');
    this.clearMsgs();
    try {
      const r = await this.api.setSnapshotBackup(this.bk().enabled, Math.round(mb));
      this.stats.update((s) => (s ? { ...s, backup: r.backup } : s));
      this.msg.set('已保存');
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  /** 切到「只留云端」要先确认归档开着，否则本地删了就真没了（服务端也挡）。 */
  protected async setMode(mode: 'local' | 'cloud'): Promise<void> {
    this.busy.set('mode');
    this.clearMsgs();
    try {
      const r = await this.api.setSnapshotMode(mode);
      this.stats.update((s) => (s ? { ...s, backup: r.backup } : s));
      this.msg.set(mode === 'cloud' ? '已切换：下次归档后删除本地副本' : '已切换：本地保留全量');
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  protected async runBackup(): Promise<void> {
    this.busy.set('pack');
    this.clearMsgs();
    try {
      await this.api.runSnapshotBackup();
      await this.pollJob();
    } catch (e) {
      this.err.set(errMsg(e));
      this.busy.set('');
    }
  }

  protected async exportDocs(): Promise<void> {
    this.busy.set('export');
    this.clearMsgs();
    try {
      await this.api.exportDocuments();
      await this.pollJob();
    } catch (e) {
      this.err.set(errMsg(e));
      this.busy.set('');
    }
  }

  protected async runDB(): Promise<void> {
    this.busy.set('db');
    this.clearMsgs();
    try {
      await this.api.runDBBackup();
      await this.pollJob();
    } catch (e) {
      this.err.set(errMsg(e));
      this.busy.set('');
    }
  }

  protected async restoreAll(): Promise<void> {
    this.busy.set('restore');
    this.clearMsgs();
    try {
      await this.api.restoreAllSnapshots();
      await this.pollJob();
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
      this.busy.set('');
    }
  }

  protected async changeDBEvery(v: string): Promise<void> {
    this.clearMsgs();
    try {
      const r = await this.api.setDBBackupEvery(Number(v));
      this.stats.update((s) => (s ? { ...s, backup: r.backup } : s));
      this.msg.set(Number(v) === 0 ? '已关闭自动备份' : `已改为每 ${v} 小时备份一次`);
    } catch (e) {
      this.err.set(errMsg(e));
      await this.load();
    }
  }

  /** 从云端按 manifest 取回一页，与本地比长度。 */
  protected async verify(): Promise<void> {
    const key = this.bk().last_key;
    if (!key) return;
    this.busy.set('verify');
    this.clearMsgs();
    try {
      const r = await this.api.restoreSnapshot(key);
      if (r.match === false) {
        this.err.set(`取回 ${r.bytes} 字节，与本地 ${r.local_bytes ?? 0} 字节不一致`);
        return;
      }
      this.msg.set(
        r.local_absent ? `已从云端取回 ${r.bytes} 字节（本地无副本）` : `取回 ${r.bytes} 字节，与本地一致`,
      );
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }

  /** 上传是分钟级操作，服务端异步跑，这里轮询到任务结束再显示结果。 */
  private async pollJob(): Promise<void> {
    for (let i = 0; i < 400; i++) {
      await new Promise((resolve) => setTimeout(resolve, 1500));
      await this.load();
      const job = this.stats()?.job;
      if (!job?.job) {
        const text = job?.message || '';
        if (text.includes('失败') || text.includes('中断')) this.err.set(text);
        else this.msg.set(text);
        this.busy.set('');
        return;
      }
    }
    this.err.set('任务仍在后台运行，稍后刷新页面查看结果');
    this.busy.set('');
  }
}
