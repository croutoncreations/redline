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
    const previousGeneration = await this.ctx.storage.get("hostGeneration");
    const generation = bytesToHex(allocateChannelId(new Set(previousGeneration ? [previousGeneration] : [])));
    await this.storeClaims(claims, generation);

    const { 0: client, 1: server } = new WebSocketPair();
    this.ctx.acceptWebSocket(server, ["host", `host:${generation}`]);
    return new Response(null, { status: 101, webSocket: client });
  }

  async connectClient() {
    const generation = await this.ctx.storage.get("hostGeneration");
    const host = generation ? this.hostForGeneration(generation) : null;
    if (!host) return jsonError(423, "no_host");

    const maxClients = await this.ctx.storage.get("maxClients");
    if (!Number.isInteger(maxClients) || maxClients < 1 || maxClients > 25) {
      return jsonError(503, "session_unavailable");
    }
    const clients = this.clientsForGeneration(generation);
    if (clients.length >= maxClients) return jsonError(409, "too_many_clients");

    const existingChannels = new Set(clients.map((ws) => channelHexFromSocket(this.ctx, ws)));
    const channel = allocateChannelId(existingChannels);
    const channelHex = bytesToHex(channel);
    const { 0: client, 1: server } = new WebSocketPair();
    server.serializeAttachment({ generation, channelHex, closeNotified: false });
    this.ctx.acceptWebSocket(server, [
      "client",
      `generation:${generation}`,
      `client:${generation}:${channelHex}`,
    ]);
    return new Response(null, { status: 101, webSocket: client });
  }

  async refreshEntitlement(request) {
    const generation = await this.ctx.storage.get("hostGeneration");
    if (!generation || !this.hostForGeneration(generation)) return jsonError(423, "no_host");
    const claims = this.claimsFromTrustedHeaders(request);
    if (!claims) return jsonError(402, "not_entitled");
    await this.storeClaims(claims, generation);
    return new Response(null, { status: 204 });
  }

  claimsFromTrustedHeaders(request) {
    if (String(this.env.ALLOW_UNENTITLED).toLowerCase() === "true") {
      return { maxClients: selfHostedMaxClients(this.env.MAX_CLIENTS_DEFAULT) };
    }

    const exp = Number(request.headers.get("X-Redline-Internal-Exp"));
    const maxClients = Number(request.headers.get("X-Redline-Internal-Max-Clients"));
    if (!Number.isFinite(exp) || exp <= 0 || !Number.isInteger(maxClients) || maxClients < 1 || maxClients > 25) {
      return null;
    }
    return { exp, maxClients };
  }

  async storeClaims(claims, generation) {
    await this.ctx.storage.deleteAll();
    await this.ctx.storage.put({
      hostGeneration: generation,
      maxClients: claims.maxClients,
      ...(claims.exp === undefined ? {} : { exp: claims.exp }),
    });
    if (claims.exp === undefined) {
      await this.ctx.storage.deleteAlarm();
      return;
    }
    await this.ctx.storage.setAlarm(claims.exp * 1000);
  }

  async webSocketMessage(ws, message) {
    const frame = messageBytes(message);
    const tags = this.ctx.getTags(ws);
    if (tags.includes("host")) {
      if (frame.byteLength < CHANNEL_BYTES) {
        await this.closeMalformedHost(ws, 1002, "host frame missing channel");
        return;
      }
      if (frame.byteLength > MAX_HOST_FRAME_BYTES) {
        await this.closeMalformedHost(ws, 1009, "frame too large");
        return;
      }
      const generation = hostGenerationFromSocket(this.ctx, ws);
      if (!generation) {
        safeClose(ws, 1002, "host generation missing");
        return;
      }
      const channelHex = bytesToHex(frame.subarray(0, CHANNEL_BYTES));
      const client = this.ctx.getWebSockets(`client:${generation}:${channelHex}`)[0];
      if (client) client.send(frame.slice(CHANNEL_BYTES));
      return;
    }

    if (frame.byteLength > MAX_PAYLOAD_BYTES) {
      safeClose(ws, 1009, "frame too large");
      return;
    }
    const generation = clientGenerationFromSocket(this.ctx, ws);
    const host = generation ? this.hostForGeneration(generation) : null;
    if (!host) return;
    const channelHex = channelHexFromSocket(this.ctx, ws);
    if (!channelHex) return;
    host.send(concatBytes(hexToBytes(channelHex), frame));
  }

  async closeMalformedHost(ws, code, reason) {
    const generation = hostGenerationFromSocket(this.ctx, ws);
    safeClose(ws, code, reason);
    if (!generation) return;
    for (const client of this.clientsForGeneration(generation)) {
      safeClose(client, 1000, "peer disconnected");
    }
    await this.clearClaims(generation);
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
      const generation = hostGenerationFromSocket(this.ctx, ws);
      if (!generation) return;
      for (const client of this.clientsForGeneration(generation)) {
        safeClose(client, 1000, "peer disconnected");
      }
      await this.clearClaims(generation);
      return;
    }

    const generation = clientGenerationFromSocket(this.ctx, ws);
    const host = generation ? this.hostForGeneration(generation) : null;
    const channelHex = channelHexFromSocket(this.ctx, ws);
    if (!host || !channelHex) return;

    let attachment = ws.deserializeAttachment() || {};
    if (attachment.closeNotified) return;
    attachment = { ...attachment, closeNotified: true };
    ws.serializeAttachment(attachment);
    try {
      host.send(hexToBytes(channelHex));
    } catch {
      // The owning host is already closing.
    }
  }

  async alarm() {
    const exp = await this.ctx.storage.get("exp");
    if (!Number.isFinite(exp)) return;
    if (exp * 1000 > Date.now()) {
      await this.ctx.storage.setAlarm(exp * 1000);
      return;
    }

    const generation = await this.ctx.storage.get("hostGeneration");
    if (!generation) return;
    const host = this.hostForGeneration(generation);
    if (host) safeClose(host, 1008, "entitlement expired");
    for (const client of this.clientsForGeneration(generation)) {
      safeClose(client, 1008, "entitlement expired");
    }
    await this.clearClaims(generation);
  }

  async clearClaims(generation) {
    if (await this.ctx.storage.get("hostGeneration") !== generation) return false;
    await this.ctx.storage.delete(["exp", "maxClients", "hostGeneration"]);
    await this.ctx.storage.deleteAlarm();
    return true;
  }

  host() {
    return this.ctx.getWebSockets("host")[0];
  }

  hostForGeneration(generation) {
    return this.ctx.getWebSockets(`host:${generation}`)[0];
  }

  clients() {
    return this.ctx.getWebSockets("client");
  }

  clientsForGeneration(generation) {
    return this.ctx.getWebSockets(`generation:${generation}`);
  }

  async debugStorageDump() {
    return Object.fromEntries(await this.ctx.storage.list());
  }
}

/** Generate a collision-free random identifier. fillRandom is injectable for collision tests. */
export function allocateChannelId(existingChannels, fillRandom = crypto.getRandomValues.bind(crypto)) {
  for (;;) {
    const channel = new Uint8Array(CHANNEL_BYTES);
    fillRandom(channel);
    if (!existingChannels.has(bytesToHex(channel))) return channel;
  }
}

/** Invalid self-host configuration keeps the historical five-client cap. */
export function selfHostedMaxClients(value) {
  const configured = Number(value);
  return Number.isInteger(configured) && configured >= 1 && configured <= 25 ? configured : 5;
}

function hostGenerationFromSocket(ctx, ws) {
  const tag = ctx.getTags(ws).find((value) => value.startsWith("host:"));
  return tag ? tag.slice("host:".length) : null;
}

function clientGenerationFromSocket(ctx, ws) {
  const tag = ctx.getTags(ws).find((value) => value.startsWith("generation:"));
  return tag ? tag.slice("generation:".length) : null;
}

function channelHexFromSocket(ctx, ws) {
  const tag = ctx.getTags(ws).find((value) => value.startsWith("client:"));
  return tag ? tag.slice(tag.lastIndexOf(":") + 1) : null;
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
