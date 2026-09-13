import './style.css';
import type { Group, Me, Member, Meta, Preferences, Snapshot } from './types';

const app = document.querySelector<HTMLDivElement>('#app')!;
const sheet = document.querySelector<HTMLDialogElement>('#sheet')!;
const toastElement = document.querySelector<HTMLDivElement>('#toast')!;
const tg = window.Telegram?.WebApp;
const icons = {
  smoke: '<svg viewBox="0 0 64 64" fill="none" aria-hidden="true"><path d="M10 39h37v9H10zM47 39h7v9h-7M41 39v9" stroke="currentColor" stroke-width="3" stroke-linejoin="round"/><path class="smoke-trail" d="M45 30c-9-8 7-9 0-18M54 30c-7-6 6-8 1-14" stroke="currentColor" stroke-width="3" stroke-linecap="round"/></svg>',
  gear: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="m9 3-.5 2-2 1-2-.5-2 3 1.5 1.5v3L2.5 15l2 3 2-.5 2 1L9 21h4l.5-2.5 2-1 2 .5 2-3-1.5-2v-3L19.5 8l-2-3-2 .5-2-1L13 3Z"/><circle cx="11" cy="12" r="3"/></svg>',
  arrow: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="m9 5 7 7-7 7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};
let token = '';
let me: Me | undefined;
let meta: Meta = { bot_username: '', check_minutes: 15, answer_minutes: 3 };
let groups: Group[] = [];
let selected = '';
let snapshot: Snapshot | undefined;
let offset = 0;
let busy = false;
let offline = false;
let expired = false;
let appActive = true;
let loadSequence = 0;
let pollTimer: ReturnType<typeof setTimeout>;
let toastTimer: ReturnType<typeof setTimeout>;
let lastFingerprint = '';
let failures = 0;

function escape(value: string | number): string {
  return String(value).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}
function now(): number { return Math.floor(Date.now() / 1000 + offset); }
function elapsed(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  const minutes = Math.floor(total / 60);
  return `${String(minutes).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`;
}
function room(): Group | undefined { return groups.find((g) => g.id === selected); }
function haptic(): void { try { tg?.HapticFeedback?.impactOccurred('medium'); } catch { /* Older Telegram clients. */ } }
function theme(): void {
  const dark = tg?.initData ? tg.colorScheme === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  if (tg?.themeParams.hint_color) document.documentElement.style.setProperty('--telegram-hint', tg.themeParams.hint_color);
  const bg = dark ? '#101512' : '#f6f8f5';
  try { tg?.setHeaderColor(bg); tg?.setBackgroundColor(bg); } catch { /* Theme stays usable without SDK support. */ }
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', bg);
}

class APIError extends Error { constructor(message: string, public status: number) { super(message); } }
async function api<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15000),
  });
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 401 && token) { expired = true; sheet.close(); render(); }
    throw new APIError(result.error || 'Не получилось. Попробуй еще раз.', response.status);
  }
  return result as T;
}
function toast(message: string): void {
  const el = toastElement;
  (sheet.open ? sheet : document.body).append(el);
  el.textContent = message; el.classList.add('visible');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('visible'), 4000);
}
function errorMessage(error: unknown): string { return error instanceof APIError ? error.message : 'Нет связи с курилкой. Попробуй еще раз.'; }
function startBot(): void {
  if (!meta.bot_username) { toast('Не удалось получить ссылку на бота. Обнови страницу.'); return; }
  const url = `https://t.me/${meta.bot_username}?start=app`;
  if (tg?.initData) tg.openTelegramLink(url); else window.location.assign(url);
}

