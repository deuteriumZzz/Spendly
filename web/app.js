// Spendly panel: plain JS module, no build step. Talks to the REST API (/api/*).

const PAGE = 50;
// Fixed order = fixed color slot: a category keeps its color whatever its rank.
const CATEGORIES = {
  'еда': '🍽️', 'транспорт': '🚕', 'жильё': '🏠', 'развлечения': '🎬',
  'здоровье': '💊', 'покупки': '🛍️', 'другое': '📦',
};
const FILTERS = [['user_id', 'f-user'], ['from', 'f-from'], ['to', 'f-to'], ['category', 'f-category'], ['text', 'f-text']];
const PRESET_LABEL = { today: 'сегодня', week: 'за 7 дней', month: 'в этом месяце', prev: 'в прошлом месяце', all: 'за всё время' };

const $ = (id) => document.getElementById(id);
const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

// ---------- formatting ----------

const money0 = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB', maximumFractionDigits: 0 });
const money2 = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB', minimumFractionDigits: 2 });
const rub = (n) => (Math.round(n * 100) % 100 === 0 ? money0 : money2).format(n);
const pct = (x) => `${Math.round(x * 100)}%`;
const plural = (n, [one, few, many]) => {
  const r = new Intl.PluralRules('ru-RU').select(n);
  return r === 'one' ? one : r === 'few' ? few : many;
};
const TRATA = ['трата', 'траты', 'трат'];
const iso = (d) => d.toLocaleDateString('sv-SE'); // YYYY-MM-DD in the browser's zone
const today = () => iso(new Date());
const short = (s) => s.split('-').reverse().slice(0, 2).join('.');
const dayFmt = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long' });
const dayFmtYear = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
const weekday = new Intl.DateTimeFormat('ru-RU', { weekday: 'short' });

function dayTitle(s) {
  if (s === today()) return 'Сегодня';
  const y = new Date();
  y.setDate(y.getDate() - 1);
  if (s === iso(y)) return 'Вчера';
  const d = new Date(`${s}T00:00:00`);
  const fmt = d.getFullYear() === new Date().getFullYear() ? dayFmt : dayFmtYear;
  return `${fmt.format(d)}, ${weekday.format(d)}`;
}

const slot = (category) => Object.keys(CATEGORIES).indexOf(category) + 1;
const initials = (name) =>
  name.split(/\s+/).filter((w) => /^\p{L}/u.test(w)).slice(0, 2).map((w) => w[0].toUpperCase()).join('') || '?';
const label = (e) => `${e.comment || e.category}, ${rub(e.amount)}, ${short(e.spent_at)}`;

const icon = (d) => `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
const ICON = {
  edit: icon('<path d="M4 20h4L19 9l-4-4L4 16v4z"/><path d="M13.5 6.5l4 4"/>'),
  trash: icon('<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>'),
  check: icon('<path d="M5 12.5l4.5 4.5L19 7.5"/>'),
  alert: icon('<circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16.5v.01"/>'),
  system: icon('<circle cx="12" cy="12" r="8.5"/><path d="M12 3.5a8.5 8.5 0 0 1 0 17z" fill="currentColor" stroke="none"/>'),
  light: icon('<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>'),
  dark: icon('<path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/>'),
};

// ---------- state & API ----------

const state = { items: [], users: [], summary: null, byUser: [], editing: null, preset: 'all' };
let loadSeq = 0;

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Ошибка ${res.status}`);
  return data;
}

const filtersActive = () => state.preset !== 'all' || FILTERS.some(([, id]) => $(id).value);

async function load({ append = false, flashId = null } = {}) {
  const seq = ++loadSeq;
  const params = new URLSearchParams({ limit: PAGE, offset: append ? state.items.length : 0 });
  for (const [key, id] of FILTERS) if ($(id).value) params.set(key, $(id).value);
  try {
    const data = await api('GET', `/api/expenses?${params}`);
    if (seq !== loadSeq) return; // a newer filter change already reloaded
    const appendFrom = append ? state.items.length : null;
    state.items = append ? state.items.concat(data.items) : data.items;
    state.summary = data.summary;
    state.byUser = data.by_user;
    render(appendFrom, flashId);
  } catch (e) {
    toast(e.message, { error: true });
  } finally {
    document.body.classList.remove('booting');
  }
}

// ---------- rendering ----------

