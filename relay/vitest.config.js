import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";
import { TEST_ISSUER_PUBLIC_KEY_B64 } from "./test/fixtures/entitlement-vector.js";

// Two projects, because the two suites need genuinely different deployments:
// the transport tests run an open relay so they can focus on forwarding, and
// the entitlement tests run a closed one with a configured issuer key.
//
// This split is load-bearing rather than tidiness. Configuration used to be
// injected per request via headers so a single deployment could be exercised
// both ways, which let a caller nominate the key its own token was verified
// against -- an authentication bypass. The environment is now the only source
// of that configuration, so tests must vary the environment too.
export default defineConfig({
  test: {
    projects: [
      {
        test: { name: "transport", include: ["test/relay.spec.js"] },
        plugins: [
          cloudflareTest({
            wrangler: { configPath: "./wrangler.toml" },
            miniflare: { bindings: { ALLOW_UNENTITLED: "true" } },
          }),
        ],
      },
      {
        test: { name: "entitlement", include: ["test/entitlement.spec.js"] },
        plugins: [
          cloudflareTest({
            wrangler: { configPath: "./wrangler.toml" },
            miniflare: {
              bindings: {
                ALLOW_UNENTITLED: "false",
                // Public half of the throwaway issuer keypair in the spec.
                ENTITLEMENT_PUBLIC_KEY: TEST_ISSUER_PUBLIC_KEY_B64,
              },
            },
          }),
        ],
      },
      {
        test: {
          name: "configuration",
          environment: "node",
          include: ["test/config.spec.js"],
        },
      },
    ],
  },
});
