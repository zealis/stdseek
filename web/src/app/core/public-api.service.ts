import { HttpClient, HttpParams } from '@angular/common/http';
import type { CaptchaChallenge } from './auth.service';
import { Injectable, inject } from '@angular/core';
import { lastValueFrom } from 'rxjs';
import type {
  Hit,
  PublicUsageRow,
  SearchResp,
  Status,
  SubmissionStatus,
  SummaryResp,
} from './api';

/**
 * 公开接口（/api/*）。搜索、提交、反馈、快照、摘要、运行状态。
 * 全部同源相对路径，单二进制部署时由 Go 直接提供。
 */
@Injectable({ providedIn: 'root' })
export class PublicApi {
  private readonly http = inject(HttpClient);

  search(
    q: string,
    opts: { exclude?: string[]; since?: number; page?: number; size?: number; post?: boolean } = {},
  ) {
    // X-Voidxo-Client 让服务端区分「网页搜索」和「API 调用」，
    // 每日用量的 API 请求数靠它统计。
    const headers = { 'X-Voidxo-Client': 'web' };
    if (opts.post) {
      // POST：搜索词不进地址栏。body 与 GET 的 query 参数一一对应。
      return lastValueFrom(
        this.http.post<SearchResp>(
          '/api/search',
          {
            q,
            exclude: opts.exclude?.join(',') ?? '',
            since: opts.since ? String(opts.since) : '',
            page: opts.page ?? 1,
            size: opts.size ?? 10,
          },
          { headers },
        ),
      );
    }
    let params = new HttpParams({ fromObject: { q, page: String(opts.page ?? 1), size: String(opts.size ?? 10) } });
    if (opts.exclude?.length) params = params.set('no', opts.exclude.join(','));
    if (opts.since) params = params.set('since', String(opts.since));
    return lastValueFrom(this.http.get<SearchResp>('/api/search', { params, headers }));
  }

  /** 隐私政策的生效日与修订日。 */
  privacy() {
    return lastValueFrom(
      this.http.get<{ created_at: number; updated_at: number }>('/api/privacy'),
    );
  }

  /** 维基百科摘要（单个词，懒加载；无条目时后端 404）。 */
  wiki(q: string) {
    const params = new HttpParams({ fromObject: { q } });
    return lastValueFrom(
      this.http.get<{ q: string; title: string; extract: string; url: string }>('/api/wiki', { params }),
    );
  }

  /** 用户提交站点（公开接口，进审核队列）。返回的 ticket 是查询凭证。 */
  submitSite(body: {
    url: string;
    category: string;
    reason?: string;
    captcha?: CaptchaChallenge | null;
    answer?: string;
    turnstile_token?: string;
  }) {
    return lastValueFrom(
      this.http.post<{ status: string; ticket: string }>('/api/submit', body),
    );
  }

  getSubmission(ticket: string) {
    const params = new HttpParams({ fromObject: { ticket } });
    return lastValueFrom(this.http.get<SubmissionStatus>('/api/submission', { params }));
  }

  /** 标签误判反馈：verdict 为 false_positive（打错了）或 confirmed（打得对）。 */
  submitLabelFeedback(body: {
    url: string;
    /** 批量：人机验证的 token 一次性，逐条提交时第二条必定失败 */
    labels: string[];
    verdict: 'false_positive' | 'confirmed';
    note?: string;
    captcha?: CaptchaChallenge | null;
    answer?: string;
    turnstile_token?: string;
  }) {
    return lastValueFrom(this.http.post<{ status: string }>('/api/label-feedback', body));
  }

  /** 抓取时保存的原文快照（证据链）。返回的 html 是不可信内容，只能进 sandbox iframe。 */
  pageSnapshot(url: string) {
    const params = new HttpParams({ fromObject: { url } });
    return lastValueFrom(
      this.http.get<{ url: string; key: string; size: number; html: string }>('/api/page', { params }),
    );
  }

  /**
   * 按需补抓单个页面：只抓这一页，不扩链，只对白名单站点开放。
   * indexed 为本次入索引的条数（0 表示抓到了但没进索引，比如正文过短）。
   */
  fetchPage(url: string) {
    return lastValueFrom(
      this.http.post<{ status: string; indexed: number }>('/api/fetch-page', { url }),
    );
  }

  /** 搜索词补全。失败时后端也返回空列表，不会让补全打断输入。 */
  suggest(q: string) {
    const params = new HttpParams({ fromObject: { q } });
    return lastValueFrom(
      this.http.get<{ q: string; suggestions: string[] }>('/api/suggest', { params }),
    );
  }

  /** 每日检索用量（公开）：返回最近 days 天，只含检索维度。 */
  usage(days = 30) {
    const params = new HttpParams({ fromObject: { days: String(days) } });
    return lastValueFrom(
      this.http.get<{ days: number; rows: PublicUsageRow[] }>('/api/usage', { params }),
    );
  }

  /** 公开功能开关：前台据此决定是否渲染原文摘要入口。失败按「全关」处理。 */
  publicFeatures() {
    return lastValueFrom(this.http.get<{ features: Record<string, boolean> }>('/api/features'))
      .catch(() => ({ features: {} as Record<string, boolean> }));
  }

  /** 首页运行状态；失败按空库处理，保证首屏总能渲染。 */
  status() {
    return lastValueFrom(this.http.get<Status>('/api/status'))
      .catch(() => ({ documents: 0, sites: 0, features: {} as Record<string, boolean> }));
  }

  /** 证据链摘要：conclusions 为空或 empty=true 时 UI 显示「未验证 / 未找到可靠来源」。 */
  summarize(url: string, q: string) {
    return lastValueFrom(this.http.post<SummaryResp>('/api/summary', { url, q }));
  }
}

export type { Hit };
