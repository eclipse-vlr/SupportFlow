'use strict';
const $ = (id) => document.getElementById(id);
const categories = {billing:'Оплата',access:'Доступ',technical:'Техническое',other:'Другое'};
const statuses = {pending:'В очереди',processing:'Анализируется',completed:'Готово',failed:'Ошибка анализа'};
const priorities = {low:'Низкий приоритет',medium:'Средний приоритет',high:'Высокий приоритет'};
const errors = {queue_full:'Очередь заполнена. Попробуйте позже.',ticket_conflict:'Обращение ещё не готово или запрос конфликтует с предыдущим.',internal_error:'Не удалось выполнить операцию. Проверьте соединение с сервером.'};
let selected = null, rendered = '', dirty = false, offset = 0, refreshing = false, pendingKey = null, pendingPayload = '';

function notice(text) { $('notice').textContent = text; $('notice').hidden = !text; }
async function request(path,options = {}) {
  const response = await fetch(path,options);
  const data = await response.json();
  if (!response.ok) throw new Error(errors[data.error] || data.error || 'Ошибка запроса');
  return data;
}
function element(tag,text,className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function date(text) { return new Date(text).toLocaleString('ru-RU',{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'}); }

async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    const params = new URLSearchParams({limit:'10',offset:String(offset),category:$('category-filter').value,status:$('status-filter').value});
    const data = await request('/api/v1/tickets?' + params);
    $('ticket-list').replaceChildren();
    if (!data.items.length) $('ticket-list').append(element('p','Здесь появятся обращения.','muted'));
    for (const ticket of data.items) {
      const row = element('button',undefined,'ticket-row' + (ticket.id === selected ? ' selected' : ''));
      row.type = 'button';
      row.append(element('strong',ticket.title));
      const meta = element('div',undefined,'row-meta');
      meta.append(element('span',statuses[ticket.status],'badge'),element('span',categories[ticket.review?.category || ticket.analysis?.category] || '—'));
      row.append(meta,element('small',date(ticket.created_at) + (ticket.review?.approved ? ' · Ответ подтверждён' : '')));
      row.addEventListener('click',() => {
        if (dirty && !window.confirm('В черновике есть несохранённые изменения. Перейти к другому обращению?')) return;
        selected = ticket.id; rendered = ''; dirty = false; refresh();
      });
      $('ticket-list').append(row);
    }
    $('previous').disabled = offset === 0;
    $('next').disabled = data.items.length < 10;
    $('page-label').textContent = 'Страница ' + (offset / 10 + 1);
    if (selected) {
      const ticket = await request('/api/v1/tickets/' + selected);
      const signature = JSON.stringify(ticket);
      if (!dirty && signature !== rendered) { renderDetail(ticket); rendered = signature; }
    }
  } catch (error) { notice('Не удалось обновить данные: ' + error.message); }
  finally { refreshing = false; }
}

function renderDetail(ticket) {
  $('empty-detail').hidden = true; $('detail').hidden = false;
  $('ticket-title').textContent = ticket.title;
  $('ticket-message').textContent = ticket.message;
  $('ticket-status').textContent = statuses[ticket.status];
  $('ticket-date').textContent = date(ticket.created_at);
  $('analysis').hidden = ticket.status !== 'completed';
  $('waiting').hidden = ticket.status === 'completed';
  $('waiting').textContent = ticket.status === 'failed'
    ? 'Анализ не завершён. Код: ' + ticket.last_error + '. Проверьте настройки провайдера. Попыток: ' + ticket.attempts
    : 'Обращение сохранено. Анализ выполняется в фоне — результат появится автоматически.';
  if (!ticket.analysis) return;
  $('summary').textContent = ticket.analysis.summary;
  $('priority').textContent = priorities[ticket.analysis.priority];
  $('priority').dataset.priority = ticket.analysis.priority;
  $('priority-reason').textContent = ticket.analysis.priority_reason;
  $('original-reply').textContent = ticket.analysis.draft_reply;
  $('review-category').value = ticket.review?.category || ticket.analysis.category;
  $('review-reply').value = ticket.review?.draft_reply || ticket.analysis.draft_reply;
  $('review-status').textContent = ticket.review?.approved ? 'Подтверждён' : 'Черновик';
  $('usage').textContent = ticket.usage.model === 'demo-rules'
    ? 'Демонстрационный анализ по правилам. Внешняя модель не использовалась.'
    : ticket.usage.model + ' · Вход: ' + ticket.usage.input_tokens + ' токенов · Выход: ' + ticket.usage.output_tokens + ' · Попыток: ' + ticket.attempts;
}

$('create-form').addEventListener('submit',async (event) => {
  event.preventDefault();
  const payload = JSON.stringify({title:$('title').value.trim(),message:$('message').value.trim()});
  if (new TextEncoder().encode($('message').value.trim()).length > 16384) { notice('Сообщение слишком длинное: максимум 16 КиБ.'); return; }
  if (dirty && !window.confirm('Создать обращение и закрыть текущий несохранённый черновик?')) return;
  if (payload !== pendingPayload || !pendingKey) { pendingKey = crypto.randomUUID(); pendingPayload = payload; }
  $('create-button').disabled = true; notice('');
  try {
    const ticket = await request('/api/v1/tickets',{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':pendingKey},body:payload});
    selected = ticket.id; rendered = ''; dirty = false; pendingKey = null;
    offset = 0; $('category-filter').value = ''; $('status-filter').value = '';
    $('create-form').reset(); renderDetail(ticket); await refresh();
  } catch (error) { notice('Не удалось создать обращение: ' + error.message); }
  finally { $('create-button').disabled = false; }
});

async function saveReview(approved) {
  if (!$('review-form').reportValidity()) return;
  const id = selected;
  $('save-review').disabled = true; $('approve-review').disabled = true;
  try {
    await request('/api/v1/tickets/' + id + '/review',{method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({category:$('review-category').value,draft_reply:$('review-reply').value.trim(),approved})});
    if (selected === id) { dirty = false; rendered = ''; }
    notice(approved ? 'Ответ подтверждён и сохранён.' : 'Черновик сохранён.');
    await refresh();
  } catch (error) { notice('Не удалось сохранить: ' + error.message); }
  finally { $('save-review').disabled = false; $('approve-review').disabled = false; }
}
$('review-form').addEventListener('submit',(event) => {event.preventDefault();saveReview(false);});
$('approve-review').addEventListener('click',() => saveReview(true));
for (const id of ['review-category','review-reply']) $(id).addEventListener('input',() => {dirty = true;$('review-status').textContent = 'Есть изменения';});
for (const id of ['category-filter','status-filter']) $(id).addEventListener('change',() => {offset = 0;refresh();});
$('refresh').addEventListener('click',refresh);
$('previous').addEventListener('click',() => {offset = Math.max(0,offset - 10);refresh();});
$('next').addEventListener('click',() => {offset += 10;refresh();});
window.addEventListener('beforeunload',(event) => {if (dirty) {event.preventDefault();event.returnValue = '';}});
request('/api/v1/config').then((data) => {
  $('provider').textContent = data.provider === 'demo' ? 'Деморежим · без LLM' : 'LLM подключена';
  $('provider').classList.toggle('demo',data.provider === 'demo');
}).catch(() => notice('Сервер недоступен.'));
refresh();
setInterval(refresh,2000);
