'use strict';

const POLL_ACTIVE_MS = 1000;
const POLL_IDLE_MS = 5000;

const STATE_LABELS = {
  queued: 'в очереди',
  running: 'качается',
  done: 'готово',
  failed: 'ошибка',
  cancelled: 'отменено',
};

const state = {
  query: '',
  page: 1,
  manga: null,
  chapters: [],
  selected: new Set(),
  lastClicked: -1,
  pollTimer: null,
  openWarnings: new Set(),
};

const $ = (id) => document.getElementById(id);

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (key === 'class') node.className = value;
    else if (key === 'dataset') Object.assign(node.dataset, value);
    else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (key in node) node[key] = value;
    else node.setAttribute(key, value);
  }
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : String(child));
  }
  return node;
}

async function api(path, options = {}) {
  const headers = { Accept: 'application/json' };
  if (options.body) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, { ...options, headers });
  let payload;
  try {
    payload = await res.json();
  } catch {
    throw new Error(`HTTP ${res.status}`);
  }
  if (!res.ok || !payload.ok) throw new Error(payload.error || `HTTP ${res.status}`);
  return payload.data;
}

function showStatus(message, kind = 'info') {
  const box = $('status');
  box.textContent = message;
  box.className = `status status-${kind}`;
  box.hidden = false;
}

function hideStatus() {
  $('status').hidden = true;
}

function showView(name) {
  $('results-view').hidden = name !== 'results';
  $('manga-view').hidden = name !== 'manga';
}

function formatSize(bytes) {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} КБ`;
  return `${(bytes / 1024 / 1024).toFixed(1)} МБ`;
}

// ---------- search ----------

async function search(query, page = 1) {
  state.query = query;
  state.page = page;
  showView('results');
  showStatus('Ищу…');
  try {
    const data = await api(`/api/search?q=${encodeURIComponent(query)}&page=${page}`);
    renderResults(data.items, page > 1);
    $('more').hidden = !data.hasNext;
    if (!data.items.length && page === 1) showStatus('Ничего не найдено', 'warn');
    else hideStatus();
  } catch (err) {
    showStatus(err.message, 'error');
  }
}

function renderResults(items, append) {
  const grid = $('results');
  if (!append) grid.replaceChildren();
  for (const item of items) {
    const meta = [item.type, item.year, item.status, item.age].filter(Boolean).join(' · ');
    grid.append(el('button', { class: 'card', type: 'button', onclick: () => openManga(item.slug) },
      item.cover ? el('img', { src: item.cover, alt: '', loading: 'lazy' }) : el('div', { class: 'no-cover' }),
      el('div', { class: 'card-body' },
        el('strong', {}, item.title),
        item.altTitle ? el('span', { class: 'muted' }, item.altTitle) : null,
        el('span', { class: 'meta' }, meta),
        item.licensed ? el('span', { class: 'badge badge-warn' }, 'лицензия: главы могут быть удалены') : null,
      ),
    ));
  }
}

// ---------- manga ----------

async function openManga(slug) {
  if (location.hash !== `#/manga/${slug}`) {
    location.hash = `#/manga/${slug}`;
    return; // hashchange handler calls openManga again
  }
  showView('manga');
  $('chapters').replaceChildren();
  showStatus('Загружаю список глав…');
  try {
    const data = await api(`/api/manga/${encodeURIComponent(slug)}`);
    state.manga = data.manga;
    state.chapters = data.chapters;
    state.branches = data.branches;
    state.selected = new Set(data.chapters.map((c) => c.id));
    state.lastClicked = -1;
    renderManga();
    if (!data.chapters.length) showStatus('У тайтла нет доступных глав', 'warn');
    else hideStatus();
  } catch (err) {
    showStatus(err.message, 'error');
  }
}

