import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: "./wrangler.toml" },
      miniflare: {
        bindings: {
          // Tests that exercise entitlements opt in per request; the default
          // open mode keeps the transport tests focused on forwarding.
          ALLOW_UNENTITLED: "true",
        },
      },
    }),
  ],
});