function header(): string {
  return `<header class="topbar"><a class="brand" href="/" aria-label="ВКурилке, главная"><span class="brand-mark">${icons.smoke}</span><span>ВКурилке<span class="brand-dot">.</span></span></a>${me ? `<button class="icon-button" data-action="settings" aria-label="Настройки">${icons.gear}</button>` : '<span class="small-label">СВОИ РЯДОМ</span>'}</header>`;
}
function avatar(m: Member): string {
  const initial = escape(Array.from(m.first_name)[0] || '?');
  return `<span class="avatar avatar-${m.id % 4}">${initial}${m.photo_url ? `<img src="${escape(m.photo_url)}" alt="" loading="lazy" referrerpolicy="no-referrer" />` : ''}<i class="avatar-dot ${m.status}" aria-hidden="true"></i></span>`;
}
function memberRow(m: Member): string {
  const label = { idle: 'Пока не в курилке', going: 'Спускается', smoking: 'В курилке' }[m.status];
  const timer = m.status === 'smoking' ? `<span class="member-time" data-elapsed="${m.started_at}"></span>` : m.status === 'going' ? `<span class="member-time going" data-countdown="${m.target_time}"></span>` : '<span class="idle-dash">—</span>';
  return `<li class="member">${avatar(m)}<div class="member-info"><span class="member-name">${escape(m.first_name)}${m.id === me?.user.id ? '<span class="you">ты</span>' : ''}</span><span class="member-status ${m.status}">${label}</span></div>${timer}</li>`;
}
function render(): void {
  if (expired) {
    app.innerHTML = `<main class="shell">${header()}<section class="welcome"><span class="welcome-art">${icons.smoke}</span><h1>Увидимся<br>в курилке<span>.</span></h1><p>Сессия закончилась. Закрой приложение и открой его заново через бота.</p><button class="primary" data-action="bot">Перейти к боту ↗</button></section></main>`;
    return;
  }
  if (!me) {
    app.innerHTML = `<main class="shell">${header()}<section class="welcome"><span class="eyebrow">ТВОЯ КОМПАНИЯ · ТВОЯ ПАУЗА</span><span class="welcome-art">${icons.smoke}</span><h1>Свои уже<br>на месте<span>.</span></h1><p>Загляни в Telegram, чтобы узнать, кто в курилке, и присоединиться одним нажатием.</p><button class="primary" data-action="bot">Открыть в Telegram ↗</button><span class="footnote">Только по приглашению. Только свои.</span></section></main>`;
    return;
  }
  const g = room();
  if (!g) {
    app.innerHTML = `<main class="shell">${header()}<section class="welcome"><span class="eyebrow">ПРИВЕТ, ${escape(me.user.first_name.toLocaleUpperCase('ru'))}</span><span class="welcome-art">${icons.smoke}</span><h1>Найди свою<br>компанию<span>.</span></h1><p>Попроси приглашение у администратора комнаты. Случайных людей здесь нет.</p><button class="primary" data-action="join">У меня есть приглашение ↗</button>${me.is_admin ? '<button class="secondary" data-action="create">＋ Создать комнату</button>' : ''}<span class="footnote">Твой Telegram ID: ${me.user.id}</span></section></main>`;
    return;
  }
  const active = snapshot?.active;
  const own = active?.group_id === g.id ? active : null;
  const status = own?.status || 'idle';
  const members = snapshot?.members || [];
  const smoking = members.filter((m) => m.status === 'smoking').length;
  const going = members.filter((m) => m.status === 'going').length;
  const inOtherRoom = active && !own;
  const circleContent = status === 'idle' ? `${icons.smoke}<span class="power-label">Я в курилке</span><span class="power-hint">Нажми, когда придешь</span>` : status === 'going' ? `<span class="power-emoji">🏃</span><span class="power-time" data-countdown="${own!.target_time}"></span><span class="power-label">Я уже на месте</span>` : `${icons.smoke}<span class="power-time" data-elapsed="${own!.started_at}"></span><span class="power-label">Я вернулся</span>`;
  app.innerHTML = `<main class="shell">${header()}
    <div class="room-line"><button class="room-picker" data-action="rooms"><span class="room-symbol">⌂</span><span>${escape(g.name)}</span><span class="chevron">⌄</span></button><span class="connection ${offline ? 'lost' : ''}" role="status"><i></i>${offline ? 'Нет связи' : 'На связи'}</span></div>
    ${offline ? '<div class="notice warning">Не удалось обновить статусы. Пробуем подключиться…</div>' : ''}
    ${inOtherRoom ? `<div class="notice">Ты сейчас в «${escape(groups.find((r) => r.id === active.group_id)?.name || 'другой комнате')}». Нажатие перенесет твой статус сюда.</div>` : ''}
    ${!me.user.bot_started ? '<div class="notice">Для проверки присутствия нужно запустить бота. <button class="text-button" data-action="bot">Запустить ↗</button><button class="text-button" data-action="recheck-bot">Я запустил — проверить</button></div>' : ''}
    <section class="hero ${status}"><span class="eyebrow">${status === 'smoking' ? 'ТЫ НА МЕСТЕ' : status === 'going' ? 'СКОРО В КОМПАНИИ' : 'МОМЕНТ ДЛЯ ПАУЗЫ'}</span><h1>${status === 'smoking' ? 'Хорошей компании<span>.</span>' : status === 'going' ? 'Тебя уже ждут<span>.</span>' : 'Выйдем на минутку<span>?</span>'}</h1>
      <div class="power-wrap"><span class="orbit orbit-one"></span><span class="orbit orbit-two"></span><button class="power ${status}" data-action="power" aria-pressed="${status === 'smoking'}" aria-label="${status === 'smoking' ? 'Я вернулся. Выключить статус' : status === 'going' ? 'Я уже на месте' : 'Я в курилке. Включить статус'}" ${busy || !snapshot || !me.user.bot_started ? 'disabled' : ''}>${circleContent}</button></div>
      <div class="hero-caption">${status === 'smoking' ? '<span class="live-dot"></span> Время в курилке · нажми, когда уйдешь' : status === 'going' ? '<button class="text-button muted" data-action="cancel-going">Не пойду</button>' : 'Один клик — и свои знают, где ты'}</div>
    </section>
    ${own?.check_token ? `<section class="check-card" aria-label="Проверка присутствия"><span>🚬</span><div><strong>Все еще в курилке?</strong><p>${own.check_deadline ? `Ждем ответ <span data-countdown="${own.check_deadline}"></span>` : 'Подтверди, что ты еще на месте'}</p></div><button class="small-primary" data-action="confirm-here">Да, я тут</button></section>` : ''}
    <section class="people"><div class="section-heading"><h2>Наша компания <span>${members.length}</span></h2><span class="live-count">${smoking > 0 ? `${smoking} на месте` : going > 0 ? `${going} в пути` : 'Все дома'}</span></div>
      ${!snapshot ? '<div class="empty-state">Обновляем список…</div>' : smoking + going === 0 ? '<div class="empty-state"><span>🌿</span><div><strong>На лавочке пока тихо</strong><p>Будь первым — компания подтянется.</p></div></div>' : ''}
      <ul class="member-list">${members.map(memberRow).join('')}</ul>
    </section><footer><span class="footer-dot"></span> Свои рядом. Без лишних сообщений.</footer>
  </main>`;
  updateTimers();
}
function updateTimers(): void {
  document.querySelectorAll<HTMLElement>('[data-elapsed]').forEach((el) => { el.textContent = elapsed(now() - Number(el.dataset.elapsed)); });
  document.querySelectorAll<HTMLElement>('[data-countdown]').forEach((el) => { el.textContent = elapsed(Number(el.dataset.countdown) - now()); });
}