function render(appendFrom, flashId) {
  renderKPIs();
  renderBars();
  renderRows(appendFrom, flashId);
  $('reset').hidden = !filtersActive();
  const n = FILTERS.filter(([key, id]) => key !== 'from' && key !== 'to' && $(id).value).length
    + (state.preset === 'custom' ? 1 : 0);
  $('filters-toggle').textContent = n ? `Фильтры · ${n}` : 'Фильтры';
}

function periodLabel() {
  if (state.preset !== 'custom') return PRESET_LABEL[state.preset];
  const from = $('f-from').value;
  const to = $('f-to').value;
  if (from && to) return `с ${short(from)} по ${short(to)}`;
  if (from) return `с ${short(from)}`;
  if (to) return `по ${short(to)}`;
  return PRESET_LABEL.all;
}

// countTo animates a number from its last shown value; a newer call cancels an older one.
function countTo(el, to, fmt) {
  const from = Number(el.dataset.value ?? 0);
  el.dataset.value = to;
  cancelAnimationFrame(el._raf);
  if (reducedMotion.matches || from === to) {
    el.textContent = fmt(to);
    return;
  }
  const start = performance.now();
  const step = (now) => {
    const t = Math.min(1, (now - start) / 650);
    const eased = 1 - (1 - t) ** 3;
    el.textContent = fmt(from + (to - from) * eased);
    if (t < 1) el._raf = requestAnimationFrame(step);
  };
  el._raf = requestAnimationFrame(step);
}

function renderKPIs() {
  const s = state.summary;
  countTo($('kpi-total'), s.total, (n) => money0.format(n));
  countTo($('kpi-count'), s.count, (n) => Math.round(n).toLocaleString('ru-RU'));
  countTo($('kpi-avg'), s.count ? s.total / s.count : 0, (n) => money0.format(n));
  $('kpi-period').textContent = periodLabel();
  const members = state.byUser.length;
  $('kpi-members').textContent = members ? `у ${members} ${members === 1 ? 'участника' : 'участников'}` : 'нет трат';

  const top = Object.entries(s.by_category).sort((a, b) => b[1] - a[1])[0];
  if (top) {
    const [category, amount] = top;
    $('kpi-top').innerHTML = `<span class="dot" style="--c: var(--cat-${slot(category)})"></span><span>${CATEGORIES[category] ?? ''} ${esc(category)}</span>`;
    $('kpi-top-sub').textContent = `${rub(amount)} · ${pct(amount / s.total)} всех трат`;
  } else {
    $('kpi-top').textContent = '—';
    $('kpi-top-sub').textContent = 'нет трат за период';
  }
}

function renderBars() {
  const total = state.summary.total;
  const cats = Object.entries(state.summary.by_category).sort((a, b) => b[1] - a[1]);
  barList($('by-category'), cats.map(([c, v]) => ({
    key: c,
    label: `<span class="dot" style="--c: var(--cat-${slot(c)})"></span><span>${CATEGORIES[c] ?? ''} ${esc(c)}</span>`,
    value: v,
    color: `var(--cat-${slot(c)})`,
    tip: `${c}: ${rub(v)} · ${pct(v / total)} всех трат`,
    active: $('f-category').value === c,
  })), (key) => toggleFilter('f-category', key));

  barList($('by-user'), state.byUser.map((u) => ({
    key: String(u.user_id),
    label: `<span class="avatar">${esc(initials(u.name))}</span><span>${esc(u.name)}</span>`,
    value: u.total,
    color: 'var(--accent)',
    tip: `${u.name}: ${rub(u.total)} · ${u.count} ${plural(u.count, TRATA)}`,
    active: $('f-user').value === String(u.user_id),
  })), (key) => toggleFilter('f-user', key));
}

