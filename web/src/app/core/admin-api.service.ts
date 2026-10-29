import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { lastValueFrom } from 'rxjs';
import type { AuthUser } from './auth.service';
import type { GeoInfo, GeoRiskRow, IPBan } from './geo-types';
import type {
  BlacklistSite,
  Branding,
  CrawlIssue,
  DimRow,
  FeatureFlag,
  LabelFeedback,
  PrecheckResult,
  ReviewItem,
  SnapshotBackup,
  SnapshotStats,
  SnapshotStorage,
  UsageRow,
  UsageTotals,
  WhitelistSite,
} from './api';

/** AI 接口配置。密钥只回传「有没有」，不回传明文。 */
export interface AIConfig {
  enabled: boolean;
  base_url: string;
  model: string;
  has_key: boolean;
  ready: boolean;
  /** 备用接口：主接口调用失败时自动顶上。 */
  backup_base_url: string;
  backup_model: string;
  backup_has_key: boolean;
  backup_ready: boolean;
  /** 第二备用接口：主接口与备用都不可用时才用。 */
  backup2_base_url: string;
  backup2_model: string;
  backup2_has_key: boolean;
  backup2_ready: boolean;
  /** AI 概览优先使用的接口：primary / backup / backup2。 */
  summary_endpoint: string;
  /** 主接口故障期内（已切到备用）为 true。 */
  failover: boolean;
}

/** 管理接口（/admin/*）。令牌由 adminTokenInterceptor 统一附加。 */
@Injectable({ providedIn: 'root' })
export class AdminApi {
  private readonly http = inject(HttpClient);

  /** 每日用量（最近 days 天，缺省 30）。只有有数据的天会返回。 */
  usage(days = 30) {
    const params = new HttpParams({ fromObject: { days: String(days) } });
    return lastValueFrom(
      this.http.get<{ days: number; rows: UsageRow[]; totals?: UsageTotals }>('/admin/usage', {
        params,
      }),
    );
  }

  /** 邮件（SMTP）配置。pass 不下发明文，只有 has_pass。 */
  getMailConfig() {
    return lastValueFrom(
      this.http.get<{
        host: string;
        port: number;
        user: string;
        from: string;
        secure: boolean;
        has_pass: boolean;
        enabled: boolean;
      }>('/admin/mail'),
    );
  }

  setMailConfig(body: {
    host: string;
    port: number;
    user: string;
    pass: string;
    from: string;
    secure: boolean;
  }) {
    return lastValueFrom(this.http.post<{ enabled: boolean }>('/admin/mail', body));
  }

  testMail(to: string) {
    return lastValueFrom(this.http.post<{ message: string }>('/admin/mail/test', { to }));
  }

  /** 人机验证配置。secret 不下发明文，只有 has_secret。 */
  getCaptchaConfig() {
    return lastValueFrom(
      this.http.get<{
        provider: string;
        site_key: string;
        endpoint: string;
        has_secret: boolean;
        ready: boolean;
      }>('/admin/captcha'),
    );
  }

  setCaptchaConfig(body: { provider: string; site_key: string; secret: string; endpoint: string }) {
    return lastValueFrom(
      this.http.post<{ provider: string; ready: boolean }>('/admin/captcha', body),
    );
  }

  /** 修正一条反馈（后台改判）。 */
  /**
   * 按反馈修「链接本身」：kind=0 改这一页的页级标签（空数组 = 这一页没有标签），
   * kind=1 改这个站点的分类。反馈记录不动——它是「用户说过什么」的历史。
   */
  fixLink(
    body:
      | { url: string; kind: 0; labels: string[] }
      | { url: string; kind: 1; category: string },
  ) {
    return lastValueFrom(
      this.http.post<{ status: string; indexed: boolean }>('/admin/feedback/fix', body),
    );
  }

  /** 高风险 IP 聚合（按关联账号数降序，支持搜索与分页）。 */
  geoRisk(kw = '', page = 1, size = 20) {
    const params = new HttpParams({ fromObject: { kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ items: GeoRiskRow[]; total: number; page: number; threshold: number }>(
        '/admin/geo/risk',
        { params },
      ),
    );
  }

  ipBans() {
    return lastValueFrom(this.http.get<{ items: IPBan[] }>('/admin/ip-bans'));
  }

  addIPBan(pattern: string, reason: string, days: number) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/ip-bans', { pattern, reason, days }));
  }

