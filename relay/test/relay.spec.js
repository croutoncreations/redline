import { env, SELF, evictDurableObject, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { CHANNEL_BYTES, MAX_PAYLOAD_BYTES, allocateChannelId } from "../src/session.js";

async function connect(sessionId, role) {
  return SELF.fetch(`https://relay.example.com/v1/session/${sessionId}?role=${role}`, {
    headers: { Upgrade: "websocket" },
  });
}

async function connectSocket(sessionId, role) {
  const res = await connect(sessionId, role);
  expect(res.status).toBe(101);
  const ws = res.webSocket;
  ws.accept();
  return ws;
}

function nextMessage(ws, timeoutMs = 2000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for a message")), timeoutMs);
    ws.addEventListener("message", (event) => {
      clearTimeout(timer);
      resolve(typeof event.data === "string" ? event.data : new Uint8Array(event.data));
    }, { once: true });
  });
}

function nextClose(ws, timeoutMs = 2000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for close")), timeoutMs);
    ws.addEventListener("close", (event) => {
      clearTimeout(timer);
      resolve(event);
    }, { once: true });
  });
}

function concat(...parts) {
  const size = parts.reduce((sum, part) => sum + part.byteLength, 0);
  const out = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.byteLength;
  }
  return out;
}

function bytes(text) {
  return new TextEncoder().encode(text);
}

function payload(frame) {
  return frame.slice(CHANNEL_BYTES);
}

function channel(frame) {
  return frame.slice(0, CHANNEL_BYTES);
}

describe("host-gated relay session", () => {
  it("returns structured 423 when a client has no attached host", async () => {
    const res = await connect("session-no-host-relaytestpadding", "client");
    expect(res.status).toBe(423);
    expect(await res.json()).toEqual({ code: "no_host" });
  });

  it("keeps duplicate host rejection structured", async () => {
    await connectSocket("session-dup-relaytestpadding", "host");
    const second = await connect("session-dup-relaytestpadding", "host");
    expect(second.status).toBe(409);
    expect(await second.json()).toEqual({ code: "role_already_connected" });
  });

  it("uses MAX_CLIENTS_DEFAULT in open mode and rejects the sixth client", async () => {
    const session = "session-open-cap-relaytestpadding";
    await connectSocket(session, "host");
    for (let i = 0; i < 5; i += 1) await connectSocket(session, "client");
    const sixth = await connect(session, "client");
    expect(sixth.status).toBe(409);
    expect(await sixth.json()).toEqual({ code: "too_many_clients" });
  });

  it("does not make the entitlement refresh endpoint unauthenticated in open mode", async () => {
    const session = "session-open-refresh-relaytest";
    await connectSocket(session, "host");
    const res = await SELF.fetch(`https://relay.example.com/v1/session/${session}/entitlement`, {
      method: "POST",
    });
    expect(res.status).toBe(402);
    expect(await res.json()).toEqual({ code: "not_entitled" });
  });

  it("stores only maxClients and does not schedule an alarm in open mode", async () => {
    const session = "session-open-storage-relaytest";
    const id = env.SESSIONS.idFromName(session);
    const stub = env.SESSIONS.get(id);
    await connectSocket(session, "host");
    expect(await stub.debugStorageDump()).toEqual({ maxClients: 5 });
    await runInDurableObject(stub, async (_instance, state) => {
      expect(await state.storage.getAlarm()).toBeNull();
    });
  });

  it("closes every client and clears storage and alarm when the host disconnects", async () => {
    const session = "session-host-bye-relaytestpadding";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const host = await connectSocket(session, "host");
    const first = await connectSocket(session, "client");
    const second = await connectSocket(session, "client");
    const closes = [nextClose(first), nextClose(second)];
    host.close(1000, "done");
    const events = await Promise.all(closes);
    expect(events.map((event) => [event.code, event.reason])).toEqual([
      [1000, "peer disconnected"],
      [1000, "peer disconnected"],
    ]);
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(await stub.debugStorageDump()).toEqual({});
    await runInDurableObject(stub, async (_instance, state) => {
      expect(await state.storage.getAlarm()).toBeNull();
    });
  });
});

