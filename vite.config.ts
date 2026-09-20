import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  plugins: [vue(), viteSingleFile()],
  base: './',
  build: {
    outDir: 'ui',
    emptyOutDir: true,
    chunkSizeWarningLimit: 4096,
  },
});
