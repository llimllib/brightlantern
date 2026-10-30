// Client-side behavior.
//
// Deliberately small. HTMX drives search and lazy tool expansion, <details>
// handles collapsing, and navigation is plain links, so the only things left
// for JavaScript are the ones the platform genuinely lacks: keyboard
// navigation, scroll restoration, placing sessions that arrive while the page
// is open, and the clipboard.

const listPane = () => document.querySelector<HTMLElement>(".list");
const rowLinks = () =>
  Array.from(document.querySelectorAll<HTMLAnchorElement>("a.row"));

/**
 * Moving the cursor means moving DOM focus to the row's anchor.
 *
 * The alternative -- tracking a "cursor" class ourselves -- means
 * reimplementing scroll-into-view, Enter-to-activate, and focus visibility,
 * and leaves the keyboard cursor invisible to assistive technology. Focus
 * gives all of that from the platform.
 */
function focusRow(index: number): void {
  const rows = rowLinks();
  if (rows.length === 0) return;

  const clamped = Math.max(0, Math.min(index, rows.length - 1));
  const target = rows[clamped];
  if (!target) return;

  target.focus({ preventScroll: true });
  target.scrollIntoView({ block: "nearest" });
}

/**
 * Where the cursor is now: the focused row if one has focus, otherwise the
 * selected one, so the first `j` continues from what is on screen rather than
 * jumping to the top of the list.
 */
function currentIndex(): number {
  const rows = rowLinks();
  const focused = rows.findIndex((r) => r === document.activeElement);
  if (focused !== -1) return focused;
  const selected = rows.findIndex((r) => r.matches('[aria-current="page"]'));
  return selected === -1 ? -1 : selected;
}

function moveCursor(delta: number): void {
  const from = currentIndex();
  // From nothing, j goes to the first row and k to the last, which is what
  // both vim and every mail client do.
  if (from === -1) {
    focusRow(delta > 0 ? 0 : rowLinks().length - 1);
    return;
  }
  focusRow(from + delta);
}

const searchBox = () =>
  document.querySelector<HTMLInputElement>('input[name="q"]');

function focusSearch(): void {
  const box = searchBox();
  if (!box || box.disabled) return;
  box.focus();
  box.select();
}

/** True when the user is typing, and keystrokes are theirs rather than ours. */
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target.isContentEditable
  );
}

// ---------------------------------------------------------------- help sheet

function helpDialog(): HTMLDialogElement | null {
  return document.querySelector<HTMLDialogElement>("#help");
}

function toggleHelp(): void {
  const dialog = helpDialog();
  if (!dialog) return;
  if (dialog.open) dialog.close();
  else dialog.showModal();
}

// ------------------------------------------------------------ scroll restore

/**
 * Navigation is a full page load, so the list pane would otherwise jump back
 * to the top every time a session is opened.
 *
 * Keyed by the query, because the list holds different rows when filtered and
 * restoring a scroll position across that is meaningless.
 */
function scrollKey(): string {
  const q = new URLSearchParams(location.search).get("q") ?? "";
  return `brightlantern:list-scroll:${q}`;
}

/**
 * Saved as the row at the top of the pane and how far it sat from the top,
 * not as a pixel offset. Sessions arrive between page loads, and the next
 * page renders them, so the same scrollTop would land on different rows.
 */
interface SavedScroll {
  id: string;
  offset: number;
}

/** The first row whose bottom edge is inside the pane. */
function topVisibleRow(pane: HTMLElement): HTMLLIElement | null {
  const top = pane.getBoundingClientRect().top;
  for (const li of pane.querySelectorAll<HTMLLIElement>("#rows > li[data-id]")) {
    if (li.getBoundingClientRect().bottom > top + slack) return li;
  }
  return null;
}

/**
 * Below this many pixels of a row showing, it is out of sight. Row heights
 * are fractional and scrollTop often is not, so a row moved exactly its own
 * height above the pane can leave a hundredth of a pixel inside it -- which
 * counted as visible and kept the arrivals note from ever appearing.
 */
