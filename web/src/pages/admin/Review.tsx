import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle,
  MenuItem, Stack, TextField, Typography,
} from '@mui/material';
import { approveReview, listReview, rejectReview, type ReviewItem } from '../../api';
import AdminTable from './AdminTable';

const STATUS = [
  { value: 'pending', label: '待审核' },
  { value: 'approved', label: '已通过' },
  { value: 'rejected', label: '已拒绝' },
  { value: '', label: '全部' },
];

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
        columns={[
          { key: 'domain', label: '域名', render: (r) => <Typography variant="body2" fontWeight={600}>{r.domain}</Typography> },
          {
            key: 'url', label: '提交地址',
            render: (r) => <Typography variant="caption" color="text.secondary" noWrap sx={{ display: 'block', maxWidth: 320 }}>{r.url}</Typography>,
          },
          { key: 'category', label: '分类', width: 100, render: (r) => r.category },
          { key: 'reason', label: '理由', render: (r) => r.reason || '—' },
          {
            key: 'status', label: '状态', width: 90,
            render: (r) => (
              <Chip size="small" label={{ pending: '待审核', approved: '已通过', rejected: '已拒绝' }[r.status] ?? r.status}
                color={r.status === 'approved' ? 'primary' : r.status === 'rejected' ? 'error' : 'default'}
                variant={r.status === 'pending' ? 'outlined' : 'filled'} />
            ),
          },
          {
            key: 'time', label: '提交时间', width: 160,
            render: (r) => <Typography variant="caption" color="text.secondary">{r.created_at ? new Date(r.created_at).toLocaleString('zh-CN') : '—'}</Typography>,
          },
        ]}
        rowActions={(r) => (
          <Stack direction="row" spacing={0.5}>
            <Button size="small" disabled={r.status !== 'pending'} onClick={() => void approve(r.id)}>通过</Button>
            <Button size="small" color="error" disabled={r.status !== 'pending'} onClick={() => setRejectId(r.id)}>拒绝</Button>
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
