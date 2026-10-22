import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle,
  MenuItem, Stack, TextField, Typography,
} from '@mui/material';
import { addWhitelist, listWhitelist, toggleWhitelist, type WhitelistSite } from '../../api';
import AdminTable from './AdminTable';

const CATEGORIES = [
  { value: 'official', label: '官方文档' },
  { value: 'opensource', label: '开源项目' },
  { value: 'blog', label: '个人博客' },
  { value: 'platform', label: '平台' },
  { value: 'media', label: '媒体' },
];

export default function Whitelist() {
  const [rows, setRows] = useState<WhitelistSite[]>([]);
  const [total, setTotal] = useState(0);
  const [kw, setKw] = useState('');
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState('');
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ domain: '', category: 'official', crawl_interval: 86400 });

  const load = useCallback(async (k: string, p: number) => {
    setLoading(true);
    try {
      const r = await listWhitelist(k, p);
      setRows(r.items ?? []);
      setTotal(r.total);
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(kw, page); }, [kw, page, load]);

  const setEnabled = async (id: number, enabled: boolean) => {
    try {
      await toggleWhitelist(id, enabled);
      await load(kw, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  const add = async () => {
    try {
      await addWhitelist(form);
      setOpen(false);
      setForm({ domain: '', category: 'official', crawl_interval: 86400 });
      await load(kw, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  return (
    <>
      {msg && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setMsg('')}>{msg}</Alert>}
      <AdminTable<WhitelistSite>
        title="白名单"
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
          { key: 'category', label: '分类', width: 110, render: (r) => CATEGORIES.find((c) => c.value === r.category)?.label ?? r.category },
          {
            key: 'enabled', label: '状态', width: 90,
            render: (r) => <Chip size="small" label={r.enabled ? '启用' : '禁用'}
              color={r.enabled ? 'primary' : 'default'} variant={r.enabled ? 'filled' : 'outlined'} />,
          },
          {
            key: 'last', label: '最后抓取', width: 170,
            render: (r) => (
              <Typography variant="caption" color="text.secondary">
                {r.last_crawled_at ? new Date(r.last_crawled_at * 1000).toLocaleString('zh-CN') : '—'}
                {r.last_status ? ` · ${r.last_status}` : ''}
              </Typography>
            ),
          },
          {
            key: 'interval', label: '抓取间隔', width: 100,
            render: (r) => `${Math.round(r.crawl_interval / 3600)} 小时`,
          },
        ]}
        rowActions={(r) => (
          <Button size="small" onClick={() => void setEnabled(r.id, !r.enabled)}>
            {r.enabled ? '禁用' : '启用'}
          </Button>
        )}
        batchActions={[
          { label: '批量启用', onRun: async (ids) => { for (const id of ids) await toggleWhitelist(id, true); await load(kw, page); } },
          { label: '批量禁用', danger: true, onRun: async (ids) => { for (const id of ids) await toggleWhitelist(id, false); await load(kw, page); } },
        ]}
        toolbar={<Button variant="contained" onClick={() => setOpen(true)}>添加站点</Button>}
      />

      <Dialog open={open} onClose={() => setOpen(false)} fullWidth maxWidth="xs">
        <DialogTitle>添加白名单站点</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField label="域名" placeholder="go.dev" value={form.domain}
              onChange={(e) => setForm({ ...form, domain: e.target.value.trim().toLowerCase() })} />
            <TextField select label="分类" value={form.category}
              onChange={(e) => setForm({ ...form, category: e.target.value })}>
              {CATEGORIES.map((c) => <MenuItem key={c.value} value={c.value}>{c.label}</MenuItem>)}
            </TextField>
            <TextField label="抓取间隔（秒）" type="number" value={form.crawl_interval}
              onChange={(e) => setForm({ ...form, crawl_interval: Number(e.target.value) })} />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>取消</Button>
          <Button variant="contained" onClick={() => void add()} disabled={!form.domain}>添加</Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
