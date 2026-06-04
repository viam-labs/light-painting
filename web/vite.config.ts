import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// base "./" so the built app works when served from a Viam Application path.
export default defineConfig({
  base: "./",
  plugins: [svelte()],
});