function openSheet(title: string, body: string): void {
  if (sheet.contains(toastElement)) document.body.append(toastElement);
  sheet.innerHTML = `<div class="sheet-handle"></div><div class="sheet-heading"><h2 id="sheet-title">${escape(title)}</h2><button class="icon-button close" data-action="close" aria-label="Закрыть">×</button></div>${body}`;
  if (!sheet.open) sheet.showModal();
}
function showRooms(): void {
  openSheet('Твои комнаты', `<div class="room-list">${groups.map((g) => `<button class="room-option ${g.id === selected ? 'selected' : ''}" data-action="select-room" data-id="${g.id}"><span class="room-symbol">⌂</span><span>${escape(g.name)}<small>${({ owner: 'Владелец', admin: 'Администратор', member: 'Участник' })[g.role]}</small></span><span>${g.id === selected ? '✓' : '↗'}</span></button>`).join('')}</div><button class="secondary full" data-action="join">＋ Вступить по приглашению</button>${me?.is_admin ? '<button class="primary full" data-action="create">Создать комнату</button>' : ''}${room() ? '<button class="text-button full spaced" data-action="room-settings">Управление текущей комнатой →</button>' : ''}`);
}
function showSettings(): void {
  if (!me) return;
  const options: [keyof Preferences, string, string][] = [['session', 'Карточка сеанса', 'Когда выходят без тебя. В свой сеанс карточка приходит всегда']];
  openSheet('Без лишнего шума', `<p class="sheet-description">Выбери, о чем писать тебе в личку.</p><div class="settings-list">${options.map(([key, title, description]) => `<label class="setting"><span><strong>${title}</strong><small>${description}</small></span><input type="checkbox" data-preference="${key}" role="switch" ${me!.preferences[key] ? 'checked' : ''} /><span class="switch" aria-hidden="true"></span></label>`).join('')}</div><div class="note">🚬 Проверка «Все еще в курилке?» приходит через ${meta.check_minutes} мин. На ответ — ${meta.answer_minutes} мин. Она нужна, чтобы статус оставался актуальным.</div><div class="profile"><span class="profile-initial">${escape(Array.from(me.user.first_name)[0] || '?')}</span><div><strong>${escape(me.user.first_name)}</strong><small>Telegram ID: ${me.user.id}</small></div></div>${room() ? '<button class="secondary full" data-action="room-settings">Настройки комнаты →</button>' : ''}`);
}
function showRoomSettings(): void {
  const g = room(); if (!g) return;
  const canManage = g.role !== 'member';
  const link = g.invite_code ? `https://t.me/${meta.bot_username}?start=${g.invite_code}` : '';
  const members = snapshot?.members || [];
  openSheet(g.name, `${canManage ? `<div class="invite-card"><span class="eyebrow">ПРИГЛАШЕНИЕ ДЛЯ СВОИХ</span><p>Отправь ссылку тому, кого ждете в комнате.</p><input aria-label="Ссылка приглашения" class="input" value="${escape(link)}" readonly /><button class="primary full" data-action="copy-invite">Скопировать ссылку ↗</button><button class="text-button muted full" data-action="rotate-invite">Заменить ссылку приглашения</button></div><form data-form="add-member"><label class="field-label" for="member-id">Добавить или вернуть участника по ID</label><div class="input-row"><input class="input" id="member-id" name="user_id" inputmode="numeric" pattern="[0-9]+" placeholder="Telegram ID" required /><button class="small-primary" type="submit">＋</button></div><p class="field-help">Человек должен сначала запустить бота. ID есть в его настройках.</p></form>` : '<p class="sheet-description">Приглашениями и составом управляют администраторы комнаты.</p>'}
    <h3 class="subheading">Участники · ${members.length}</h3><div class="manage-list">${members.map((m) => {
    const editable = canManage && m.id !== me?.user.id && m.role !== 'owner' && (g.role === 'owner' || m.role !== 'admin');
    return `<div class="manage-member"><div class="member-info"><strong>${escape(m.first_name)}</strong><small>${({ owner: 'Владелец', admin: 'Администратор', member: 'Участник' })[m.role]}</small></div>${editable ? `<button class="text-button" data-action="member-menu" data-id="${m.id}" aria-label="Управление участником ${escape(m.first_name)}">•••</button>` : ''}</div>`;
  }).join('')}</div>${g.role !== 'owner' ? '<button class="text-button danger full spaced" data-action="leave-room">Выйти из комнаты</button>' : ''}`);
}
function joinForm(): void { openSheet('Свои ждут', '<p class="sheet-description">Вставь ссылку или код приглашения от администратора.</p><form data-form="join"><label class="field-label" for="invite">Приглашение</label><input id="invite" class="input" name="code" placeholder="Ссылка или код" autocomplete="off" required /><button class="primary full" type="submit">Вступить в комнату ↗</button></form>'); }
function createForm(): void { openSheet('Новая компания', '<form data-form="create"><label class="field-label" for="room-name">Как назовем комнату?</label><input id="room-name" class="input" name="name" maxlength="60" placeholder="Например, Общага" required /><button class="primary full" type="submit">Создать комнату ↗</button></form>'); }

