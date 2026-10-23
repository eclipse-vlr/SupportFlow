'use strict';

const $ = (id) => document.getElementById(id);
const categories = {billing: 'Оплата', access: 'Доступ', technical: 'Техническое', other: 'Другое'};
const statuses = {pending: 'В очереди', processing: 'Обработка', completed: 'Разбор готов', failed: 'Ошибка'};
const priorities = {low: 'Низкий приоритет', medium: 'Средний приоритет', high: 'Высокий приоритет'};
const errorMessages = {
  queue_full: 'Очередь заполнена. Попробуйте чуть позже.',
  ticket_conflict: 'Не удалось сохранить: обращение ещё обрабатывается или запрос уже использован.',
  review_conflict: 'Ответ изменён в другой вкладке. Ваш текст остался в форме. Скопируйте его при необходимости и загрузите сохранённую версию.',
  ticket_version_conflict: 'Обращение изменилось в другой вкладке. Загрузите актуальную версию перед повторным сохранением.',
  ticket_closed: 'Обращение закрыто. Загрузите актуальную версию и откройте его снова, чтобы изменить ответ.',
  approval_required: 'Сначала сохраните и подтвердите ответ.',
  invalid_feedback: 'Выберите оценку и укажите причину, если пришлось переписать ответ. Максимум — 1000 символов.',
  invalid_search: 'Поиск ограничен 200 символами.',
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
  feedbackDirty: false,
  feedbackVersion: 0,
  ticket: null,
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
  const badge = element('span', ticket.closed_at ? 'Закрыто' : statuses[ticket.status], 'badge');
  badge.dataset.status = ticket.closed_at ? 'closed' : ticket.status;
  return badge;
}

function canLeaveDraft() {
  if (state.saving) {
    notice('Дождитесь сохранения ответа.', 'error');
    return false;
  }
  return !(state.dirty || state.feedbackDirty) || window.confirm('Есть несохранённые изменения в ответе или оценке. Перейти без сохранения?');
}

