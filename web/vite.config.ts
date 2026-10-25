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
      // React Compiler can hoist an asserted property read into a render-time memo check.
      "typescript/no-non-null-assertion": "error",
    },
    options: { typeAware: true, typeCheck: true },
    overrides: [
      {
        // useVirtualizer callers are intentionally opted out with "use no memo".
        files: ["src/virtual.tsx"],
        rules: { "react/incompatible-library": "off" },
      },
    ],
  },
  plugins: lazyPlugins(() => [react({ compiler: true })]),
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  build: { outDir: "dist", emptyOutDir: true },
});
