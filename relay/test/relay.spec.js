import { env, SELF, evictDurableObject, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import {
  CHANNEL_BYTES,
  MAX_PAYLOAD_BYTES,
  allocateChannelId,
  selfHostedMaxClients,
} from "../src/session.js";

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

  it("uses a documented cap of five when a self-host cap is outside 1 through 25", () => {
    for (const configured of [undefined, "", "nope", "0", "-1", "2.5", "26", "1000"]) {
      expect(selfHostedMaxClients(configured)).toBe(5);
    }
    expect(selfHostedMaxClients("1")).toBe(1);
    expect(selfHostedMaxClients("25")).toBe(25);
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
    expect(await stub.debugStorageDump()).toMatchObject({
      maxClients: 5,
      hostGeneration: expect.stringMatching(/^[0-9a-f]{16}$/),
    });
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

  it("fails closed with a structured error when stored maxClients is missing or invalid", async () => {
    for (const [suffix, value] of [["missing", undefined], ["invalid", 0]]) {
      const session = `session-bad-cap-${suffix}-relaytest`;
      const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
      await connectSocket(session, "host");
      await runInDurableObject(stub, async (_instance, state) => {
        if (value === undefined) await state.storage.delete("maxClients");
        else await state.storage.put("maxClients", value);
      });
      const response = await connect(session, "client");
      expect(response.status).toBe(503);
      expect(await response.json()).toEqual({ code: "session_unavailable" });
    }
  });

  it("a delayed old-host callback closes only old-generation clients", async () => {
    const session = "session-host-generation-relaytest";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const oldHost = await connectSocket(session, "host");
    const oldClient = await connectSocket(session, "client");
    let oldCallbackQueued = false;
    let oldServer;
    let releaseSocket;

    await runInDurableObject(stub, async (instance, state) => {
      const oldGeneration = await state.storage.get("hostGeneration");
      releaseSocket = instance.releaseSocket.bind(instance);
      instance.releaseSocket = async (ws) => {
        if (state.getTags(ws).includes(`host:${oldGeneration}`)) {
          oldServer = ws;
          oldCallbackQueued = true;
          return;
        }
        return releaseSocket(ws);
      };
    });

    oldHost.close(1000, "replace");
    await expect.poll(() => oldCallbackQueued).toBe(true);
    await runInDurableObject(stub, async (_instance, state) => {
      expect(state.getWebSockets(`generation:${await state.storage.get("hostGeneration")}`)).toHaveLength(1);
    });

    const replacementHost = await connectSocket(session, "host");
    const replacementClient = await connectSocket(session, "client");
    const { hostGeneration: replacementGeneration } = await stub.debugStorageDump();
    const replacementExp = Math.floor(Date.now() / 1000) + 3600;
    await runInDurableObject(stub, async (_instance, state) => {
      await state.storage.put("exp", replacementExp);
      await state.storage.setAlarm(replacementExp * 1000);
      expect(state.getWebSockets(`generation:${replacementGeneration}`)).toHaveLength(1);
    });
    const before = await stub.debugStorageDump();
    const oldClose = nextClose(oldClient);
    const replacementStaysOpen = expect(nextClose(replacementClient, 200)).rejects.toThrow(/timed out/);

    await runInDurableObject(stub, async () => releaseSocket(oldServer));

    const oldEvent = await oldClose;
    expect([oldEvent.code, oldEvent.reason]).toEqual([1000, "peer disconnected"]);
    expect(await stub.debugStorageDump()).toEqual(before);
    await runInDurableObject(stub, async (_instance, state) => {
      expect(await state.storage.getAlarm()).toBe(replacementExp * 1000);
      expect(state.getWebSockets(`generation:${replacementGeneration}`)).toHaveLength(1);
    });
    await replacementStaysOpen;
    replacementClient.send(bytes("still-owned"));
    expect(Array.from(payload(await nextMessage(replacementHost)))).toEqual(Array.from(bytes("still-owned")));
  });

  it("does not notify a replacement host from a delayed old-client callback", async () => {
    const session = "session-client-generation-relaytest";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const oldHost = await connectSocket(session, "host");
    await connectSocket(session, "client");
    let oldClientServer;
    await runInDurableObject(stub, async (_instance, state) => {
      [oldClientServer] = state.getWebSockets("client");
    });

    oldHost.close(1000, "replace");
    await new Promise((resolve) => setTimeout(resolve, 50));
    const replacementHost = await connectSocket(session, "host");
    await connectSocket(session, "client");

    await runInDurableObject(stub, async (instance) => instance.releaseSocket(oldClientServer));
    await expect(nextMessage(replacementHost, 200)).rejects.toThrow(/timed out/);
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

  it("notifies the host when a client WebSocket errors", async () => {
    const session = "session-channel-error-relaytest";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    client.send(bytes("identify-error"));
    const tagged = await nextMessage(host);
    const notified = nextMessage(host);
    await runInDurableObject(stub, async (instance, state) => {
      const [serverClient] = state.getWebSockets("client");
      await instance.webSocketError(serverClient, new Error("test error"));
    });
    expect(Array.from(await notified)).toEqual(Array.from(channel(tagged)));
  });

  it("closes a host and its clients for undersized and oversized wire frames", async () => {
    for (const [suffix, frame, expectedCode] of [
      ["short", new Uint8Array(CHANNEL_BYTES - 1), 1002],
      ["oversized", new Uint8Array(MAX_PAYLOAD_BYTES + CHANNEL_BYTES + 1), 1009],
    ]) {
      const session = `session-host-frame-${suffix}-relaytest`;
      const host = await connectSocket(session, "host");
      const client = await connectSocket(session, "client");
      const hostClose = nextClose(host, 5000);
      const clientClose = nextClose(client, 5000);
      host.send(frame);
      expect((await hostClose).code).toBe(expectedCode);
      const clientEvent = await clientClose;
      expect(clientEvent.code).toBe(1000);
      expect(clientEvent.reason).toBe("peer disconnected");
    }
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

  it("tags each host and client with the same persisted generation", async () => {
    const session = "session-generation-tags-relaytest";
    const stub = env.SESSIONS.get(env.SESSIONS.idFromName(session));
    await connectSocket(session, "host");
    await connectSocket(session, "client");
    const { hostGeneration } = await stub.debugStorageDump();
    await runInDurableObject(stub, async (_instance, state) => {
      const [host] = state.getWebSockets("host");
      const [client] = state.getWebSockets("client");
      expect(state.getTags(host)).toContain(`host:${hostGeneration}`);
      expect(state.getTags(client)).toContain(`generation:${hostGeneration}`);
      expect(state.getTags(client)).toContainEqual(
        expect.stringMatching(new RegExp(`^client:${hostGeneration}:[0-9a-f]{16}$`)),
      );
    });
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
    expect(dump).toMatchObject({
      maxClients: 5,
      hostGeneration: expect.stringMatching(/^[0-9a-f]{16}$/),
    });
    expect(JSON.stringify(dump)).not.toContain("secret-ciphertext-frame");
  });

  it("answers health and 404 routes", async () => {
    expect((await SELF.fetch("https://relay.example.com/health")).status).toBe(200);
    expect((await SELF.fetch("https://relay.example.com/nope")).status).toBe(404);
  });
});
