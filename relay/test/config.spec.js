import { readFile } from "node:fs/promises";
import { parse } from "smol-toml";
import { describe, expect, it } from "vitest";
import {
  RETIRED_PRODUCTION_PUBLIC_KEY,
  assertProductionDeployable,
} from "../scripts/production-deploy-preflight.mjs";

describe("deployment configuration", () => {
  it("keeps the default self-host profile open and hosted production closed", async () => {
    const source = await readFile(new URL("../wrangler.toml", import.meta.url), "utf8");
    const config = parse(source);

    expect(config.vars.ALLOW_UNENTITLED).toBe("true");
    expect(config.vars.ENTITLEMENT_PUBLIC_KEY).toBe("");
    expect(config.vars.MAX_CLIENTS_DEFAULT).toBe("5");
    expect(config.routes).toBeUndefined();
    expect(config.durable_objects.bindings).toHaveLength(1);
    expect(config.migrations).toHaveLength(1);

    const production = config.env.production;
    expect(production.vars.ALLOW_UNENTITLED).toBe("false");
    expect(production.vars.ENTITLEMENT_PUBLIC_KEY).toMatch(/\S+/);
    const publicKey = Buffer.from(production.vars.ENTITLEMENT_PUBLIC_KEY, "base64");
    expect(publicKey).toHaveLength(32);
    expect(publicKey.toString("base64")).toBe(production.vars.ENTITLEMENT_PUBLIC_KEY);
    expect(production.vars.MAX_CLIENTS_DEFAULT).toBe("5");
    expect(production.routes).toEqual([
      { pattern: "redline-relay.croutoncreations.com", custom_domain: true },
    ]);
    expect(production.durable_objects.bindings).toHaveLength(1);
    expect(production.migrations).toHaveLength(1);
  });

  it("blocks only the retired production verifier key pending issuer rotation", async () => {
    const source = await readFile(new URL("../wrangler.toml", import.meta.url), "utf8");
    const config = parse(source);
    expect(config.env.production.vars.ENTITLEMENT_PUBLIC_KEY).toBe(RETIRED_PRODUCTION_PUBLIC_KEY);
    expect(() => assertProductionDeployable(config)).toThrow(/retired production entitlement key/);

    const malformed = structuredClone(config);
    malformed.env.production.vars.ENTITLEMENT_PUBLIC_KEY = "merely-nonempty";
    expect(() => assertProductionDeployable(malformed)).toThrow(/canonical base64/);

    const replacement = structuredClone(config);
    replacement.env.production.vars.ENTITLEMENT_PUBLIC_KEY = Buffer.alloc(32, 7).toString("base64");
    expect(() => assertProductionDeployable(replacement)).not.toThrow();
  });

  it("runs the production preflight before npm deploy invokes Wrangler", async () => {
    const pkg = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
    expect(pkg.scripts.deploy).toBe(
      "node scripts/production-deploy-preflight.mjs && wrangler deploy --env production",
    );
  });
});
