// r3: the owner selected Option A with one change: the detail cards become pages of the SAME Claude tile.
// Page 0 = Overview (all accounts), pages 1..N = one account each, all inside the one card area.
// Default page is Overview. Synthetic data only. Depends on the shared helpers from prototype.html
// (tone, meter, BADGE, ROUTE, miniMeter, A, header, tabs, codexCard), copied in by build-r3.cjs.

const REV3 = 'r3';
const accts = [A.personal, A.client, A.side];
const N = accts.length;
const PAGES = N + 1; // Overview + one page per account

function phone3(inner, overlay='') {
  return `<div class="phone"><div class="mockflag">OPTION A ${REV3} · MOCKUP · SYNTHETIC DATA</div>
  <div class="sysbar"><span>2:45</span><span class="icons"><i></i><i></i><i class="b"></i></span></div>
  ${inner}<div class="navbar"><i></i></div>${overlay}</div>`;
}
const shell3 = (content) => header() + `<div class="content">${content}</div>` + tabs('Usage');

// Pager strip at the top of the tile: tappable Overview pill, segment dots, ‹ ›, and a page label.
function pagerStrip(page) {
  const segs = Array.from({length: PAGES}, (_, k) => {
    const ex = k > 0 && accts[k-1].s.ex;
    const cls = k === page ? 'on' : (ex ? 'ex' : '');
    return k === 0 ? `<i class="ov ${cls}" title="Overview"></i>` : `<i class="${cls}"></i>`;
  }).join('');
  const label = page === 0 ? 'Overview' : `Account ${page} of ${N}`;
  return `<div class="pg">
    <span class="pg-ov ${page===0?'on':''}">${page===0?'Overview':'‹ Overview'}</span>
    <span class="pg-dots" aria-hidden="true">${segs}</span>
    <span class="pg-nav"><span class="pbtn sm ${page===0?'dis':''}" aria-label="Previous page">‹</span><span class="pbtn sm ${page===PAGES-1?'dis':''}" aria-label="Next page">›</span></span>
  </div>
  <div class="pg-label">${label}${page===0?' · swipe left for each account':''}</div>`;
}

// Overview page = the summary rows; each row opens its detail page.
function overviewPage() {
  const ready = accts.filter(a => a.route === 'on').length;
  const rows = accts.map((a, i) => {
    const [txt, dot] = ROUTE[a.route];
    return `<div class="acct${i===0?' first':''}"><div class="top"><div class="name">${a.alias}</div><span class="chip plan">${a.plan}</span><span class="go">Details ›</span></div>
      <div class="mini">${miniMeter('5-hour', a.s)}${miniMeter('Weekly', a.w)}</div>
      <div class="route"><i style="background:${dot}"></i>${txt}</div></div>`;
  }).join('');
  return `<div class="sum-sub" style="margin-top:0">Each account has its own limits. Numbers are per account and never added together.</div>${rows}`;
}

// Detail page = the per-account content, inside the same tile (no outer card).
function detailPage(a, i) {
  const routed = a.route !== 'mon';
  return `<div class="title" style="overflow-wrap:anywhere;line-height:1.3">${a.alias}</div>
   <div class="row" style="gap:6px;margin-top:6px;flex-wrap:wrap"><span class="chip plan">${a.plan}</span><span class="chip">ID ${a.hint}</span>${BADGE[a.route]}</div>
   <div class="src">${a.src}</div>
   ${meter('5-hour window', a.s, {reserve: routed?25:0})}
   ${meter('Weekly allowance', a.w, {floors: routed?(a.floors||[]):[]})}
   ${a.fable?meter('Fable · model weekly', a.fable):''}
   ${a.sched?`<div class="sched"><i style="background:${a.sched[0]==='ADMIT'?'var(--good)':'var(--accent)'}"></i><span>${a.sched[1]}</span></div>`
     :`<div class="sched"><i style="background:#59636b"></i><span>Monitor only · no execution profile routes tasks to this account</span></div>`}
   ${a.banked!=null?`<div class="banked">Banked quota resets<b>${a.banked} available</b></div>`:''}
   <div class="actions">${routed?'<span class="act">View queue</span><span class="act em">Pause</span>':''}<span class="act txt">Refresh</span></div>`;
}

// One tile; the peeks hint at the neighbouring pages inside the same carousel.
function tile(page) {
  const body = page === 0 ? overviewPage() : detailPage(accts[page-1], page-1);
  const left = page > 0 ? '<div class="tpeek l"></div>' : '';
  const right = page < PAGES-1 ? '<div class="tpeek r"></div>' : '';
  const ready = accts.filter(a => a.route === 'on').length;
  const head = `<div class="sum-h"><span class="title">Claude</span><span class="chip">${N} accounts</span><span class="sp"></span><span class="status">${ready} of ${N} dispatching</span></div>`;
  return `<div class="tile-wrap">${left}<div class="card tile">${head}${pagerStrip(page)}<div class="tile-body">${body}</div></div>${right}</div>`;
}

