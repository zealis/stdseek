import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// 构建产物直接进入 internal/webui/dist，由 go:embed 打进单二进制
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/admin': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
});