// barList keeps one element per key, so on a filter change bars glide from
// their old width to the new one instead of being rebuilt from zero.
function barList(root, rows, onPick) {
  if (!rows.length) {
    root.innerHTML = '<p class="bars-empty">Нет трат за выбранный период</p>';
    return;
  }
  const max = Math.max(...rows.map((r) => r.value));
  const existing = new Map([...root.querySelectorAll('.bar')].map((el) => [el.dataset.key, el]));
  const els = rows.map((r, i) => {
    let el = existing.get(r.key);
    if (!el) {
      el = document.createElement('button');
      el.type = 'button';
      el.className = 'bar';
      el.dataset.key = r.key;
      el.innerHTML = '<span class="bar-label"></span><span class="bar-value"></span><span class="bar-track"><span class="bar-fill"></span></span>';
      el.addEventListener('click', () => onPick(el.dataset.key));
    }
    el.querySelector('.bar-label').innerHTML = r.label;
    el.querySelector('.bar-value').innerHTML = `<b>${rub(r.value)}</b>`;
    el.style.setProperty('--c', r.color);
    el.style.setProperty('--d', `${i * 45}ms`);
    el.dataset.tip = r.tip;
    el.dataset.target = `${(r.value / max) * 100}%`;
    el.setAttribute('aria-pressed', String(r.active));
    el.setAttribute('aria-label', `${r.tip}. ${r.active ? 'Снять фильтр' : 'Показать только эти траты'}`);
    return el;
  });
  root.replaceChildren(...els);
  void root.offsetWidth; // commit the start widths so new bars grow from zero
  for (const el of els) el.querySelector('.bar-fill').style.setProperty('--w', el.dataset.target);
}

function toggleFilter(id, value) {
  $(id).value = $(id).value === value ? '' : value;
  load();
}

function rowHTML(e, cls, i) {
  const s = slot(e.category);
  return `<tr class="row ${cls}" style="--i:${i}" data-id="${e.id}">
    <td class="c-user"><span class="who"><span class="avatar sm">${esc(initials(e.user_name))}</span><span>${esc(e.user_name)}</span></span></td>
    <td class="c-cat"><span class="cat"><span class="dot" style="--c: var(--cat-${s})"></span>${CATEGORIES[e.category] ?? ''} ${esc(e.category)}</span></td>
    <td class="c-comment">${e.comment ? esc(e.comment) : '<span class="muted">без комментария</span>'}</td>
    <td class="c-amount num">${rub(e.amount)}</td>
    <td class="c-actions">
      <button class="ghost icon-btn" type="button" data-edit="${e.id}" aria-label="Изменить: ${esc(label(e))}">${ICON.edit}</button>
      <button class="ghost icon-btn danger" type="button" data-delete="${e.id}" aria-label="Удалить: ${esc(label(e))}">${ICON.trash}</button>
    </td>
  </tr>`;
}

function renderRows(appendFrom, flashId) {
  const html = [];
  let day = null;
  state.items.forEach((e, i) => {
    if (e.spent_at !== day) {
      day = e.spent_at;
      html.push(`<tr class="day"><th colspan="5" scope="rowgroup">${esc(dayTitle(day))}</th></tr>`);
    }
    // Only rows that just appeared animate in: the first screen on reload, or the new page on «Показать ещё».
    const k = appendFrom == null ? i : i - appendFrom;
    const cls = [k >= 0 && k < 16 ? 'enter' : '', e.id === flashId ? 'flash' : ''].join(' ');
    html.push(rowHTML(e, cls, Math.max(k, 0)));
  });
  $('rows').innerHTML = html.join('');

  const { count } = state.summary;
  $('list-count').textContent = count ? `${count} ${plural(count, TRATA)}` : '';
  const left = count - state.items.length;
  $('more').hidden = left <= 0;
  $('more-btn').textContent = `Показать ещё · осталось ${left}`;

  const empty = state.items.length === 0;
  $('empty').hidden = !empty;
  if (empty) {
    const filtered = filtersActive();
    $('empty-title').textContent = filtered ? 'Ничего не найдено' : 'Пока ни одной траты';
    $('empty-text').textContent = filtered
      ? 'По этим фильтрам трат нет. Попробуйте другой период или сбросьте фильтры.'
      : 'Напишите боту «кофе 350» — трата появится здесь. Или добавьте её вручную.';
    $('empty-action').textContent = filtered ? 'Сбросить фильтры' : 'Добавить трату';
    $('empty-action').onclick = filtered ? resetFilters : () => openForm();
  }
}

// ---------- toasts & tooltip ----------

function toast(text, { error = false, action, onAction } = {}) {
  const el = document.createElement('div');
  el.className = `toast${error ? ' error' : ''}`;
  el.setAttribute('role', error ? 'alert' : 'status');
  el.innerHTML = `<span class="toast-icon">${error ? ICON.alert : ICON.check}</span><span class="toast-text"></span>`;
  el.querySelector('.toast-text').textContent = text;
  const dismiss = () => {
    if (el.classList.contains('out')) return;
    el.classList.add('out');
    setTimeout(() => el.remove(), reducedMotion.matches ? 0 : 200);
  };
  if (action) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'toast-action';
    b.textContent = action;
    b.addEventListener('click', () => { dismiss(); onAction(); });
    el.append(b);
  }
  $('toasts').append(el);
  setTimeout(dismiss, action ? 6000 : 3500);
}

