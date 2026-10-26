import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Box, Chip, MenuItem, Stack, TextField, Typography,
} from '@mui/material';
import { deleteLabelFeedback, listLabelFeedback, type LabelFeedback } from '../../api';
import { labelText } from '../../components/LabelFeedbackDialog';
import AdminTable from './AdminTable';

// 标签误判反馈列表。规划书红线要求概率性标签「可解释 + 可反馈」，
// 这个页面的作用是评估规则精确率：某个标签误判数一直涨，就该收紧它的判定条件。
const VERDICTS = [
  { value: '', label: '全部' },
  { value: 'false_positive', label: '用户判为误判' },
  { value: 'confirmed', label: '用户确认正确' },
];

export default function Labels() {
  const [rows, setRows] = useState<LabelFeedback[]>([]);
  const [total, setTotal] = useState(0);
  const [byLabel, setByLabel] = useState<Record<string, number>>({});
  const [kw, setKw] = useState('');
  const [verdict, setVerdict] = useState('false_positive');
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState('');

  const load = useCallback(async (k: string, v: string, p: number) => {
    setLoading(true);
    try {
      const r = await listLabelFeedback(k, v, p);
      setRows(r.items ?? []);
      setTotal(r.total);
      setByLabel(r.by_label ?? {});
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(kw, verdict, page); }, [kw, verdict, page, load]);

  const removeMany = async (ids: number[]) => {
    try {
      await deleteLabelFeedback(ids);
      await load(kw, verdict, page);
    } catch (e) {
      setMsg((e as Error).message);
    }
  };

  const summary = Object.keys(byLabel).length > 0 && (
    <Stack direction="row" spacing={0.75} sx={{ mb: 2, flexWrap: 'wrap' }} useFlexGap alignItems="center">
      <Typography variant="caption" color="text.secondary">累计误判：</Typography>
      {Object.entries(byLabel).sort((a, b) => b[1] - a[1]).map(([label, n]) => (
        <Chip key={label} size="small"
          label={`${labelText(label)} ${n}`}
          sx={{ height: 22, bgcolor: 'rgba(47,79,79,0.07)', color: 'primary.main' }} />
      ))}
    </Stack>
  );

  return (
    <>
      {msg && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setMsg('')}>{msg}</Alert>}
      <Box sx={{ mb: 1 }}>
        <Typography variant="body2" color="text.secondary">
          标签在索引期按规则自动判定，误判会随反馈累积暴露出来。这里的数据只用于调整规则，
          不会立刻撤销单条页面的标签。
        </Typography>
      </Box>
      {summary}
      <AdminTable<LabelFeedback>
        title="标签反馈"
        rows={rows}
        total={total}
        page={page}
        size={10}
        kw={kw}
        loading={loading}
        onKw={(k) => { setKw(k); setPage(1); }}
        onPage={setPage}
        columns={[
          {
            key: 'url', label: '页面',
            render: (r) => (
              <Box>
                <Typography variant="body2" component="a" href={r.url} target="_blank" rel="noreferrer"
                  sx={{ color: 'primary.main', textDecoration: 'none' }}>
                  {r.domain}
                </Typography>
                <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
                  {r.url.length > 64 ? `${r.url.slice(0, 64)}…` : r.url}
                </Typography>
              </Box>
            ),
          },
          {
            key: 'label', label: '标签', width: 120,
            render: (r) => <Chip size="small" label={labelText(r.label)} variant="outlined"
              sx={{ height: 22, borderColor: '#E0D6C4' }} />,
          },
          {
            key: 'verdict', label: '结论', width: 110,
            render: (r) => (
              <Chip size="small" sx={{ height: 22 }}
                color={r.verdict === 'false_positive' ? 'error' : 'success'}
                variant={r.verdict === 'false_positive' ? 'filled' : 'outlined'}
                label={r.verdict === 'false_positive' ? '误判' : '正确'} />
            ),
          },
          {
            key: 'note', label: '用户说明',
            render: (r) => (
              <Typography variant="body2" color={r.note ? 'text.primary' : 'text.secondary'}>
                {r.note || '—'}
              </Typography>
            ),
          },
          {
            key: 'updated', label: '时间', width: 130,
            render: (r) => (
              <Typography variant="caption" color="text.secondary">
                {new Date(r.updated_at).toLocaleDateString('zh-CN')}
              </Typography>
            ),
          },
        ]}
        batchActions={[
          { label: '批量删除', danger: true, onRun: (ids) => removeMany(ids) },
        ]}
        toolbar={
          // 不用 TextField 的 label：小尺寸下标签会压在边框上，读起来别扭。
          // 用前置说明文字更干净，选项本身就是自解释的中文。
          <Stack direction="row" spacing={1} alignItems="center">
            <Typography variant="caption" color="text.secondary" sx={{ whiteSpace: 'nowrap' }}>
              结论
            </Typography>
            <TextField
              select
              size="small"
              value={verdict}
              onChange={(e) => { setVerdict(e.target.value); setPage(1); }}
              sx={{ width: 150 }}
            >
              {VERDICTS.map((v) => (
                <MenuItem key={v.value} value={v.value}>{v.label}</MenuItem>
              ))}
            </TextField>
          </Stack>
        }
        footer="只统计「误判」用于规则调整；「确认正确」用来反向验证规则没有大面积误伤。"
      />
    </>
  );
}
