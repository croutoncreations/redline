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
   * Tell the other peer when one side goes away.
   *
   * Without this a phone waits for a reply that can never arrive, which looks
   * like a hang rather than a disconnection.
   */
  webSocketClose(ws, code, reason) {
    const partner = this.partnerOf(ws);
    if (partner) {
      // 1000 regardless of the incoming code: some codes are not valid to
      // send onward, and the partner only needs to know the session ended.
      try {
        partner.close(1000, "peer disconnected");
      } catch {
        // Already closing; nothing to do.
      }
    }
  }

  webSocketError(ws) {
    this.webSocketClose(ws);
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
