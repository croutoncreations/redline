import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { parse } from "smol-toml";

// This verifier predates the coordinated issuer cutover and is intentionally
// unusable for a new production deployment. The corresponding private key
// must not be recreated or placed in this repository.
export const RETIRED_PRODUCTION_PUBLIC_KEY = "GvAg4IZvAvBKgx3STrizOwtSLT62TYmMnny+xOq1FlI=";

export function assertProductionDeployable(config) {
  const vars = config?.env?.production?.vars;
  if (vars?.ALLOW_UNENTITLED !== "false") {
    throw new Error("production relay must require entitlements");
  }
  const publicKey = vars.ENTITLEMENT_PUBLIC_KEY;
  if (typeof publicKey !== "string" || publicKey.trim() === "") {
    throw new Error("production relay requires an entitlement public key");
  }
  const decoded = Buffer.from(publicKey, "base64");
  if (decoded.byteLength !== 32 || decoded.toString("base64") !== publicKey) {
    throw new Error("production entitlement public key must be canonical base64 for 32 bytes");
  }
  if (publicKey === RETIRED_PRODUCTION_PUBLIC_KEY) {
    throw new Error("production deploy blocked: retired production entitlement key is still configured");
  }
}

async function main() {
  const source = await readFile(new URL("../wrangler.toml", import.meta.url), "utf8");
  assertProductionDeployable(parse(source));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
