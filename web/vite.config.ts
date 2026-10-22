import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// 开发时把 /api（含 SSE）代理到本机 Hub。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:17310',
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // 依赖拆成几个稳定的块，前端更新时浏览器可以复用缓存。
        manualChunks: (id) => {
          if (!id.includes('node_modules')) return undefined
          if (/react-dom|react-router|scheduler|\/react\//.test(id)) return 'react'
          if (/motion|framer/.test(id)) return 'motion'
          if (/radix|vaul|sonner|lucide/.test(id)) return 'ui'
          return 'vendor'
        },
      },
    },
  },
})
