'use strict';

const $ = (id) => document.getElementById(id);
const categories = {billing: 'Оплата', access: 'Доступ', technical: 'Техническое', other: 'Другое'};
const statuses = {pending: 'В очереди', processing: 'Обработка', completed: 'Готово', failed: 'Ошибка'};
const priorities = {low: 'Низкий приоритет', medium: 'Средний приоритет', high: 'Высокий приоритет'};
const errorMessages = {
  queue_full: 'Очередь заполнена. Попробуйте чуть позже.',
  ticket_conflict: 'Не удалось сохранить: обращение ещё обрабатывается или запрос уже использован.',
  internal_error: 'Не удалось выполнить операцию. Попробуйте ещё раз.',
  invalid_ticket_id: 'Некорректный номер обращения.',
  ticket_not_found: 'Обращение не найдено.',
  'title must contain 1 to 200 characters': 'Укажите тему длиной до 200 символов.',
  'message must contain 1 to 16384 bytes': 'Укажите сообщение длиной до 16 КиБ.',
  'invalid review category or reply': 'Проверьте категорию и текст ответа. Максимум — 12 КиБ.'
};
const state = {
  selected: null,
  detailSignature: '',
  listSignature: '',
  dirty: false,
  offset: 0,
  refreshing: false,
  refreshRequested: false,
  networkError: false,
  pendingKey: null,
  pendingPayload: ''
};

function notice(text, kind = 'success') {
  $('notice').textContent = text;
  $('notice').dataset.kind = kind;
  $('notice').hidden = !text;
}

async function request(path, options = {}) {
  const response = await fetch(path, options);
  let data;
  try {
    data = await response.json();
  } catch {
    throw new Error('Сервер вернул непонятный ответ. Попробуйте обновить страницу.');
  }
  if (!response.ok) throw new Error(errorMessages[data.error] || data.error || 'Ошибка запроса');
  return data;
}

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}

function date(value) {
  return new Date(value).toLocaleString('ru-RU', {
    day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit'
  });
}

function statusBadge(ticket) {
  const badge = element('span', statuses[ticket.status], 'badge');
  badge.dataset.status = ticket.status;
  return badge;
}

function canLeaveDraft() {
  return !state.dirty || window.confirm('Есть несохранённые изменения в ответе. Закрыть черновик?');
}

function selectTicket(id) {
  if (id === state.selected || !canLeaveDraft()) return;
  state.selected = id;
  state.detailSignature = '';
  state.dirty = false;
  refresh();
}

function renderList(items) {
  const signature = JSON.stringify({items, selected: state.selected, offset: state.offset});
  if (signature === state.listSignature) return;
  state.listSignature = signature;
  $('ticket-list').replaceChildren();
  $('queue-count').textContent = items.length;
  $('queue-count').title = 'Обращений на текущей странице';

  if (!items.length) {
    $('ticket-list').append(element('p', 'По этим фильтрам обращений нет.', 'queue-empty'));
  }
  for (const ticket of items) {
    const row = element('button', undefined, 'ticket-row' + (ticket.id === state.selected ? ' selected' : ''));
    row.type = 'button';
    row.setAttribute('aria-pressed', String(ticket.id === state.selected));

    const top = element('div', undefined, 'row-top');
    top.append(element('span', '#' + ticket.id.slice(0, 8), 'reference'), statusBadge(ticket));
    row.append(top, element('strong', ticket.title));
    row.append(element('p', ticket.message.slice(0, 150), 'ticket-excerpt'));

    const meta = element('div', undefined, 'row-meta');
    meta.append(
      element('span', categories[ticket.review?.category || ticket.analysis?.category] || 'Без категории'),
      element('span', ticket.review?.approved ? 'Подтверждён ✓' : date(ticket.created_at))
    );
    row.append(meta);
    row.addEventListener('click', () => selectTicket(ticket.id));
    $('ticket-list').append(row);
  }
  $('previous').disabled = state.offset === 0;
  $('next').disabled = items.length < 10;
  $('page-label').textContent = String(state.offset / 10 + 1);
}

async function refresh() {
  if (state.refreshing) {
    state.refreshRequested = true;
    return;
  }
  state.refreshing = true;
  try {
    const params = new URLSearchParams({
      limit: '10', offset: String(state.offset),
      category: $('category-filter').value, status: $('status-filter').value
    });
    const data = await request('/api/v1/tickets?' + params);
    if (!state.selected && data.items.length) state.selected = data.items[0].id;
    renderList(data.items);
    if (state.selected) {
      const selected = state.selected;
      const ticket = await request('/api/v1/tickets/' + selected);
      const signature = JSON.stringify(ticket);
      if (selected === state.selected && !state.dirty && signature !== state.detailSignature) {
        renderDetail(ticket);
        state.detailSignature = signature;
      }
    }
    if (state.networkError) {
      notice('');
      state.networkError = false;
    }
  } catch (error) {
    state.networkError = true;
    notice('Не удалось обновить обращения. ' + error.message, 'error');
  } finally {
    state.refreshing = false;
    if (state.refreshRequested) {
      state.refreshRequested = false;
      refresh();
    }
  }
}

function failureMessage(code) {
  if (code === 'provider_http_401' || code === 'provider_http_403') {
    return 'Провайдер отклонил запрос. Проверьте API-ключ и доступ к модели.';
  }
  if (code === 'provider_refusal') return 'Модель отказалась обрабатывать это сообщение.';
  if (code === 'provider_incomplete') return 'Ответ модели оказался неполным. Проверьте лимит выходных токенов.';
  return 'Анализ не завершён. Проверьте настройки провайдера и соединение.';
}