const frames3 = [];
const add3 = (id, cap, sub, html) => frames3.push({id, cap, sub, html});

add3('A3-0-overview-r3', 'Selected A (r3) · page 0: Overview (default on open)',
  'One Claude tile. It opens on Overview. Swipe left, tap ›, or tap a row\u2019s “Details ›” to go to that account\u2019s page. Codex stays below.',
  phone3(shell3(tile(0) + codexCard())));
add3('A3-1-detail-dispatching-r3', 'Selected A (r3) · page 1: Personal, dispatching',
  'Same tile, now showing account 1 of 3. “‹ Overview” returns to Overview. Floor copy corrected: weekly 41% left, no floor armed yet.',
  phone3(shell3(tile(1) + codexCard())));
add3('A3-2-detail-exhausted-r3', 'Selected A (r3) · page 2: Client, 5-hour used up',
  'This account is held; its tasks wait for it and are not moved to another. The long name wraps in full on the detail page.',
  phone3(shell3(tile(2) + codexCard())));
add3('A3-3-detail-monitor-r3', 'Selected A (r3) · page 3: Side project, monitor only',
  'The last page: › is disabled. Monitor-only accounts show usage but are not routed, so they have no queue or pause actions.',
  phone3(shell3(tile(3) + codexCard())));

// Interaction notes card (not a phone frame).
add3('A3-interaction-notes-r3', 'Selected A (r3) · navigation and accessibility notes', 'How the single carousel behaves.',
  `<div class="notes3">
   <h3>One carousel in the Claude tile</h3>
   <ol>
    <li><b>Pages:</b> 0 = Overview (all accounts); 1…N = one account each. All pages occupy the same tile; there is no separate detail stack below.</li>
    <li><b>Default:</b> the tile opens on Overview every time the app is opened. The page is kept while you switch tabs and scroll within one session.</li>
    <li><b>Ways to move:</b>
      <ul><li>Swipe horizontally on the tile.</li><li>Tap ‹ or ›.</li><li>Tap “Details ›” on an Overview row to jump straight to that account.</li><li>Tap “‹ Overview”, or tap the Overview segment, to go back from any page.</li><li>System Back on an account page returns to Overview before it leaves the screen.</li></ul></li>
    <li><b>Position:</b> “Overview” or “Account 2 of 3”, plus segment dots. The first segment is the wider Overview pill. Dots of used-up accounts are dark red. The Claude header (accounts, dispatching count) stays fixed on every page.</li>
    <li><b>Scroll safety:</b> the pager claims only horizontal drags (HorizontalPager). Vertical scrolling of the screen and pull-to-refresh pass through. Meters and buttons don’t start page swipes.</li>
    <li><b>Height:</b> the tile animates to each page’s height, so a short page never leaves a gap, and Codex stays directly below.</li>
    <li><b>TalkBack:</b> the tile is one pager with custom actions (Overview, Previous account, Next account). Page change announces “Claude account 2 of 3, Client — Northwind long-running engagement, Team plan, dispatch held”. Overview rows are buttons: “Open details for Personal”.</li>
    <li><b>Names:</b> aliases wrap in full on Overview rows and detail headings, and are never shortened. No emails are shown.</li>
    <li><b>Many accounts (more than 5):</b> dots become “Account 3 of 7 ▾”, which opens the account chooser sheet (as in the earlier frame 08).</li>
    <li><b>One account:</b> no carousel. Today’s single Claude card is unchanged.</li>
   </ol></div>`);

document.getElementById('root').innerHTML = frames3.map(f =>
  `<div class="slot"><div class="caption">${f.cap}<small>${f.sub}</small></div><div id="${f.id}">${f.html}</div></div>`).join('');
window.FRAMES = frames3.map(f=>f.id);

// Compact strip: four pages of the one tile, in order.
const byId3 = Object.fromEntries(frames3.map(f=>[f.id,f.html]));
const strip = document.createElement('div');
strip.id = 'comparison';
const order = [['A3-0-overview-r3','0 · Overview (default)'],['A3-1-detail-dispatching-r3','1 · Personal · dispatching'],['A3-2-detail-exhausted-r3','2 · Client · 5-hour used up'],['A3-3-detail-monitor-r3','3 · Side project · monitor only']];
strip.innerHTML = `<div class="cmp-head"><b>Selected A (r3) · one Claude tile, swipe through pages <span class="r3tag">r3 · the owner’s change</span></b><span>Synthetic data · Pixel 9 · dark theme. Swipe ← → or tap ‹ ›; “Details ›” on the Overview jumps to an account; “‹ Overview” returns.</span></div>
 <div class="cmp-grid4">${order.map(([id,l])=>`<div><div class="cmp-t4">${l}</div><div class="cmp-ph">${'<div class="cmp-sc">'+byId3[id]+'</div>'}</div></div>`).join('<div class="arrow">→</div>')}</div>`;
document.body.prepend(strip);