function renderManga() {
  const m = state.manga;
  const cover = $('m-cover');
  cover.hidden = !m.cover;
  if (m.cover) cover.src = m.cover;
  $('m-title').textContent = m.title;
  $('m-alt').textContent = [m.altTitle, ...(m.otherNames || [])].filter(Boolean).join(' / ');
  $('m-meta').textContent = [m.type, m.year, m.status, m.age].filter(Boolean).join(' · ');
  const people = [];
  if (m.authors.length) people.push(`Автор: ${m.authors.join(', ')}`);
  if (m.artists.length) people.push(`Художник: ${m.artists.join(', ')}`);
  $('m-people').textContent = people.join(' · ');
  $('m-genres').replaceChildren(...m.genres.map((g) => el('span', { class: 'tag' }, g)));
  $('m-summary').textContent = m.summary || 'Нет описания';
  $('m-link').href = m.url;

  const branch = $('opt-branch');
  branch.replaceChildren(el('option', { value: '' }, 'Авто (самый полный)'));
  for (const b of state.branches) {
    branch.append(el('option', { value: String(b.key) }, `${b.teams} — ${b.chapters} гл.`));
  }
  branch.disabled = state.branches.length < 2;
  renderChapters();
}

function selectedBranch() {
  const value = $('opt-branch').value;
  return value === '' ? null : Number(value);
}

function chapterLabel(c) {
  return c.name ? `Глава ${c.number} — ${c.name}` : `Глава ${c.number}`;
}

function groupByVolume(chapters) {
  const groups = new Map();
  chapters.forEach((chapter, index) => {
    if (!groups.has(chapter.volume)) groups.set(chapter.volume, []);
    groups.get(chapter.volume).push({ chapter, index });
  });
  return groups;
}

function renderChapters() {
  const branch = selectedBranch();
  const list = $('chapters');
  list.replaceChildren();
  for (const [volume, items] of groupByVolume(state.chapters)) {
    const volBox = el('input', { type: 'checkbox', dataset: { volume } });
    volBox.addEventListener('change', () => toggleVolume(volume, volBox.checked));
    const rows = items.map(({ chapter, index }) => {
      const box = el('input', { type: 'checkbox', checked: state.selected.has(chapter.id), dataset: { id: String(chapter.id) } });
      box.addEventListener('click', (e) => onChapterClick(e, index, box.checked));
      const missing = branch !== null && !chapter.branches.includes(branch);
      return el('label', { class: 'chapter' }, box, chapterLabel(chapter),
        missing ? el('span', { class: 'badge badge-warn' }, 'другой перевод') : null);
    });
    list.append(el('div', { class: 'volume' },
      el('label', { class: 'volume-head' }, volBox, `Том ${volume}`, el('span', { class: 'muted' }, ` (${items.length} гл.)`)),
      el('div', { class: 'volume-chapters' }, ...rows),
    ));
  }
  updateSelectionUI();
}

function onChapterClick(event, index, checked) {
  if (event.shiftKey && state.lastClicked >= 0) {
    const [from, to] = [Math.min(state.lastClicked, index), Math.max(state.lastClicked, index)];
    for (let i = from; i <= to; i++) setSelected(state.chapters[i].id, checked);
  } else {
    setSelected(state.chapters[index].id, checked);
  }
  state.lastClicked = index;
  syncCheckboxes();
}

function setSelected(id, checked) {
  if (checked) state.selected.add(id);
  else state.selected.delete(id);
}

function toggleVolume(volume, checked) {
  for (const c of state.chapters) if (c.volume === volume) setSelected(c.id, checked);
  syncCheckboxes();
}

function syncCheckboxes() {
  for (const box of $('chapters').querySelectorAll('input[data-id]')) {
    box.checked = state.selected.has(Number(box.dataset.id));
  }
  updateSelectionUI();
}

function updateSelectionUI() {
  for (const [volume, items] of groupByVolume(state.chapters)) {
    const box = $('chapters').querySelector(`input[data-volume="${CSS.escape(volume)}"]`);
    if (!box) continue;
    const count = items.filter(({ chapter }) => state.selected.has(chapter.id)).length;
    box.checked = count === items.length;
    box.indeterminate = count > 0 && count < items.length;
  }
  const n = state.selected.size;
  $('sel-count').textContent = `Выбрано: ${n} из ${state.chapters.length}`;
  $('download').disabled = n === 0;
}

async function startDownload() {
  const ids = state.chapters.filter((c) => state.selected.has(c.id)).map((c) => c.id);
  if (!ids.length) return;
  const body = {
    slug: state.manga.slug,
    chapterIds: ids.length === state.chapters.length ? [] : ids,
    branch: selectedBranch(),
    mode: $('opt-mode').value,
    format: $('opt-format').value,
  };
  const button = $('download');
  button.disabled = true;
  try {
    await api('/api/jobs', { method: 'POST', body: JSON.stringify(body) });
    showStatus('Загрузка добавлена в очередь', 'ok');
    refreshJobs();
  } catch (err) {
    showStatus(err.message, 'error');
  } finally {
    button.disabled = false;
  }
}

