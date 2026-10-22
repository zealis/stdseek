import { useMemo, useState, type ReactNode } from 'react';
import {
  Box, Button, Checkbox, CircularProgress, Menu, MenuItem, Pagination, Paper, Stack,
  Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField, Typography,
} from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';

export type Column<T> = {
  key: string;
  label: string;
  width?: number | string;
  render: (row: T) => ReactNode;
};

export type BatchAction = {
  label: string;
  danger?: boolean;
  onRun: (ids: number[]) => void | Promise<void>;
};

type Props<T extends { id: number }> = {
  title: string;
  columns: Column<T>[];
  rows: T[];
  total: number;
  page: number;
  size: number;
  kw: string;
  loading?: boolean;
  onKw: (kw: string) => void;
  onPage: (page: number) => void;
  batchActions?: BatchAction[];
  rowActions?: (row: T) => ReactNode;
  toolbar?: ReactNode;
};

// 通用后台表格：统一搜索栏 + 多选 + 批量操作（下拉收纳）+ 分页（规划书后台规范）
export default function AdminTable<T extends { id: number }>(props: Props<T>) {
  const {
    title, columns, rows, total, page, size, kw, loading,
    onKw, onPage, batchActions = [], rowActions, toolbar,
  } = props;

  const [selected, setSelected] = useState<number[]>([]);
  const [kwInput, setKwInput] = useState(kw);
  const [anchor, setAnchor] = useState<null | HTMLElement>(null);

  const pages = Math.max(1, Math.ceil(total / size));
  const allChecked = rows.length > 0 && selected.length === rows.length;
  const indeterminate = selected.length > 0 && !allChecked;

  const selectedSet = useMemo(() => new Set(selected), [selected]);

  const toggleAll = () => setSelected(allChecked ? [] : rows.map((r) => r.id));
  const toggleOne = (id: number) => {
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ mb: 2 }} alignItems={{ sm: 'center' }}>
        <Typography variant="h6" sx={{ flexGrow: 1 }}>{title}</Typography>
        <TextField
          placeholder="搜索域名/关键词"
          value={kwInput}
          onChange={(e) => setKwInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') onKw(kwInput.trim()); }}
          sx={{ width: { xs: '100%', sm: 240 } }}
        />
        <Button variant="outlined" onClick={() => onKw(kwInput.trim())}>搜索</Button>
        {toolbar}
        {batchActions.length > 0 && (
          <>
            <Button
              variant="outlined"
              disabled={selected.length === 0}
              onClick={(e) => setAnchor(e.currentTarget)}
            >
              批量操作{selected.length ? `（${selected.length}）` : ''}
            </Button>
            <Menu anchorEl={anchor} open={Boolean(anchor)} onClose={() => setAnchor(null)}>
              {batchActions.map((a) => (
                <MenuItem
                  key={a.label}
                  sx={a.danger ? { color: 'error.main' } : undefined}
                  onClick={async () => {
                    setAnchor(null);
                    await a.onRun(selected);
                    setSelected([]);
                  }}
                >
                  {a.label}
                </MenuItem>
              ))}
            </Menu>
          </>
        )}
      </Stack>

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}><CircularProgress size={26} /></Box>
      ) : (
        <TableContainer component={Paper} variant="outlined" sx={{ bgcolor: 'background.paper' }}>
          <Table size="small">
            <TableHead>
              <TableRow>
                {batchActions.length > 0 && (
                  <TableCell padding="checkbox">
                    <Checkbox
                      checked={allChecked}
                      indeterminate={indeterminate}
                      onChange={toggleAll}
                      inputProps={{ 'aria-label': '全选' }}
                    />
                  </TableCell>
                )}
                {columns.map((c) => (
                  <TableCell key={c.key} sx={{ width: c.width, fontWeight: 600 }}>{c.label}</TableCell>
                ))}
                {rowActions && <TableCell sx={{ width: 160 }}>操作</TableCell>}
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.length === 0 && (
                <TableRow>
                  <TableCell colSpan={columns.length + (batchActions.length ? 1 : 0) + (rowActions ? 1 : 0)}>
                    <Typography variant="body2" color="text.secondary" sx={{ py: 3, textAlign: 'center' }}>
                      暂无数据
                    </Typography>
                  </TableCell>
                </TableRow>
              )}
              {rows.map((row) => (
                <TableRow key={row.id} hover selected={selectedSet.has(row.id)}>
                  {batchActions.length > 0 && (
                    <TableCell padding="checkbox">
                      <Checkbox
                        checked={selectedSet.has(row.id)}
                        onChange={() => toggleOne(row.id)}
                        inputProps={{ 'aria-label': `选择 ${row.id}` }}
                      />
                    </TableCell>
                  )}
                  {columns.map((c) => <TableCell key={c.key}>{c.render(row)}</TableCell>)}
                  {rowActions && <TableCell>{rowActions(row)}</TableCell>}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mt: 2 }}>
        <Typography variant="caption" color="text.secondary">共 {total} 条</Typography>
        <Pagination count={pages} page={page} onChange={(_, p) => { setSelected([]); onPage(p); }} size="small" />
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
        审核通过后站点进入白名单，成为爬虫种子；拒绝则进黑名单。详见 <RouterLink to="/settings">搜索偏好</RouterLink>。
      </Typography>
    </Box>
  );
}