const slack = 1;

/**
 * The row that has to stay where it is when rows arrive: the one with the
 * keyboard cursor, else the one under the pointer, else the top one on
 * screen. Not always the top one: an arrival dated between the top row and
 * the cursor would otherwise push the cursor's row down, and a click aimed at
 * the hovered row would land on its neighbour. Only a row on screen counts,
 * since keeping an off-screen one still would let the visible rows move.
 */
function anchorRow(pane: HTMLElement): HTMLLIElement | null {
  const p = pane.getBoundingClientRect();
  const onScreen = (li: HTMLLIElement | null | undefined): li is HTMLLIElement => {
    if (!li) return false;
    const r = li.getBoundingClientRect();
    return r.bottom > p.top + slack && r.top < p.bottom - slack;
  };
  const focused = document.activeElement?.closest<HTMLLIElement>("#rows > li[data-id]");
  if (onScreen(focused)) return focused;
  const hovered = pane.querySelector<HTMLLIElement>("#rows > li[data-id]:hover");
  if (onScreen(hovered)) return hovered;
  return topVisibleRow(pane);
}

function saveScroll(): void {
  const pane = listPane();
  if (!pane) return;
  const li = topVisibleRow(pane);
  if (!li?.dataset.id) return;
  const saved: SavedScroll = {
    id: li.dataset.id,
    offset: li.getBoundingClientRect().top - pane.getBoundingClientRect().top,
  };
  try {
    sessionStorage.setItem(scrollKey(), JSON.stringify(saved));
  } catch {
    // Private browsing, or a full quota. Losing a scroll position is not
    // worth an error.
  }
}

function restoreScroll(): void {
  const pane = listPane();
  if (!pane) return;
  let saved: SavedScroll | null = null;
  try {
    const raw = sessionStorage.getItem(scrollKey());
    if (raw !== null) saved = JSON.parse(raw) as SavedScroll;
  } catch {
    // Unreadable, or a bare number saved by an older page. Starting at the
    // top once is the whole cost.
    return;
  }
  if (!saved || typeof saved.id !== "string") return;

  const li = rowByID(saved.id);
  if (!li) return;
  pane.scrollTop +=
    li.getBoundingClientRect().top - pane.getBoundingClientRect().top - saved.offset;
}

function rowByID(id: string): HTMLLIElement | null {
  for (const li of document.querySelectorAll<HTMLLIElement>("#rows > li[data-id]")) {
    if (li.dataset.id === id) return li;
  }
  return null;
}

// ---------------------------------------------------------------- arrivals

/**
 * The status poll delivers rows for sessions indexed since the page rendered,
 * in a <template> inside #status. They go into the list where its order puts
 * them -- newest first -- without moving anything someone is looking at.
 *
 * Only the unfiltered list takes them. A search's results are ranked, and
 * whether a new session belongs in them at all is a question for the server
 * (#96); until then, a search page counts arrivals instead and offers to go
 * to them.
 */
