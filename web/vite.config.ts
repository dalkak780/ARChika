import { defineConfig, lazyPlugins } from "vite-plus";
import react from "@vitejs/plugin-react";

export default defineConfig({
  fmt: {},
  lint: {
    plugins: ["react", "typescript", "oxc", "unicorn"],
    jsPlugins: [{ name: "vite-plus", specifier: "vite-plus/oxlint-plugin" }],
    rules: {
      "vite-plus/prefer-vite-plus-imports": "error",
      "react/rules-of-hooks": "error",
      "react/exhaustive-deps": "warn",
    },
    options: { typeAware: true, typeCheck: true },
  },
  plugins: lazyPlugins(() => [react({ compiler: true })]),
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  build: { outDir: "dist", emptyOutDir: true },
});
