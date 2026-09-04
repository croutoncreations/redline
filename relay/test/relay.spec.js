import { env, SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

// Connect a WebSocket to the relay the way a real peer would.
async function connect(sessionId, role, extra = "") {
  const res = await SELF.fetch(
    `https://relay.example.com/v1/session/${sessionId}?role=${role}${extra}`,
    { headers: { Upgrade: "websocket" } },
  );
  return res;
}

async function connectSocket(sessionId, role, extra = "") {
  const res = await connect(sessionId, role, extra);
  expect(res.status).toBe(101);
  const ws = res.webSocket;
  ws.accept();
  return ws;
}

// Collect the next message as a string, with a timeout so a hang fails loudly
// rather than stalling the suite.
function nextMessage(ws, timeoutMs = 2000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for a message")), timeoutMs);
    ws.addEventListener("message", (event) => {
      clearTimeout(timer);
      resolve(typeof event.data === "string" ? event.data : new Uint8Array(event.data));
    }, { once: true });
  });
}

describe("relay session", () => {
  it("forwards a frame from the phone to the desktop", async () => {
    const desktop = await connectSocket("session-forward-1-relaytestpadding", "host");
    const phone = await connectSocket("session-forward-1-relaytestpadding", "client");

    phone.send("opaque-frame-from-phone");
    await expect(nextMessage(desktop)).resolves.toBe("opaque-frame-from-phone");
  });

  it("forwards a frame from the desktop to the phone", async () => {
    const desktop = await connectSocket("session-forward-2-relaytestpadding", "host");
    const phone = await connectSocket("session-forward-2-relaytestpadding", "client");

    desktop.send("opaque-frame-from-desktop");
    await expect(nextMessage(phone)).resolves.toBe("opaque-frame-from-desktop");
  });

  it("forwards binary frames unchanged", async () => {
    const desktop = await connectSocket("session-binary-relaytestpadding", "host");
    const phone = await connectSocket("session-binary-relaytestpadding", "client");

    // A real Noise frame is binary and may contain NUL and invalid UTF-8.
    const frame = new Uint8Array([0x00, 0xff, 0xfe, 0x41, 0x00, 0x80]);
    phone.send(frame);

    const received = await nextMessage(desktop);
    expect(Array.from(received)).toEqual(Array.from(frame));
  });

  it("keeps separate sessions isolated", async () => {
    const desktopA = await connectSocket("session-a-relaytestpadding", "host");
    const phoneB = await connectSocket("session-b-relaytestpadding", "client");
    const desktopB = await connectSocket("session-b-relaytestpadding", "host");

    phoneB.send("meant-for-b");
    await expect(nextMessage(desktopB)).resolves.toBe("meant-for-b");

    // Nothing should ever have reached the other session.
    await expect(nextMessage(desktopA, 300)).rejects.toThrow(/timed out/);
  });

  it("refuses a second peer in the same role", async () => {
    await connectSocket("session-dup-relaytestpadding", "host");
    const second = await connect("session-dup-relaytestpadding", "host");
    expect(second.status).toBe(409);
  });

  it("rejects an unknown role", async () => {
    const res = await connect("session-role-relaytestpadding", "middlebox");
    expect(res.status).toBe(400);
  });

  it("rejects a session id that is not opaque and random-looking", async () => {
    // Short or structured ids invite guessing another user's session.
    for (const bad of ["", "a", "../etc/passwd", "x".repeat(200)]) {
      const res = await connect(encodeURIComponent(bad), "client");
      expect(res.status).toBeGreaterThanOrEqual(400);
    }
  });

  it("requires a websocket upgrade", async () => {
    const res = await SELF.fetch("https://relay.example.com/v1/session/session-plain-relaytestpadding?role=client");
    expect(res.status).toBe(426);
  });

  it("tells a peer when its partner disconnects", async () => {
    const desktop = await connectSocket("session-bye-relaytestpadding", "host");
    const phone = await connectSocket("session-bye-relaytestpadding", "client");

    const closed = new Promise((resolve) => {
      phone.addEventListener("close", () => resolve("closed"), { once: true });
    });
    desktop.close(1000, "going away");

    // A phone that is never told will sit waiting for a reply that cannot come.
    await expect(closed).resolves.toBe("closed");
  });

  it("buffers nothing: a frame sent before the partner arrives is not stored", async () => {
    // The relay is a forwarder, not a mailbox. Storing frames would mean
    // holding user ciphertext at rest, which is exactly what we promise not
    // to do, and would make the DO a queue we have to bound.
    const phone = await connectSocket("session-early-relaytestpadding", "client");
    phone.send("sent-before-anyone-listened");

    const desktop = await connectSocket("session-early-relaytestpadding", "host");
    await expect(nextMessage(desktop, 300)).rejects.toThrow(/timed out/);
  });
});

describe("relay opacity", () => {
  it("never persists frame contents", async () => {
    const id = env.SESSIONS.idFromName("session-storage-relaytestpadding");
    const stub = env.SESSIONS.get(id);

    const desktop = await connectSocket("session-storage-relaytestpadding", "host");
    const phone = await connectSocket("session-storage-relaytestpadding", "client");
    phone.send("secret-ciphertext-frame");
    await nextMessage(desktop);

    // Whatever the DO kept, none of it may be the frame.
    const dump = await stub.debugStorageDump();
    expect(JSON.stringify(dump)).not.toContain("secret-ciphertext-frame");
  });
});

describe("health", () => {
  it("answers a health check without touching a session", async () => {
    const res = await SELF.fetch("https://relay.example.com/health");
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("ok");
  });

  it("404s an unknown path rather than guessing", async () => {
    const res = await SELF.fetch("https://relay.example.com/nope");
    expect(res.status).toBe(404);
  });
});

// A phone hanging up must not take the desktop's leg with it.
//
// The desktop holds one long-lived outbound connection and cannot be dialled;
// the phone comes and goes. Closing the desktop when the phone leaves meant
// every relayed session cost the desktop a reconnect, and its backoff --
// 2.3s, then 4.9s, then 7.6s -- was long enough that the next refresh found
// nobody home. Observed on a real phone: "relayed", then "offline".
//
// Only the departing peer closes. The survivor keeps its socket, so the next
// phone to arrive is paired immediately.
describe("a peer leaving", () => {
  it("leaves the other peer connected", async () => {
    const session = "session-survives-a-departure-0123";
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");

    let hostClosed = false;
    host.addEventListener("close", () => {
      hostClosed = true;
    });

    // The phone goes away, as it does whenever the screen is closed.
    client.close(1000, "done");
    await new Promise((resolve) => setTimeout(resolve, 100));

    expect(hostClosed).toBe(false);

    // And the desktop is still paired: a new phone reaches it without the
    // desktop having to redial.
    const secondClient = await connectSocket(session, "client");
    secondClient.send("still here");
    expect(await nextMessage(host)).toBe("still here");
  });

  it("still frees the role so the same peer can return", async () => {
    const session = "session-frees-the-role-abcdefgh12";
    const host = await connectSocket(session, "host");
    const client = await connectSocket(session, "client");
    client.close(1000, "done");
    await new Promise((resolve) => setTimeout(resolve, 100));

    // The slot must be free, or a returning phone gets 409 forever.
    const res = await connect(session, "client");
    expect(res.status).toBe(101);
    host.close();
  });
});