function placeArrivals(status: HTMLElement): void {
  const template = status.querySelector<HTMLTemplateElement>("template.arrivals");
  if (!template) return;
  template.remove();

  const list = document.querySelector<HTMLOListElement>("#rows");
  const pane = listPane();
  if (!list || !pane) return;

  // Keyed once per poll: a cold build can deliver a thousand rows into a
  // list of a thousand, and a scan per arrival would be a million lookups.
  const keyed = Array.from(
    list.querySelectorAll<HTMLLIElement>(":scope > li[data-id]"),
    (li) => ({ li, key: sortKey(li) }),
  );
  const have = new Set(keyed.map((k) => k.key.id));

  // The page records its watermark before listing sessions, so one indexed
  // in between arrives already on screen.
  const arrived = Array.from(
    template.content.querySelectorAll<HTMLLIElement>("li[data-id]"),
  ).filter((li) => !have.has(li.dataset.id ?? ""));
  if (arrived.length === 0) return;

  if (!list.hasAttribute("data-browsing")) {
    for (const li of arrived) if (li.dataset.id) unplaced.add(li.dataset.id);
    updateArrivalsNote();
    return;
  }

  // Hold still unless nobody is looking: someone scrolled down, reading with
  // the pointer over the list, or moving through it with the keyboard would
  // otherwise see every row jump down by one. Measured against anchorRow, so
  // a row placed below it costs nothing.
  const hold =
    pane.scrollTop > 0 || pane.matches(":hover") || pane.contains(document.activeElement);
  const anchor = anchorRow(pane);
  const before = anchor?.getBoundingClientRect().top;

  list.querySelector(":scope > li.empty")?.remove();
  for (const li of arrived) {
    li.querySelector(".row")?.classList.add("is-arrived");
    const key = sortKey(li);
    const at = position(keyed, key);
    list.insertBefore(li, keyed[at]?.li ?? null);
    keyed.splice(at, 0, { li, key });
  }

  if (hold && anchor && before !== undefined) {
    pane.scrollTop += anchor.getBoundingClientRect().top - before;
  }
  // Whatever landed above the pane's top edge is out of sight, and gets
  // counted until it is scrolled into view.
  const top = pane.getBoundingClientRect().top;
  for (const li of arrived) {
    if (li.getBoundingClientRect().bottom <= top + slack) watchAbove(li);
  }
}

interface SortKey {
  at: string;
  id: string;
}

/**
 * Where a row with this key goes among rows already in the list's order:
 * started_at descending, then id ascending, as ListSessions sorts.
 * datetime is RFC 3339 in UTC, to the second, so it compares as text.
 */
function position(keyed: { key: SortKey }[], key: SortKey): number {
  let lo = 0;
  let hi = keyed.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    const k = keyed[mid]!.key;
    const after = k.at < key.at || (k.at === key.at && k.id > key.id);
    if (after) hi = mid;
    else lo = mid + 1;
  }
  return lo;
}

function sortKey(li: HTMLLIElement): SortKey {
  return {
    at: li.querySelector("time")?.getAttribute("datetime") ?? "",
    id: li.dataset.id ?? "",
  };
}

// Arrivals that are not on screen: above the pane when browsing, or not in
// the list at all when searching. The note says how many, and goes to them.
const above = new Set<HTMLLIElement>();
const unplaced = new Set<string>();
let aboveObserver: IntersectionObserver | null = null;

function watchAbove(li: HTMLLIElement): void {
  const pane = listPane();
  if (!pane) return;
  aboveObserver ??= new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        // Half in sight, not merely touching: see slack.
        if (entry.intersectionRatio < 0.5) continue;
        above.delete(entry.target as HTMLLIElement);
        aboveObserver?.unobserve(entry.target);
      }
      updateArrivalsNote();
    },
    { root: pane, threshold: 0.5 },
  );
  above.add(li);
  aboveObserver.observe(li);
  updateArrivalsNote();
}

function forgetArrivals(): void {
  aboveObserver?.disconnect();
  above.clear();
  unplaced.clear();
  updateArrivalsNote();
}

function updateArrivalsNote(): void {
  const note = document.querySelector<HTMLElement>(".arrivals-note");
  const button = note?.querySelector("button");
  if (!note || !button) return;

  const n = above.size || unplaced.size;
  note.hidden = n === 0;
  if (n === 0) return;
  const noun = `${n} new session${n === 1 ? "" : "s"}`;
  button.textContent = above.size ? `\u2191 ${noun}` : noun;
}

