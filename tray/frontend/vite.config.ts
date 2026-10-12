import { defineConfig } from "vite";
import wails from "@wailsio/runtime/plugins/vite";
import { writeFileSync } from "node:fs";

// The sign-in window is plain TypeScript with no framework. Vite emits only
// external scripts and styles, so the strict CSP in index.html applies.
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  build: {
    modulePreload: false,
    cssCodeSplit: false,
  },
  plugins: [
    wails("./bindings"),
    {
      // Vite empties dist/; the placeholder keeps //go:embed all:frontend/dist
      // valid for go vet and go test on a checkout that has not run the build.
      name: "keep-dist-placeholder",
      closeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
});
