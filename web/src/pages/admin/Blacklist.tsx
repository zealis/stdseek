import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, Stack, TextField, Typography,
} from '@mui/material';
import { addBlacklist, listBlacklist, removeBlacklist, type BlacklistSite } from '../../api';
import AdminTable from './AdminTable';

export default function Blacklist() {
  const [rows, setRows] = useState<BlacklistSite[]>([]);
  const [total, setTotal] = useState(0);
  const [kw, setKw] = useState('');
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState('');
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ domain: '', reason: '' });

  const load = useCallback(async (k: string, p: number) => {
    setLoading(true);
    try {
      const r = await listBlacklist(k, p);
      setRows(r.items ?? []);
      setTotal(r.total);
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(kw, page); }, [kw, page, load]);

  const add = async () => {
    try {
      await addBlacklist(form);
      setOpen(false);
      setForm({ domain: '', reason: '' });
      await load(kw, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  const remove = async (id: number) => {
    try {
      await removeBlacklist(id);
      await load(kw, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  return (
    <>
      {msg && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setMsg('')}>{msg}</Alert>}
      <AdminTable<BlacklistSite>
        title="黑名单"
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
          { key: 'reason', label: '理由', render: (r) => r.reason || '—' },
          { key: 'reject', label: '提交被拒', width: 100, render: (r) => r.reject_count },
          { key: 'blocked', label: '爬取拦截', width: 100, render: (r) => r.blocked_count },
        ]}
        rowActions={(r) => <Button size="small" color="error" onClick={() => void remove(r.id)}>移除</Button>}
        batchActions={[
          {
            label: '批量移除', danger: true,
            onRun: async (ids) => { for (const id of ids) await removeBlacklist(id); await load(kw, page); },
          },
        ]}
        toolbar={<Button variant="contained" color="error" onClick={() => setOpen(true)}>加入黑名单</Button>}
      />

      <Dialog open={open} onClose={() => setOpen(false)} fullWidth maxWidth="xs">
        <DialogTitle>加入黑名单</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField label="域名" placeholder="spam.example" value={form.domain}
              onChange={(e) => setForm({ ...form, domain: e.target.value.trim().toLowerCase() })} />
            <TextField label="原因" value={form.reason} multiline minRows={2}
              onChange={(e) => setForm({ ...form, reason: e.target.value })} />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>取消</Button>
          <Button variant="contained" color="error" onClick={() => void add()} disabled={!form.domain}>确认</Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
