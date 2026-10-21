import { defineConfig } from "vite";

// Wails 通过 embed.FS 提供 frontend/dist，因此资源必须用相对路径（base: "./"）。
export default defineConfig({
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    assetsInlineLimit: 4096,
    cssCodeSplit: false,
    sourcemap: false,
  },
  worker: {
    format: "es",
  },
  server: {
    port: 5173,
    strictPort: true,
  },
});