const tip = $('tooltip');
function showTip(el, x, y) {
  tip.textContent = el.dataset.tip;
  tip.hidden = false;
  const r = tip.getBoundingClientRect();
  tip.style.left = `${Math.max(8, Math.min(innerWidth - r.width - 8, x + 14))}px`;
  tip.style.top = `${Math.max(8, y - r.height - 12)}px`;
}
document.addEventListener('pointermove', (ev) => {
  const el = ev.target.closest?.('[data-tip]');
  if (el) showTip(el, ev.clientX, ev.clientY);
  else tip.hidden = true;
});
document.addEventListener('focusin', (ev) => {
  const el = ev.target.closest?.('[data-tip]');
  if (!el) return;
  const r = el.getBoundingClientRect();
  showTip(el, r.left + 12, r.top);
});
document.addEventListener('focusout', () => { tip.hidden = true; });

// ---------- create / edit / delete ----------

const dialog = $('dialog');
const form = $('form');

function openForm(e = null) {
  if (!state.users.length) {
    toast('В семье пока нет участников: задайте FAMILY_MEMBERS или напишите боту', { error: true });
    return;
  }
  state.editing = e;
  form.reset();
  $('form-title').textContent = e ? 'Изменить трату' : 'Новая трата';
  $('form-error').textContent = '';
  form.user_id.value = e ? e.user_id : ($('f-user').value || state.users[0].id);
  form.amount.value = e ? String(e.amount).replace('.', ',') : '';
  form.querySelector(`input[name="category"][value="${e ? e.category : 'еда'}"]`).checked = true;
  form.spent_at.value = e ? e.spent_at : today();
  form.spent_at.max = today();
  form.comment.value = e ? e.comment : '';
  dialog.showModal();
  form.amount.focus();
}

function closeForm() {
  if (!dialog.open || dialog.classList.contains('closing')) return;
  dialog.classList.add('closing');
  const finish = () => {
    if (!dialog.classList.contains('closing')) return;
    dialog.classList.remove('closing');
    dialog.close();
  };
  dialog.addEventListener('animationend', finish, { once: true });
  setTimeout(finish, 400); // in case animationend never fires
}

form.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const data = new FormData(form);
  const body = {
    user_id: Number(data.get('user_id')),
    amount: String(data.get('amount')).trim(),
    category: data.get('category'),
    spent_at: data.get('spent_at'),
    comment: String(data.get('comment')).trim(),
  };
  const save = $('save');
  save.setAttribute('aria-busy', 'true');
  try {
    const editing = state.editing;
    const saved = editing
      ? await api('PATCH', `/api/expenses/${editing.id}`, body)
      : await api('POST', '/api/expenses', body);
    closeForm();
    toast(`${editing ? 'Изменено' : 'Записано'}: ${saved.comment || saved.category} — ${rub(saved.amount)}`);
    load({ flashId: saved.id });
  } catch (e) {
    $('form-error').textContent = e.message;
  } finally {
    save.removeAttribute('aria-busy');
  }
});

async function remove(e) {
  const row = document.querySelector(`tr.row[data-id="${e.id}"]`);
  row?.classList.add('leaving');
  try {
    await api('DELETE', `/api/expenses/${e.id}`);
    toast(`Удалено: ${e.comment || e.category} — ${rub(e.amount)}`, { action: 'Вернуть', onAction: () => restore(e) });
    load();
  } catch (err) {
    row?.classList.remove('leaving');
    toast(err.message, { error: true });
  }
}

// Undo re-creates the expense with the same data (it gets a new id).
async function restore(e) {
  try {
    const back = await api('POST', '/api/expenses', {
      user_id: e.user_id, amount: e.amount, category: e.category, spent_at: e.spent_at, comment: e.comment,
    });
    toast('Трата возвращена');
    load({ flashId: back.id });
  } catch (err) {
    toast(err.message, { error: true });
  }
}

$('rows').addEventListener('click', (ev) => {
  const btn = ev.target.closest('button[data-edit], button[data-delete]');
  if (!btn) return;
  const e = state.items.find((x) => x.id === Number(btn.dataset.edit || btn.dataset.delete));
  if (!e) return;
  if (btn.dataset.edit) openForm(e);
  else remove(e);
});

