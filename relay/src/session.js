import { DurableObject } from "cloudflare:workers";

export const CHANNEL_BYTES = 8;
// This is the pre-multiplexing encrypted-frame limit. The host-side wire
// limit is this payload plus CHANNEL_BYTES, so adding a channel never shrinks
// a frame that was valid before multiplexing.
export const MAX_PAYLOAD_BYTES = 1024 * 1024;
export const MAX_HOST_FRAME_BYTES = MAX_PAYLOAD_BYTES + CHANNEL_BYTES;

export class RelaySession extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.ctx = ctx;
    this.env = env;
  }

  async fetch(request) {
    const url = new URL(request.url);
    if (request.method === "POST" && url.pathname.endsWith("/entitlement")) {
      return this.refreshEntitlement(request);
    }

    const role = url.searchParams.get("role");
    if (role === "host") return this.connectHost(request);
    if (role === "client") return this.connectClient();
    return new Response("role must be host or client", { status: 400 });
  }

  async connectHost(request) {
    if (this.host()) return jsonError(409, "role_already_connected");

    const claims = this.claimsFromTrustedHeaders(request);
    if (!claims) return jsonError(402, "not_entitled");
    await this.storeClaims(claims);

    const { 0: client, 1: server } = new WebSocketPair();
    this.ctx.acceptWebSocket(server, ["host"]);
    return new Response(null, { status: 101, webSocket: client });
  }

  async connectClient() {
    if (!this.host()) return jsonError(423, "no_host");
    const maxClients = await this.ctx.storage.get("maxClients");
    const clients = this.clients();
    if (clients.length >= maxClients) return jsonError(409, "too_many_clients");

    const existingChannels = new Set(clients.map((ws) => channelHexFromSocket(this.ctx, ws)));
    const channel = allocateChannelId(existingChannels);
    const channelHex = bytesToHex(channel);
    const { 0: client, 1: server } = new WebSocketPair();
    server.serializeAttachment({ channelHex, closeNotified: false });
    this.ctx.acceptWebSocket(server, ["client", `client:${channelHex}`]);
    return new Response(null, { status: 101, webSocket: client });
  }

  async refreshEntitlement(request) {
    if (!this.host()) return jsonError(423, "no_host");
    const claims = this.claimsFromTrustedHeaders(request);
    if (!claims) return jsonError(402, "not_entitled");
    await this.storeClaims(claims);
    return new Response(null, { status: 204 });
  }

  claimsFromTrustedHeaders(request) {
    if (String(this.env.ALLOW_UNENTITLED).toLowerCase() === "true") {
      const configured = Number(this.env.MAX_CLIENTS_DEFAULT);
      return {
        maxClients: Number.isInteger(configured) && configured > 0 ? configured : 5,
      };
    }

    const exp = Number(request.headers.get("X-Redline-Internal-Exp"));
    const maxClients = Number(request.headers.get("X-Redline-Internal-Max-Clients"));
    if (!Number.isFinite(exp) || exp <= 0 || !Number.isInteger(maxClients) || maxClients < 1 || maxClients > 25) {
      return null;
    }
    return { exp, maxClients };
  }

  async storeClaims(claims) {
    await this.ctx.storage.deleteAll();
    await this.ctx.storage.put("maxClients", claims.maxClients);
    if (claims.exp === undefined) {
      await this.ctx.storage.deleteAlarm();
      return;
    }
    await this.ctx.storage.put("exp", claims.exp);
    await this.ctx.storage.setAlarm(claims.exp * 1000);
  }

  webSocketMessage(ws, message) {
    const bytes = messageBytes(message);
    const tags = this.ctx.getTags(ws);
    if (tags.includes("host")) {
      if (bytes.byteLength < CHANNEL_BYTES || bytes.byteLength > MAX_HOST_FRAME_BYTES) return;
      const channelHex = bytesToHex(bytes.subarray(0, CHANNEL_BYTES));
      const client = this.ctx.getWebSockets(`client:${channelHex}`)[0];
      if (client) client.send(bytes.slice(CHANNEL_BYTES));
      return;
    }

    if (bytes.byteLength > MAX_PAYLOAD_BYTES) {
      try {
        ws.close(1009, "frame too large");
      } catch {
        // Already closing.
      }
      return;
    }
    const host = this.host();
    if (!host) return;
    const channelHex = channelHexFromSocket(this.ctx, ws);
    if (!channelHex) return;
    host.send(concatBytes(hexToBytes(channelHex), bytes));
  }

  async webSocketClose(ws) {
    await this.releaseSocket(ws);
  }

  async webSocketError(ws) {
    await this.releaseSocket(ws);
  }

  async releaseSocket(ws) {
    const tags = this.ctx.getTags(ws);
    if (tags.includes("host")) {
      for (const client of this.clients()) safeClose(client, 1000, "peer disconnected");
      await this.clearClaims();
      return;
    }

    const host = this.host();
    const channelHex = channelHexFromSocket(this.ctx, ws);
    if (!host || !channelHex) return;

    let attachment = ws.deserializeAttachment() || {};
    if (attachment.closeNotified) return;
    attachment = { ...attachment, closeNotified: true };
    ws.serializeAttachment(attachment);
    try {
      host.send(hexToBytes(channelHex));
    } catch {
      // The host is already closing.
    }
  }

  async alarm() {
    for (const socket of this.ctx.getWebSockets()) {
      safeClose(socket, 1008, "entitlement expired");
    }
    await this.clearClaims();
  }

  async clearClaims() {
    await this.ctx.storage.deleteAll();
    await this.ctx.storage.deleteAlarm();
  }

  host() {
    return this.ctx.getWebSockets("host")[0];
  }

  clients() {
    return this.ctx.getWebSockets("client");
  }

  async debugStorageDump() {
    return Object.fromEntries(await this.ctx.storage.list());
  }
}

/** Generate a collision-free random channel. fillRandom is injectable for the collision test. */
export function allocateChannelId(existingChannels, fillRandom = crypto.getRandomValues.bind(crypto)) {
  for (;;) {
    const channel = new Uint8Array(CHANNEL_BYTES);
    fillRandom(channel);
    if (!existingChannels.has(bytesToHex(channel))) return channel;
  }
}

function channelHexFromSocket(ctx, ws) {
  const tag = ctx.getTags(ws).find((value) => value.startsWith("client:"));
  return tag ? tag.slice("client:".length) : null;
}

function messageBytes(message) {
  if (typeof message === "string") return new TextEncoder().encode(message);
  if (message instanceof ArrayBuffer) return new Uint8Array(message);
  return new Uint8Array(message.buffer, message.byteOffset, message.byteLength);
}

function concatBytes(first, second) {
  const combined = new Uint8Array(first.byteLength + second.byteLength);
  combined.set(first, 0);
  combined.set(second, first.byteLength);
  return combined;
}

function bytesToHex(bytes) {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function hexToBytes(hex) {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i += 1) bytes[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return bytes;
}

function safeClose(socket, code, reason) {
  try {
    socket.close(code, reason);
  } catch {
    // Already closing.
  }
}

function jsonError(status, code) {
  return new Response(JSON.stringify({ code }), {
    status,
    headers: { "content-type": "application/json" },
  });
}
