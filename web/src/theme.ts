import { createTheme } from '@mui/material/styles';

// 规划书主题：#F8F8F0 米白背景 + #2F4F4F 深灰绿主色（对比度 AAA）
export const theme = createTheme({
  palette: {
    background: {
      default: '#F8F8F0',
      paper: '#FFFFFF',
    },
    primary: { main: '#2F4F4F' },
    secondary: { main: '#8B7355' },
    error: { main: '#C0392B' },
    text: { primary: '#1A1A1A', secondary: '#5A5A50' },
  },
  shape: { borderRadius: 8 },
  typography: {
    fontFamily: '"Inter", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif',
    h1: { fontWeight: 700, letterSpacing: '-0.02em' },
    h4: { fontWeight: 700 },
    h5: { fontWeight: 600 },
    h6: { fontWeight: 600 },
  },
  components: {
    MuiChip: { defaultProps: { size: 'small' } },
    MuiButton: { defaultProps: { disableElevation: true } },
    MuiContainer: { defaultProps: { maxWidth: 'md' } },
    MuiTextField: { defaultProps: { size: 'small' } },
    MuiTableCell: { styleOverrides: { root: { fontSize: '0.875rem' } } },
  },
});