  removeIPBan(id: number) {
    return lastValueFrom(this.http.delete<{ status: string }>('/admin/ip-bans', { body: { id } }));
  }

  /** 解析单个 IP 的归属地/信誉（命中缓存则秒回）。 */
  resolveGeo(ip: string) {
    return lastValueFrom(
      this.http.post<GeoInfo>('/admin/geo/resolve', { ip }),
    );
  }

  getGeo() {
    return lastValueFrom(
      this.http.get<{ api: string; ready: boolean }>('/admin/geo'),
    );
  }

  setGeo(api: string) {
    return lastValueFrom(this.http.post<{ ready: boolean }>('/admin/geo', { api }));
  }

  /** 测试当前人机验证配置是否可用。 */
  testCaptcha() {
    return lastValueFrom(this.http.post<{ message: string }>('/admin/captcha/test', {}));
  }

  /** 账号列表（后台）。kw 非空时按邮箱模糊搜索。 */
  listUsers(page: number, size = 20, kw = '') {
    const params = new HttpParams({ fromObject: { page: String(page), size: String(size), kw } });
    return lastValueFrom(
      this.http.get<{ total: number; items: AuthUser[]; page: number }>('/admin/users', { params }),
    );
  }

  /** 批量改角色或启停。 */
  batchUsers(ids: number[], action: 'role' | 'status', value: string) {
    return lastValueFrom(
      this.http.post<{ status: string }>('/admin/users/batch', { ids, action, value }),
    );
  }

