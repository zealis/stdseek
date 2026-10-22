import { useCallback, useEffect, useState, type ReactNode } from 'react';
import {
  Alert, Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle,
  Divider, MenuItem, Stack, TextField, Typography,
} from '@mui/material';
import {
  approveReview, listReview, precheckReview, rejectReview,
  type QueuePrecheck, type ReviewItem,
} from '../../api';
import AdminTable from './AdminTable';

const STATUS = [
  { value: 'pending', label: '待审核' },
  { value: 'approved', label: '已通过' },
  { value: 'rejected', label: '已拒绝' },
  { value: '', label: '全部' },
];

// 队列里的 precheck 是历史 JSON：旧条目可能只有 blacklist_history，解析失败按「未预检」处理。
function parsePrecheck(raw: string): QueuePrecheck | null {
  if (!raw) return null;
  try {
    const v: unknown = JSON.parse(raw);
    return typeof v === 'object' && v !== null ? (v as QueuePrecheck) : null;
  } catch {
    return null;
  }
}

type Badge = { label: string; color: 'primary' | 'warning' | 'error' | 'default'; filled: boolean };

// 行内只展示一条最该被看到的结论，完整字段在详情弹窗
function badgeOf(p: QueuePrecheck): Badge {
  if (p.blacklist_history) return { label: '黑名单历史', color: 'error', filled: true };
  if (p.error) return { label: '预检失败', color: 'warning', filled: false };
  if (p.reachable === false) {
    return { label: p.status_code ? `不可达 ${p.status_code}` : '不可达', color: 'warning', filled: false };
  }
  if (p.robots_allowed === false) return { label: 'robots 禁止', color: 'warning', filled: false };
  if (p.reachable) return { label: `可达 ${p.status_code ?? ''}`.trim(), color: 'primary', filled: true };
  return { label: '已预检', color: 'default', filled: false };
}

function robotsText(p: QueuePrecheck): string {
  if (p.robots_found === false) return '无 robots.txt（视为允许）';
  if (p.robots_allowed === false) return '禁止抓取';
  if (p.robots_allowed) return '允许';
  return '—';
}

// 短时间格式：表格里不需要年份秒，MM-DD HH:mm 足够
function shortTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 行内按钮统一收窄：默认 min-width 64px 会让三按钮操作列溢出
const actionSx = { minWidth: 0, px: 1.25 };

function Field({ label, value }: { label: string; value: ReactNode }) {
  return (
    <Stack direction="row" spacing={1} sx={{ py: 0.75 }}>
      <Typography variant="body2" color="text.secondary" sx={{ width: 96, flexShrink: 0 }}>{label}</Typography>
      <Box sx={{ minWidth: 0, wordBreak: 'break-all' }}>
        <Typography variant="body2" component="div">{value}</Typography>
      </Box>
    </Stack>
  );
}