// ---------- jobs ----------

async function refreshJobs() {
  clearTimeout(state.pollTimer);
  let active = false;
  try {
    const jobs = await api('/api/jobs');
    renderJobs(jobs);
    active = jobs.some((j) => j.state === 'queued' || j.state === 'running');
  } catch {
    // server may be restarting; keep polling
  }
  state.pollTimer = setTimeout(refreshJobs, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
}

function renderJobs(jobs) {
  const list = $('jobs-list');
  if (!jobs.length) {
    list.replaceChildren(el('p', { class: 'muted' }, 'Пока пусто'));
    return;
  }
  list.replaceChildren(...jobs.map(jobCard));
}

function jobCard(job) {
  const running = job.state === 'queued' || job.state === 'running';
  const progress = job.totalChapters
    ? el('progress', { max: job.totalChapters, value: job.doneChapters })
    : el('progress');
  const current = job.state === 'running' && job.currentChapter
    ? `${job.currentChapter}: стр. ${job.donePages}/${job.currentPages}`
    : '';

  return el('div', { class: `job job-${job.state}` },
    el('div', { class: 'job-head' },
      el('strong', {}, job.title),
      el('span', { class: `badge badge-${job.state}` }, STATE_LABELS[job.state] || job.state),
    ),
    running || job.totalChapters ? progress : null,
    el('div', { class: 'meta' }, job.totalChapters ? `Глав: ${job.doneChapters}/${job.totalChapters}` : '', current ? ` · ${current}` : ''),
    job.error ? el('div', { class: 'error' }, job.error) : null,
    job.files.length ? el('ul', { class: 'files' }, ...job.files.map((f, i) => el('li', {},
      el('a', { href: `/api/jobs/${encodeURIComponent(job.id)}/files/${i}`, download: f.name }, f.name),
      el('span', { class: 'muted' }, ` ${formatSize(f.size)}${f.skipped ? ', уже был' : ''}`),
    ))) : null,
    job.dir && job.state === 'done' ? el('div', { class: 'muted path' }, job.dir) : null,
    job.warnings.length ? warningsBlock(job) : null,
    running ? el('button', { type: 'button', class: 'link', onclick: () => cancelJob(job.id) }, 'Отменить') : null,
  );
}

function warningsBlock(job) {
  const details = el('details', { open: state.openWarnings.has(job.id) },
    el('summary', {}, `Предупреждения: ${job.warnings.length}`),
    el('ul', {}, ...job.warnings.map((w) => el('li', {}, w))),
  );
  details.addEventListener('toggle', () => {
    if (details.open) state.openWarnings.add(job.id);
    else state.openWarnings.delete(job.id);
  });
  return details;
}

async function cancelJob(id) {
  try {
    await api(`/api/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
  } catch (err) {
    showStatus(err.message, 'error');
  }
  refreshJobs();
}

// ---------- wiring ----------

function route() {
  const match = location.hash.match(/^#\/manga\/(.+)$/);
  if (match) {
    openManga(decodeURIComponent(match[1]));
  } else {
    showView('results');
  }
}

$('search-form').addEventListener('submit', (e) => {
  e.preventDefault();
  const query = $('search-input').value.trim();
  if (!query) return;
  if (location.hash) history.pushState(null, '', location.pathname);
  search(query);
});
$('more').addEventListener('click', () => search(state.query, state.page + 1));
$('back').addEventListener('click', () => {
  history.pushState(null, '', location.pathname);
  hideStatus();
  showView('results');
});
$('sel-all').addEventListener('click', () => {
  state.selected = new Set(state.chapters.map((c) => c.id));
  syncCheckboxes();
});
$('sel-none').addEventListener('click', () => {
  state.selected.clear();
  syncCheckboxes();
});
$('opt-branch').addEventListener('change', renderChapters);
$('download').addEventListener('click', startDownload);
window.addEventListener('hashchange', route);

route();
refreshJobs();
