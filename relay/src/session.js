import { DurableObject } from "cloudflare:workers";

/**
 * One Durable Object per pairing session, holding at most two peers.
 *
 * The object is a forwarder, not a mailbox. It keeps no frame, writes nothing
 * to storage, and has no way to read what it copies. If this class ever grows
 * a queue or a log of frame contents, the zero-knowledge claim is gone.
 *
 * Hibernation matters for more than tidiness: an accepted WebSocket bills
 * duration for as long as it is held in memory, so acceptWebSocket (rather
 * than the plain accept()) is what keeps an idle session from costing money.
 */
export class RelaySession extends DurableObject {
  async fetch(request) {
    const url = new URL(request.url);
    const role = url.searchParams.get("role");

    // Reject a duplicate before creating the pair, or a second desktop could
    // silently take over a phone's session.
    if (this.peerForRole(role)) {
      return new Response("that role is already connected", { status: 409 });
    }

    const { 0: client, 1: server } = new WebSocketPair();

    // Hibernatable: the runtime may evict this object between frames and
    // re-create it on the next one, so nothing may live in instance fields.
    this.ctx.acceptWebSocket(server, [role]);

    return new Response(null, { status: 101, webSocket: client });
  }

  /**
   * Copy one frame to the other peer.
   *
   * The message is passed through untouched and unparsed. There is no branch
   * here on its contents, because there is nothing in it this code could
   * understand.
   */
  webSocketMessage(ws, message) {
    const partner = this.partnerOf(ws);
    if (!partner) {
      // No one to deliver to. Dropping is correct: buffering would mean
      // holding user ciphertext at rest and would need a bound to stop a
      // peer filling memory.
      return;
    }
    partner.send(message);
  }

  /**
   * Let one peer leave without ending the other's connection.
   *
   * The two legs are not alike. The desktop holds a single long-lived outbound
   * socket and cannot be dialled; the phone connects when someone opens the
   * app and goes away when they close it. Closing the desktop because the
   * phone left cost it a full reconnect on every relayed session, and its
   * backoff -- 2.3s, then 4.9s, then 7.6s -- was long enough that the next
   * refresh arrived while nothing was listening. Observed on a real phone as
   * "relayed", then "offline".
   *
   * Hibernation makes the survivor cheap: an accepted socket with no traffic
   * bills nothing, so keeping the desktop attached costs only the memory the
   * runtime may evict anyway.
   *
   * The departing socket is already closing, and getWebSockets stops returning
   * it, so the role frees up for the same peer to return.
   */
  webSocketClose(ws) {
    this.releasePartner(ws);
  }

  webSocketError(ws) {
    this.releasePartner(ws);
  }

  /**
   * Close the partner only when the departing peer was the desktop.
   *
   * The direction matters. A phone waiting on a desktop that has gone would
   * sit forever on a reply that cannot come, so it has to be told. A desktop
   * whose phone has gone simply has nothing to answer, and telling it costs a
   * reconnect it did not need.
   */
  releasePartner(ws) {
    const tags = this.ctx.getTags(ws);
    if (!tags.includes("host")) {
      return;
    }
    const partner = this.peerForRole("client");
    if (!partner) {
      return;
    }
    // 1000 regardless of the incoming code: some codes are not valid to send
    // onward, and the partner only needs to know the session ended.
    try {
      partner.close(1000, "peer disconnected");
    } catch {
      // Already closing; nothing to do.
    }
  }

  peerForRole(role) {
    return this.ctx.getWebSockets(role)[0];
  }

  partnerOf(ws) {
    const tags = this.ctx.getTags(ws);
    const partnerRole = tags.includes("host") ? "client" : "host";
    return this.peerForRole(partnerRole);
  }

  /**
   * Test-only: expose whatever this object has persisted so a test can assert
   * that no frame contents were kept. It should always be empty.
   */
  async debugStorageDump() {
    const all = await this.ctx.storage.list();
    return Object.fromEntries(all);
  }
}