  setUserRole(id: number, role: 'admin' | 'member') {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/users/${id}/role`, { role }));
  }

  /** 某账号最近登录的设备（后台画像用）。 */
  userSessions(id: number) {
    return lastValueFrom(
      this.http.get<{
        items: { id: number; ip: string; ua: string; kind: string; created_at: number; current: boolean }[];
      }>(`/admin/users/${id}/sessions`),
    );
  }

  /** 站点信息（名称/描述/关键词/管理员邮箱）。 */
  site() {
    return lastValueFrom(this.http.get<Record<string, string>>('/admin/site'));
  }

  saveSite(body: Record<string, string>) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/site', body));
  }

  /** 反查：用过某个 IP 的账号。 */
  accountsByIP(ip: string) {
    const params = new HttpParams({ fromObject: { ip } });
    return lastValueFrom(
      this.http.get<{ items: AuthUser[] }>(`/admin/accounts-by-ip`, { params }),
    );
  }

  setUserStatus(id: number, status: number) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/users/${id}/status`, { status }));
  }

  /** 用量维度排行：API 调用来源域名与请求地区。 */
  usageDims(days = 30) {
    const params = new HttpParams({ fromObject: { days: String(days) } });
    return lastValueFrom(
      this.http.get<{
        days: number;
        origins: DimRow[];
        countries: DimRow[];
        geo_header: string;
      }>('/admin/usage/dims', { params }),
    );
  }

  // ---- 白名单 ----

  listWhitelist(kw: string, page: number, size = 10, cat = '') {
    const params = new HttpParams({
      fromObject: { kw, page: String(page), size: String(size), cat },
    });
    return lastValueFrom(
      this.http.get<{
        total: number;
        items: WhitelistSite[];
        cat_counts: Record<string, number>;
      }>('/admin/whitelist', { params }),
    );
  }

  addWhitelist(body: { domain: string; category: string; crawl_interval?: number }) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/whitelist', body));
  }

  /**
   * 批量改抓取间隔。interval=0 表示按各站分类重算（新闻 30 分钟、官方站 1 天…），
   * >0 表示统一设为该秒数（「全站退回 1 天」这条退路就靠它）。
   */
  resetIntervals(ids: number[], interval = 0) {
    return lastValueFrom(
      this.http.post<{ changed: number; interval: number }>('/admin/whitelist/intervals', {
        ids,
        interval,
      }),
    );
  }

  toggleWhitelist(id: number, enabled: boolean) {
    return lastValueFrom(
      this.http.post<{ status: string }>(`/admin/whitelist/${id}/toggle`, { enabled }),
    );
  }

  // ---- 抓取异常名单 ----

  /** counts 是各类型总数（不受 kind 筛选影响），页面拿它显示「无法访问 3」。 */
  listCrawlIssues(kind: string, kw: string, page: number, size = 10) {
    const params = new HttpParams({
      fromObject: { kind, kw, page: String(page), size: String(size) },
    });
    return lastValueFrom(
      this.http.get<{ total: number; items: CrawlIssue[]; counts: Record<string, number> }>(
        '/admin/issues',
        { params },
      ),
    );
  }

  /** 重新抓取：服务端异步执行（单站几十秒），返回即已排队。 */
  recrawlIssues(domains: string[]) {
    return lastValueFrom(
      this.http.post<{ status: string; count: number }>('/admin/issues/recrawl', { domains }),
    );
  }

  /** 移出异常名单：下一轮抓取仍失败会自动回来，不是删除数据。 */
  clearCrawlIssues(domains: string[]) {
    return lastValueFrom(
      this.http.post<{ status: string; cleared: number }>('/admin/issues/clear', { domains }),
    );
  }

  // ---- 黑名单 ----

  listBlacklist(kw: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: BlacklistSite[] }>('/admin/blacklist', { params }),
    );
  }

  addBlacklist(body: { domain: string; reason: string }) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/blacklist', body));
  }

  removeBlacklist(id: number) {
    return lastValueFrom(this.http.delete<{ status: string }>(`/admin/blacklist/${id}`));
  }

  // ---- 审核队列 ----

  listReview(status: string, kw: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { status, kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: ReviewItem[] }>('/admin/review', { params }),
    );
  }

  /** 队列里存的预检是历史 JSON；提交时后端已同步预检过一次，这里可随时重跑。 */
  precheckReview(id: number) {
    return lastValueFrom(
      this.http.post<{ id: number; precheck: PrecheckResult; blacklisted: boolean }>(
        `/admin/review/${id}/precheck`,
        {},
      ),
    );
  }

  approveReview(id: number) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/review/${id}/approve`, {}));
  }

  rejectReview(id: number, reason: string) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/review/${id}/reject`, { reason }));
  }

  // ---- 预检（管理员直提前手动触发） ----

  precheck(url: string) {
    return lastValueFrom(
      this.http.post<{ precheck: PrecheckResult; blacklisted: boolean }>('/admin/precheck', { url }),
    );
  }

  // ---- 用户反馈（标签 + 分类） ----

  listLabelFeedback(kw: string, verdict: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { kw, verdict, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: LabelFeedback[]; by_label: Record<string, number> | null }>(
        '/admin/label-feedback',
        { params },
      ),
    );
  }

  deleteLabelFeedback(ids: number[]) {
    return lastValueFrom(
      this.http.post<{ deleted: number }>('/admin/label-feedback/delete', { ids }),
    );
  }

  // ---- 功能开关：标题与说明由服务端下发，前端不维护开关清单 ----

  listFeatures() {
    return lastValueFrom(this.http.get<{ features: FeatureFlag[] }>('/admin/settings'));
  }

  setFeature(key: string, enabled: boolean) {
    return lastValueFrom(
      this.http.post<{ key: string; enabled: boolean }>('/admin/settings/feature', { key, enabled }),
    );
  }

  /** AI 站点分类的接口配置（key 不下发明文，只有 has_key）。 */
  aiConfig() {
    return lastValueFrom(this.http.get<AIConfig>('/admin/ai'));
  }

  /** api_key / backup_api_key / backup2_api_key 留空表示不修改。 */
  saveAIConfig(body: {
    base_url: string;
    model: string;
    api_key: string;
    backup_base_url: string;
    backup_model: string;
    backup_api_key: string;
    backup2_base_url: string;
    backup2_model: string;
    backup2_api_key: string;
    summary_endpoint: string;
  }) {
    return lastValueFrom(
      this.http.post<{ ready: boolean; summary_endpoint: string }>('/admin/ai', body),
    );
  }

  testAI() {
    return lastValueFrom(
      this.http.post<{ category: string; label: string; model: string; message: string }>(
        '/admin/ai/test',
        {},
      ),
    );
  }

  /** 对选中的白名单站点跑一次 AI 分类（单次上限 20 个）。 */
  classifySites(ids: number[]) {
    return lastValueFrom(
      this.http.post<{
        results: {
          id: number;
          domain: string;
          category: string;
          label: string;
          labels?: string[];
          label_texts?: string[];
          error?: string;
        }[];
        ok: number;
        failed: number;
      }>('/admin/ai/classify', { ids }),
    );
  }

  /** 原文快照：开关状态 + 占用 + 孤儿数。 */
  snapshotStats() {
    return lastValueFrom(this.http.get<SnapshotStats>('/admin/snapshot'));
  }

  /** 开关只管「以后还存不存」，已有快照保留。 */
  setSnapshot(enabled: boolean) {
    return lastValueFrom(this.http.post<{ enabled: boolean }>('/admin/snapshot', { enabled }));
  }

  /** 保存 S3/B2 连接配置；密钥留空表示不修改。本阶段不改读写路径。 */
  saveSnapshotStorage(body: {
    endpoint: string;
    bucket: string;
    region: string;
    prefix: string;
    access_key: string;
    secret_key: string;
    path_style: boolean;
    no_proxy: boolean;
  }) {
    return lastValueFrom(
      this.http.post<{ status: string; storage: SnapshotStorage }>('/admin/snapshot/storage', body),
    );
  }

  /** 测试 S3/B2 连接（未保存的值也可测；密钥留空用已保存值）。 */
  testSnapshotStorage(body: {
    endpoint: string;
    bucket: string;
    region: string;
    prefix: string;
    access_key: string;
    secret_key: string;
    path_style: boolean;
    no_proxy: boolean;
  }) {
    return lastValueFrom(
      this.http.post<{ ok: boolean; message: string }>('/admin/snapshot/storage/test', body),
    );
  }

  /** 清理：mode='orphan' 只删索引里已不存在的页，'all' 全清。 */
  cleanupSnapshot(mode: 'orphan' | 'all') {
    return lastValueFrom(
      this.http.post<{
        mode: string;
        removed: number;
        truncated: boolean;
        bytes_before: number;
        bytes_after: number;
        freed: number;
      }>('/admin/snapshot/cleanup', { mode }),
    );
  }

  /** 云冷备开关与单包大小。开启前服务端会校验桶配置是否完整。 */
  setSnapshotBackup(enabled: boolean, partMb: number) {
    return lastValueFrom(
      this.http.post<{ enabled: boolean; backup: SnapshotBackup }>('/admin/snapshot/backup', {
        enabled,
        part_mb: partMb,
      }),
    );
  }

  /** 立即打包上传尚未归档的页面；返回 202，任务状态从 snapshotStats().job 读。 */
  runSnapshotBackup() {
    return lastValueFrom(
      this.http.post<{ status: string; backup: SnapshotBackup }>('/admin/snapshot/backup/run', {}),
    );
  }

  /** 上传站点图标（PNG / ICO / SVG / WebP，≤512KB）。 */
  uploadBrandIcon(file: File) {
    const body = new FormData();
    body.append('file', file);
    // 不设 Content-Type：交给浏览器带 boundary，手写会让服务端解析失败。
    return lastValueFrom(
      this.http.post<{ branding: Branding }>('/admin/branding/icon', body),
    );
  }

  /** 恢复自带图标。 */
  clearBrandIcon() {
    return lastValueFrom(
      this.http.post<{ branding: Branding }>('/admin/branding/icon/clear', {}),
    );
  }

  /** 整库备份的频率（小时，0=关闭）。 */
  setDBBackupEvery(hours: number) {
    return lastValueFrom(
      this.http.post<{ backup: SnapshotBackup }>('/admin/snapshot/db', { every_hours: hours }),
    );
  }

  /** 立刻备份一份整库（异步，进度看 snapshotStats().job）。 */
  runDBBackup() {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/snapshot/db/run', {}));
  }

  /** 把云端归档的页面全部取回本地（本地已有的跳过）。 */
  restoreAllSnapshots() {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/snapshot/restore-all', {}));
  }

  /** 快照存放方式：local = 本地留全量；cloud = 上传成功后删本地、读取回源。 */
  setSnapshotMode(mode: 'local' | 'cloud') {
    return lastValueFrom(
      this.http.post<{ mode: string; backup: SnapshotBackup }>('/admin/snapshot/mode', { mode }),
    );
  }

  /** 文档元数据导出为 JSONL.gz；同样返回 202，进度看 snapshotStats().job。 */
  exportDocuments() {
    return lastValueFrom(
      this.http.post<{ status: string; backup: SnapshotBackup }>('/admin/snapshot/export', {}),
    );
  }

  /** 按 manifest 从云归档读回一页，验证偏移量与内容长度。 */
  restoreSnapshot(key: string) {
    return lastValueFrom(
      this.http.post<{
        key: string;
        bytes: number;
        local_bytes?: number;
        match?: boolean;
        local_absent?: boolean;
      }>(
        '/admin/snapshot/restore',
        { key },
      ),
    );
  }
}