function selectTicket(id) {
  if (id === state.selected || !canLeaveDraft()) return;
  state.selected = id;
  state.detailSignature = '';
  state.dirty = false;
  state.feedbackDirty = false;
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
  $('selection-note').hidden = !state.selected || items.some((item) => item.id === state.selected);

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

function listParams() {
  return new URLSearchParams({
    limit: '10', offset: String(state.offset),
    category: $('category-filter').value, status: $('status-filter').value,
    view: $('view-filter').value, q: $('ticket-search').value.trim()
  }).toString();
}

async function refresh() {
  if (state.refreshing) {
    state.refreshRequested = true;
    return;
  }
  state.refreshing = true;
  try {
    const params = listParams();
    const data = await request('/api/v1/tickets?' + params);
    if (params !== listParams()) { state.refreshRequested = true; return; }
    if (!state.selected && data.items.length) state.selected = data.items[0].id;
    renderList(data.items);
    if (state.selected) {
      const selected = state.selected;
      const ticket = await request('/api/v1/tickets/' + selected);
      const signature = JSON.stringify(ticket);
      if (selected === state.selected && !state.dirty && !state.feedbackDirty && !state.saving && signature !== state.detailSignature) {
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
  if (code === 'provider_reply_needs_editing') return 'Алиса дважды вернула черновик со служебными пометками, разметкой или неподтверждёнными обещаниями. Такой ответ не сохранён. Уточните сообщение и создайте новое обращение.';
  return 'Анализ не завершён. Проверьте настройки провайдера и соединение.';
}

function renderSources(ticket) {
  const sources = ticket.analysis.sources || [];
  $('source-list').replaceChildren();
  $('source-list').hidden = sources.length === 0;
  $('sources-note').textContent = sources.length
    ? 'Эти статьи вошли в запрос к модели. Сверьте с ними черновик.'
    : 'У этого разбора нет сохранённых источников.';
  for (const source of sources) {
    const link = element('a', source.title);
    link.href = '/api/v1/tickets/' + encodeURIComponent(ticket.id) + '/sources/' + encodeURIComponent(source.id);
    link.addEventListener('click', (event) => {
      if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      $('source-heading').textContent = source.title;
      $('source-content').textContent = source.content;
      $('source-version').textContent = 'Сохранённая версия · ' + source.version.slice(0, 12);
      $('source-dialog').showModal();
    });
    const row = element('li');
    row.append(link);
    $('source-list').append(row);
  }
}

$('close-source').addEventListener('click', () => $('source-dialog').close());

function syncActions() {
  const closed = Boolean(state.ticket?.closed_at);
  $('save-review').disabled = state.saving || closed;
  $('approve-review').disabled = state.saving || closed;
  $('review-category').disabled = state.saving || closed;
  $('review-reply').readOnly = state.saving || closed;
  $('close-ticket').disabled = state.saving || state.dirty || state.feedbackDirty || (!closed && !state.ticket?.review?.approved);
  $('close-ticket').textContent = closed ? 'Открыть снова' : 'Закрыть обращение';
  $('feedback-rating').disabled = state.saving;
  $('feedback-reason').disabled = state.saving;
  $('save-feedback').disabled = state.saving || !state.feedbackDirty;
  $('cancel-feedback').disabled = state.saving || !state.feedbackDirty;
  $('reload-feedback').disabled = state.saving;
  $('reload-review').disabled = state.saving;
  $('new-ticket').disabled = state.saving;
  $('empty-new-ticket').disabled = state.saving;
  $('title').disabled = state.saving;
  $('message').disabled = state.saving;
  for (const button of document.querySelectorAll('[data-example]')) button.disabled = state.saving;
  syncCopyButton();
}

function syncFeedbackReason() {
  const needed = $('feedback-rating').value === 'rewritten';
  $('feedback-reason').required = needed;
  $('feedback-reason-label').textContent = needed ? 'Почему пришлось переписать?' : 'Причина (необязательно)';
}

function renderDetail(ticket, {preserveReview = false, preserveFeedback = false} = {}) {
  const sameFeedback = JSON.stringify(state.ticket?.feedback || null) === JSON.stringify(ticket.feedback || null);
  if (!preserveFeedback || sameFeedback) state.feedbackVersion = ticket.version;
  state.ticket = ticket;
  state.approvedReply = ticket.review?.approved ? ticket.review.draft_reply : null;
  syncCopyButton();
  $('empty-detail').hidden = true;
  $('detail').hidden = false;
  $('ticket-reference').textContent = '#' + ticket.id.slice(0, 8);
  $('ticket-title').textContent = ticket.title;
  $('ticket-message').textContent = ticket.message;
  $('ticket-status').textContent = ticket.closed_at ? 'Закрыто' : statuses[ticket.status];
  $('ticket-status').dataset.status = ticket.closed_at ? 'closed' : ticket.status;
  $('ticket-date').textContent = date(ticket.created_at);
  $('analysis').hidden = ticket.status !== 'completed';
  $('waiting').hidden = ticket.status === 'completed';
  $('waiting').textContent = ticket.status === 'failed'
    ? failureMessage(ticket.last_error)
    : 'Сообщение сохранено. Помощник разбирает обращение — результат появится здесь.';

  if (!ticket.analysis) { syncActions(); return; }
  $('summary').textContent = ticket.analysis.summary;
  $('priority').textContent = priorities[ticket.analysis.priority];
  $('priority').dataset.priority = ticket.analysis.priority;
  $('priority-reason').textContent = ticket.analysis.priority_reason;
  $('original-reply').textContent = ticket.analysis.draft_reply;
  $('original-category').textContent = 'Категория модели: ' + categories[ticket.analysis.category];
  renderSources(ticket);
  if (!preserveReview) {
    state.reviewRevision = ticket.review_revision;
    $('reload-review').hidden = true;
    $('review-category').value = ticket.review?.category || ticket.analysis.category;
    $('review-reply').value = ticket.review?.draft_reply || ticket.analysis.draft_reply;
    $('review-status').textContent = (ticket.review?.approved ? 'Подтверждён ✓' : 'Черновик')
      + (ticket.review_revision ? ' · версия ' + ticket.review_revision : '');
    $('review-status').dataset.state = ticket.review?.approved ? 'approved' : 'draft';
  }
  $('closure-status').textContent = ticket.closed_at ? 'Закрыто ' + date(ticket.closed_at) + '. Для изменения ответа откройте обращение снова.'
    : ticket.review?.approved ? 'Ответ подтверждён. Обращение можно закрыть.' : 'Чтобы закрыть обращение, сначала подтвердите ответ.';
  const ratings = {useful: 'Полезен', rewritten: 'Пришлось переписать'};
  $('feedback-heading').textContent = 'Оценка черновика' + (ticket.feedback ? ' · ' + ratings[ticket.feedback.rating] : '');
  if (!preserveFeedback) {
    $('feedback-rating').value = ticket.feedback?.rating || '';
    $('feedback-reason').value = ticket.feedback?.reason || '';
    $('feedback-status').textContent = ticket.feedback ? 'Оценка сохранена.' : 'Оценка ещё не сохранена.';
    $('reload-feedback').hidden = true;
    syncFeedbackReason();
  } else if (!sameFeedback) {
    $('feedback-status').textContent = 'Сохранённая оценка изменилась в другой вкладке. Ваши изменения остались в форме.';
    $('reload-feedback').hidden = false;
  }
  syncActions();
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
  state.feedbackDirty = false;
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

const examples = {
  billing: {title: 'Двойное списание за подписку', message: 'За один месяц подписки дважды списали деньги. Какие данные нужны для проверки платежей?'},
  access: {title: 'Не приходит письмо для входа', message: 'Запросил письмо для восстановления доступа, но оно не пришло. Проверил папку со спамом. Что ещё можно уточнить?'},
  technical: {title: 'Ошибка при выгрузке отчёта', message: 'При выгрузке отчёта за сентябрь в CSV появляется ошибка E-500. В другом браузере результат тот же.'},
  other: {title: 'Пожелание к интерфейсу', message: 'Было бы удобнее переключать язык интерфейса. Планируете добавить испанский?'}
};
for (const button of document.querySelectorAll('[data-example]')) {
  button.addEventListener('click', () => {
    if (($('title').value.trim() || $('message').value.trim()) && !window.confirm('Заменить текст в форме выбранным примером?')) return;
    const example = examples[button.dataset.example];
    $('title').value = example.title;
    $('message').value = example.message;
    $('title').setCustomValidity('');
    $('message').setCustomValidity('');
    $('title').focus();
  });
}

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
  state.saving = true;
  syncActions();
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
    state.feedbackDirty = false;
    state.pendingKey = null;
    state.offset = 0;
    $('category-filter').value = '';
    $('status-filter').value = '';
    $('view-filter').value = 'open';
    $('ticket-search').value = '';
    $('create-form').reset();
    $('create-dialog').close();
    renderDetail(ticket);
    await refresh();
  } catch (error) {
    notice('Не удалось создать обращение. ' + error.message, 'error');
    $('create-dialog').close();
  } finally {
    $('create-button').disabled = false;
    state.saving = false;
    syncActions();
  }
});
for (const id of ['title', 'message']) {
  $(id).addEventListener('input', () => $(id).setCustomValidity(''));
}

async function saveReview(approved) {
  if (state.saving || state.ticket?.closed_at || !$('review-form').reportValidity()) return;
  const id = state.selected;
  state.saving = true;
  syncActions();
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
      renderDetail(saved, {preserveFeedback: state.feedbackDirty});
    }
    notice(approved ? 'Ответ подтверждён.' : 'Черновик сохранён.');
    await refresh();
  } catch (error) {
    state.dirty = true;
    if (error.code === 'review_conflict' || error.code === 'ticket_closed') $('reload-review').hidden = false;
    notice('Не удалось сохранить ответ. ' + error.message, 'error');
  } finally {
    state.saving = false;
    syncActions();
  }
}