describe("client channel multiplexing", () => {
  it("routes two clients independently without crosstalk", async () => {
    const session = "session-multiplex-relaytestpadding";
    const host = await connectSocket(session, "host");
    const first = await connectSocket(session, "client");
    const second = await connectSocket(session, "client");

    first.send(bytes("from-first"));
    const firstHostFrame = await nextMessage(host);
    second.send(bytes("from-second"));
    const secondHostFrame = await nextMessage(host);

    expect(Array.from(payload(firstHostFrame))).toEqual(Array.from(bytes("from-first")));
    expect(Array.from(payload(secondHostFrame))).toEqual(Array.from(bytes("from-second")));
    expect(Array.from(channel(firstHostFrame))).not.toEqual(Array.from(channel(secondHostFrame)));

    host.send(concat(channel(secondHostFrame), bytes("reply-second")));
    expect(Array.from(await nextMessage(second))).toEqual(Array.from(bytes("reply-second")));
    await expect(nextMessage(first, 150)).rejects.toThrow(/timed out/);

    host.send(concat(channel(firstHostFrame), bytes("reply-first")));
    expect(Array.from(await nextMessage(first))).toEqual(Array.from(bytes("reply-first")));
  });

  it("drops a host frame for an unknown channel", async () => {
    const session = "session-unknown-channel-relaytest";
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    host.send(concat(new Uint8Array(CHANNEL_BYTES).fill(0xff), bytes("not yours")));
    await expect(nextMessage(client, 150)).rejects.toThrow(/timed out/);
  });

  it("notifies the host exactly once with the bare 8-byte channel when a client closes", async () => {
    const session = "session-channel-close-relaytest";
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    client.send(bytes("identify"));
    const tagged = await nextMessage(host);
    client.close(1000, "done");
    const closed = await nextMessage(host);
    expect(closed.byteLength).toBe(CHANNEL_BYTES);
    expect(Array.from(closed)).toEqual(Array.from(channel(tagged)));
    await expect(nextMessage(host, 150)).rejects.toThrow(/timed out/);
  });

  it("retries an 8-byte random tag collision", () => {
    const colliding = new Uint8Array(CHANNEL_BYTES).fill(1);
    const unique = new Uint8Array(CHANNEL_BYTES).fill(2);
    const generated = [colliding, unique];
    const result = allocateChannelId(new Set(["0101010101010101"]), (target) => {
      target.set(generated.shift());
    });
    expect(Array.from(result)).toEqual(Array.from(unique));
  });

  it("preserves the old maximum payload at the exact prefixed boundary", async () => {
    const session = "session-frame-boundary-relaytest";
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    const original = new Uint8Array(MAX_PAYLOAD_BYTES);
    original[0] = 1;
    original[original.length - 1] = 2;
    client.send(original);
    const tagged = await nextMessage(host, 5000);
    expect(tagged.byteLength).toBe(MAX_PAYLOAD_BYTES + CHANNEL_BYTES);
    expect(tagged[CHANNEL_BYTES]).toBe(1);
    expect(tagged[tagged.length - 1]).toBe(2);

    host.send(concat(channel(tagged), original));
    const returned = await nextMessage(client, 5000);
    expect(returned.byteLength).toBe(MAX_PAYLOAD_BYTES);
    expect(returned[0]).toBe(1);
    expect(returned[returned.length - 1]).toBe(2);
  });

  it("routes correctly after Durable Object hibernation", async () => {
    const session = "session-hibernation-relaytestpadding";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    await evictDurableObject(stub);
    client.send(bytes("after-hibernation"));
    const tagged = await nextMessage(host);
    expect(Array.from(payload(tagged))).toEqual(Array.from(bytes("after-hibernation")));
  });
});

describe("request validation and opacity", () => {
  it("rejects unknown roles, invalid session ids, and non-upgrades", async () => {
    expect((await connect("session-role-relaytestpadding", "middlebox")).status).toBe(400);
    expect((await connect("a", "client")).status).toBe(400);
    const plain = await SELF.fetch("https://relay.example.com/v1/session/session-plain-relaytestpadding?role=client");
    expect(plain.status).toBe(426);
  });

  it("never persists frame contents", async () => {
    const session = "session-storage-relaytestpadding";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    client.send(bytes("secret-ciphertext-frame"));
    await nextMessage(host);
    const dump = await stub.debugStorageDump();
    expect(dump).toEqual({ maxClients: 5 });
    expect(JSON.stringify(dump)).not.toContain("secret-ciphertext-frame");
  });

  it("answers health and 404 routes", async () => {
    expect((await SELF.fetch("https://relay.example.com/health")).status).toBe(200);
    expect((await SELF.fetch("https://relay.example.com/nope")).status).toBe(404);
  });
});