async function refreshGroups(): Promise<void> {
  groups = await api<Group[]>('/groups');
  if (!groups.some((g) => g.id === selected)) { selected = groups[0]?.id || ''; snapshot = undefined; lastFingerprint = ''; }
}
async function refreshStatus(): Promise<void> {
  if (!selected || expired) return;
  const group = selected; const sequence = ++loadSequence;
  try {
    const state = await api<Snapshot>(`/status?group_id=${group}`);
    if (selected !== group || sequence !== loadSequence) return;
    offset = state.server_time - Date.now() / 1000;
    const fingerprint = JSON.stringify([selected, state.members, state.active]);
    const changed = fingerprint !== lastFingerprint || offline;
    snapshot = state; offline = false; failures = 0; lastFingerprint = fingerprint;
    if (changed) render();
  } catch (error) {
    if (selected !== group || sequence !== loadSequence) return;
    if (error instanceof APIError && error.status === 403) {
      snapshot = undefined; await refreshGroups(); render(); return;
    }
    offline = true; failures++; render();
    if (failures === 1) toast(errorMessage(error));
  }
}
function schedulePoll(): void {
  clearTimeout(pollTimer);
  if (!token || expired) return;
  pollTimer = setTimeout(async () => {
    if (!document.hidden && appActive && !busy) {
      try {
        const previous = JSON.stringify(groups);
        await refreshGroups();
        if (JSON.stringify(groups) !== previous) render();
        await refreshStatus();
      } catch { offline = true; failures++; render(); }
    }
    schedulePoll();
  }, Math.min(30000, 5000 * (1 + failures)));
}
async function mutation(fn: () => Promise<void>): Promise<void> {
  if (busy) return;
  busy = true; haptic();
  document.querySelectorAll<HTMLButtonElement>('button').forEach((el) => { el.disabled = true; });
  try { await fn(); } catch (error) { toast(errorMessage(error)); }
  finally { busy = false; document.querySelectorAll<HTMLButtonElement>('button').forEach((el) => { el.disabled = false; }); render(); schedulePoll(); }
}