function onArrivalsNote(): void {
  if (above.size) {
    // The topmost of them, which brings the rest into view on the way.
    const first = Array.from(above).sort((a, b) =>
      a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1,
    )[0];
    first?.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  // From a search, the sessions are in the list a search is not showing.
  location.href = "/";
}

// --------------------------------------------------------------- match scroll

/**
 * Opening a search result lands at the top of a session that may be hundreds
 * of messages long, so the server names the message that matched and we bring
 * it into view.
 *
 * Nothing happens when the attribute is absent, which is the browsing case and
 * the case where a match could not be located.
 */
function scrollToMatch(): void {
  const pane = document.querySelector<HTMLElement>(".reading");
  const id = pane?.dataset.scrollTo;
  if (!pane || !id) return;

  // getElementById rather than querySelector(`#${id}`), which would treat the
  // value as a selector and throw on anything that is not a valid one.
  const target = document.getElementById(id);
  if (!target) return;

  // Centre a match that fits, so it reads with the message before it. A match
  // taller than the pane cannot be centred -- that puts its first line above
  // the fold and lands the reader mid-sentence -- so show its top, nudged down
  // far enough to be visibly not the start of the session.
  const fits = target.getBoundingClientRect().height < pane.clientHeight;
  target.scrollIntoView({ block: fits ? "center" : "start" });
  if (!fits) pane.scrollBy(0, -16);
}

// ------------------------------------------------------------ copy markdown

/**
 * Copies a turn's markdown source, which the server embeds beside the
 * rendered prose in a <template>. Selecting the prose and copying gives the
 * rendered text, with code fences, links and emphasis flattened out of it.
 */
async function copyTurn(button: HTMLButtonElement): Promise<void> {
  const source = button
    .closest(".turn")
    ?.querySelector<HTMLTemplateElement>("template.turn-source");
  if (!source) return;

  let ok = true;
  try {
    await writeClipboard(source.content.textContent ?? "");
  } catch {
    ok = false;
  }
  flash(button, ok ? "is-copied" : "is-failed", ok ? "Copied" : "Copy failed");
}

/**
 * navigator.clipboard exists only in a secure context. 127.0.0.1 is one;
 * `--addr 0.0.0.0:8080` reached by LAN address is not, and there the
 * deprecated execCommand is the only way left.
 */
async function writeClipboard(text: string): Promise<void> {
  if (navigator.clipboard) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  const copied = document.execCommand("copy");
  area.remove();
  if (!copied) throw new Error("execCommand copy refused");
}

const flashTimers = new WeakMap<HTMLElement, number>();

/** Shows a result on the button for a moment, and tells a screen reader. */
function flash(button: HTMLButtonElement, cls: string, label: string): void {
  const original = button.title;
  button.classList.remove("is-copied", "is-failed");
  button.classList.add(cls);
  button.setAttribute("aria-label", label);

  window.clearTimeout(flashTimers.get(button));
  flashTimers.set(
    button,
    window.setTimeout(() => {
      button.classList.remove(cls);
      button.setAttribute("aria-label", original);
    }, 1500),
  );
}

// ------------------------------------------------------------ find in page

/**
 * Find in this session: the app's cmd-F, in a bar at the foot of the reading
 * pane. The app's menu calls open, next, previous and useSelection by name
 * (find.swift); everything else, the bar included, is here.
 *
 * WebKit's own find cannot be pointed at one element, so it matched the
 * session list as well, and it scrolled only the document, which never
 * scrolls here: the reading pane does. So matching is done here, in the
 * reading pane alone, drawn with the CSS Custom Highlight API, which marks
 * ranges without touching the DOM the transcript is made of.
 *
 * Only text you can read matches: an opened tool call's output does, a
 * collapsed one's does not. Nor does any tool call's summary line, which is
 * cut off with an ellipsis -- its text is all in the DOM and counts as
 * visible, so a match could land past the "…" where nobody can see it.
 * Expanding a call on a hit would not rescue those: its output is fetched
 * when it opens, so only the clipped summary could ever have matched.
 */

// Safari stops at 1000 too. A one-letter query in a long session would
// otherwise build tens of thousands of ranges per keystroke.
const findLimit = 1000;

interface FindResult {
  /** 1-based; 0 when nothing matched. */
  current: number;
  total: number;
  /** True when there were more than `total`. */
  more: boolean;
}

let findQuery = "";
let findMatches: Range[] = [];
let findIndex = -1;
let findMore = false;
// Set when the pane changes under a search -- a tool's output arriving, a
// <details> opening -- so the next step searches again instead of walking
// ranges that no longer cover what is on screen.
let findStale = false;
let findObserver: MutationObserver | undefined;

const readingPane = () => document.querySelector<HTMLElement>(".reading");

function findResult(): FindResult {
  return {
    current: findIndex + 1,
    total: findMatches.length,
    more: findMore,
  };
}

/**
 * The pane's rendered text as one string, with where each text node starts
 * in it. Text in different blocks is joined with a newline, so a paragraph
 * ending "foo" and the next starting "bar" do not match "foobar".
 */
function paneText(pane: HTMLElement): { text: string; nodes: Text[]; starts: number[] } {
  const visible = new Map<Element, boolean>();
  const blocks = new Map<Element, Element>();
  const blockOf = (el: Element): Element => {
    let block = blocks.get(el);
    if (!block) {
      block =
        el === pane || !getComputedStyle(el).display.startsWith("inline")
          ? el
          : blockOf(el.parentElement ?? pane);
      blocks.set(el, block);
    }
    return block;
  };

  const nodes: Text[] = [];
  const starts: number[] = [];
  let text = "";
  let lastBlock: Element | undefined;
  const walker = document.createTreeWalker(pane, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const node = n as Text;
    const parent = node.parentElement;
    if (!parent || !node.data) continue;
    let shown = visible.get(parent);
    if (shown === undefined) {
      shown = parent.checkVisibility() && !parent.closest("summary");
      visible.set(parent, shown);
    }
    if (!shown) continue;

    const block = blockOf(parent);
    if (lastBlock && block !== lastBlock) text += "\n";
    lastBlock = block;
    nodes.push(node);
    starts.push(text.length);
    text += node.data;
  }
  return { text, nodes, starts };
}

/** The node holding character `pos`, by binary search over the starts. */
function nodeAt(starts: number[], pos: number): number {
  let lo = 0;
  let hi = starts.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if ((starts[mid] ?? 0) <= pos) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Every match of `query` in the pane, as live ranges. A regular expression
 * rather than lowercasing both sides, because lowercasing can change a
 * string's length and every offset after it.
 */
function collectMatches(pane: HTMLElement, query: string): Range[] {
  const { text, nodes, starts } = paneText(pane);
  const pattern = new RegExp(escapeRegExp(query), "giu");
  const ranges: Range[] = [];
  findMore = false;
  for (const m of text.matchAll(pattern)) {
    if (ranges.length === findLimit) {
      findMore = true;
      break;
    }
    const start = m.index;
    const end = start + m[0].length;
    const a = nodeAt(starts, start);
    // end - 1: the node holding the last character, not the one after it.
    const b = nodeAt(starts, end - 1);
    const startNode = nodes[a];
    const endNode = nodes[b];
    if (!startNode || !endNode) continue;
    const range = document.createRange();
    range.setStart(startNode, start - (starts[a] ?? 0));
    range.setEnd(endNode, end - (starts[b] ?? 0));
    ranges.push(range);
  }
  return ranges;
}

/**
 * Where a search starts: at the old current match while typing, so adding a
 * letter keeps the place when it can, as Safari does; otherwise at the first
 * match not above what is on screen.
 */
function startingIndex(pane: HTMLElement, anchor: Range | undefined): number {
  if (findMatches.length === 0) return -1;
  if (anchor) {
    const i = findMatches.findIndex(
      (r) => r.compareBoundaryPoints(Range.START_TO_START, anchor) >= 0,
    );
    return i === -1 ? 0 : i;
  }
  const top = pane.getBoundingClientRect().top;
  const i = findMatches.findIndex((r) => r.getBoundingClientRect().bottom >= top);
  return i === -1 ? 0 : i;
}

function scrollable(overflow: string): boolean {
  return overflow === "auto" || overflow === "scroll";
}

/**
 * Scrolls every box between the match and the page until it shows: a code
 * block's own horizontal scroll first, then the pane. Centred, as Safari
 * does, but only when it is not already in view.
 */
function revealRange(range: Range): void {
  for (let el = range.startContainer.parentElement; el; el = el.parentElement) {
    const style = getComputedStyle(el);
    const box = el.getBoundingClientRect();
    const r = range.getBoundingClientRect();
    if (scrollable(style.overflowY) && el.scrollHeight > el.clientHeight) {
      if (r.top < box.top || r.bottom > box.bottom) {
        el.scrollTop += r.top - box.top - (el.clientHeight - r.height) / 2;
      }
    }
    if (scrollable(style.overflowX) && el.scrollWidth > el.clientWidth) {
      if (r.left < box.left || r.right > box.right) {
        el.scrollLeft += r.left - box.left - (el.clientWidth - r.width) / 2;
      }
    }
  }
}

/**
 * The two highlights are registered once and changed in place, never
 * replaced. WebKit repaints a range added to or removed from a registered
 * highlight, but not the ranges of one swapped into the registry: cmd-G
 * between two matches already on screen moved the current match and left
 * the old one painted, until something else -- a scroll, the status poll --
 * happened to redraw.
 */
let findAll: Highlight | undefined;
let findCurrent: Highlight | undefined;

function findHighlights(): [Highlight, Highlight] {
  if (!findAll || !findCurrent) {
    findAll = new Highlight();
    findCurrent = new Highlight();
    CSS.highlights.set("find", findAll);
    CSS.highlights.set("find-current", findCurrent);
  }
  return [findAll, findCurrent];
}

/**
 * Makes WebKit repaint all of `el`. Changing a highlight repaints only the
 * height of the glyphs, while its background fills the line, so a match that
 * stopped being current kept a band of the current colour along its top.
 * Opacity is a change WebKit repaints the whole box for, and 0.999 is not a
 * change anyone can see; it is taken off again two frames later.
 */
function repaint(el: Element | null | undefined): void {
  if (!el) return;
  el.classList.add("find-repaint");
  requestAnimationFrame(() =>
    requestAnimationFrame(() => el.classList.remove("find-repaint")),
  );
}

/** The paragraph, list item, heading or code block a match sits in. */
function blockAround(range: Range | undefined): Element | null | undefined {
  const node = range?.commonAncestorContainer;
  const el = node instanceof Element ? node : node?.parentElement;
  return el?.closest("p, li, pre, td, th, h1, h2, h3, h4, h5, h6, blockquote, dt, dd") ?? el;
}

/** Every match afresh, for a new search. */
function paintMatches(): void {
  const [all, current] = findHighlights();
  all.clear();
  current.clear();
  findMatches.forEach((r, i) => (i === findIndex ? current : all).add(r));
  // Matches the last keystroke had and this one does not are anywhere on
  // screen, so the whole pane.
  repaint(readingPane());
}

/** One match handing over to the next: two ranges change, not a thousand. */
function moveCurrent(from: number, to: number): void {
  const [all, current] = findHighlights();
  const before = findMatches[from];
  const after = findMatches[to];
  current.clear();
  if (before) all.add(before);
  if (after) {
    all.delete(after);
    current.add(after);
  }
  repaint(blockAround(before));
  repaint(blockAround(after));
}

function runFind(query: string, anchor: Range | undefined): FindResult {
  const pane = readingPane();
  findQuery = query;
  findStale = false;
  findMatches = pane && query ? collectMatches(pane, query) : [];
  findIndex = pane ? startingIndex(pane, anchor) : -1;

  if (pane && !findObserver) {
    findObserver = new MutationObserver(() => {
      findStale = true;
    });
    findObserver.observe(pane, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
      attributeFilter: ["open"],
    });
  }

  paintMatches();
  const current = findMatches[findIndex];
  if (current) revealRange(current);
  return findResult();
}

/** Closing the bar leaves the current match selected, as Safari does. */
function clearFind(): void {
  const current = findMatches[findIndex];
  findAll?.clear();
  findCurrent?.clear();
  repaint(readingPane());
  if (current) {
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(current);
  }
  findMatches = [];
  findIndex = -1;
}

const findBar = () => document.querySelector<HTMLElement>(".findbar");
const findInput = () => document.querySelector<HTMLInputElement>(".findbar-input");
const findBarOpen = () => findBar()?.hidden === false;

// Safari's wording, with its 1000 cap: past it the count says so rather than
// claiming an exact number.
function matchCount(r: FindResult): string {
  if (!findQuery) return "";
  if (r.total === 0) return "Not found";
  if (r.total === 1 && !r.more) return "1 match";
  return `${r.current} of ${r.more ? `more than ${r.total}` : r.total} matches`;
}

function showCount(r: FindResult): FindResult {
  const count = document.querySelector(".findbar-count");
  if (count) count.textContent = matchCount(r);
  for (const b of document.querySelectorAll<HTMLButtonElement>(".findbar-steps button")) {
    b.disabled = r.total === 0;
  }
  return r;
}

/**
 * Opening another session is a page load, which would close the bar and lose
 * its query; Safari keeps both. So they are kept per tab, like the list's
 * scroll position, and put back when the next page starts.
 */
const findKey = "brightlantern-find";

interface SavedFind {
  open: boolean;
  query: string;
}

function saveFind(): void {
  const saved: SavedFind = { open: findBarOpen(), query: findInput()?.value ?? "" };
  try {
    sessionStorage.setItem(findKey, JSON.stringify(saved));
  } catch {
    // Storage refused: the bar just closes on the next page.
  }
}

function restoreFind(): void {
  const bar = findBar();
  const input = findInput();
  if (!bar || !input) return;
  let saved: SavedFind | null = null;
  try {
    saved = JSON.parse(sessionStorage.getItem(findKey) ?? "null") as SavedFind | null;
  } catch {
    return;
  }
  if (!saved) return;
  input.value = saved.query;
  // Without focus: what was clicked to get here was the list, and keys
  // should go on going there.
  if (saved.open) {
    bar.hidden = false;
    find.search(saved.query);
  }
}

const find = {
  search(query: string): FindResult {
    return showCount(runFind(query, findMatches[findIndex]));
  },

  step(delta: number): FindResult {
    if (findStale) {
      // Back to the current match, or the first after it if it has gone,
      // then on from there: a step after the pane changed still moves.
      runFind(findQuery, findMatches[findIndex]);
    }
    const n = findMatches.length;
    if (n === 0) return showCount(findResult());
    const from = findIndex;
    findIndex = (((findIndex + delta) % n) + n) % n;
    moveCurrent(from, findIndex);
    const current = findMatches[findIndex];
    if (current) revealRange(current);
    return showCount(findResult());
  },

  /** cmd-F. Again with the bar open, it selects the query to type over. */
  open(): void {
    const bar = findBar();
    const input = findInput();
    if (!bar || !input) return;
    if (bar.hidden) {
      bar.hidden = false;
      find.search(input.value);
      saveFind();
    }
    input.focus();
    input.select();
  },

  close(): void {
    const bar = findBar();
    if (!bar || bar.hidden) return;
    bar.hidden = true;
    findInput()?.blur();
    clearFind();
    saveFind();
  },

  /** cmd-G. With nothing to find yet, it opens the bar to ask. */
  next(): void {
    if (findBarOpen() && findInput()?.value) find.step(1);
    else find.open();
  },

  previous(): void {
    if (findBarOpen() && findInput()?.value) find.step(-1);
    else find.open();
  },

  /** cmd-E: the selection becomes the query, without opening the bar. */
  useSelection(): void {
    const text = window.getSelection()?.toString() ?? "";
    const input = findInput();
    if (!text || !input) return;
    input.value = text;
    saveFind();
    if (findBarOpen()) find.search(text);
  },
};

function bindFindBar(): void {
  const input = findInput();
  input?.addEventListener("input", () => {
    find.search(input.value);
    saveFind();
  });
  // Return and shift-Return step, Escape closes: Safari's keys.
  input?.addEventListener("keydown", (event) => {
    if (event.isComposing) return;
    if (event.key === "Enter") {
      event.preventDefault();
      find.step(event.shiftKey ? -1 : 1);
    } else if (event.key === "Escape") {
      event.preventDefault();
      find.close();
    }
  });
  document.querySelector(".findbar-prev")?.addEventListener("click", () => find.step(-1));
  document.querySelector(".findbar-next")?.addEventListener("click", () => find.step(1));
  document.querySelector(".findbar-done")?.addEventListener("click", () => find.close());
}

// ------------------------------------------------------------------ bindings

// gg, as in vim: g is a prefix, and only a second g within the timeout acts.
let pendingG = false;
let pendingGTimer: number | undefined;

function noteG(): void {
  pendingG = true;
  window.clearTimeout(pendingGTimer);
  pendingGTimer = window.setTimeout(() => {
    pendingG = false;
  }, 600);
}

function onKeydown(event: KeyboardEvent): void {
  // Cmd-K reaches the search box from anywhere, including the search box.
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    focusSearch();
    return;
  }

  if (event.key === "Escape") {
    const box = searchBox();
    if (document.activeElement === box && box) {
      box.blur();
      focusRow(Math.max(currentIndex(), 0));
      event.preventDefault();
    } else if (helpDialog()?.open) {
      helpDialog()?.close();
    }
    return;
  }

  // Everything below is a bare key, so it must not fire while typing or
  // while a modifier is held.
  if (isTyping(event.target) || event.metaKey || event.ctrlKey || event.altKey) {
    return;
  }

  const wasPendingG = pendingG;
  if (event.key !== "g") pendingG = false;

  switch (event.key) {
    case "j":
    case "ArrowDown":
      event.preventDefault();
      moveCursor(1);
      break;

    case "k":
    case "ArrowUp":
      event.preventDefault();
      moveCursor(-1);
      break;

    case "g":
      event.preventDefault();
      if (wasPendingG) {
        pendingG = false;
        focusRow(0);
      } else {
        noteG();
      }
      break;

    case "G":
      event.preventDefault();
      focusRow(rowLinks().length - 1);
      break;

    case "/":
      event.preventDefault();
      focusSearch();
      break;

    case "?":
      event.preventDefault();
      toggleHelp();
      break;

    // Enter needs no handler: the cursor is real focus on a real link.
  }
}

