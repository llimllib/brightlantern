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