export default function Review() {
  const [rows, setRows] = useState<ReviewItem[]>([]);
  const [total, setTotal] = useState(0);
  const [kw, setKw] = useState('');
  const [status, setStatus] = useState('pending');
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState('');
  const [rejectId, setRejectId] = useState<number | null>(null);
  const [reason, setReason] = useState('');
  const [detail, setDetail] = useState<{ item: ReviewItem; pre: QueuePrecheck | null } | null>(null);
  const [checking, setChecking] = useState<number | null>(null);

  const load = useCallback(async (k: string, st: string, p: number) => {
    setLoading(true);
    try {
      const r = await listReview(st, k, p);
      setRows(r.items ?? []);
      setTotal(r.total);
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(kw, status, page); }, [kw, status, page, load]);

  const approve = async (id: number) => {
    try {
      await approveReview(id);
      setDetail(null);
      await load(kw, status, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  const reject = async () => {
    if (rejectId == null) return;
    try {
      await rejectReview(rejectId, reason);
      setRejectId(null);
      setReason('');
      await load(kw, status, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  // 重新预检：结果写回队列，同时更新列表行与详情弹窗（避免整页闪烁）
  const rerun = async (row: ReviewItem) => {
    setChecking(row.id);
    try {
      const r = await precheckReview(row.id);
      const pre = r.precheck as QueuePrecheck;
      const precheckAt = pre.checked_at ?? Math.floor(Date.now() / 1000);
      setRows((prev) => prev.map((x) => (x.id === row.id
        ? { ...x, precheck: JSON.stringify(pre), precheck_at: precheckAt }
        : x)));
      setDetail((d) => (d && d.item.id === row.id
        ? { item: { ...d.item, precheck: JSON.stringify(pre), precheck_at: precheckAt }, pre }
        : d));
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setChecking(null);
    }
  };

  const detailPre = detail?.pre ?? null;
  const detailBadge = detailPre ? badgeOf(detailPre) : null;
  const checkedAt = detailPre?.checked_at ?? detail?.item.precheck_at ?? null;

  return (
    <>
      {msg && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setMsg('')}>{msg}</Alert>}
      <AdminTable<ReviewItem>
        title="审核队列"
        rows={rows}
        total={total}
        page={page}
        size={10}
        kw={kw}
        loading={loading}
        onKw={(k) => { setKw(k); setPage(1); }}
        onPage={setPage}
        footer="提交时自动预检并随条目保存；站点恢复或规则调整后点「预检」刷新。通过进白名单，拒绝进黑名单。"
        fixedLayout
        actionsWidth="16%"
        columns={[
          {
            key: 'domain', label: '域名', width: '16%',
            render: (r) => (
              <Box>
                <Typography variant="body2" fontWeight={600} noWrap sx={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {r.domain}
                </Typography>
                <Typography variant="caption" color="text.secondary">{r.category}</Typography>
              </Box>
            ),
          },
          {
            key: 'url', label: '提交地址', width: '17%',
            render: (r) => (
              <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {r.url}
              </Typography>
            ),
          },
          {
            key: 'reason', label: '理由', width: '19%',
            render: (r) => (
              <Typography variant="body2" sx={{
                display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden',
              }}>
                {r.reason || '—'}
              </Typography>
            ),
          },
          {
            key: 'precheck', label: '预检', width: '10%',
            render: (r) => {
              const pre = parsePrecheck(r.precheck);
              if (!pre) {
                return (
                  <Typography variant="caption" color="text.secondary">未预检</Typography>
                );
              }
              const b = badgeOf(pre);
              return (
                <Chip
                  size="small"
                  label={b.label}
                  color={b.color}
                  variant={b.filled ? 'filled' : 'outlined'}
                  onClick={() => setDetail({ item: r, pre })}
                  sx={{ cursor: 'pointer' }}
                />
              );
            },
          },
          {
            key: 'status', label: '状态', width: '8%',
            render: (r) => (
              <Chip size="small" label={{ pending: '待审核', approved: '已通过', rejected: '已拒绝' }[r.status] ?? r.status}
                color={r.status === 'approved' ? 'primary' : r.status === 'rejected' ? 'error' : 'default'}
                variant={r.status === 'pending' ? 'outlined' : 'filled'} />
            ),
          },
          {
            key: 'time', label: '提交时间', width: '9%',
            render: (r) => <Typography variant="caption" color="text.secondary">{shortTime(r.created_at)}</Typography>,
          },
        ]}
        rowActions={(r) => (
          <Stack direction="row" spacing={0.5}>
            {/* MUI Button 默认 min-width 64px，三个按钮会把操作列撑出横向滚动，这里收窄 */}
            <Button size="small" sx={actionSx} disabled={r.status !== 'pending'} onClick={() => void approve(r.id)}>通过</Button>
            <Button size="small" sx={actionSx} color="error" disabled={r.status !== 'pending'} onClick={() => setRejectId(r.id)}>拒绝</Button>
            <Button size="small" sx={actionSx} disabled={checking === r.id} onClick={() => void rerun(r)}>
              {checking === r.id ? '预检中' : '预检'}
            </Button>
          </Stack>
        )}
        batchActions={[
          {
            label: '批量通过',
            onRun: async (ids) => {
              for (const id of ids) {
                try { await approveReview(id); } catch { /* 已审核的条目跳过 */ }
              }
              await load(kw, status, page);
            },
          },
        ]}
        toolbar={
          <TextField select size="small" value={status} sx={{ width: 120 }}
            onChange={(e) => { setStatus(e.target.value); setPage(1); }}>
            {STATUS.map((s) => <MenuItem key={s.value} value={s.value}>{s.label}</MenuItem>)}
          </TextField>
        }
      />

      <Dialog open={detail != null} onClose={() => setDetail(null)} fullWidth maxWidth="sm">
        <DialogTitle>预检详情</DialogTitle>
        <DialogContent>
          {detail && (
            <Stack divider={<Divider flexItem />}>
              {detailBadge && (
                <Box sx={{ pb: 1 }}>
                  <Chip size="small" label={detailBadge.label} color={detailBadge.color}
                    variant={detailBadge.filled ? 'filled' : 'outlined'} />
                </Box>
              )}
              <Field label="域名" value={detail.item.domain} />
              <Field label="提交地址" value={detail.item.url} />
              <Field label="最终地址" value={detailPre?.final_url || '—'} />
              <Field label="可达性" value={detailPre ? (detailPre.reachable ? '可达' : '不可达') : '未预检'} />
              <Field label="状态码" value={detailPre?.status_code || '—'} />
              <Field label="内容类型" value={detailPre?.content_type || '—'} />
              <Field label="HTML 页面" value={detailPre ? (detailPre.is_html ? '是' : '否') : '—'} />
              <Field label="robots" value={detailPre ? robotsText(detailPre) : '—'} />
              <Field label="黑名单历史" value={detailPre?.blacklist_history ? '命中（该域名曾被拒绝）' : '无'} />
              <Field label="检查时间" value={checkedAt ? new Date(checkedAt * 1000).toLocaleString('zh-CN') : '—'} />
              {detailPre?.error && (
                <Field label="错误" value={<Typography variant="body2" color="error">{detailPre.error}</Typography>} />
              )}
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          {detail && (
            <Button disabled={checking === detail.item.id} onClick={() => void rerun(detail.item)}>
              {checking === detail.item.id ? '预检中…' : '重新预检'}
            </Button>
          )}
          <Button onClick={() => setDetail(null)}>关闭</Button>
        </DialogActions>
      </Dialog>

      <Dialog open={rejectId != null} onClose={() => setRejectId(null)} fullWidth maxWidth="xs">
        <DialogTitle>拒绝提交</DialogTitle>
        <DialogContent>
          <TextField fullWidth label="拒绝原因" value={reason} multiline minRows={2}
            onChange={(e) => setReason(e.target.value)} sx={{ mt: 1 }} />
          <Typography variant="caption" color="text.secondary">
            拒绝后域名会进入黑名单，爬虫将永久跳过。
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRejectId(null)}>取消</Button>
          <Button color="error" variant="contained" onClick={() => void reject()}>确认拒绝</Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