function init(): void {
  restoreScroll();
  scrollToMatch();

  // A global because the app reaches it by name. Harmless in a browser,
  // whose own cmd-F never calls it, so the bar never opens there.
  Object.assign(window, { brightlanternFind: find });
  bindFindBar();
  restoreFind();

  document.addEventListener("keydown", onKeydown);

  document
    .querySelector<HTMLButtonElement>("#help-open")
    ?.addEventListener("click", toggleHelp);

  document
    .querySelector<HTMLButtonElement>(".arrivals-note button")
    ?.addEventListener("click", onArrivalsNote);

  // htmx:load fires for each element a swap adds, and the poll's response
  // is a new #status.
  document.body.addEventListener("htmx:load", (event) => {
    const elt = (event as CustomEvent<{ elt: Element }>).detail.elt;
    if (elt instanceof HTMLElement && elt.id === "status") placeArrivals(elt);
  });

  // Delegated, so one listener covers every turn in however long a session.
  document.addEventListener("click", (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const button = target.closest<HTMLButtonElement>("button.turn-copy");
    if (button) void copyTurn(button);
  });

  // pagehide rather than unload: it fires for back/forward cache navigations
  // too, which unload does not.
  window.addEventListener("pagehide", saveScroll);

  // New results are a new list, so start at the top rather than restoring a
  // position that referred to different rows.
  document.body.addEventListener("htmx:afterSwap", (event) => {
    const target = (event as CustomEvent).target;
    if (target instanceof HTMLElement && target.id === "rows") {
      listPane()?.scrollTo({ top: 0 });
      // The server rendered this list after every arrival counted so far,
      // so they are in it, or are not meant to be.
      forgetArrivals();
    }
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