$('review-form').addEventListener('submit', (event) => {
  event.preventDefault();
  saveReview(false);
});
$('approve-review').addEventListener('click', () => saveReview(true));
$('close-ticket').addEventListener('click', async () => {
  if (state.saving || state.dirty || state.feedbackDirty || !state.ticket) return;
  const closed = !state.ticket.closed_at;
  state.saving = true;
  syncActions();
  try {
    const saved = await request('/api/v1/tickets/' + state.selected + '/closure', {
      method: 'PATCH', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({closed, expected_version: state.ticket.version})
    });
    renderDetail(saved);
    state.detailSignature = '';
    notice(closed ? 'Обращение закрыто.' : 'Обращение снова открыто.');
    await refresh();
  } catch (error) {
    state.detailSignature = '';
    notice('Не удалось изменить состояние. ' + error.message, 'error');
    await refresh();
  } finally {
    state.saving = false;
    syncActions();
  }
});

for (const id of ['feedback-rating', 'feedback-reason']) {
  $(id).addEventListener('input', () => {
    state.feedbackDirty = true;
    syncFeedbackReason();
    $('feedback-status').textContent = 'Изменения оценки не сохранены.';
    syncActions();
  });
}
$('feedback-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (state.saving || !$('feedback-form').reportValidity()) return;
  state.saving = true;
  syncActions();
  try {
    const saved = await request('/api/v1/tickets/' + state.selected + '/feedback', {
      method: 'PUT', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({rating: $('feedback-rating').value, reason: $('feedback-reason').value.trim(), expected_version: state.feedbackVersion})
    });
    state.feedbackDirty = false;
    renderDetail(saved, {preserveReview: state.dirty});
    state.detailSignature = '';
    notice('Оценка исходного черновика сохранена.');
    await refresh();
  } catch (error) {
    state.feedbackDirty = true;
    if (error.code === 'ticket_version_conflict') $('reload-feedback').hidden = false;
    notice('Не удалось сохранить оценку. ' + error.message, 'error');
  } finally {
    state.saving = false;
    syncActions();
  }
});
$('cancel-feedback').addEventListener('click', () => {
  if (state.saving || !state.ticket) return;
  state.feedbackDirty = false;
  renderDetail(state.ticket, {preserveReview: state.dirty});
  notice('Изменения оценки отменены.');
});
$('reload-feedback').addEventListener('click', async () => {
  if (state.saving || (state.feedbackDirty && !window.confirm('Заменить несохранённую оценку версией с сервера? Текст ответа останется в форме.'))) return;
  state.saving = true;
  syncActions();
  try {
    const item = await request('/api/v1/tickets/' + state.selected);
    state.feedbackDirty = false;
    renderDetail(item, {preserveReview: state.dirty});
    state.detailSignature = '';
    notice('Сохранённая оценка загружена.');
  } catch (error) {
    notice('Не удалось загрузить оценку. ' + error.message, 'error');
  } finally {
    state.saving = false;
    syncActions();
  }
});
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
    syncActions();
  }
});
for (const id of ['review-category', 'review-reply']) {
  $(id).addEventListener('input', () => {
    state.dirty = true;
    syncActions();
    $('review-status').textContent = 'Не сохранён';
    $('review-status').dataset.state = 'dirty';
  });
}
for (const id of ['category-filter', 'status-filter', 'view-filter']) {
  $(id).addEventListener('change', () => {
    state.offset = 0;
    refresh();
  });
}
let searchTimer;
$('ticket-search').addEventListener('input', () => {
  clearTimeout(searchTimer);
  state.offset = 0;
  searchTimer = setTimeout(refresh, 250);
});
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
  if (state.dirty || state.feedbackDirty) {
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
