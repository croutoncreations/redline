// Redline writes this pi extension for each pi run on a Claude (Anthropic)
// API credits account and loads it with `-e`. pi prefers a stored Anthropic
// OAuth login over ANTHROPIC_API_KEY, and passing --api-key would put the
// key in argv, so the run instead moves every anthropic / anthropic-cli model
// onto a provider that reads the key from the environment by name. No key
// is ever written into this file.
//
// The pin fails closed: if a model cannot be moved, or a request is about to
// go out on anthropic / anthropic-cli anyway, the run exits with status 78
// rather than spend a subscription login. pi catches handler exceptions, so
// a throw would not stop the run; process.exit does.
const PROVIDER = "redline-anthropic-credits";
const SUBSCRIPTION_PROVIDERS = ["anthropic", "anthropic-cli"];
export default function (pi: any) {
  if (process.env.REDLINE_ANTHROPIC_API_CREDITS !== "1") return;
  const refuse = (model: any) => {
    process.stderr.write(`Redline: this Claude API credits run could not move ${model.provider}/${model.id} onto the account's API key; refusing to run on a subscription login\n`);
    process.exit(78);
  };
  // A copied model keeps only its description: any endpoint, header or
  // credential of its own (from a models.json pointing anthropic at a proxy,
  // say) could send the key elsewhere or attach another identity. compat is
  // kept: for anthropic-messages it holds capability flags only.
  const copyModel = ({ provider, baseUrl, headers, apiKey, authHeader, oauth, ...m }: any) => m;
  const pin = async (ctx: any, model: any) => {
    if (!model || model.provider === PROVIDER) return;
    if (!SUBSCRIPTION_PROVIDERS.includes(model.provider)) return;
    if (!ctx.modelRegistry.getProvider(PROVIDER)) {
      const models = ctx.modelRegistry.getModelsOfType("chat", "anthropic").map(copyModel);
      ctx.modelRegistry.registerProvider(PROVIDER, { baseUrl: "https://api.anthropic.com", api: "anthropic-messages", apiKey: "$ANTHROPIC_API_KEY", models });
    }
    const target = ctx.modelRegistry.find(PROVIDER, model.id);
    if (!target) return refuse(model);
    let moved = false;
    try {
      moved = (await pi.setModel(target)) === true;
    } catch {
      moved = false;
    }
    if (!moved) return refuse(model);
  };
  pi.on("model_select", async (e: any, ctx: any) => pin(ctx, e.model));
  pi.on("before_agent_start", async (_e: any, ctx: any) => pin(ctx, ctx.model));
  // The hard gate: no request leaves on a subscription provider, whatever
  // path (compaction, a retry, another model switch) chose the model.
  pi.on("before_provider_request", (_e: any, ctx: any) => {
    if (ctx.model && SUBSCRIPTION_PROVIDERS.includes(ctx.model.provider)) refuse(ctx.model);
    return undefined;
  });
}
