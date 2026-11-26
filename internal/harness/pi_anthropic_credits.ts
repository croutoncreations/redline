// Redline writes this pi extension for each pi run on a Claude (Anthropic)
// API credits account and loads it with `-e`. pi prefers a stored Anthropic
// OAuth login over ANTHROPIC_API_KEY, and passing --api-key would put the
// key in argv, so the run instead moves every anthropic / anthropic-cli model
// onto a provider that reads the key from the environment by name. No key
// is ever written into this file.
const PROVIDER = "redline-anthropic-credits";
export default function (pi: any) {
  if (process.env.REDLINE_ANTHROPIC_API_CREDITS !== "1") return;
  const pin = async (ctx: any, model: any) => {
    if (!model || model.provider === PROVIDER) return;
    if (model.provider !== "anthropic" && model.provider !== "anthropic-cli") return;
    if (!ctx.modelRegistry.getProvider(PROVIDER)) {
      const models = ctx.modelRegistry.getModelsOfType("chat", "anthropic").map(({ provider, ...m }: any) => m);
      ctx.modelRegistry.registerProvider(PROVIDER, { baseUrl: "https://api.anthropic.com", api: "anthropic-messages", apiKey: "$ANTHROPIC_API_KEY", models });
    }
    const target = ctx.modelRegistry.find(PROVIDER, model.id);
    if (target) await pi.setModel(target);
  };
  pi.on("model_select", async (e: any, ctx: any) => pin(ctx, e.model));
  pi.on("before_agent_start", async (_e: any, ctx: any) => pin(ctx, ctx.model));
}