function renderDetail(ticket) {
  $('empty-detail').hidden = true;
  $('detail').hidden = false;
  $('ticket-reference').textContent = '#' + ticket.id.slice(0, 8);
  $('ticket-title').textContent = ticket.title;
  $('ticket-message').textContent = ticket.message;
  $('ticket-status').textContent = statuses[ticket.status];
  $('ticket-status').dataset.status = ticket.status;
  $('ticket-date').textContent = date(ticket.created_at);
  $('analysis').hidden = ticket.status !== 'completed';
  $('waiting').hidden = ticket.status === 'completed';
  $('waiting').textContent = ticket.status === 'failed'
    ? failureMessage(ticket.last_error)
    : 'Сообщение сохранено. Помощник разбирает обращение — результат появится здесь.';

  if (!ticket.analysis) return;
  $('summary').textContent = ticket.analysis.summary;
  $('priority').textContent = priorities[ticket.analysis.priority];
  $('priority').dataset.priority = ticket.analysis.priority;
  $('priority-reason').textContent = ticket.analysis.priority_reason;
  $('original-reply').textContent = ticket.analysis.draft_reply;
  $('review-category').value = ticket.review?.category || ticket.analysis.category;
  $('review-reply').value = ticket.review?.draft_reply || ticket.analysis.draft_reply;
  $('review-status').textContent = ticket.review?.approved ? 'Подтверждён ✓' : 'Черновик';
  $('review-status').dataset.state = ticket.review?.approved ? 'approved' : 'draft';
  $('usage').textContent = ticket.usage.model === 'demo-rules'
    ? 'Демо: анализ по ключевым словам, без обращения к модели.'
    : ticket.usage.model + ' · Токены: ' + ticket.usage.input_tokens + ' вход / ' + ticket.usage.output_tokens + ' выход';
}

function openCreateForm() {
  if (!$('create-dialog').open) $('create-dialog').showModal();
}
$('new-ticket').addEventListener('click', openCreateForm);
$('empty-new-ticket').addEventListener('click', openCreateForm);
$('close-create').addEventListener('click', () => $('create-dialog').close());

$('create-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const input = {title: $('title').value.trim(), message: $('message').value.trim()};
  if (!input.title || !input.message) {
    $('title').setCustomValidity(input.title ? '' : 'Введите тему обращения.');
    $('message').setCustomValidity(input.message ? '' : 'Введите сообщение клиента.');
    $('create-form').reportValidity();
    return;
  }
  if (new TextEncoder().encode(input.message).length > 16384) {
    $('message').setCustomValidity('Сообщение слишком длинное: максимум 16 КиБ.');
    $('create-form').reportValidity();
    return;
  }
  if (!canLeaveDraft()) return;
  const payload = JSON.stringify(input);
  if (payload !== state.pendingPayload || !state.pendingKey) {
    state.pendingKey = crypto.randomUUID();
    state.pendingPayload = payload;
  }
  $('create-button').disabled = true;
  notice('');
  try {
    const ticket = await request('/api/v1/tickets', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Idempotency-Key': state.pendingKey},
      body: payload
    });
    state.selected = ticket.id;
    state.detailSignature = '';
    state.dirty = false;
    state.pendingKey = null;
    state.offset = 0;
    $('category-filter').value = '';
    $('status-filter').value = '';
    $('create-form').reset();
    $('create-dialog').close();
    renderDetail(ticket);
    await refresh();
  } catch (error) {
    notice('Не удалось создать обращение. ' + error.message, 'error');
    $('create-dialog').close();
  } finally {
    $('create-button').disabled = false;
  }
});
for (const id of ['title', 'message']) {
  $(id).addEventListener('input', () => $(id).setCustomValidity(''));
}

async function saveReview(approved) {
  if (!$('review-form').reportValidity()) return;
  const id = state.selected;
  $('save-review').disabled = true;
  $('approve-review').disabled = true;
  try {
    await request('/api/v1/tickets/' + id + '/review', {
      method: 'PATCH',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        category: $('review-category').value,
        draft_reply: $('review-reply').value.trim(),
        approved
      })
    });
    if (state.selected === id) {
      state.dirty = false;
      state.detailSignature = '';
    }
    notice(approved ? 'Ответ подтверждён.' : 'Черновик сохранён.');
    await refresh();
  } catch (error) {
    notice('Не удалось сохранить ответ. ' + error.message, 'error');
  } finally {
    $('save-review').disabled = false;
    $('approve-review').disabled = false;
  }
}

$('review-form').addEventListener('submit', (event) => {
  event.preventDefault();
  saveReview(false);
});
$('approve-review').addEventListener('click', () => saveReview(true));
for (const id of ['review-category', 'review-reply']) {
  $(id).addEventListener('input', () => {
    state.dirty = true;
    $('review-status').textContent = 'Не сохранён';
    $('review-status').dataset.state = 'dirty';
  });
}
for (const id of ['category-filter', 'status-filter']) {
  $(id).addEventListener('change', () => {
    state.offset = 0;
    refresh();
  });
}
$('refresh').addEventListener('click', refresh);
$('previous').addEventListener('click', () => {
  state.offset = Math.max(0, state.offset - 10);
  refresh();
});
$('next').addEventListener('click', () => {
  state.offset += 10;
  refresh();
});
window.addEventListener('beforeunload', (event) => {
  if (state.dirty) {
    event.preventDefault();
    event.returnValue = '';
  }
});
request('/api/v1/config').then((data) => {
  $('provider').textContent = data.provider === 'demo' ? 'Деморежим' : 'Режим LLM';
  $('provider').classList.toggle('demo', data.provider === 'demo');
}).catch(() => notice('Не удалось подключиться к серверу.', 'error'));

refresh();
setInterval(refresh, 2000);
