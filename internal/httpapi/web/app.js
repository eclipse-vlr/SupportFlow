'use strict';

const $ = (id) => document.getElementById(id);
const categories = {billing: 'Оплата', access: 'Доступ', technical: 'Техническое', other: 'Другое'};
const statuses = {pending: 'В очереди', processing: 'Обработка', completed: 'Готово', failed: 'Ошибка'};
const priorities = {low: 'Низкий приоритет', medium: 'Средний приоритет', high: 'Высокий приоритет'};
const errorMessages = {
  queue_full: 'Очередь заполнена. Попробуйте чуть позже.',
  ticket_conflict: 'Не удалось сохранить: обращение ещё обрабатывается или запрос уже использован.',
  review_conflict: 'Ответ изменён в другой вкладке. Ваш текст остался в форме. Скопируйте его при необходимости и загрузите сохранённую версию.',
  invalid_expected_revision: 'Обновите страницу перед сохранением ответа.',
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
  saving: false,
  copying: false,
  approvedReply: null,
  reviewRevision: 0,
  historyKey: '',
  historyGeneration: 0,
  historyBefore: 0,
  historyLoaded: false,
  historyLoading: false,
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
  if (!response.ok) {
    const error = new Error(errorMessages[data.error] || data.error || 'Ошибка запроса');
    error.code = data.error;
    throw error;
  }
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

function syncCopyButton() {
  const ready = Boolean(state.approvedReply) && !state.dirty && !state.saving && !state.copying;
  $('copy-review').disabled = !ready;
  $('copy-review').title = ready ? 'Скопировать подтверждённый ответ' : 'Сначала сохраните и подтвердите ответ';
}

function statusBadge(ticket) {
  const badge = element('span', statuses[ticket.status], 'badge');
  badge.dataset.status = ticket.status;
  return badge;
}

function canLeaveDraft() {
  if (state.saving) {
    notice('Дождитесь сохранения ответа.', 'error');
    return false;
  }
  return !state.dirty || window.confirm('Есть несохранённые изменения в ответе. Закрыть черновик?');
}

function selectTicket(id) {
  if (id === state.selected || !canLeaveDraft()) return;
  state.selected = id;
  state.detailSignature = '';
  state.dirty = false;
  state.approvedReply = null;
  syncCopyButton();
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
      if (selected === state.selected && !state.dirty && !state.saving && signature !== state.detailSignature) {
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
  if (code === 'provider_http_402') {
    return 'Недостаточно средств на API-балансе провайдера. Пополните баланс и создайте новое обращение.';
  }
  if (code === 'provider_http_401' || code === 'provider_http_403') {
    return 'Провайдер отклонил запрос. Проверьте API-ключ и доступ к модели.';
  }
  if (code === 'provider_refusal') return 'Модель отказалась обрабатывать это сообщение.';
  if (code === 'provider_incomplete') return 'Ответ модели оказался неполным. Проверьте лимит выходных токенов.';
  return 'Анализ не завершён. Проверьте настройки провайдера и соединение.';
}

function renderDetail(ticket) {
  state.approvedReply = ticket.review?.approved ? ticket.review.draft_reply : null;
  syncCopyButton();
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
  state.reviewRevision = ticket.review_revision;
  $('reload-review').hidden = true;
  $('summary').textContent = ticket.analysis.summary;
  $('priority').textContent = priorities[ticket.analysis.priority];
  $('priority').dataset.priority = ticket.analysis.priority;
  $('priority-reason').textContent = ticket.analysis.priority_reason;
  $('original-reply').textContent = ticket.analysis.draft_reply;
  $('original-category').textContent = 'Категория модели: ' + categories[ticket.analysis.category];
  $('review-category').value = ticket.review?.category || ticket.analysis.category;
  $('review-reply').value = ticket.review?.draft_reply || ticket.analysis.draft_reply;
  $('review-status').textContent = (ticket.review?.approved ? 'Подтверждён ✓' : 'Черновик')
    + (ticket.review_revision ? ' · версия ' + ticket.review_revision : '');
  $('review-status').dataset.state = ticket.review?.approved ? 'approved' : 'draft';
  $('usage').textContent = ticket.usage.model === 'demo-rules'
    ? 'Демо: анализ по ключевым словам, без обращения к модели.'
    : ticket.usage.model.split('/').pop() + ' · Токены: ' + ticket.usage.input_tokens + ' вход / ' + ticket.usage.output_tokens + ' выход';
  const historyKey = ticket.id + ':' + ticket.review_revision;
  if (state.historyKey !== historyKey) {
    state.historyKey = historyKey;
    state.historyGeneration++;
    state.historyBefore = 0;
    state.historyLoaded = false;
    state.historyLoading = false;
    $('history-list').replaceChildren();
    $('history-more').hidden = true;
    $('history-status').textContent = 'Откройте историю, чтобы посмотреть сохранённые версии.';
    if ($('review-history').open) loadHistory();
  }
}

async function loadHistory(older = false) {
  if (state.historyLoading || !state.selected || (!older && state.historyLoaded)) return;
  const id = state.selected;
  const generation = state.historyGeneration;
  state.historyLoading = true;
  $('history-more').disabled = true;
  $('history-status').textContent = 'Загрузка истории…';
  try {
    const params = new URLSearchParams({limit: '10'});
    if (older && state.historyBefore) params.set('before_revision', state.historyBefore);
    const page = await request('/api/v1/tickets/' + id + '/history?' + params);
    if (id !== state.selected || generation !== state.historyGeneration) return;
    for (const entry of page.items) {
      const item = element('details', undefined, 'history-entry');
      const action = entry.kind === 'imported' ? 'Перенесено при обновлении'
        : entry.review.approved ? 'Ответ подтверждён' : 'Черновик сохранён';
      item.append(element('summary', 'Версия ' + entry.revision + ' · ' + action));
      item.append(element('p', new Date(entry.created_at).toLocaleString('ru-RU')
        + ' · ' + categories[entry.review.category], 'muted'));
      item.append(element('p', entry.review.draft_reply, 'preserve'));
      $('history-list').append(item);
    }
    state.historyBefore = page.next_before_revision;
    state.historyLoaded = true;
    $('history-more').textContent = 'Показать более ранние версии';
    $('history-more').hidden = !state.historyBefore;
    $('history-status').textContent = $('history-list').children.length
      ? 'Исходный ответ модели хранится отдельно выше. Здесь — сохранения оператора.'
      : 'Правок пока нет. Первое сохранение появится здесь.';
  } catch (error) {
    if (id !== state.selected || generation !== state.historyGeneration) return;
    $('history-status').textContent = 'Не удалось загрузить историю. ' + error.message;
    $('history-more').textContent = 'Повторить загрузку';
    $('history-more').hidden = false;
  } finally {
    if (generation === state.historyGeneration) {
      state.historyLoading = false;
      $('history-more').disabled = false;
    }
  }
}

$('review-history').addEventListener('toggle', () => {
  if ($('review-history').open) loadHistory();
});
$('history-more').addEventListener('click', () => loadHistory(state.historyLoaded));
$('reload-review').addEventListener('click', () => {
  if (!canLeaveDraft()) return;
  state.dirty = false;
  state.detailSignature = '';
  notice('');
  refresh();
});

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
  if (state.saving || !$('review-form').reportValidity()) return;
  const id = state.selected;
  state.saving = true;
  syncCopyButton();
  $('save-review').disabled = true;
  $('approve-review').disabled = true;
  $('review-category').disabled = true;
  $('review-reply').disabled = true;
  try {
    const saved = await request('/api/v1/tickets/' + id + '/review', {
      method: 'PATCH',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        category: $('review-category').value,
        draft_reply: $('review-reply').value.trim(),
        approved,
        expected_revision: state.reviewRevision
      })
    });
    if (state.selected === id) {
      state.dirty = false;
      state.detailSignature = '';
      renderDetail(saved);
    }
    notice(approved ? 'Ответ подтверждён.' : 'Черновик сохранён.');
    await refresh();
  } catch (error) {
    state.dirty = true;
    if (error.code === 'review_conflict') $('reload-review').hidden = false;
    notice('Не удалось сохранить ответ. ' + error.message, 'error');
  } finally {
    state.saving = false;
    syncCopyButton();
    $('save-review').disabled = false;
    $('approve-review').disabled = false;
    $('review-category').disabled = false;
    $('review-reply').disabled = false;
  }
}

$('review-form').addEventListener('submit', (event) => {
  event.preventDefault();
  saveReview(false);
});
$('approve-review').addEventListener('click', () => saveReview(true));
$('copy-review').addEventListener('click', async () => {
  if (!state.approvedReply || state.dirty || state.saving || state.copying) return;
  const reply = state.approvedReply;
  state.copying = true;
  syncCopyButton();
  try {
    await navigator.clipboard.writeText(reply);
    notice('Подтверждённый ответ скопирован. Его можно вставить в письмо или чат.');
  } catch {
    notice('Не удалось скопировать ответ. Выделите текст и скопируйте его вручную.', 'error');
  } finally {
    state.copying = false;
    syncCopyButton();
  }
});
for (const id of ['review-category', 'review-reply']) {
  $(id).addEventListener('input', () => {
    state.dirty = true;
    syncCopyButton();
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
  const providers = {demo: 'Деморежим', yandex: 'Алиса AI', openai: 'OpenAI'};
  $('provider').textContent = providers[data.provider] || 'Режим LLM';
  $('provider').classList.toggle('demo', data.provider === 'demo');
}).catch(() => notice('Не удалось подключиться к серверу.', 'error'));

refresh();
setInterval(refresh, 2000);
