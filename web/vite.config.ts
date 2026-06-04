import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { readFileSync } from "node:fs";

const { version } = JSON.parse(readFileSync("./package.json", "utf-8"));

// base "./" so the built app works when served from a Viam Application path.
export default defineConfig({
  base: "./",
  plugins: [svelte()],
  define: {
    __APP_VERSION__: JSON.stringify(version),
  },
});