dialog.addEventListener('cancel', (ev) => { ev.preventDefault(); closeForm(); }); // Esc animates out too
dialog.addEventListener('click', (ev) => { if (ev.target === dialog) closeForm(); }); // backdrop click
$('cancel').addEventListener('click', closeForm);
$('close-x').addEventListener('click', closeForm);
$('add').addEventListener('click', () => openForm());
$('more-btn').addEventListener('click', () => load({ append: true }));

// ---------- filters ----------

// The same periods the bot understands: 7 days including today, month from the 1st.
const PRESETS = {
  today: () => [today(), today()],
  week: () => { const d = new Date(); d.setDate(d.getDate() - 6); return [iso(d), today()]; },
  month: () => { const d = new Date(); return [iso(new Date(d.getFullYear(), d.getMonth(), 1)), today()]; },
  prev: () => { const d = new Date(); return [iso(new Date(d.getFullYear(), d.getMonth() - 1, 1)), iso(new Date(d.getFullYear(), d.getMonth(), 0))]; },
  all: () => ['', ''],
};

function pressPreset(name) {
  state.preset = name;
  for (const b of document.querySelectorAll('[data-preset]')) b.setAttribute('aria-pressed', String(b.dataset.preset === name));
}

function resetFilters() {
  for (const [, id] of FILTERS) $(id).value = '';
  pressPreset('all');
  load();
}

for (const b of document.querySelectorAll('[data-preset]')) {
  b.addEventListener('click', () => {
    [$('f-from').value, $('f-to').value] = PRESETS[b.dataset.preset]();
    pressPreset(b.dataset.preset);
    load();
  });
}
for (const id of ['f-user', 'f-category']) $(id).addEventListener('change', () => load());
for (const id of ['f-from', 'f-to']) $(id).addEventListener('change', () => { pressPreset('custom'); load(); });
let searchTimer;
$('f-text').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(load, 300); });
$('reset').addEventListener('click', resetFilters);
$('filters-toggle').addEventListener('click', () => {
  const open = document.querySelector('.toolbar').classList.toggle('open');
  $('filters-toggle').setAttribute('aria-expanded', String(open));
});

// N — new expense (Т on the Russian layout), / — search.
document.addEventListener('keydown', (ev) => {
  if (dialog.open || ev.metaKey || ev.ctrlKey || ev.altKey || ev.target.closest('input, select, textarea')) return;
  if (ev.key === 'n' || ev.key === 'т') { ev.preventDefault(); openForm(); }
  if (ev.key === '/') { ev.preventDefault(); $('f-text').focus(); }
});

// ---------- theme ----------

const THEMES = ['system', 'light', 'dark'];
const THEME_NAME = { system: 'как в системе', light: 'светлая', dark: 'тёмная' };
let theme = 'system';
try { theme = localStorage.getItem('spendly-theme') || 'system'; } catch { /* private mode */ }

function applyTheme(t) {
  theme = t;
  if (t === 'system') delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
  $('theme').innerHTML = ICON[t];
  $('theme').setAttribute('aria-label', `Тема: ${THEME_NAME[t]}. Переключить`);
  $('theme').title = `Тема: ${THEME_NAME[t]}`;
  try { localStorage.setItem('spendly-theme', t); } catch { /* private mode */ }
}
$('theme').addEventListener('click', () => applyTheme(THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length]));
applyTheme(THEMES.includes(theme) ? theme : 'system');

// ---------- start ----------

(async () => {
  try {
    const [users, categories] = await Promise.all([api('GET', '/api/users'), api('GET', '/api/categories')]);
    state.users = users;
    const userOpts = users.map((u) => `<option value="${u.id}">${esc(u.name)}</option>`).join('');
    $('f-user').insertAdjacentHTML('beforeend', userOpts);
    form.user_id.innerHTML = userOpts;
    $('f-category').insertAdjacentHTML('beforeend',
      categories.map((c) => `<option value="${esc(c)}">${CATEGORIES[c] ?? ''} ${esc(c)}</option>`).join(''));
    $('cat-chips').innerHTML = categories.map((c) => `
      <label class="chip" style="--c: var(--cat-${slot(c)})">
        <input type="radio" name="category" value="${esc(c)}" required>
        <span>${CATEGORIES[c] ?? ''} ${esc(c)}</span>
      </label>`).join('');
  } catch (e) {
    toast(e.message, { error: true });
  }
  load();
})();