document.addEventListener('click', (event) => {
  const button = (event.target as Element).closest<HTMLElement>('[data-action]');
  if (!button) return;
  const action = button.dataset.action;
  if (action === 'bot') { startBot(); return; }
  if (action === 'close') { sheet.close(); return; }
  if (busy) return;
  if (action === 'settings') { showSettings(); return; }
  if (action === 'rooms') { showRooms(); return; }
  if (action === 'join') { joinForm(); return; }
  if (action === 'create') { createForm(); return; }
  if (action === 'room-settings') { showRoomSettings(); return; }
  if (action === 'member-menu') {
    const member = snapshot?.members.find((m) => m.id === Number(button.dataset.id)); if (!member) return;
    openSheet(member.first_name, `${room()?.role === 'owner' ? `<button class="secondary full" data-action="set-role" data-id="${member.id}" data-role="${member.role === 'admin' ? 'member' : 'admin'}">${member.role === 'admin' ? 'Снять права администратора' : 'Назначить администратором'}</button>` : ''}<button class="secondary full danger" data-action="remove-member" data-id="${member.id}">Исключить из комнаты</button><p class="field-help">Исключенный участник не сможет вернуться по старому приглашению.</p>`); return;
  }
  if (action === 'leave-room') {
    openSheet('Выйти из комнаты?', '<p class="sheet-description">Твой активный статус здесь выключится. Для возвращения понадобится приглашение.</p><button class="primary full" data-action="confirm-leave">Да, выйти</button>'); return;
  }
  if (action === 'rotate-invite') {
    openSheet('Заменить приглашение?', '<p class="sheet-description">Предыдущая ссылка перестанет работать. Участники комнаты останутся на месте.</p><button class="primary full" data-action="confirm-rotate">Создать новую ссылку</button>'); return;
  }
  void mutation(async () => {
    const g = room();
    switch (action) {
      case 'recheck-bot': me = await api<Me>('/me'); if (!me.user.bot_started) toast('Отправь боту /start и попробуй еще раз.'); break;
      case 'select-room': selected = button.dataset.id!; snapshot = undefined; lastFingerprint = ''; sheet.close(); await refreshStatus(); break;
      case 'power': {
        if (!g || !snapshot) return;
        const own = snapshot.active?.group_id === g.id ? snapshot.active : null;
        await api('/status/change', 'POST', { group_id: g.id, status: own?.status === 'smoking' ? 'idle' : 'smoking' });
        await refreshStatus(); break;
      }
      case 'cancel-going': if (g) { await api('/status/change', 'POST', { group_id: g.id, status: 'idle' }); await refreshStatus(); } break;
      case 'confirm-here': if (snapshot?.active?.check_token) { await api('/status/confirm', 'POST', { token: snapshot.active.check_token, here: true }); await refreshStatus(); toast('Отлично, ты на месте 🚬'); } break;
      case 'copy-invite': if (g?.invite_code) { const link = `https://t.me/${meta.bot_username}?start=${g.invite_code}`; try { await navigator.clipboard.writeText(link); toast('Приглашение скопировано'); } catch { const input = sheet.querySelector<HTMLInputElement>('input[readonly]'); input?.focus(); input?.select(); toast('Выделили ссылку — скопируй ее'); } } break;
      case 'confirm-rotate': if (g) { await api(`/groups/${g.id}/invite`, 'POST', {}); await refreshGroups(); showRoomSettings(); } break;
      case 'confirm-leave': if (g) { await api(`/groups/${g.id}/membership`, 'DELETE'); sheet.close(); await refreshGroups(); await refreshStatus(); } break;
      case 'remove-member': case 'set-role': if (g) { await api(`/groups/${g.id}/members`, 'POST', { user_id: Number(button.dataset.id), action: action === 'remove-member' ? 'remove' : button.dataset.role }); await refreshStatus(); showRoomSettings(); toast('Состав комнаты обновлен'); } break;
    }
  });
});
document.addEventListener('submit', (event) => {
  const form = event.target as HTMLFormElement;
  if (!form.dataset.form) return;
  event.preventDefault(); const data = new FormData(form);
  void mutation(async () => {
    switch (form.dataset.form) {
      case 'create': {
        const g = await api<Group>('/groups', 'POST', { name: data.get('name') }); selected = g.id; await refreshGroups(); sheet.close(); await refreshStatus(); break;
      }
      case 'join': {
        let code = String(data.get('code') || '').trim();
        if (code.includes('://')) { try { code = new URL(code).searchParams.get('start') || ''; } catch { throw new APIError('Проверь ссылку приглашения', 400); } }
        const g = await api<Group>('/groups/join', 'POST', { code }); selected = g.id; await refreshGroups(); sheet.close(); await refreshStatus(); break;
      }
      case 'add-member': {
        const id = Number(data.get('user_id')); if (!Number.isSafeInteger(id) || id <= 0) throw new APIError('Введи корректный Telegram ID', 400);
        await api(`/groups/${selected}/members`, 'POST', { user_id: id, action: 'add' }); await refreshStatus(); showRoomSettings(); toast('Участник добавлен'); break;
      }
    }
  });
});
document.addEventListener('change', (event) => {
  const input = event.target as HTMLInputElement; const key = input.dataset.preference as keyof Preferences | undefined;
  if (!key || !me) return;
  if (busy) { input.checked = me.preferences[key]; return; }
  const preferences = { ...me.preferences, [key]: input.checked };
  void mutation(async () => { try { me!.preferences = await api<Preferences>('/settings/notifications', 'PUT', preferences); toast('Настройки сохранены'); } finally { input.checked = me!.preferences[key]; } });
});
sheet.addEventListener('click', (event) => { if (event.target === sheet) { const rect = sheet.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) sheet.close(); } });
sheet.addEventListener('close', () => { document.body.append(toastElement); });
document.addEventListener('error', (event) => { if (event.target instanceof HTMLImageElement) event.target.remove(); }, true);
document.addEventListener('visibilitychange', () => { if (!document.hidden && token && !busy) void refreshStatus().finally(schedulePoll); });
window.addEventListener('online', () => { if (token) void refreshStatus().finally(schedulePoll); });
tg?.onEvent('themeChanged', theme);
tg?.onEvent('deactivated', () => { appActive = false; });
tg?.onEvent('activated', () => { appActive = true; if (token && !busy) void refreshStatus().finally(schedulePoll); });
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', theme);
setInterval(() => { if (!document.hidden && appActive) updateTimers(); }, 1000);

async function boot(): Promise<void> {
  theme(); tg?.ready(); tg?.expand();
  try {
    meta = await api<Meta>('/meta');
    if (!tg?.initData) { render(); return; }
    const auth = await api<{ token: string }>('/auth', 'POST', { init_data: tg.initData }); token = auth.token;
    me = await api<Me>('/me');
    selected = new URLSearchParams(location.search).get('room') || '';
    await refreshGroups(); render(); await refreshStatus(); schedulePoll();
  } catch (error) {
    app.innerHTML = `<main class="shell">${header()}<section class="welcome"><span class="welcome-art">${icons.smoke}</span><h1>Пока не на связи<span>.</span></h1><p>${escape(errorMessage(error))}</p><a class="primary" href="/">Попробовать снова ↻</a></section></main>`;
  }
}
void boot();
