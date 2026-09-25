// ApiCluster 管理界面逻辑
const state = { config: null, models: [], filter: 'free' };

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}

function fmt(n) {
  n = Number(n) || 0;
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

// toast 右下角浮层：保存失败这类「必须让人看见、但不该拦住操作」的反馈
function toast(msg, bad) {
  let box = $('#toast-box');
  if (!box) {
    box = document.createElement('div');
    box.id = 'toast-box';
    box.className = 'toast-box';
    document.body.appendChild(box);
  }
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  box.appendChild(el);
  const hold = bad ? 5200 : 2200;
  setTimeout(() => el.classList.add('out'), hold - 400);
  setTimeout(() => el.remove(), hold);
}

// debounce 防抖：停止触发 ms 毫秒后才执行，避免连续输入时频繁请求
function debounce(fn, ms) {
  let t = null;
  return (...args) => {
    if (t) clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

// copyText 可靠地把文本写入剪贴板（WebView2 的 HTTP 环境下 navigator.clipboard 可能不可用，
// 用 execCommand('copy') 兜底）。返回 Promise<boolean> 表示是否成功。
function copyText(text) {
  return new Promise((resolve) => {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text)
        .then(() => resolve(true))
        .catch(() => resolve(legacyCopy(text)));
      return;
    }
    resolve(legacyCopy(text));
  });
}

function legacyCopy(text) {
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    ta.style.top = '0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    ta.setSelectionRange(0, ta.value.length);
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch (e) {
    return false;
  }
}

// copyFlash 复制后把按钮短暂变成 ✓ / ✕，让「有没有拷进去」看得见
// postJSON 发一个 JSON 请求，失败时给出可见反馈并返回 false
async function postJSON(url, payload, errPrefix) {
  try {
    const res = await fetch(url, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload)
    });
    if (res.ok) return true;
    const d = await res.json().catch(() => ({}));
    toast((errPrefix || '保存失败') + '：' + (d.error || ('HTTP ' + res.status)), true);
    return false;
  } catch (e) {
    toast((errPrefix || '保存失败') + '：' + e.message, true);
    return false;
  }
}

function copyFlash(btn, ok) {
  if (!btn) return;
  const old = btn.textContent;
  btn.textContent = ok ? '✓' : '✕';
  setTimeout(() => { btn.textContent = old; }, 1000);
}

// copyTo 复制并把结果如实反馈在按钮上
async function copyTo(btn, text) {
  if (!text) return false;
  const ok = await copyText(text);
  copyFlash(btn, ok);
  return ok;
}

window.logoFallback = function (img) {
  const s = document.createElement('span');
  s.className = 'logo-fallback';
  s.textContent = (img.dataset.fb || '?').slice(0, 1).toUpperCase();
  if (img.dataset.fbBg) s.style.background = img.dataset.fbBg; // 远程图标挂掉时保持本站点专属配色
  img.replaceWith(s);
};

function openExternal(url) {
  if (!url) return;
  fetch('/api/open?url=' + encodeURIComponent(url));
}

// ---------- 数据 ----------
async function fetchConfig() {
  const editing = document.querySelector('.edit-area:not(.hidden)') || document.querySelector('.card.new-card') ||
    !!document.querySelector('#provider-grid input:focus, #custom-grid input:focus');
  const vaultFocused = !!document.querySelector('#vault-grid input:focus, #vault-grid textarea:focus');
  let res;
  try {
    res = await fetch('/api/config');
  } catch (e) {
    showBootError('无法连接本地服务（' + e.message + '），3 秒后自动重试…');
    setTimeout(fetchConfig, 3000);
    return;
  }
  if (!res.ok) {
    showBootError('读取配置失败（HTTP ' + res.status + '），3 秒后自动重试…');
    setTimeout(fetchConfig, 3000);
    return;
  }
  state.config = await res.json();
  runningPort = state.config.running_port || 3003;
  hideBootError();
  const models = [];
  const seen = new Set();
  const push = (id, owner) => {
    if (seen.has(id)) return;
    seen.add(id);
    models.push({ id, owner });
  };
  ['inurl', 'inurl-text', 'inurl-code', 'inurl-image', 'inurl-video', 'inurl-audio'].forEach((m) => push(m, '自动路由'));
  (state.config.providers || []).forEach((p) => (p.models || []).forEach((m) => {
    if (isGenerationCaps(m.capabilities)) return; // 生成类模型不进对话选择
    push(m.id, p.name);
  }));
  (state.config.custom || []).forEach((c) => (c.models || []).forEach((m) => {
    if (isGenerationCaps(c.model_caps && c.model_caps[m])) return;
    push(m, c.name + '（自定义）');
  }));
  state.models = models;
  // 有展开的编辑区时不重建网格，避免打断编辑；
  // 有测试结果展示时也不重建网格，否则 20 秒轮询会把正在/刚完成的测试反馈清掉（表现为「直接没了」）。
  const hasTestResult = !!document.querySelector('.test-result');
  if (editing || hasTestResult) {
    renderSettings();
    renderRouteConfig();
    renderStatus();
    renderModelSelect();
    if (!vaultFocused) renderVault(); // 账号库正在输入时不动，避免抢走光标
    return;
  }
  renderAll();
}

// showBootError / hideBootError 初始化失败时的可见提示（避免静默空白）
function showBootError(msg) {
  let el = $('#boot-error');
  if (!el) {
    el = document.createElement('div');
    el.id = 'boot-error';
    el.style.cssText = 'position:fixed;top:14px;left:50%;transform:translateX(-50%);z-index:9999;' +
      'background:#fee2e2;color:#b91c1c;border:1px solid #fca5a5;border-radius:10px;padding:10px 18px;' +
      'font-size:13px;box-shadow:0 4px 16px rgba(0,0,0,.12);';
    document.body.appendChild(el);
  }
  el.textContent = '⚠ ' + msg;
}
function hideBootError() {
  const el = $('#boot-error');
  if (el) el.remove();
}

// ---------- 渲染 ----------
function renderAll() {
  renderGrid();
  renderVault();
  renderSettings();
  renderRouteConfig();
  renderStatus();
  renderModelSelect();
}

// ---------- 密钥库：收藏 / 分组 ----------
// 标记单独存一份（marks: 厂商ID -> {favorite, group}），不塞进 ProviderConfig：
// 那条记录每条保存路径都是整体覆盖，塞进去很容易被别的写回清掉。
function marks() { return (state.config && state.config.marks) || {}; }

function markOf(id) {
  const m = marks()[id];
  return { favorite: !!(m && m.favorite), group: (m && m.group) || '' };
}

function allGroups() {
  const set = new Set();
  Object.values(marks()).forEach((m) => { if (m && m.group) set.add(m.group); });
  return Array.from(set).sort((a, b) => a.localeCompare(b, 'zh'));
}

// 已知存在的厂商 id（删除厂商后残留的标记不该再被计入）
function knownIds() {
  const out = new Set();
  ((state.config && state.config.providers) || []).forEach((p) => out.add(p.id));
  ((state.config && state.config.custom) || []).forEach((c) => out.add(c.id));
  return out;
}

function favoriteCount() {
  const known = knownIds();
  return Object.keys(marks()).filter((id) => known.has(id) && marks()[id].favorite).length;
}

// gridBusy 有人正在编辑卡片时不能整页重建：否则展开的编辑区 / 填一半的新建表单会被抹掉
function gridBusy() {
  return !!document.querySelector('#provider-grid .edit-area:not(.hidden), #custom-grid .edit-area:not(.hidden)') ||
    !!document.querySelector('#custom-grid .card.new-card') ||
    !!document.querySelector('#provider-grid input:focus, #custom-grid input:focus');
}

// patchCardMarks 就地更新这一张卡的 ★ 与分组标签（排序留到下次自然重渲染）
function patchCardMarks(id) {
  const card = document.querySelector('#provider-grid .card[data-id="' + CSS.escape(id) + '"], #custom-grid .card[data-id="' + CSS.escape(id) + '"]');
  const mk = markOf(id);
  if (card) {
    const fav = card.querySelector('.fav-btn');
    if (fav) fav.classList.toggle('on', mk.favorite);
    const grp = card.querySelector('.grp-btn');
    if (grp) grp.textContent = (mk.group ? '📁 ' + mk.group : '📂 未分组') + ' ▾';
    if (grp) grp.classList.toggle('set', !!mk.group);
  }
  const nf = $('#num-fav');
  if (nf) nf.textContent = favoriteCount();
  renderGroupSelect();
}

function setMark(id, patch) {
  const all = Object.assign({}, marks());
  const cur = Object.assign({ favorite: false, group: '' }, all[id] || {}, patch);
  if (!cur.favorite && !cur.group) delete all[id];
  else all[id] = { favorite: !!cur.favorite, group: cur.group || '' };
  state.config.marks = all;
  if (gridBusy()) patchCardMarks(id); else renderGrid();
  debouncedSaveMarks();
}

async function saveMarksNow() {
  try {
    const res = await fetch('/api/marks', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(state.config.marks || {})
    });
    const d = await res.json().catch(() => ({}));
    if (!res.ok) { toast('❌ 收藏/分组保存失败：' + (d.error || ('HTTP ' + res.status)), true); return; }
    if (d.marks) state.config.marks = d.marks; // 服务端会剔除空标记，两边保持一致
  } catch (e) {
    toast('❌ 收藏/分组保存失败：' + e.message, true);
  }
}
const debouncedSaveMarks = debounce(saveMarksNow, 500);

// renameGroup 改名 / 删除分组要一次影响整组卡片
function renameGroup(from, to) {
  const all = Object.assign({}, marks());
  let touched = false;
  Object.keys(all).forEach((id) => {
    if (all[id] && all[id].group === from) {
      all[id] = Object.assign({}, all[id], { group: to || '' });
      touched = true;
    }
  });
  if (!touched) return;
  state.config.marks = all;
  if (gridBusy()) $$('#provider-grid .card, #custom-grid .card').forEach((c) => patchCardMarks(c.dataset.id));
  else renderGrid();
  saveMarksNow();
}

function placePopMenu(menu, anchor) {
  document.body.appendChild(menu);
  const r = anchor.getBoundingClientRect();
  const mh = menu.offsetHeight, mw = menu.offsetWidth;
  let top = r.bottom + 6;
  if (top + mh > innerHeight - 8) top = Math.max(8, r.top - mh - 6); // 贴底时朝上弹
  menu.style.top = top + 'px';
  menu.style.left = Math.max(8, Math.min(r.left, innerWidth - mw - 12)) + 'px';
  setTimeout(() => {
    const close = (ev) => {
      if (!menu.contains(ev.target)) { menu.remove(); document.removeEventListener('click', close); }
    };
    document.addEventListener('click', close);
  }, 0);
}

// showGroupMenu 给某张卡片选分组：已有分组 / 新建 / 改名 / 删除 / 取消
function showGroupMenu(anchor, id) {
  document.querySelectorAll('.logo-menu').forEach((m) => m.remove());
  const cur = markOf(id).group;
  const gs = allGroups();
  const menu = document.createElement('div');
  menu.className = 'logo-menu';
  let html = '<div class="lm-title">归入分组</div><div class="lm-rows">';
  gs.forEach((g) => {
    html += '<button type="button" class="lm-row' + (g === cur ? ' on' : '') + '" data-group="' + esc(g) + '">' +
      (g === cur ? '● ' : '　') + '📁 ' + esc(g) + '</button>';
  });
  html += '<button type="button" class="lm-row" data-group="">' + (cur ? '　' : '● ') + '📂 未分组</button>';
  html += '</div><div class="lm-sep"></div><div class="lm-rows">';
  html += '<button type="button" class="lm-row" data-act="new">＋ 新建分组…</button>';
  if (cur) {
    html += '<button type="button" class="lm-row" data-act="rename">✎ 重命名「' + esc(cur) + '」…</button>';
    html += '<button type="button" class="lm-row" data-act="delete">🗑 删除分组「' + esc(cur) + '」</button>';
  }
  html += '</div>';
  menu.innerHTML = html;
  placePopMenu(menu, anchor);
  menu.addEventListener('click', (e) => {
    const item = e.target.closest('[data-group],[data-act]');
    if (!item) return;
    if (item.hasAttribute('data-group')) {
      setMark(id, { group: item.dataset.group || '' });
    } else if (item.dataset.act === 'new') {
      const name = prompt('新分组名称（最多 16 字）：', '');
      if (name && name.trim()) setMark(id, { group: name.trim().slice(0, 16) });
    } else if (item.dataset.act === 'rename') {
      const name = prompt('把分组「' + cur + '」改名为：', cur);
      if (name && name.trim() && name.trim() !== cur) renameGroup(cur, name.trim().slice(0, 16));
    } else if (item.dataset.act === 'delete') {
      if (confirm('删除分组「' + cur + '」？组内卡片会变成未分组，Key 与配置不受影响。')) renameGroup(cur, '');
    }
    menu.remove();
  });
}

// grpChipHtml 分组标签（点开分组菜单）
function grpChipHtml(id) {
  const g = markOf(id).group;
  return '<button class="tag grp-btn' + (g ? ' set' : '') + '" title="归入分组 / 管理分组">' +
    (g ? '📁 ' + esc(g) : '📂 未分组') + ' ▾</button>';
}

function favBtnHtml(id) {
  return '<button class="fav-btn' + (markOf(id).favorite ? ' on' : '') + '" title="收藏（收藏的卡片排在最前，可用「★ 收藏」筛出）">★</button>';
}

// dropMark 厂商被删除后，它的收藏/分组标记也要一起走
function dropMark(id) {
  const all = Object.assign({}, marks());
  if (!(id in all)) return;
  delete all[id];
  state.config.marks = all;
  saveMarksNow();
}

function groupFilterValue() {
  const el = $('#group-filter');
  return el ? el.value : '';
}

// renderGroupSelect 分组下拉：全部 / 未分组 / 各分组（带数量）
function renderGroupSelect() {
  const sel = $('#group-filter');
  if (!sel) return;
  const prev = sel.value;
  const gs = allGroups();
  let html = '<option value="">全部分组</option>';
  if (gs.length) {
    html += '<option value="__none">未分组</option>';
    gs.forEach((g) => {
      const c = Object.values(marks()).filter((m) => m && m.group === g).length;
      html += '<option value="' + esc(g) + '">' + esc(g) + ' (' + c + ')</option>';
    });
  }
  sel.innerHTML = html;
  sel.value = Array.from(sel.options).some((o) => o.value === prev) ? prev : '';
  sel.disabled = gs.length === 0;
  sel.title = gs.length ? '按分组筛选' : '还没有分组：在卡片上点「📂 未分组 ▾」归入分组';
}

function ftClass(ft) {
  switch (ft) {
    case '完全免费': return 'ft-free';
    case '注册送额度': return 'ft-quota';
    case '无需 Key': return 'ft-nokey';
    default: return 'ft-paid';
  }
}

function logoHtml(p) {
  if (p.logo_url) {
    return `<img src="${esc(p.logo_url)}" alt="" data-fb="${esc(p.name)}" onerror="logoFallback(this)">`;
  }
  return `<span class="logo-fallback">${esc(p.name.slice(0, 1))}</span>`;
}

function maskKey(key) {
  if (!key) return '';
  if (key.length <= 8) return '****';
  return key.slice(0, 4) + '****' + key.slice(-4);
}

// routedName 解码响应头里的厂商/模型名（后端对非 ASCII 做了百分号编码，避免 HTTP 头乱码）
function routedName(v) {
  if (!v) return '';
  try { return decodeURIComponent(v); } catch (e) { return v; }
}

// keyLabel 生成一行 Key 的显示名：优先用自定义名称，否则回退为 Key N
// keyLabel 生成一行的标签：●/○ 表示是否为当前使用，其后是自定义名称或稳定序号 Key N。
// no 是该 key 的稳定序号（创建时分配，拖拽排序不变），缺省时退回按位置计算。
function keyLabel(i, name, first, no) {
  return (first ? '● ' : '○ ') + (name || ('Key ' + (no || (i + 1))));
}

// keyItemHtml 生成一行 Key 的 HTML（拖拽手柄 + 名称 + 输入框 + 显示/复制/测试/删除）
function keyItemHtml(i, key, opts) {
  opts = opts || {};
  // models：该厂商全部模型 [{id, caps}]。测试按钮点击后弹出选择器，让用户挑要测哪个模型。
  const models = opts.models || [];
  const testBtn = models.length
    ? `<button class="eye-mini btn-test-key" data-models="${esc(JSON.stringify(models))}" data-key-index="${i}" title="选择模型并用此 Key 测试">⚡</button>`
    : '';
  return `<span class="key-drag" title="按住拖拽调整顺序（置顶 = 当前使用）">⋮⋮</span>
      <span class="key-tag" title="双击可重命名">${esc(keyLabel(i, opts.name, opts.first, opts.no))}</span>
      <input type="password" class="key-input" data-key-index="${i}" placeholder="${esc(opts.placeholder || '粘贴 API Key')}" value="${esc(key || '')}">
      <button class="eye-mini btn-eye" title="显示/隐藏">👁</button>
      <button class="eye-mini btn-copy" title="复制">📋</button>
      ${testBtn}
      ${opts.removable ? '<button class="eye-mini btn-remove-key" title="删除此 Key">✕</button>' : ''}`;
}

// keyListHtml 渲染多 Key 列表。列表顺序即优先级：第一行 = 当前使用的 Key。
// nos 是每个 key 的稳定序号（与 api_keys 一一对应），拖拽排序时序号不随之改变。
// models 是该厂商全部模型 [{id, caps}]，供测试按钮弹出选择器。
function keyListHtml(keys, keyIndex, models, names, nos) {
  names = names || [];
  nos = nos || [];
  if (!keys.length) {
    // 至少给一个空输入框，方便首次填写
    return `<div class="key-item active" draggable="true" data-key-index="0" data-key-name="" data-key-no="1">${keyItemHtml(0, '', { first: true, models, no: 1 })}</div>`;
  }
  return keys.map((key, i) => `
    <div class="key-item ${i === keyIndex ? 'active' : ''}" draggable="true" data-key-index="${i}" data-key-name="${esc(names[i] || '')}" data-key-no="${nos[i] || (i + 1)}">${keyItemHtml(i, key, {
      first: i === keyIndex,
      models,
      name: names[i] || '',
      no: nos[i] || (i + 1),
      removable: keys.length > 1
    })}</div>`).join('');
}

// usageBarHtml 用量展示：填了额度就给出进度与剩余，否则只报已用 tokens
function usageBarHtml(quota, used) {
  if (quota > 0) {
    const pct = Math.min(100, (used / quota) * 100);
    return `<span class="usage">已用 ${fmt(used)} / ${fmt(quota)} tokens · 剩余 <b>${fmt(Math.max(0, quota - used))}</b></span>
      <div class="progress"><div style="width:${pct}%"></div></div>`;
  }
  return `<span class="usage">已用 ${fmt(used || 0)} tokens（在「编辑」里填额度可显示剩余）</span>`;
}

function keyAreaHtml(p, k, models) {
  const keys = (k && k.api_keys && k.api_keys.length) ? k.api_keys : (k && k.api_key ? [k.api_key] : []);
  const names = (k && k.key_names) || [];
  const nos = (k && k.key_nos) || [];
  const hasKey = keys.length > 0;
  const keyIndex = k ? k.key_index : 0;
  const quota = k ? k.quota : 0;
  const used = k ? k.used : 0;
  const usageHtml = usageBarHtml(quota, used);
  const balanceBtn = hasKey && p.balance_url
    ? `<button class="mini-btn btn-balance" title="查询所有 Key 的账户余额（数值单位以厂商为准，不是 tokens）">💰 查余额</button>`
    : '';
  const keyHint = keys.length > 1
    ? '<div class="key-hint">拖拽左侧 ⋮⋮ 调整顺序（第一位即当前使用）；双击名称可重命名</div>'
    : '';
  return `
  <div class="key-area" data-has-keys="${keys.length}">
    <div class="key-list">${keyListHtml(keys, keyIndex, models, names, nos)}</div>
    ${keyHint}
    <div class="key-actions">
      <button class="mini-btn btn-add-key" title="添加更多 API Key（一个用完自动切下一个）">＋ 添加 Key</button>
      <button class="mini-btn btn-edit" title="展开编辑（Base URL / 模型列表）">编辑</button>
      ${hasKey ? '<button class="del-mini btn-del" title="删除该厂商的全部配置（Key / 覆盖 / 用量）">🗑</button>' : ''}
    </div>
    <div class="key-meta">
      <div class="meta-left">${balanceBtn}${usageHtml}</div>
      ${p.signup_url ? `<button class="getkey btn-getkey"><span class="tri"></span>前往获取 Key</button>` : ''}
    </div>
  </div>`;
}

// ---------- 模型覆盖编辑（模型 ID + 支持类型，交互与自动路由方案的能力标签一致） ----------

// catalogCaps 目录里某模型 ID 的能力标签（目录没有则 null）
function catalogCaps(p, mid) {
  const m = ((p && p.models) || []).find((x) => (x.id || x) === mid);
  return (m && m.capabilities && m.capabilities.length) ? m.capabilities : null;
}

// overrideCapsFor 模型能力显示优先级：用户手选（k.model_caps）> 目录默认 > 文本
function overrideCapsFor(k, p, mid) {
  const uc = (k && k.model_caps && k.model_caps[mid]) || [];
  if (uc.length) return uc;
  return catalogCaps(p, mid) || ['text'];
}

// overrideRows 构造编辑区模型行数据：优先用户覆盖的模型列表；无覆盖时返回空数组
function overrideRows(k, p) {
  const models = (k && k.models && k.models.length) ? k.models : [];
  const limits = (k && k.model_limits) || {};
  return models.map((mid) => ({ id: mid, caps: overrideCapsFor(k, p, mid), limit: limits[mid] }));
}

// modelRowHtml 编辑区单个模型行（两行布局，避免能力标签过多时把 ID 输入框挤扁）：
// 上行 = 模型 ID 输入框 + 删除；下行 = 能力标签（可换行）+ 并发/限流输入。
// limit: { concurrency, rpm } 用户配置（0 或空 = 不限制）
function modelRowHtml(mid, caps, limit) {
  const l = limit || {};
  return `<div class="m-row">
    <div class="m-row-top">
      <input class="m-id" placeholder="模型 ID，如 glm-4-flash" value="${esc(mid || '')}">
      <button class="eye-mini m-del" title="删除该模型">✕</button>
    </div>
    <div class="m-row-bottom">
      <span class="s-caps">${capTogglesHtml(caps)}</span>
      <span class="m-limits" title="留空或 0 = 不限制">
        <input class="m-conc" type="number" min="0" placeholder="并发" value="${l.concurrency || ''}">
        <input class="m-rpm" type="number" min="0" placeholder="次/分" value="${l.rpm || ''}">
      </span>
    </div>
  </div>`;
}

// modelRowsHtml 编辑区模型行列表 + 添加按钮
function modelRowsHtml(rows) {
  const list = rows || [];
  return `<div class="m-rows">${list.map((r) => modelRowHtml(r.id, r.caps, r.limit)).join('')}</div>
    <button class="mini-btn m-add" type="button" title="新增一行模型 ID">＋ 添加模型</button>`;
}

// readModelRows 收集编辑区模型行：跳过空 ID、去重；未勾选任何能力的行不写 caps（按默认）
function readModelRows(scope) {
  const models = [];
  const modelCaps = {};
  const modelLimits = {};
  scope.querySelectorAll('.m-row').forEach((row) => {
    const inp = row.querySelector('.m-id');
    const id = inp ? inp.value.trim() : '';
    if (!id || models.includes(id)) return;
    models.push(id);
    const caps = Array.from(row.querySelectorAll('.cap-chip.on')).map((c) => c.dataset.cap);
    if (caps.length) modelCaps[id] = caps;
    const conc = parseInt(row.querySelector('.m-conc').value, 10) || 0;
    const rpm = parseInt(row.querySelector('.m-rpm').value, 10) || 0;
    if (conc > 0 || rpm > 0) modelLimits[id] = { concurrency: conc, rpm };
  });
  return { models, modelCaps, modelLimits };
}

// bindModelRows 编辑区模型行事件委托（能力切换 / 删行 / 加行）。
// 文本输入走 input 事件的自动保存；点击类操作不触发 input，需手动触发防抖保存。
function bindModelRows(area) {
  area.addEventListener('click', (e) => {
    const chip = e.target.closest('.cap-chip');
    if (chip && chip.closest('.m-row')) {
      chip.classList.toggle('on');
      if (area._debouncedSave) area._debouncedSave();
      return;
    }
    if (e.target.closest('.m-del')) {
      e.target.closest('.m-row').remove();
      if (area._debouncedSave) area._debouncedSave();
      return;
    }
    if (e.target.closest('.m-add')) {
      const rows = area.querySelector('.m-rows');
      if (!rows) return;
      const tmp = document.createElement('div');
      tmp.innerHTML = modelRowHtml('', ['text']);
      const row = tmp.firstElementChild;
      rows.appendChild(row);
      row.querySelector('.m-id').focus();
      if (area._debouncedSave) area._debouncedSave();
    }
  });
}

// 内置厂商展开编辑区
function builtinEditHtml(p, k) {
  const defModels = (p.models || []).map((m) => m.id || m).join(', ');
  return `
  <div class="edit-area hidden">
    <label>类型</label>
    <select class="e-type">
      <option value="openai" ${(k && k.type) !== 'anthropic' ? 'selected' : ''}>OpenAI 兼容</option>
      <option value="anthropic" ${(k && k.type) === 'anthropic' ? 'selected' : ''}>Anthropic</option>
    </select>
    <label>Endpoint (baseUrl)（留空 = 默认）</label>
    <input class="e-baseurl" placeholder="${esc(p.base_url)}" value="${esc(k && k.base_url ? k.base_url : '')}">
    <label>路径（留空 = 默认）</label>
    <input class="e-path" placeholder="/chat/completions" value="${esc(k && k.path ? k.path : '')}">
    <label>模型 ID 与支持类型（删光所有行 = 恢复默认模型）</label>
    ${modelRowsHtml(overrideRows(k, p))}
    <div class="muted">默认模型：${esc(defModels)}。此处填写的模型 ID 与勾选的类型会覆盖上方展示，并同步用于自动路由。</div>
    <label>鉴权头（留空 = 默认）</label>
    <input class="e-authheader" placeholder="Authorization" value="${esc(k && k.auth_header ? k.auth_header : '')}">
    <label>前缀（留空 = 默认）</label>
    <input class="e-authprefix" placeholder="Bearer " value="${esc(k && k.auth_prefix ? k.auth_prefix : '')}">
    <label>额度（tokens，可选）：用于显示「已用 / 剩余」进度条</label>
    <input class="e-quota" type="number" min="0" placeholder="如 1000000" value="${k && k.quota ? k.quota : ''}">
    <div class="edit-actions">
      <button class="save-mini btn-save-edit">完成</button>
    </div>
  </div>`;
}

function cardHtml(p, k, isCustom) {
  const freeType = isCustom ? '自定义' : (p.free_type || '');
  const typeLabel = (k && k.type === 'anthropic') ? 'Anthropic' : 'OpenAI 兼容';
  const compatTag = p.compatible === false
    ? '<span class="tag warn">⚠ 暂不支持直连</span>'
    : `<span>· ${typeLabel}</span>`;
  const overrideTag = (k && k.base_url) ? '<span class="tag warn">BaseURL 已覆盖</span>' : '';
  const modelsTag = (k && k.models && k.models.length) ? '<span class="tag warn">模型已覆盖</span>' : '';
  // 卡片展示的模型与类型：用户覆盖过则显示覆盖值（类型 = 手选 > 目录默认），否则显示目录默认。
  // 同时作为「测试选择器」的候选列表（含能力标签，据此决定走文本/文生图/文生视频测试）。
  const displayModels = (k && k.models && k.models.length)
    ? k.models.map((mid) => ({ id: mid, caps: overrideCapsFor(k, p, mid) }))
    : (p.models || []).map((m) => ({ id: m.id || m, caps: (m.capabilities && m.capabilities.length) ? m.capabilities : ['text'] }));
  return `
  <div class="card" data-id="${esc(p.id)}" data-custom="${isCustom ? '1' : ''}">
    ${p.recommended ? '<span class="rec-badge">★ 推荐</span>' : ''}
    ${favBtnHtml(p.id)}
    <div class="card-head">
      <div class="logo">${logoHtml(p)}</div>
      <div class="head-info">
        <div class="card-name">${esc(p.name)}</div>
        <div class="tags">
          ${freeType ? `<span class="tag ${ftClass(p.free_type)}">${esc(freeType)}</span>` : ''}
          ${p.country ? `<span>${esc(p.country)}</span>` : ''}
          ${compatTag}
          ${overrideTag}${modelsTag}
          ${grpChipHtml(p.id)}
        </div>
      </div>
    </div>
    <p class="desc">${esc(p.description || p.note || '')}</p>
    <div class="chips">${displayModels.map((m) => {
      const capTag = m.caps.map(c => `<span class="cap-tag cap-${esc(c)}">${CAP_LABELS[c] || c}</span>`).join('');
      return `<span class="chip">${esc(m.id)}${capTag}</span>`;
    }).join('')}</div>
    ${p.compatible === false
      ? `<div class="key-area"><span class="usage">${esc(p.note || '该厂商需专用适配器，暂不支持代理直连')}</span>
          ${p.signup_url ? `<div class="key-meta"><span></span><button class="getkey btn-getkey"><span class="tri"></span>前往获取 Key</button></div>` : ''}
        </div>`
      : keyAreaHtml(p, k, displayModels) + builtinEditHtml(p, k)}
  </div>`;
}

// 自定义厂商卡片（展开式编辑）
function customCardHtml(c) {
  const typeTag = c.type === 'anthropic' ? 'Anthropic' : 'OpenAI 兼容';
  // 自定义厂商的模型候选（含能力标签），供测试选择器使用
  const customModels = (c.models || []).map((m) => ({
    id: m,
    caps: (c.model_caps && c.model_caps[m] && c.model_caps[m].length) ? c.model_caps[m] : ['text']
  }));
  return `
  <div class="card" data-id="${esc(c.id)}" data-custom="1">
    ${favBtnHtml(c.id)}
    <div class="card-head">
      <div class="logo"><span class="logo-fallback">${esc(c.name.slice(0, 1))}</span></div>
      <div class="head-info">
        <div class="card-name">${esc(c.name)}</div>
        <div class="tags"><span class="tag ft-paid">${esc(typeTag)}</span><span>· ${esc(c.base_url)}</span>${grpChipHtml(c.id)}</div>
      </div>
    </div>
    <div class="chips">${(c.models || []).map((m) => {
      const caps = (c.model_caps && c.model_caps[m] && c.model_caps[m].length) ? c.model_caps[m] : ['text'];
      const capTag = caps.map(cc => `<span class="cap-tag cap-${esc(cc)}">${CAP_LABELS[cc] || cc}</span>`).join('');
      return `<span class="chip">${esc(m)}${capTag}</span>`;
    }).join('')}</div>
    <div class="key-area" data-has-keys="${(c.api_keys || []).length}">
      <div class="key-list">${keyListHtml(c.api_keys && c.api_keys.length ? c.api_keys : (c.api_key ? [c.api_key] : []), c.key_index || 0, customModels, c.key_names || [], c.key_nos || [])}</div>
      ${(c.api_keys || []).length > 1 ? '<div class="key-hint">拖拽左侧 ⋮⋮ 调整顺序（第一位即当前使用）；双击名称可重命名</div>' : ''}
      <div class="key-actions">
        <button class="mini-btn btn-add-key" title="添加更多 API Key（一个用完自动切下一个）">＋ 添加 Key</button>
        <button class="mini-btn btn-edit" title="展开编辑">编辑</button>
        ${hasAnyKey(c) ? '<button class="del-mini btn-del" title="删除该自定义厂商">🗑</button>' : ''}
      </div>
      <div class="key-meta">
        <div class="meta-left">${usageBarHtml(c.quota, c.used)}</div>
      </div>
    </div>
    <div class="edit-area hidden">
      <label>厂商名称</label>
      <input class="e-name" value="${esc(c.name)}">
      <label>类型</label>
      <select class="e-type">
        <option value="openai" ${c.type !== 'anthropic' ? 'selected' : ''}>OpenAI 兼容</option>
        <option value="anthropic" ${c.type === 'anthropic' ? 'selected' : ''}>Anthropic</option>
      </select>
      <label>Endpoint (baseUrl)</label>
      <input class="e-baseurl" value="${esc(c.base_url)}">
      <label>路径</label>
      <input class="e-path" placeholder="/v1/chat/completions" value="${esc(c.path || '')}">
      <label>模型 ID 与支持类型</label>
      ${modelRowsHtml((c.models || []).map((mid) => ({
        id: mid,
        caps: (c.model_caps && c.model_caps[mid] && c.model_caps[mid].length) ? c.model_caps[mid] : ['text']
      })))}
      <div class="muted">「模型 ID」是发给厂商 API 的 model 字段值，须与厂商文档完全一致（错一个字符厂商就不认）。</div>
      <label>鉴权头</label>
      <input class="e-authheader" placeholder="Authorization" value="${esc(c.auth_header || '')}">
      <label>前缀</label>
      <input class="e-authprefix" placeholder="Bearer " value="${esc(c.auth_prefix || '')}">
      <label>API Key（留空 = 保留原密钥）</label>
      <input class="e-key" type="password" placeholder="留空保留原密钥">
      <label>额度（tokens，可选）</label>
      <input class="e-quota" type="number" value="${c.quota || ''}">
      <div class="edit-actions">
        <button class="save-mini btn-save-edit">完成</button>
      </div>
    </div>
  </div>`;
}

// 新建自定义厂商（展开式空白卡片）
function newCustomCardHtml() {
  return `
  <div class="card new-card" data-custom="1">
    <div class="card-head">
      <div class="logo"><span class="logo-fallback">＋</span></div>
      <div class="head-info"><div class="card-name">新厂商</div></div>
    </div>
    <div class="edit-area">
      <label>厂商名称</label>
      <input class="e-name" placeholder="如 MyLLM">
      <label>类型</label>
      <select class="e-type">
        <option value="openai">OpenAI 兼容</option>
        <option value="anthropic">Anthropic</option>
      </select>
      <label>Endpoint (baseUrl)</label>
      <input class="e-baseurl" placeholder="https://api.xxx.com">
      <label>路径</label>
      <input class="e-path" placeholder="/v1/chat/completions">
      <label>模型 ID 与支持类型</label>
      ${modelRowsHtml([])}
      <div class="muted">「模型 ID」是发给厂商 API 的 model 字段值，须与厂商文档完全一致（错一个字符厂商就不认）。「厂商名称」只是本地显示用，不参与请求。</div>
      <label>鉴权头</label>
      <input class="e-authheader" placeholder="Authorization">
      <label>前缀</label>
      <input class="e-authprefix" placeholder="Bearer ">
      <label>API Key</label>
      <input class="e-key" type="password" placeholder="你的厂商密钥">
      <label>额度（tokens，可选）</label>
      <input class="e-quota" type="number">
      <div class="edit-actions">
        <button class="save-mini btn-save-edit">添加自定义厂商</button>
        <button class="mini-btn btn-cancel-edit">取消</button>
      </div>
    </div>
  </div>`;
}

function hasAnyKey(k) {
  if (!k) return false;
  if (k.api_keys && k.api_keys.length) return true;
  return !!k.api_key;
}

function matchFilter(p, isCustom) {
  const q = ($('#search-provider').value || '').trim().toLowerCase();
  const onlyKeyed = $('#only-keyed').checked;
  const keyed = isCustom ? hasAnyKey(p) : hasAnyKey(state.config.keys[p.id]);
  if (onlyKeyed && !keyed) return false;
  if (!isCustom && state.filter === 'free' && !p.free) return false;
  if (!isCustom && state.filter === 'paid' && p.free) return false;
  const mk = markOf(p.id);
  if (state.filter === 'fav' && !mk.favorite) return false;
  const gf = groupFilterValue();
  if (gf && (gf === '__none' ? mk.group !== '' : mk.group !== gf)) return false;
  if (q) {
    const models = (p.models || []).map((m) => m.id || m).join(',').toLowerCase();
    if (!(p.name.toLowerCase().includes(q) || models.includes(q) || mk.group.toLowerCase().includes(q))) return false;
  }
  return true;
}

function emptyGridMsg() {
  if (state.filter === 'fav') return '还没有收藏。把常用厂商点上 ★，它们会排到最前面。';
  if (groupFilterValue()) return '该分组下没有厂商。';
  return '没有匹配的厂商';
}

function renderGrid() {
  const cfg = state.config;
  const freeList = cfg.providers.filter((p) => p.free);
  const paidList = cfg.providers.filter((p) => !p.free);
  $('#num-free').textContent = freeList.length;
  $('#num-paid').textContent = paidList.length;
  $('#num-fav').textContent = favoriteCount();
  renderGroupSelect();

  // 收藏的排到最前（Array#sort 在现代引擎下稳定，未收藏的保持目录原序）
  const favFirst = (a, b) => (markOf(b.id).favorite ? 1 : 0) - (markOf(a.id).favorite ? 1 : 0);
  const builtIn = cfg.providers.filter((p) => matchFilter(p, false)).sort(favFirst);
  const html = builtIn.map((p) => cardHtml(p, cfg.keys[p.id], false)).join('');

  const customs = (cfg.custom || []).filter((c) => matchFilter(c, true)).sort(favFirst);
  const customGrid = $('#custom-grid');
  const customTitle = $('#custom-grid-title');
  const pendingNew = customGrid.querySelector('.card.new-card');
  if (customs.length) {
    customTitle.classList.remove('hidden');
    customGrid.innerHTML = customs.map(customCardHtml).join('');
  } else {
    customTitle.classList.add('hidden');
    customGrid.innerHTML = '';
  }
  // 用户主动筛选同样不该丢掉未提交的新建表单（以前只有轮询路径做了保护）
  if (pendingNew) customGrid.prepend(pendingNew);
  $('#provider-grid').innerHTML = html || '<div class="muted">' + emptyGridMsg() + '</div>';
  bindCardEvents();
}

// refreshCard 局部重建单张卡片：用最新 state.config 重新渲染并替换，立即更新
// 卡片上的模型 chips 与 ⚡ 测试按钮的模型列表，不依赖全局 renderAll（后者会因存在
// 测试结果而被 fetchConfig 跳过，导致「完成」编辑后模型显示迟迟不刷新）。
function refreshCard(card) {
  const id = card.dataset.id;
  const isCustom = card.dataset.custom === '1';
  let html = '';
  if (isCustom) {
    const c = (state.config.custom || []).find((x) => x.id === id);
    if (!c) return;
    html = customCardHtml(c);
  } else {
    const p = (state.config.providers || []).find((x) => x.id === id);
    if (!p) return;
    html = cardHtml(p, state.config.keys[id], false);
  }
  const tmp = document.createElement('div');
  tmp.innerHTML = html;
  const newCard = tmp.firstElementChild;
  card.replaceWith(newCard);
  bindCardEvents(newCard);
}

// finishEdit 结束编辑：先保存（更新内存 state.config），收起编辑区，再局部刷新卡片。
// 与 refreshCard 配合，保证「完成/收起」后模型 chips 与测试按钮列表立即生效。
async function finishEdit(card) {
  const ok = await saveEdit(card, true);
  if (!ok) return; // 自定义厂商字段不完整时保持展开，让用户修正
  const area = card.querySelector('.edit-area');
  if (area) area.classList.add('hidden');
  const editBtn = card.querySelector('.btn-edit');
  if (editBtn) editBtn.textContent = '编辑';
  refreshCard(card);
}

// ---------- 卡片事件 ----------
function bindCardEvents(root) {
  const scope = root || document;

  scope.querySelectorAll('.btn-eye').forEach((btn) => {
    btn.addEventListener('click', () => {
      const inp = btn.closest('.key-item').querySelector('.key-input');
      if (inp) inp.type = inp.type === 'password' ? 'text' : 'password';
    });
  });

  scope.querySelectorAll('.btn-copy').forEach((btn) => {
    btn.addEventListener('click', () => {
      const inp = btn.closest('.key-item').querySelector('.key-input');
      const val = inp ? inp.value : '';
      const done = () => {
        const o = btn.textContent;
        btn.textContent = '✓';
        setTimeout(() => { btn.textContent = o; }, 1000);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(val).then(done).catch(done);
      } else {
        const ta = document.createElement('textarea');
        ta.value = val;
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); } catch (e) {}
        document.body.removeChild(ta);
        done();
      }
    });
  });

  // Key 输入框内容变化即自动保存（事件委托 + 防抖，新增的输入框也生效）
  scope.querySelectorAll('.key-list').forEach((list) => {
    if (list.dataset.autoSaveBound === '1') return;
    list.dataset.autoSaveBound = '1';
    const card = list.closest('.card');
    const save = debounce(() => saveKeys(card, true), 600);
    list.addEventListener('input', (e) => {
      if (e.target.classList.contains('key-input')) save();
    });
  });

  // 添加新 Key 输入框
  scope.querySelectorAll('.btn-add-key').forEach((btn) => {
    btn.addEventListener('click', () => {
      const area = btn.closest('.key-area');
      const list = area.querySelector('.key-list');
      const card = btn.closest('.card');
      // 新行也要能立刻测试：沿用本卡片已有 ⚡ 按钮上的模型列表（之前只传了 model，
      // keyItemHtml 读的是 models，导致新增行没有测试按钮，要等整页重渲染才出现）
      const testBtn = card.querySelector('.btn-test-key');
      let models = [];
      try { models = JSON.parse((testBtn && testBtn.dataset.models) || '[]'); } catch (e) { models = []; }
      const idx = list.querySelectorAll('.key-item').length;
      // 新 key 的序号取当前最大值 +1，不与已有 key 冲突
      let maxNo = 0;
      list.querySelectorAll('.key-item').forEach((el) => {
        const n = Number(el.dataset.keyNo) || 0;
        if (n > maxNo) maxNo = n;
      });
      const newNo = maxNo + 1;
      const div = document.createElement('div');
      div.className = 'key-item';
      div.draggable = true;
      div.dataset.keyIndex = idx;
      div.dataset.keyNo = newNo;
      div.innerHTML = keyItemHtml(idx, '', { models, removable: true, no: newNo });
      list.appendChild(div);
      // 为新元素绑定事件
      bindKeyItemEvents(div);
      bindKeyListDrag(list);
      renumberKeys(list);
      // 从单 key 变为多 key 时补充拖拽提示
      if (!area.querySelector('.key-hint') && list.querySelectorAll('.key-item').length > 1) {
        const hint = document.createElement('div');
        hint.className = 'key-hint';
        hint.textContent = '拖拽左侧 ⋮⋮ 可调整顺序，排在第一位的 Key 即当前使用';
        list.after(hint);
      }
    });
  });

  // 删除单个 Key
  scope.querySelectorAll('.btn-remove-key').forEach(bindRemoveKey);

  // 双击 Key 名称重命名
  scope.querySelectorAll('.key-tag').forEach(bindKeyTagRename);

  // 拖拽调整 Key 顺序（首位 = 当前使用）
  scope.querySelectorAll('.key-list').forEach(bindKeyListDrag);

  scope.querySelectorAll('.btn-del').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const card = btn.closest('.card');
      if (!confirm(card.dataset.custom === '1'
        ? '确认删除该自定义厂商？包含它的 Key、模型列表与用量统计，不可撤销。'
        : '确认删除该厂商在本机的全部配置？不只是 Key —— Base URL 覆盖、模型覆盖、限流、额度与已用统计都会一起删除，不可撤销。')) return;
      if (card.dataset.custom === '1') {
        await fetch('/api/custom?id=' + encodeURIComponent(card.dataset.id), { method: 'DELETE' });
        dropMark(card.dataset.id);
      } else {
        await fetch('/api/key?provider_id=' + encodeURIComponent(card.dataset.id), { method: 'DELETE' });
      }
      await fetchConfig();
    });
  });

  // 编辑：展开 / 收起
  scope.querySelectorAll('.btn-edit').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const card = btn.closest('.card');
      const area = card.querySelector('.edit-area');
      if (!area) return;
      const id = card.dataset.id;
      if (area.classList.contains('hidden')) {
        // 展开时填充当前值
        if (card.dataset.custom === '1') {
          const c = state.config.custom.find((x) => x.id === id);
          if (c) fillCustomEdit(area, c);
        } else {
          const p = state.config.providers.find((x) => x.id === id);
          const k = state.config.keys[id] || {};
          if (p) fillBuiltinEdit(area, p, k);
        }
        area.classList.remove('hidden');
        btn.textContent = '收起';
      } else {
        // 收起：先保存，再立即局部刷新卡片（模型 chips + 测试按钮列表即刻生效）
        await finishEdit(card);
      }
    });
  });

  scope.querySelectorAll('.btn-cancel-edit').forEach((btn) => {
    btn.addEventListener('click', () => {
      const card = btn.closest('.card');
      if (card.classList.contains('new-card')) {
        card.remove();
        return;
      }
      const area = card.querySelector('.edit-area');
      if (area) area.classList.add('hidden');
      const editBtn = card.querySelector('.btn-edit');
      if (editBtn) editBtn.textContent = '编辑';
    });
  });

  scope.querySelectorAll('.btn-save-edit').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const card = btn.closest('.card');
      // 新建厂商：点击才创建（需字段完整）
      if (card.classList.contains('new-card')) {
        const ok = await saveEdit(card);
        if (!ok) return;
        card.remove();
        await fetchConfig();
        return;
      }
      // 已有厂商：保存 + 收起 + 立即局部刷新卡片（模型 chips 与测试按钮列表即刻生效）
      await finishEdit(card);
    });
  });

  // 编辑区模型行事件（能力切换 / 删行 / 加行；新建卡片也需要）
  scope.querySelectorAll('.card .edit-area').forEach((area) => {
    if (area.dataset.modelRowsBound === '1') return;
    area.dataset.modelRowsBound = '1';
    bindModelRows(area);
  });

  // 编辑区字段变化即自动保存（防抖；新建卡片不自动保存，等点击「添加」）
  scope.querySelectorAll('.card:not(.new-card) .edit-area').forEach((area) => {
    if (area.dataset.autoSaveBound === '1') return;
    area.dataset.autoSaveBound = '1';
    const card = area.closest('.card');
    const save = debounce(() => saveEdit(card, true), 600);
    area._debouncedSave = save;
    area.addEventListener('input', () => save());
    area.addEventListener('change', () => save());
  });

  // 按 Key 测试：每个 Key 行一个按钮，只测试该 Key 是否可用（不改变当前使用）
  scope.querySelectorAll('.btn-test-key').forEach(bindTestKeyBtn);

  scope.querySelectorAll('.btn-balance').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const card = btn.closest('.card');
      const id = card.dataset.id;
      const old = card.querySelector('.test-result');
      if (old) old.remove();
      const result = document.createElement('div');
      result.className = 'test-result loading';
      result.textContent = '💰 查询所有 Key 余额中…';
      card.appendChild(result);
      btn.disabled = true;
      try {
        const res = await fetch('/api/balance?provider_id=' + encodeURIComponent(id));
        const data = await res.json();
        // 走 setResult：结果带 ✕ 可关闭。若留着不可关闭的 .test-result，
        // hasTestResult 会恒真 → 20 秒轮询从此不刷新网格，用量/模型标签/Key 顺序全部冻住。
        if (data.keys && data.keys.length > 0) {
          // 用与卡片行一致的稳定编号与名称，避免「Key 2」贴错人；内容逐条转义
          const lines = data.keys.map((k) => {
            const tag = k.current ? '●' : '○';
            const no = k.no || (k.index + 1);
            const name = k.name ? ' ' + k.name : '';
            return `${tag} Key ${no}${name}：${k.ok ? k.text : (k.error || '查询失败')}`;
          });
          setResult(result, 'ok', '💰 账户余额（数值单位以厂商为准，不是 tokens）',
            lines.map((l) => esc(l)).join('<br>'));
        } else {
          setResult(result, 'err', '❌ ' + (data.error || '查询失败'));
        }
      } catch (e) {
        setResult(result, 'err', '❌ ' + e.message);
      } finally {
        btn.disabled = false;
      }
    });
  });

  scope.querySelectorAll('.fav-btn').forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const card = btn.closest('.card');
      setMark(card.dataset.id, { favorite: !markOf(card.dataset.id).favorite });
    });
  });

  scope.querySelectorAll('.grp-btn').forEach((btn) => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      showGroupMenu(btn, btn.closest('.card').dataset.id);
    });
  });

  scope.querySelectorAll('.btn-getkey').forEach((btn) => {
    btn.addEventListener('click', () => {
      const card = btn.closest('.card');
      const id = card.dataset.id;
      let url = '';
      if (card.dataset.custom !== '1') {
        const p = state.config.providers.find((x) => x.id === id);
        url = p && p.signup_url;
      }
      openExternal(url);
    });
  });
}

// kindForCaps 根据模型能力判断测试走哪个端点（image_gen/video_gen 优先于文本）
function kindForCaps(caps) {
  if (caps && caps.includes('image_gen')) return 'image_gen';
  if (caps && caps.includes('video_gen')) return 'video_gen';
  return 'text';
}

// showModelTestMenu 在按钮下方弹出模型选择菜单；点击某个模型后回调 onSelect 并关闭。
// models: [{id, caps}]。支持点击菜单外关闭。
function showModelTestMenu(btn, models, onSelect) {
  document.querySelectorAll('.test-menu').forEach((m) => m.remove());
  const menu = document.createElement('div');
  menu.className = 'test-menu';
  models.forEach((m) => {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'test-menu-item';
    const caps = m.caps && m.caps.length ? m.caps : ['text'];
    const capTag = caps.map((c) => `<span class="cap-tag cap-${esc(c)}">${CAP_LABELS[c] || c}</span>`).join('');
    item.innerHTML = `<span class="tm-id">${esc(m.id)}</span><span class="tm-caps">${capTag}</span>`;
    item.addEventListener('click', () => {
      menu.remove();
      onSelect(m);
    });
    menu.appendChild(item);
  });
  document.body.appendChild(menu);
  const r = btn.getBoundingClientRect();
  menu.style.position = 'fixed';
  menu.style.top = (r.bottom + 4) + 'px';
  menu.style.left = r.left + 'px';
  // 点击菜单外关闭
  setTimeout(() => {
    const close = (e) => {
      if (!menu.contains(e.target)) {
        menu.remove();
        document.removeEventListener('click', close);
      }
    };
    document.addEventListener('click', close);
  }, 0);
}

// runModelTest 用指定 Key 测试某个模型，按能力走文本/文生图/文生视频。
function runModelTest(card, area, btn, m, keyIndex) {
  const item = btn.closest('.key-item');
  const prefix = (item && item.dataset.keyName) ? item.dataset.keyName : ('Key ' + (keyIndex + 1));
  const kind = kindForCaps(m.caps);
  const kindLabel = { text: '对话', image_gen: '文生图', video_gen: '文生视频' }[kind] || '对话';
  // 追加式结果：多次/多模型测试逐行列出，不互相覆盖；最多保留最近 20 条，超出移除最旧。
  const result = document.createElement('div');
  result.className = 'test-result loading';
  result.textContent = `⚡ ${prefix} / ${m.id}（${kindLabel}）测试中…`;
  area.appendChild(result);
  const allResults = area.querySelectorAll('.test-result');
  for (let i = 0; i < allResults.length - 20; i++) allResults[i].remove();
  btn.disabled = true;

  const payload = { model: m.id, key_index: keyIndex, kind };
  if (kind === 'text') {
    payload.max_tokens = 16;
    payload.messages = [{ role: 'user', content: '你好，请只回复：OK' }];
  } else if (kind === 'image_gen') {
    payload.prompt = '一只可爱的橘猫坐在窗台上';
  } else if (kind === 'video_gen') {
    payload.prompt = '一只猫在草地上奔跑';
  }

  fetch('/api/test', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  }).then(async (res) => {
    const data = await res.json();
    const rp = routedName(res.headers.get('X-Routed-Provider'));
    const rm = routedName(res.headers.get('X-Routed-Model'));
    const route = rp ? ' · 路由: ' + rp + '/' + rm : '';
    if (!res.ok) {
      setResult(result, 'err', `❌ ${prefix} / ${m.id}: ` + (data.error?.message || data.error || ('HTTP ' + res.status)));
      return;
    }
    if (kind === 'image_gen') {
      const url = data.data?.[0]?.url || '';
      setResult(result, 'ok', `✅ ${prefix} / ${m.id}（文生图）可用${route}`,
        url ? ` <button class="mini-btn btn-open-url" data-url="${esc(url)}">查看图片 ↗</button>` : `（${esc(JSON.stringify(data).slice(0, 120))}）`);
      bindOpenUrlButtons(result);
    } else if (kind === 'video_gen') {
      const id = data.id || '';
      const status = data.task_status || '';
      if (!id) {
        setResult(result, 'ok', `✅ ${prefix} / ${m.id}（文生视频）已提交${route}（${JSON.stringify(data).slice(0, 120)}）`);
      } else {
        // 等待生成中：loading 状态（无 ✕），随后由 pollVideoResult 更新为终态并加 ✕
        result.className = 'test-result loading';
        result.textContent = `✅ ${prefix} / ${m.id}（文生视频）已提交${route} 任务ID：${id} 状态：${status || '处理中'}，正在等待生成…`;
        pollVideoResult(id, result, prefix, m, route);
      }
    } else {
      const content = data.choices?.[0]?.message?.content || '(空回复)';
      setResult(result, 'ok', `✅ ${prefix} / ${m.id} 可用` + route + ' · ' + content.slice(0, 120));
    }
  }).catch((e) => {
    setResult(result, 'err', `❌ ${prefix} / ${m.id}: ` + e.message);
  }).finally(() => {
    btn.disabled = false;
  });
}

// bindOpenUrlButtons 绑定「查看图片/视频」按钮：用系统默认浏览器打开生成的媒体地址
// （WebView2 中 target=_blank 新窗口不可靠，走 /api/open 用系统浏览器打开最稳）。
function bindOpenUrlButtons(container) {
  container.querySelectorAll('.btn-open-url').forEach((b) => {
    b.addEventListener('click', () => openExternal(b.dataset.url));
  });
}

// setResult 设置测试结果行的「终态」（成功/失败）：
// 文本内容自动转义防注入，行尾追加 ✕ 关闭按钮（点击关闭该行，不弹窗）。
// extraHtml 为可选额外元素（如「查看图片/视频」按钮），原样插入（调用方保证已转义）。
function setResult(result, className, text, extraHtml) {
  result.className = 'test-result ' + className;
  result.innerHTML = '<span class="test-result-body">' + esc(text) + (extraHtml || '') + '</span>' +
    '<button class="test-result-close" title="关闭" type="button">✕</button>';
  result.querySelector('.test-result-close').addEventListener('click', () => result.remove());
}

// pollVideoResult 轮询视频生成异步结果（POST 提交后返回 task_id，视频异步生成）。
// 每 3 秒查一次 /v1/videos/{id}，task_status=SUCCESS 后展示「查看视频」按钮。
function pollVideoResult(id, result, prefix, m, route) {
  let n = 0;
  const timer = setInterval(async () => {
    if (!result.isConnected) { clearInterval(timer); return; } // 用户已触发新测试，结果区被替换
    n++;
    if (n > 120) { // 6 分钟上限
      clearInterval(timer);
      setResult(result, 'err', `⚠ ${prefix} / ${m.id}：视频生成超时（任务 ${id} 可能仍在处理），可稍后重试`);
      return;
    }
    try {
      const res = await fetch('/v1/videos/' + encodeURIComponent(id));
      const data = await res.json();
      if (!res.ok) {
        clearInterval(timer);
        setResult(result, 'err', `❌ ${prefix} / ${m.id}：查询视频结果失败` + (data.error?.message ? '（' + data.error.message + '）' : ''));
        return;
      }
      const st = data.task_status || '';
      if (st === 'SUCCESS') {
        clearInterval(timer);
        const url = (data.video_result && data.video_result[0] && data.video_result[0].url) || '';
        setResult(result, 'ok', `✅ ${prefix} / ${m.id}（文生视频）已生成${route}`,
          url ? ` <button class="mini-btn btn-open-url" data-url="${esc(url)}">查看视频 ↗</button>` : '（未返回视频地址）');
        bindOpenUrlButtons(result);
      } else if (st === 'FAIL' || st === 'FAILED') {
        clearInterval(timer);
        setResult(result, 'err', `❌ ${prefix} / ${m.id}：视频生成失败`);
      } else {
        result.className = 'test-result loading';
        result.textContent = `⏳ ${prefix} / ${m.id}（文生视频）生成中…（${st || 'PROCESSING'}，已等待 ${n * 3}s）`;
      }
    } catch (e) {
      // 网络抖动忽略，继续轮询
    }
  }, 3000);
}

// bindTestKeyBtn 绑定「选择模型并用该 Key 测试」按钮。
// 点击后弹出模型选择器（列出该厂商全部模型），选中的模型按其能力走对应测试。
function bindTestKeyBtn(btn) {
  btn.addEventListener('click', () => {
    const card = btn.closest('.card');
    const area = btn.closest('.key-area') || card;
    const keyIndex = Number(btn.dataset.keyIndex) || 0;
    let models = [];
    try { models = JSON.parse(btn.dataset.models || '[]'); } catch (e) { models = []; }
    if (!models.length) return;
    if (models.length === 1) {
      runModelTest(card, area, btn, models[0], keyIndex);
      return;
    }
    showModelTestMenu(btn, models, (m) => runModelTest(card, area, btn, m, keyIndex));
  });
}

// bindKeyItemEvents 为单个 key-item 绑定子事件（eye/copy/test/remove）
function bindKeyItemEvents(item) {
  item.querySelectorAll('.btn-eye').forEach((btn) => {
    btn.addEventListener('click', () => {
      const inp = item.querySelector('.key-input');
      if (inp) inp.type = inp.type === 'password' ? 'text' : 'password';
    });
  });
  item.querySelectorAll('.btn-copy').forEach((btn) => {
    btn.addEventListener('click', () => {
      const inp = item.querySelector('.key-input');
      const val = inp ? inp.value : '';
      const done = () => { btn.textContent = '✓'; setTimeout(() => { btn.textContent = '📋'; }, 1000); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(val).then(done).catch(done);
      } else { done(); }
    });
  });
  item.querySelectorAll('.btn-test-key').forEach(bindTestKeyBtn);
  item.querySelectorAll('.btn-remove-key').forEach(bindRemoveKey);
  item.querySelectorAll('.key-tag').forEach(bindKeyTagRename);
}

// bindKeyTagRename 双击 Key 名称就地重命名（回车/失焦保存，Esc 取消）
function bindKeyTagRename(tag) {
  if (tag.dataset.renameBound === '1') return;
  tag.dataset.renameBound = '1';
  tag.addEventListener('dblclick', (e) => {
    e.stopPropagation();
    const item = tag.closest('.key-item');
    if (!item || tag.querySelector('input')) return;
    const list = item.parentElement;
    const cur = item.dataset.keyName || '';
    const idx = Number(item.dataset.keyIndex) || 0;
    const no = Number(item.dataset.keyNo) || (idx + 1);
    const inp = document.createElement('input');
    inp.className = 'key-name-input';
    inp.value = cur;
    inp.placeholder = 'Key ' + no;
    inp.maxLength = 24;
    tag.textContent = '';
    tag.appendChild(inp);
    inp.focus();
    inp.select();
    let done = false;
    const finish = (save) => {
      if (done) return;
      done = true;
      if (save) item.dataset.keyName = inp.value.trim();
      renumberKeys(list);
      // 命名立即持久化，避免退出后丢失
      const card = list.closest('.card');
      if (card && save) saveKeys(card);
    };
    inp.addEventListener('keydown', (ev) => {
      ev.stopPropagation();
      if (ev.key === 'Enter') { ev.preventDefault(); finish(true); }
      else if (ev.key === 'Escape') { ev.preventDefault(); finish(false); }
    });
    inp.addEventListener('blur', () => finish(true));
  });
}

// removeKeyItem 删除一个 key 输入框并重新编号（删除即自动保存）
function removeKeyItem(btn) {
  const item = btn.closest('.key-item');
  if (!item) return;
  const list = item.parentElement;
  const area = list.closest('.key-area');
  item.remove();
  renumberKeys(list);
  if (area && list.querySelectorAll('.key-item').length <= 1) {
    const hint = area.querySelector('.key-hint');
    if (hint) hint.remove();
  }
  const card = area ? area.closest('.card') : null;
  if (card) saveKeys(card);
}

function bindRemoveKey(btn) {
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    const item = btn.closest('.key-item');
    const tag = item ? item.querySelector('.key-tag') : null;
    const label = tag ? tag.textContent.trim() : '该 Key';
    if (!confirm('确认删除 Key「' + label + '」？此操作会立即保存并生效。')) return;
    removeKeyItem(btn);
  });
}

// renumberKeys 刷新 key 列表：只更新「当前使用」标记与「删除」按钮，
// 序号取自元素自身的 data-key-no（创建时分配），拖拽排序不会改变它。
function renumberKeys(list) {
  const items = Array.from(list.querySelectorAll('.key-item'));
  const multi = items.length > 1;
  items.forEach((el, i) => {
    el.dataset.keyIndex = i;
    const no = Number(el.dataset.keyNo) || (i + 1);
    el.dataset.keyNo = no;
    const inp = el.querySelector('.key-input');
    if (inp) inp.dataset.keyIndex = i;
    const tag = el.querySelector('.key-tag');
    if (tag) tag.textContent = keyLabel(i, el.dataset.keyName || '', i === 0, no);
    el.classList.toggle('active', i === 0);
    const testBtn = el.querySelector('.btn-test-key');
    if (testBtn) testBtn.dataset.keyIndex = i;
    let rm = el.querySelector('.btn-remove-key');
    if (multi && !rm) {
      rm = document.createElement('button');
      rm.className = 'eye-mini btn-remove-key';
      rm.title = '删除此 Key';
      rm.textContent = '✕';
      el.appendChild(rm);
      bindRemoveKey(rm);
    } else if (!multi && rm) {
      rm.remove();
    }
  });
}

// bindKeyListDrag 为 key 列表绑定拖拽排序（事件委托，重复调用安全）
function bindKeyListDrag(list) {
  if (list.dataset.dragBound === '1') return;
  list.dataset.dragBound = '1';
  let dragEl = null;

  // 按下时动态决定该行是否可拖：
  // 在输入框/按钮内按下 → 禁拖，保证能在 key 输入框里拖动光标、选中文本；
  // 在其它区域（手柄/名称/空白）按下 → 允许整行拖拽。
  list.addEventListener('mousedown', (e) => {
    const item = e.target.closest('.key-item');
    if (!item || !list.contains(item)) return;
    item.draggable = !e.target.closest('input, button, select, textarea, .key-name-input');
  });

  list.addEventListener('dragstart', (e) => {
    const item = e.target.closest('.key-item');
    if (!item || !list.contains(item) || !item.draggable) return;
    dragEl = item;
    dragEl.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    try { e.dataTransfer.setData('text/plain', dragEl.dataset.keyIndex || ''); } catch (err) {}
  });
  list.addEventListener('dragend', () => {
    if (dragEl) dragEl.classList.remove('dragging');
    list.querySelectorAll('.key-item').forEach((x) => x.classList.remove('drag-over'));
    dragEl = null;
  });
  list.addEventListener('dragover', (e) => {
    if (!dragEl) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const item = e.target.closest('.key-item');
    list.querySelectorAll('.key-item').forEach((x) => x.classList.toggle('drag-over', x === item));
  });
  list.addEventListener('drop', async (e) => {
    if (!dragEl) return;
    e.preventDefault();
    const item = e.target.closest('.key-item');
    list.querySelectorAll('.key-item').forEach((x) => x.classList.remove('drag-over'));
    if (!item || item === dragEl) return;
    const rect = item.getBoundingClientRect();
    const after = e.clientY > rect.top + rect.height / 2;
    if (after) item.after(dragEl); else item.before(dragEl);
    renumberKeys(list);
    // 拖拽即生效：自动保存新顺序（首位 = 当前使用）
    const card = list.closest('.card');
    if (card) await saveKeys(card);
  });
}

// collectKeys 按 DOM 顺序收集 Key 与名称（跳过空 key，保证两者一一对应）
function collectKeys(card) {
  const apiKeys = [];
  const keyNames = [];
  const keyNos = [];
  card.querySelectorAll('.key-item').forEach((el, i) => {
    const inp = el.querySelector('.key-input');
    const v = inp ? inp.value.trim() : '';
    if (!v) return;
    apiKeys.push(v);
    keyNames.push(el.dataset.keyName || '');
    keyNos.push(Number(el.dataset.keyNo) || (i + 1));
  });
  return { apiKeys, keyNames, keyNos };
}

// collectBuiltinPayload 汇总某内置厂商卡片的完整保存体。
// Key 列表（含名称/序号）始终从 DOM 收集；编辑区字段仅在展开时从 DOM 读取，
// 收起时沿用服务端值 —— 保证「Key 输入触发的保存」与「编辑区触发的保存」
// 无论谁后触发都不会用旧数据覆盖对方（此前刚输入的模型覆盖被 Key 保存清空的根因），
// 同时修复编辑保存丢 Key 名称/序号的问题。
function collectBuiltinPayload(card) {
  const id = card.dataset.id;
  const cfgK = state.config.keys[id] || {};
  const { apiKeys, keyNames, keyNos } = collectKeys(card);
  const area = card.querySelector('.edit-area');
  const open = !!(area && !area.classList.contains('hidden'));
  const field = (sel, cur) => {
    if (!open) return cur;
    const el = area.querySelector(sel);
    return el ? el.value.trim() : cur;
  };
  let models = cfgK.models || [];
  let modelCaps = cfgK.model_caps || {};
  let modelLimits = cfgK.model_limits || {};
  if (open && area.querySelector('.m-rows')) {
    const r = readModelRows(area);
    models = r.models;
    modelCaps = r.modelCaps;
    modelLimits = r.modelLimits;
  }
  return {
    provider_id: id,
    api_keys: apiKeys,
    key_names: keyNames,
    key_nos: keyNos,
    key_index: 0,
    type: field('.e-type', cfgK.type || ''),
    base_url: field('.e-baseurl', cfgK.base_url || ''),
    path: field('.e-path', cfgK.path || ''),
    auth_header: field('.e-authheader', cfgK.auth_header || ''),
    auth_prefix: field('.e-authprefix', cfgK.auth_prefix || ''),
    models,
    model_caps: modelCaps,
    model_limits: Object.keys(modelLimits).length ? modelLimits : undefined,
    quota: quotaOf(area, open, cfgK)
  };
}

// applyBuiltinLocal 保存成功后把本次提交的字段同步进内存 state，
// 使「收起再展开」与后续保存看到的都是最新值（下次 fetchConfig 会再与服务端对账）。
function applyBuiltinLocal(payload) {
  const k = state.config.keys[payload.provider_id] || (state.config.keys[payload.provider_id] = {});
  k.api_keys = payload.api_keys;
  k.key_names = payload.key_names;
  k.key_nos = payload.key_nos;
  k.key_index = 0;
  k.type = payload.type;
  k.base_url = payload.base_url;
  k.path = payload.path;
  k.auth_header = payload.auth_header;
  k.auth_prefix = payload.auth_prefix;
  k.models = payload.models;
  k.model_caps = Object.keys(payload.model_caps).length ? payload.model_caps : undefined;
  k.model_limits = Object.keys(payload.model_limits || {}).length ? payload.model_limits : undefined;
  k.quota = payload.quota;
}

// saveKeys 保存某个卡片下的全部 Key（顺序即优先级，首位为当前使用）。
// silent 为 true 时不调用 fetchConfig 重渲染，避免打断正在进行的输入。
async function saveKeys(card, silent) {
  if (!card) return;
  const id = card.dataset.id;
  if (card.dataset.custom === '1') {
    const c = state.config.custom.find((x) => x.id === id);
    if (!c) return;
    const { apiKeys, keyNames, keyNos } = collectKeys(card);
    c.api_keys = apiKeys;
    c.key_names = keyNames;
    c.key_nos = keyNos;
    c.api_key = apiKeys[0] || '';
    c.key_index = 0;
    // 编辑区展开时，模型行的未保存修改一并带上，避免用旧值覆盖
    const area = card.querySelector('.edit-area');
    if (area && !area.classList.contains('hidden') && area.querySelector('.m-rows')) {
      const r = readModelRows(area);
      c.models = r.models;
      c.model_caps = Object.keys(r.modelCaps).length ? r.modelCaps : undefined;
      c.model_limits = Object.keys(r.modelLimits).length ? r.modelLimits : undefined;
    }
    // c 就是 state.config.custom 里的那一项，已在上面就地改过；失败只需给出可见反馈
    await postJSON('/api/custom', c, '保存失败');
  } else {
    const payload = collectBuiltinPayload(card);
    if (await postJSON('/api/key', payload, '保存失败')) applyBuiltinLocal(payload);
  }
  if (!silent) await fetchConfig();
}

function fillBuiltinEdit(area, p, k) {
  area.querySelector('.e-type').value = k.type || 'openai';
  area.querySelector('.e-baseurl').value = k.base_url || '';
  area.querySelector('.e-path').value = k.path || '';
  area.querySelector('.m-rows').innerHTML = overrideRows(k, p).map((r) => modelRowHtml(r.id, r.caps, r.limit)).join('');
  area.querySelector('.e-authheader').value = k.auth_header || '';
  area.querySelector('.e-authprefix').value = k.auth_prefix || '';
  const q = area.querySelector('.e-quota');
  if (q) q.value = k.quota || '';
}

function fillCustomEdit(area, c) {
  area.querySelector('.e-name').value = c.name;
  area.querySelector('.e-type').value = c.type || 'openai';
  area.querySelector('.e-baseurl').value = c.base_url;
  area.querySelector('.e-path').value = c.path || '';
  area.querySelector('.m-rows').innerHTML = (c.models || []).map((mid) => {
    const caps = (c.model_caps && c.model_caps[mid] && c.model_caps[mid].length) ? c.model_caps[mid] : ['text'];
    const limit = (c.model_limits && c.model_limits[mid]) ? c.model_limits[mid] : undefined;
    return modelRowHtml(mid, caps, limit);
  }).join('');
  area.querySelector('.e-authheader').value = c.auth_header || '';
  area.querySelector('.e-authprefix').value = c.auth_prefix || '';
  area.querySelector('.e-key').value = '';
  area.querySelector('.e-quota').value = c.quota || '';
}

async function saveBuiltinEdit(card) {
  const payload = collectBuiltinPayload(card);
  if (!(await postJSON('/api/key', payload, '保存失败'))) return false;
  applyBuiltinLocal(payload);
  return true;
}

async function saveCustomEdit(card, silent) {
  const id = card.dataset.id;
  const old = id ? state.config.custom.find((x) => x.id === id) : null;
  const eKey = card.querySelector('.e-key');
  // 收集多 key 列表中所有已填写的 key 及其名称/序号
  const collected = collectKeys(card);
  let apiKeys = collected.apiKeys;
  const keyNames = collected.keyNames;
  const keyNos = collected.keyNos;
  // 编辑区「API Key」字段若填了新值，作为新 key 追加（分配新的稳定序号）
  if (eKey && eKey.value.trim()) {
    const nk = eKey.value.trim();
    if (!apiKeys.includes(nk)) {
      const maxNo = keyNos.reduce((a, b) => (b > a ? b : a), 0);
      apiKeys = [nk, ...apiKeys];
      keyNames.unshift('');
      keyNos.unshift(maxNo + 1);
    }
  }
  if (!apiKeys.length && old && old.api_keys && old.api_keys.length) {
    // 未改动时保留原有 key 及其名称/序号
    apiKeys = [...old.api_keys];
    keyNames.length = 0;
    keyNos.length = 0;
    apiKeys.forEach((_, i) => {
      keyNames.push((old.key_names && old.key_names[i]) || '');
      keyNos.push((old.key_nos && old.key_nos[i]) || (i + 1));
    });
  }
  const mr = readModelRows(card);
  const p = {
    id: id || undefined,
    name: card.querySelector('.e-name').value.trim(),
    type: card.querySelector('.e-type').value,
    base_url: card.querySelector('.e-baseurl').value.trim().replace(/\/+$/, ''),
    path: card.querySelector('.e-path').value.trim(),
    auth_header: card.querySelector('.e-authheader').value.trim(),
    auth_prefix: card.querySelector('.e-authprefix').value.trim(),
    models: mr.models,
    model_caps: Object.keys(mr.modelCaps).length ? mr.modelCaps : undefined,
    model_limits: Object.keys(mr.modelLimits).length ? mr.modelLimits : undefined,
    api_keys: apiKeys,
    key_names: keyNames,
    key_nos: keyNos,
    api_key: apiKeys[0] || '',
    quota: Number(card.querySelector('.e-quota').value) || (old ? old.quota : 0)
  };
  if (!p.name || !p.base_url || !p.models.length) {
    if (!silent) alert('请填写名称、Endpoint 和至少一个模型ID');
    return false;
  }
  if (!(await postJSON('/api/custom', p, '保存失败'))) return false;
  // 已有厂商：同步内存 state，使「收起再展开」与 Key 保存看到的都是最新值
  if (id) {
    const idx = state.config.custom.findIndex((x) => x.id === id);
    if (idx >= 0) state.config.custom[idx] = Object.assign({}, p, { id });
  }
  return true;
}

function saveEdit(card, silent) {
  if (card.dataset.custom === '1') return saveCustomEdit(card, silent);
  return saveBuiltinEdit(card);
}

// ---------- 对话测试 ----------
function renderModelSelect() {
  const sel = $('#model-select');
  const byOwner = {};
  state.models.forEach((m) => {
    (byOwner[m.owner] = byOwner[m.owner] || []).push(m.id);
  });
  let html = '';
  Object.keys(byOwner).forEach((owner) => {
    html += `<optgroup label="${esc(owner)}">`;
    byOwner[owner].forEach((id) => { html += `<option value="${esc(id)}">${esc(id)}</option>`; });
    html += '</optgroup>';
  });
  const prev = sel.value;
  sel.innerHTML = html;
  sel.value = prev || 'inurl';
  if (!sel.value) sel.value = 'inurl';
  applyModelFilter();
}

function applyModelFilter() {
  const q = ($('#model-search').value || '').trim().toLowerCase();
  $$('#model-select optgroup').forEach((g) => {
    let any = false;
    g.querySelectorAll('option').forEach((o) => {
      const match = !q || o.value.toLowerCase().includes(q) || o.textContent.toLowerCase().includes(q);
      o.hidden = !match;
      if (match) any = true;
    });
    g.hidden = !any;
  });
  // 搜出来的结果里没有当前选中项时，自动跟到第一个可见项：
  // 否则界面显示的是过滤结果、发请求用的却还是隐藏着的旧值
  const sel = $('#model-select');
  const cur = sel.selectedOptions[0];
  if (!cur || cur.hidden) {
    const first = Array.from(sel.options).find((x) => !x.hidden);
    if (first) sel.value = first.value;
  }
}

// chatHistory 保留本轮上下文：以前每次只发一条 user 消息，
// 连着问「它是谁」「第二段改一下」必然答非所问。
let chatHistory = [];
let chatAbort = null;

function setupChat() {
  $('#model-search').addEventListener('input', applyModelFilter);
  const clear = $('#btn-clear-chat');
  if (clear) clear.addEventListener('click', () => { chatHistory = []; $('#chat-log').innerHTML = ''; });
  const send = async () => {
    if (chatAbort) return; // 一次只跑一条，避免并发把气泡顺序搞乱
    const model = $('#model-select').value;
    const text = $('#chat-text').value.trim();
    if (!model) { alert('请选择模型'); return; }
    if (!text) return;
    addMsg('user', text);
    chatHistory.push({ role: 'user', content: text });
    $('#chat-text').value = '';
    const msgEl = addMsg('assistant', '思考中…');
    const btn = $('#btn-send');
    btn.disabled = true;
    btn.textContent = '回答中…';
    const ctl = new AbortController();
    chatAbort = ctl;
    const timer = setTimeout(() => ctl.abort(), 300000);
    try {
      const maxTokens = Number($('#chat-max-tokens').value) || 128;
      const res = await fetch('/api/test', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: ctl.signal,
        // 只带最近 10 条（约 5 轮），够追问又不至于撑爆上下文
        body: JSON.stringify({ model, max_tokens: maxTokens, messages: chatHistory.slice(-10) })
      });
      const data = await res.json();
      if (!res.ok) {
        msgEl.className = 'msg error';
        msgEl.textContent = data.error?.message || data.error || ('HTTP ' + res.status);
        chatHistory.pop(); // 失败的一轮不留在上下文里
      } else {
        const content = data.choices?.[0]?.message?.content || '(无内容)';
        const rp = routedName(res.headers.get('X-Routed-Provider'));
        const rm = routedName(res.headers.get('X-Routed-Model'));
        msgEl.textContent = rp
          ? content + '\n\n—— 由 ' + rp + ' 的 ' + rm + ' 回答'
          : content;
        chatHistory.push({ role: 'assistant', content });
      }
    } catch (e) {
      msgEl.className = 'msg error';
      msgEl.textContent = ctl.signal.aborted ? '已超时或被中断，可重试或换个模型' : ('请求失败: ' + e.message);
      chatHistory.pop();
    } finally {
      clearTimeout(timer);
      chatAbort = null;
      btn.disabled = false;
      btn.textContent = '发送';
    }
  };
  $('#btn-send').addEventListener('click', send);
  $('#chat-text').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
}

function addMsg(role, text) {
  const el = document.createElement('div');
  el.className = 'msg ' + role;
  el.textContent = text;
  $('#chat-log').appendChild(el);
  $('#chat-log').scrollTop = $('#chat-log').scrollHeight;
  return el;
}

// ---------- 设置 ----------
// runningPort 本次进程实际在监听的端口（改了配置不重启就用不上）
let runningPort = 3003;

function renderSettings() {
  const s = state.config.settings || {};
  $('#set-autostart').checked = !!state.config.autostart;
  $('#set-port').value = s.proxy_port || 3003;
  const port = s.proxy_port || 3003;
  $('#status-addr').textContent = 'http://localhost:' + port + '/v1';
  // 底部示例里的端口以前硬编码 3003，改了端口就一直教错人
  $$('.js-port').forEach((el) => { el.textContent = String(port); });
  const tip = $('#port-restart-tip');
  if (tip) tip.classList.toggle('hidden', String(port) === String(runningPort));
}

// restartApp 重启整个程序（与托盘「重启」同一路径）：改端口后不必再去翻托盘
async function restartApp() {
  if (!confirm('需要重启 ApiCluster 才能让新端口生效。现在重启？\n（会短暂断开正在进行的请求）')) return;
  const btn = $('#btn-restart-app');
  if (btn) { btn.disabled = true; btn.textContent = '正在重启…'; }
  try {
    const res = await fetch('/api/restart', { method: 'POST' });
    const d = await res.json().catch(() => ({}));
    if (!res.ok) toast('❌ ' + (d.error || ('HTTP ' + res.status)), true);
    else toast('正在重启，窗口几秒后重新出现…');
  } catch (e) {
    toast('❌ 重启失败：' + e.message, true);
  }
  if (btn) { btn.disabled = false; btn.textContent = '重启程序'; }
}

function renderStatus() {
  $('#status-dot').classList.add('on');
  $('#status-text').textContent = '已连接';
}

function setupSettings() {
  const save = debounce(async () => {
    const cur = state.config.settings || {};
    const s = {
      autostart: $('#set-autostart').checked,
      proxy_port: Number($('#set-port').value) || 3003,
      // 这两项设置页没有控件，必须把服务端现值原样带回去（以前硬写 false，
      // 用户在别处改的「自动打开管理界面」会被这里悄悄抹掉）
      auto_open_browser: cur.auto_open_browser !== false,
      auto_route_enabled: cur.auto_route_enabled !== false,
      auto_models: cur.auto_models || '',
      auto_provider_order: cur.auto_provider_order || ''
    };
    let d;
    try {
      const res = await fetch('/api/settings', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(s)
      });
      d = await res.json().catch(() => ({}));
      if (!res.ok) { toast('保存失败：' + (d.error || ('HTTP ' + res.status)), true); return; }
    } catch (e) {
      toast('保存失败：' + e.message, true);
      return;
    }
    if (d.autostart !== undefined) state.config.autostart = d.autostart;
    // renderSettings 是从 state.config.settings 回读输入框的，不同步就会拿旧值把用户刚填的盖回去
    state.config.settings = Object.assign({}, state.config.settings || {}, s);
    // 用服务端回的真实监听端口，才能判断「配置已改但还没重启」
    if (d.running_port) runningPort = d.running_port;
    renderSettings();
    const hint = $('#settings-hint');
    if (hint) { hint.textContent = '✓ 已保存。修改端口后需重启程序生效。'; setTimeout(() => { hint.textContent = '修改自动保存'; }, 4000); }
  }, 600);

  $('#set-autostart').addEventListener('change', save);
  $('#set-port').addEventListener('input', save);
  const rb = $('#btn-restart-app');
  if (rb) rb.addEventListener('click', restartApp);
}

// ---------- 自动路由配置（方案） ----------
const CAP_LABELS = { text: '文本', code: '代码', image: '图像', video: '视频', audio: '音频', image_gen: '文生图', video_gen: '文生视频' };

function capText(caps) {
  return (caps && caps.length ? caps : ['text']).map((c) => CAP_LABELS[c] || c).join('/');
}

// keyedModels 返回所有「已配置 Key 且可直连」的模型（带能力标签与厂商 logo），供方案选择
function keyedModels() {
  const out = [];
  const seen = new Set();
  const cfg = state.config;
  (cfg.providers || []).forEach((p) => {
    if (p.compatible === false || !p.base_url) return;
    const k = cfg.keys[p.id];
    if (!hasAnyKey(k)) return;
    const ids = (k && k.models && k.models.length) ? k.models : (p.models || []).map((m) => m.id);
    ids.forEach((mid) => {
      if (seen.has(mid)) return;
      seen.add(mid);
      const caps = overrideCapsFor(k, p, mid);
      if (isGenerationCaps(caps)) return; // 生成类模型不进自动路由方案
      out.push({ model: mid, provider: p.name, caps, logo: p.logo_url || '' });
    });
  });
  (cfg.custom || []).forEach((c) => {
    if (!hasAnyKey(c) || !c.base_url) return;
    (c.models || []).forEach((mid) => {
      if (seen.has(mid)) return;
      seen.add(mid);
      const caps = (c.model_caps && c.model_caps[mid] && c.model_caps[mid].length) ? c.model_caps[mid] : ['text'];
      if (isGenerationCaps(caps)) return;
      out.push({ model: mid, provider: c.name, caps, logo: '' });
    });
  });
  return out;
}

function modelInfo(mid) {
  return keyedModels().find((m) => m.model === mid);
}

// 全部可选能力（顺序即展示顺序）。image_gen/video_gen 为文生图/文生视频生成能力，
// 走独立端点，不参与 chat 自动路由（inurl-* 均不会命中它们）。
const ALL_CAPS = ['text', 'code', 'image', 'video', 'audio', 'image_gen', 'video_gen'];

// 生成类能力（文生图 / 文生视频）：不出现在对话模型选择与自动路由方案里
const GEN_CAPS = new Set(['image_gen', 'video_gen']);
function isGenerationCaps(caps) {
  return !!(caps && caps.length) && caps.some((c) => GEN_CAPS.has(c));
}

// schemeLogoHtml 渲染模型行的厂商 logo（无 logo 用首字母兜底）
function schemeLogoHtml(info) {
  if (!info) return '<span class="s-logo"><span class="logo-fallback">?</span></span>';
  if (info.logo) {
    return `<span class="s-logo"><img src="${esc(info.logo)}" alt="" data-fb="${esc(info.provider)}" onerror="logoFallback(this)"></span>`;
  }
  return `<span class="s-logo"><span class="logo-fallback">${esc((info.provider || '?').slice(0, 1))}</span></span>`;
}

// capsReadonlyHtml 方案行里的能力只做展示：这些勾选过去完全不参与路由（纯装饰），
// 会误导用户以为在这里能改分流。真正的开关在厂商卡片的编辑区（model_caps）。
function capsReadonlyHtml(caps) {
  return (caps && caps.length ? caps : ['text']).map((c) =>
    `<span class="cap-tag cap-${esc(c)}">${CAP_LABELS[c] || c}</span>`).join('');
}

// capTogglesHtml 渲染可点击切换的能力标签（选中高亮），让用户自己决定模型支持哪些能力
function capTogglesHtml(selected) {
  const sel = new Set((selected && selected.length) ? selected : ['text']);
  return ALL_CAPS.map((c) =>
    `<span class="cap-chip ${sel.has(c) ? 'on' : ''}" data-cap="${c}" title="${CAP_LABELS[c]}：点击切换">${CAP_LABELS[c]}</span>`
  ).join('');
}

function renderRouteConfig() {
  const s = state.config.settings || {};
  $('#route-enabled').checked = s.auto_route_enabled !== false;
  renderSchemes();
}

function schemeModelRow(mid, caps) {
  const info = modelInfo(mid);
  const selected = (caps && caps.length) ? caps : (info ? info.caps : ['text']);
  return `<div class="s-model${info ? '' : ' stale'}" draggable="true" data-model="${esc(mid)}" title="${info ? '' : '该模型当前没有已配 Key 的厂商，路由时会被跳过'}">
    <span class="drag-handle" title="拖拽排序">⋮⋮</span>
    ${schemeLogoHtml(info)}
    <span class="s-model-name">${esc(mid)}</span>
    ${info ? `<span class="s-provider">${esc(info.provider)}</span>` : '<span class="s-stale-tag">未配 Key</span>'}
    <span class="s-caps" title="这里只展示能力；要改请在「密钥库」该厂商的编辑区里改模型支持类型">${capsReadonlyHtml(selected)}</span>
    <button class="mini-btn s-model-del" title="移除该模型">✕</button>
  </div>`;
}

function schemeModelOptions(sc) {
  const existing = new Set(sc.models || []);
  return keyedModels().map((m) =>
    `<option value="${esc(m.model)}" ${existing.has(m.model) ? 'disabled' : ''}>${esc(m.model)}（${capText(m.caps)} · ${esc(m.provider)}）</option>`).join('');
}

function schemeHtml(sc) {
  const caps = sc.caps || {};
  const models = (sc.models || []).map((mid) => schemeModelRow(mid, caps[mid])).join('');
  const dis = sc.enabled === false ? '' : 'checked';
  return `<div class="scheme-card" data-id="${esc(sc.id)}">
    <div class="scheme-head">
      <span class="drag-handle" title="拖拽调整方案顺序（拖这里）">⋮⋮</span>
      <input class="s-name" value="${esc(sc.name)}" placeholder="方案名（客户端填的 model 名）">
      <label class="switch" title="启用/停用"><input type="checkbox" class="s-enabled" ${dis}><span class="slider"></span></label>
      <button class="mini-btn s-test" title="用该方案发一条测试请求">▶ 测试</button>
      <button class="mini-btn s-del" title="删除该方案">删除</button>
    </div>
    <div class="scheme-models">${models}
      <div class="s-add">
        <select class="s-add-select"><option value="">${keyedModels().length ? '＋ 添加已配 Key 的模型…' : '＋ 没有可选模型：先去密钥库给厂商填 Key…'}</option>${schemeModelOptions(sc)}</select>
      </div>
    </div>
    <div class="scheme-test-result"></div>
  </div>`;
}

// routeBusy 正在方案区里编辑、或正看着某条测试结果时，不重建方案列表：
// 以前 20 秒轮询会把手工加进去但还没落库的模型行/测试结果直接抹掉。
function routeBusy() {
  const list = $('#scheme-list');
  if (!list) return false;
  const ae = document.activeElement;
  if (ae && list.contains(ae)) return true;
  return Array.from(list.querySelectorAll('.scheme-test-result')).some((el) => el.textContent.trim());
}

function renderSchemes() {
  const list = $('#scheme-list');
  if (!list) return;
  if (routeBusy()) return;
  const schemes = state.config.schemes || [];
  if (!schemes.length) {
    list.innerHTML = '<div class="muted">还没有方案。点击右上角「＋ 新建方案」，填一个名字（客户端把 model 填成该名字），再添加模型。</div>';
    return;
  }
  list.innerHTML = schemes.map(schemeHtml).join('');
  bindSchemeDrag(list);
}

// bindSchemeDrag 方案卡片与方案内模型的拖拽排序（事件委托，重复调用安全）
function bindSchemeDrag(list) {
  if (list.dataset.dragBound === '1') return;
  list.dataset.dragBound = '1';
  let dragEl = null;
  let scope = null; // 'card' | 'model'

  // 动态设置 dragard / model 是否可拖（排除输入控件与能力标签）
  list.addEventListener('mousedown', (e) => {
    if (e.target.closest('input, button, select, textarea, .cap-chip')) return;
    const card = e.target.closest('.scheme-card');
    if (!card) return;
    // 卡片拖拽：必须从卡片头部的手柄区域发起
    if (e.target.closest('.scheme-head') && !e.target.closest('.scheme-models')) {
      card.draggable = true;
      card.querySelectorAll('.s-model').forEach(m => { m.draggable = false; });
    } else {
      card.draggable = false;
    }
    // 模型拖拽：从模型行发起
    const model = e.target.closest('.s-model');
    if (model) {
      model.draggable = true;
    }
  });

  list.addEventListener('dragstart', (e) => {
    if (e.target.closest('input, button, select, textarea, .cap-chip')) return;
    const model = e.target.closest('.s-model');
    const card = e.target.closest('.scheme-card');
    if (model && card && model.draggable) {
      dragEl = model; scope = 'model';
    } else if (card && card.draggable && !model) {
      dragEl = card; scope = 'card';
    } else {
      return;
    }
    dragEl.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    try { e.dataTransfer.setData('text/plain', ''); } catch (err) {}
  });
  list.addEventListener('dragover', (e) => {
    if (!dragEl) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const sel = scope === 'model' ? '.s-model' : '.scheme-card';
    const t = e.target.closest(sel);
    list.querySelectorAll(sel).forEach((x) => x.classList.toggle('drag-over', x === t));
  });
  list.addEventListener('drop', (e) => {
    if (!dragEl) return;
    e.preventDefault();
    const sel = scope === 'model' ? '.s-model' : '.scheme-card';
    const t = e.target.closest(sel);
    list.querySelectorAll(sel).forEach((x) => x.classList.remove('drag-over'));
    if (!t || t === dragEl) return;
    const rect = t.getBoundingClientRect();
    const after = e.clientY > rect.top + rect.height / 2;
    if (after) t.after(dragEl); else t.before(dragEl);
    // 拖拽即保存
    saveSchemesSilent();
  });
  list.addEventListener('dragend', () => {
    if (dragEl) dragEl.classList.remove('dragging');
    list.querySelectorAll('.dragging,.drag-over').forEach((x) => x.classList.remove('dragging', 'drag-over'));
    dragEl = null; scope = null;
  });
}

// saveSchemesSilent 静默保存方案（任何变更后自动调用，不重渲染、不弹提示）。
// 同步内存中的 schemes，避免后续 fetchConfig 重渲染时用旧数据覆盖。
async function saveSchemesSilent() {
  const schemes = collectSchemes();
  state.config.schemes = schemes;
  // 逐张卡判定，坏的那张不提交并标红：以前「任意一张名为空或重名」会整批 return，
  // 用户改好另一张却以为自己所有改动都存了。
  const seen = new Set();
  const valid = [];
  let badMsg = '';
  schemes.forEach((sc, i) => {
    const why = schemeBad(sc, seen);
    const card = $$('#scheme-list .scheme-card')[i];
    if (card) card.classList.toggle('bad', !!why);
    if (why) { if (!badMsg) badMsg = why; return; }
    seen.add(sc.name.toLowerCase());
    valid.push(sc);
  });
  const hint = $('#route-hint');
  if (badMsg && hint) { hint.textContent = '⚠ ' + badMsg + '（其余方案已保存）'; hint.style.color = 'var(--red)'; }
  if (!valid.length && !schemes.length) {
    try { await fetch('/api/schemes', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '[]' }); } catch (e) {}
    return;
  }
  if (!valid.length) return;
  try {
    await fetch('/api/schemes', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(valid) });
    const h2 = $('#route-hint');
    if (h2 && !badMsg) { h2.textContent = '✓ 已自动保存'; h2.style.color = ''; setTimeout(() => { h2.textContent = '修改自动保存'; }, 2000); }
  } catch (e) { /* 忽略 */ }
}

// collectSchemes 按 DOM 顺序收集方案（含每个模型手动选择的能力）
function collectSchemes() {
  return Array.from($$('#scheme-list .scheme-card')).map((card) => {
    // 能力标签以厂商实际配置为准（以前的手勾 caps 完全不参与路由，留着只会误导）
    const caps = {};
    card.querySelectorAll('.s-model').forEach((m) => {
      const info = modelInfo(m.dataset.model);
      if (info && info.caps) caps[m.dataset.model] = info.caps;
    });
    return {
      id: card.dataset.id,
      name: card.querySelector('.s-name').value.trim(),
      enabled: card.querySelector('.s-enabled').checked,
      models: Array.from(card.querySelectorAll('.s-model')).map((m) => m.dataset.model),
      caps
    };
  });
}

// testScheme 用指定方案发一条测试请求（用方案名作为 model）
async function testScheme(btn) {
  const card = btn.closest('.scheme-card');
  const name = (card.querySelector('.s-name').value || '').trim();
  const resultEl = card.querySelector('.scheme-test-result');
  if (!name) { alert('请先填写方案名'); return; }
  if (!card.querySelectorAll('.s-model').length) { alert('请先为该方案添加至少一个模型'); return; }
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = '…';
  resultEl.innerHTML = '<div class="test-result loading">正在测试方案「' + esc(name) + '」…</div>';
  try {
    const res = await fetch('/api/test', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: name, max_tokens: 16, messages: [{ role: 'user', content: '你好，请只回复：OK' }] })
    });
    const data = await res.json();
    if (res.ok) {
      const content = data.choices?.[0]?.message?.content || '(空回复)';
      const rp = routedName(res.headers.get('X-Routed-Provider'));
      const rm = routedName(res.headers.get('X-Routed-Model'));
      const route = rp ? ' · 由 ' + rp + '/' + rm + ' 回答' : '';
      resultEl.innerHTML = '<div class="test-result ok">✅ 方案可用' + route + ' · ' + esc(content.slice(0, 120)) + '</div>';
    } else {
      resultEl.innerHTML = '<div class="test-result err">❌ ' + esc(data.error?.message || ('HTTP ' + res.status)) + '</div>';
    }
  } catch (e) {
    resultEl.innerHTML = '<div class="test-result err">❌ ' + esc(e.message) + '</div>';
  } finally {
    btn.textContent = original;
    btn.disabled = false;
  }
}

function setupRouteConfig() {
  $('#btn-add-scheme').addEventListener('click', () => {
    const schemes = state.config.schemes || [];
    state.config.schemes = schemes.concat({ id: 'scheme-' + Date.now(), name: '', models: [], enabled: true });
    renderSchemes();
    const cards = $$('#scheme-list .scheme-card');
    if (cards.length) {
      const inp = cards[cards.length - 1].querySelector('.s-name');
      if (inp) inp.focus();
    }
  });

  // 事件委托：删除方案(确认) / 删除模型 / 测试方案 / 能力标签切换
  $('#scheme-list').addEventListener('click', async (e) => {
    const delCard = e.target.closest('.s-del');
    if (delCard) {
      const card = delCard.closest('.scheme-card');
      const name = (card.querySelector('.s-name').value || '').trim() || '该方案';
      if (!confirm('确认删除方案「' + name + '」？此操作不可撤销。')) return;
      card.remove();
      saveSchemesSilent();
      return;
    }
    const delModel = e.target.closest('.s-model-del');
    if (delModel) { delModel.closest('.s-model').remove(); saveSchemesSilent(); return; }
    const testBtn = e.target.closest('.s-test');
    if (testBtn) { await testScheme(testBtn); return; }
    // 方案里的能力标签是只读展示（要改去厂商卡片编辑区），此处不再处理点击
  });

  // 方案名输入 / 启用开关变化 → 防抖自动保存
  $('#scheme-list').addEventListener('input', (e) => {
    if (e.target.classList.contains('s-name')) debouncedSaveSchemes();
  });
  $('#scheme-list').addEventListener('change', (e) => {
    if (e.target.classList.contains('s-enabled')) debouncedSaveSchemes();
  });

  // 下拉选择即添加模型（添加后自动保存）
  $('#scheme-list').addEventListener('change', (e) => {
    const sel = e.target.closest('.s-add-select');
    if (!sel || !sel.value) return;
    const modelsEl = sel.closest('.scheme-models');
    const row = document.createElement('div');
    row.className = 's-model';
    row.draggable = true;
    row.dataset.model = sel.value;
    const info = modelInfo(sel.value);
    row.classList.toggle('stale', !info);
    row.innerHTML = `<span class="drag-handle" title="拖拽排序">⋮⋮</span>
      ${schemeLogoHtml(info)}
      <span class="s-model-name">${esc(sel.value)}</span>
      ${info ? `<span class="s-provider">${esc(info.provider)}</span>` : '<span class="s-stale-tag">未配 Key</span>'}
      <span class="s-caps" title="只展示能力；要改请到「密钥库」该厂商的编辑区">${capsReadonlyHtml(info ? info.caps : ['text'])}</span>
      <button class="mini-btn s-model-del" title="移除">✕</button>`;
    modelsEl.insertBefore(row, sel.closest('.s-add'));
    sel.value = '';
    saveSchemesSilent();
  });
}

// 内置能力别名：方案若与它们同名会静默顶掉别名（路由时方案优先），必须拒绝
const RESERVED_SCHEME_NAMES = ['inurl', 'inurl-text', 'inurl-code', 'inurl-image', 'inurl-video', 'inurl-audio', 'auto', 'default'];

function schemeBad(sc, seen) {
  if (!sc.name) return '方案名还没填';
  if (seen.has(sc.name.toLowerCase())) return '方案名重复';
  if (RESERVED_SCHEME_NAMES.indexOf(sc.name.toLowerCase()) >= 0) return '方案名占了内置别名（inurl 等）';
  if (!sc.models.length) return '还没有添加模型';
  return '';
}

// debouncedSaveSchemes 方案名/开关变化的防抖保存
const debouncedSaveSchemes = debounce(() => saveSchemesSilent(), 600);

// ---------- 账号库（网站登录账号 / 密码） ----------

// 内置可选图标：与厂商目录同一套 CDN。取不到图时 onerror 自动回退成首字母色块。
const LOGO_CDN = 'https://cdn.jsdelivr.net/npm/@lobehub/icons-static-png@1.97.0/light/';
const LOGO_PRESETS = [
  ['OpenAI', LOGO_CDN + 'openai.png'],
  ['Claude', LOGO_CDN + 'claude-color.png'],
  ['Gemini', LOGO_CDN + 'gemini-color.png'],
  ['Grok', LOGO_CDN + 'grok.png'],
  ['通义千问', LOGO_CDN + 'qwen-color.png'],
  ['阿里云', LOGO_CDN + 'alibaba-color.png'],
  ['智谱', LOGO_CDN + 'zhipu-color.png'],
  ['Kimi', LOGO_CDN + 'kimi-color.png'],
  ['DeepSeek', LOGO_CDN + 'deepseek-color.png'],
  ['豆包', LOGO_CDN + 'doubao-color.png'],
  ['硅基流动', LOGO_CDN + 'siliconcloud-color.png'],
  ['OpenRouter', LOGO_CDN + 'openrouter-color.png'],
];

// 上传的 logo 先用 canvas 压到边长 128px 再以 data URL 内联保存（几 KB，随 keys.json 一起备份）
const LOGO_MAX_PX = 128;

function vaultHint(msg, keep) {
  const el = $('#vault-hint');
  if (!el) return;
  el.textContent = msg;
  el.style.color = msg.startsWith('❌') ? 'var(--red)' : '';
  if (keep) return;
  setTimeout(() => { el.textContent = ''; }, 2500);
}

// hostOf 从网址取主机名（用户常省略协议，这里补 https:// 再解析）
function hostOf(url) {
  const s = (url || '').trim();
  if (!s) return '';
  try { return new URL(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(s) ? s : 'https://' + s).hostname; } catch (e) { return ''; }
}

// faviconFromUrl 直接取该站自己的 favicon，不依赖第三方图标服务
function faviconFromUrl(url) {
  const h = hostOf(url);
  return h ? 'https://' + h + '/favicon.ico' : '';
}

// nameColor 由站点名派生稳定色相，让首字母 logo 彼此可辨
function nameColor(name) {
  let h = 0;
  const s = String(name || '?');
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
  return 'linear-gradient(135deg, hsl(' + h + ' 68% 56%), hsl(' + ((h + 40) % 360) + ' 70% 48%))';
}

// vaultLogoLetter 首字母兜底：名称为空时退回用网址域名，避免出现一堆「?」
function vaultLogoLetter(site) {
  const name = (site.name || '').trim();
  const src = name || hostOf(site.url) || '?';
  return src.slice(0, 1).toUpperCase();
}

function vaultLogoHtml(site) {
  if (site.logo) {
    const fb = (site.name || '').trim() || hostOf(site.url) || '?';
    return `<img src="${esc(site.logo)}" alt="" data-fb="${esc(fb)}" data-fb-bg="${nameColor(site.name || site.url)}" onerror="logoFallback(this)">`;
  }
  return `<span class="logo-fallback" style="background:${nameColor(site.name || site.url)}">${esc(vaultLogoLetter(site))}</span>`;
}

// vaultAccountHtml 一组账号：上排账号、下排密码，第三排备注（点 📝 展开）。
// 无密码（验证码 / 第三方登录）时加 .no-pass：隐藏 👁/📋 并显式标出「未设置」。
function vaultAccountHtml(a, i) {
  a = a || {};
  const label = a.label || ('账号 ' + (i + 1));
  const hasNote = !!(a.note || '').trim();
  return `<div class="acc-item${a.password ? '' : ' no-pass'}${hasNote ? ' show-note' : ''}" data-index="${i}" data-acc-label="${esc(a.label || '')}">
    <div class="acc-row">
      <span class="acc-tag acc-rename" title="${esc(a.label || '') || '双击重命名，如「Google 登录」「主号」'}">${esc(label)}</span>
      <input class="acc-user" placeholder="邮箱 / 手机号 / 用户名" value="${esc(a.user || '')}" autocomplete="off" spellcheck="false">
      <button class="eye-mini btn-acc-copy" data-field="user" title="复制账号">📋</button>
      <button class="eye-mini btn-acc-copy-both" title="一次复制账号 + 密码（两行）">⧉</button>
      <button class="eye-mini btn-acc-del" title="删除此账号">✕</button>
    </div>
    <div class="acc-row">
      <span class="acc-tag">密码</span>
      <input class="acc-pass" type="password" placeholder="密码（只有验证码 / 第三方登录时留空）" value="${esc(a.password || '')}" autocomplete="off" spellcheck="false">
      <span class="np-badge">未设置 · 验证码登录</span>
      <button class="eye-mini btn-acc-eye" title="显示 / 隐藏密码">👁</button>
      <button class="eye-mini btn-acc-copy" data-field="password" title="复制密码">📋</button>
      <button class="eye-mini btn-acc-note${hasNote ? ' on' : ''}" title="这个账号的备注（如「公司号，月底到期」）">📝</button>
    </div>
    <div class="acc-row acc-note-row">
      <span class="acc-tag">备注</span>
      <input class="acc-note" placeholder="如「公司号，月底到期」「绑了 xx 邮箱」" value="${esc(a.note || '')}" autocomplete="off">
    </div>
  </div>`;
}

function vaultSiteHtml(s) {
  const accounts = (s.accounts && s.accounts.length) ? s.accounts : [{}];
  return `<div class="card vault-card" data-id="${esc(s.id)}" data-logo="${esc(s.logo || '')}">
    <div class="card-head">
      <span class="drag-handle v-drag" title="按住拖拽调整站点顺序">⋮⋮</span>
      <div class="logo v-logo" title="点击更换图标，或把图片直接拖到这里">${vaultLogoHtml(s)}</div>
      <div class="head-info">
        <input class="v-name" placeholder="站点名称，如 OpenAI / Qoder" value="${esc(s.name || '')}" autocomplete="off">
        <div class="v-urlrow">
          <input class="v-url" placeholder="登录页网址，如 https://platform.openai.com" value="${esc(s.url || '')}" autocomplete="off" spellcheck="false">
          <button class="mini-btn v-open" title="用系统浏览器打开该网址">打开 ↗</button>
        </div>
      </div>
    </div>
    <input class="v-note" placeholder="备注（可选）：登录方式、绑定手机、注意事项…" value="${esc(s.note || '')}">
    <div class="acc-list">${accounts.map(vaultAccountHtml).join('')}</div>
    <div class="key-actions">
      <button class="mini-btn btn-add-acc">＋ 添加账号</button>
      <button class="del-mini btn-v-del" title="删除该站点及其全部账号">🗑</button>
    </div>
  </div>`;
}

function renderVault() {
  const grid = $('#vault-grid');
  if (!grid) return;
  const ae = document.activeElement;
  if (ae && grid.contains(ae) && /^(INPUT|TEXTAREA|SELECT)$/.test(ae.tagName)) return; // 正在输入，不打断
  const sites = (state.config && state.config.vault) || [];
  grid.innerHTML = sites.map(vaultSiteHtml).join('');
  const count = $('#vault-count');
  if (count) {
    const accs = sites.reduce((n, s) => n + ((s.accounts && s.accounts.length) || 0), 0);
    count.textContent = sites.length ? (sites.length + ' 家站点 · ' + accs + ' 个账号') : '';
  }
  const empty = $('#vault-empty');
  empty.textContent = sites.length ? '' : '还没有站点。点右上角「＋ 新建站点」，填名称 / 网址，再填账号密码即可。';
  empty.classList.toggle('hidden', !!sites.length);
  applyVaultFilter();
}

// vaultCardText 汇总卡片里可被搜索的文本（不含密码）
function vaultCardText(card) {
  const parts = [card.querySelector('.v-name').value, card.querySelector('.v-url').value, card.querySelector('.v-note').value];
  card.querySelectorAll('.acc-item').forEach((row) => {
    parts.push(row.dataset.accLabel || '', row.querySelector('.acc-user').value, row.querySelector('.acc-note').value);
  });
  return parts.join(' ').toLowerCase();
}

// applyVaultFilter 按关键字隐藏不匹配的卡片（保留在 DOM 中，收集时才不会漏站点）
function applyVaultFilter() {
  const q = ($('#vault-search').value || '').trim().toLowerCase();
  let shown = 0;
  $$('#vault-grid .vault-card').forEach((card) => {
    const ok = !q || vaultCardText(card).includes(q);
    card.classList.toggle('hidden', !ok);
    if (ok) shown++;
  });
  const empty = $('#vault-empty');
  if (q) {
    empty.textContent = shown ? '' : '没有匹配的站点。';
    empty.classList.toggle('hidden', !!shown);
  }
}

// collectVault 按 DOM 顺序收集全部站点（含被搜索隐藏的）
function collectVault() {
  const out = [];
  $$('#vault-grid .vault-card').forEach((card) => {
    const accounts = [];
    card.querySelectorAll('.acc-item').forEach((row) => {
      const user = row.querySelector('.acc-user').value.trim();
      const password = row.querySelector('.acc-pass').value; // 密码不 trim，空格可能是密码的一部分
      const note = row.querySelector('.acc-note').value.trim();
      const label = row.dataset.accLabel || '';
      if (!label && !user && !password && !note) return;
      accounts.push({ label, user, password, note });
    });
    out.push({
      id: card.dataset.id,
      name: card.querySelector('.v-name').value.trim(),
      url: card.querySelector('.v-url').value.trim(),
      logo: card.dataset.logo || '',
      note: card.querySelector('.v-note').value.trim(),
      accounts
    });
  });
  return out;
}

async function saveVault() {
  const vault = collectVault();
  state.config.vault = vault; // 先同步内存，20 秒轮询回来前渲染到的也是最新值
  try {
    const res = await fetch('/api/vault', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(vault)
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { vaultHint('❌ ' + (data.error || ('HTTP ' + res.status)), true); return; }
    if (data.vault) state.config.vault = data.vault;
    vaultHint('✓ 已保存');
  } catch (e) {
    vaultHint('❌ 保存失败：' + e.message, true);
  }
}
const debouncedSaveVault = debounce(() => saveVault(), 600);

// renumberAcc 增删账号行后刷新默认别名（自定义过名称的行不受影响）
function renumberAcc(list) {
  Array.from(list.querySelectorAll('.acc-item')).forEach((row, i) => {
    row.dataset.index = i;
    const tag = row.querySelector('.acc-rename');
    if (tag && !tag.querySelector('input')) tag.textContent = row.dataset.accLabel || ('账号 ' + (i + 1));
  });
}

// isBlankSiteCard 刚点「＋ 新建站点」生成、还没填任何东西的卡片
function isBlankSiteCard(card) {
  if (card.querySelector('.v-name').value.trim() !== '未命名站点') return false;
  if (card.querySelector('.v-url').value.trim() || card.querySelector('.v-note').value.trim()) return false;
  if (card.dataset.logo) return false;
  return Array.from(card.querySelectorAll('.acc-item')).every((row) =>
    !(row.dataset.accLabel || '') && !row.querySelector('.acc-user').value.trim() &&
    !row.querySelector('.acc-pass').value && !row.querySelector('.acc-note').value.trim());
}

function addVaultSite() {
  const grid = $('#vault-grid');
  // 已经有一张没填的空卡片就复用它：连点两次不该留下两个「未命名站点」
  const blank = Array.from(grid.querySelectorAll('.vault-card')).find(isBlankSiteCard);
  if (blank) {
    blank.scrollIntoView({ block: 'nearest' });
    const reuse = blank.querySelector('.v-name');
    reuse.focus();
    reuse.select();
    return;
  }
  const tmp = document.createElement('div');
  // 名称直接给一个真实默认值：新站点立即落库，不会被后续重渲染清掉
  tmp.innerHTML = vaultSiteHtml({ id: 'site-' + Date.now(), name: '未命名站点', accounts: [{}] });
  const card = tmp.firstElementChild;
  grid.prepend(card);
  $('#vault-empty').classList.add('hidden');
  const name = card.querySelector('.v-name');
  name.focus();
  name.select();
  debouncedSaveVault();
}

function addVaultAccount(card) {
  const list = card.querySelector('.acc-list');
  const tmp = document.createElement('div');
  tmp.innerHTML = vaultAccountHtml({}, list.querySelectorAll('.acc-item').length);
  const row = tmp.firstElementChild;
  list.appendChild(row);
  renumberAcc(list);
  row.querySelector('.acc-user').focus();
  debouncedSaveVault();
}

function removeVaultAccount(btn) {
  const row = btn.closest('.acc-item');
  const list = row.parentElement;
  // 确认框里优先显示账号本身（最能认出是哪一条），其次别名
  const who = row.querySelector('.acc-user').value.trim() || row.dataset.accLabel || '该账号';
  if (!confirm('确认删除账号「' + who + '」？')) return;
  row.remove();
  renumberAcc(list);
  debouncedSaveVault();
}

async function deleteVaultSite(card) {
  const name = card.querySelector('.v-name').value.trim() || '该站点';
  const n = card.querySelectorAll('.acc-item').length;
  if (!confirm('确认删除站点「' + name + '」及其 ' + n + ' 个账号？此操作不可撤销。')) return;
  card.remove();
  await saveVault();
  renderVault();
}

// ===== 密码可见性：显示一会儿就自动收回，别让明文一直摊在屏幕上 =====
let maskTimer = null;

function maskVaultPasswords() {
  clearTimeout(maskTimer);
  $$('#vault-grid .acc-pass[type="text"]').forEach((inp) => { inp.type = 'password'; });
}

function toggleVaultPass(btn) {
  const inp = btn.closest('.acc-item').querySelector('.acc-pass');
  inp.type = inp.type === 'password' ? 'text' : 'password';
  clearTimeout(maskTimer);
  if (inp.type === 'text') maskTimer = setTimeout(maskVaultPasswords, 20000); // 20 秒后自动遮回
}

async function copyVaultField(btn) {
  const row = btn.closest('.acc-item');
  const inp = row.querySelector(btn.dataset.field === 'password' ? '.acc-pass' : '.acc-user');
  const v = inp ? inp.value : '';
  if (!v) { vaultHint('这条目还是空的'); return; }
  const ok = await copyText(v);
  const old = btn.textContent;
  btn.textContent = ok ? '✓' : '✕';
  if (!ok) vaultHint('❌ 复制失败，请在输入框里手动选中后复制', true);
  setTimeout(() => { btn.textContent = old; }, 1000);
}

// copyVaultBoth 一次拿到「账号 + 换行 + 密码」，登录时先粘账号再粘密码
async function copyVaultBoth(btn) {
  const row = btn.closest('.acc-item');
  const user = row.querySelector('.acc-user').value;
  const pass = row.querySelector('.acc-pass').value;
  const text = pass ? user + '\n' + pass : user;
  const ok = await copyText(text);
  const old = btn.textContent;
  btn.textContent = ok ? '✓' : '✕';
  vaultHint(ok ? '✓ 已复制账号' + (pass ? ' + 密码' : '（该账号没填密码）') : '❌ 复制失败', !ok);
  setTimeout(() => { btn.textContent = old; }, 1000);
}

// startAccLabelRename 双击账号别名就地重命名（回车/失焦保存，Esc 取消）
function startAccLabelRename(tag) {
  if (tag.querySelector('input')) return;
  const row = tag.closest('.acc-item');
  const inp = document.createElement('input');
  inp.className = 'acc-name-input';
  inp.value = row.dataset.accLabel || '';
  inp.placeholder = tag.textContent;
  inp.maxLength = 24;
  tag.textContent = '';
  tag.appendChild(inp);
  inp.focus();
  inp.select();
  let done = false;
  const finish = (save) => {
    if (done) return;
    done = true;
    if (save) row.dataset.accLabel = inp.value.trim();
    tag.textContent = row.dataset.accLabel || ('账号 ' + (Number(row.dataset.index) || 0) + 1);
    if (save) debouncedSaveVault();
  };
  inp.addEventListener('keydown', (ev) => {
    ev.stopPropagation();
    if (ev.key === 'Enter') { ev.preventDefault(); finish(true); }
    else if (ev.key === 'Escape') { ev.preventDefault(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
}

function openVaultSiteUrl(card) {
  let u = (card.querySelector('.v-url').value || '').trim();
  if (!u) { vaultHint('请先填写网址'); return; }
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(u)) u = 'https://' + u;
  openExternal(u);
}

function hasFileTransfer(e) {
  const dt = e.dataTransfer || (e.originalEvent && e.originalEvent.dataTransfer);
  if (!dt) return false;
  return Array.from(dt.types || []).indexOf('Files') >= 0;
}

// renderCardLogo 按卡片当前数据重画 logo 区（无 logo 时的首字母随名称变化）
function renderCardLogo(card) {
  card.querySelector('.v-logo').innerHTML =
    vaultLogoHtml({ logo: card.dataset.logo, name: card.querySelector('.v-name').value });
}

// setCardLogo 换 logo：更新卡片数据 + 即时预览 + 自动保存
function setCardLogo(card, logo) {
  card.dataset.logo = logo || '';
  renderCardLogo(card);
  debouncedSaveVault();
}

// readLogoFile 把图片等比缩到 128px 内联成 data URL
function readLogoFile(card, file) {
  if (!file || !/^image\//.test(file.type)) { vaultHint('❌ 只能使用图片文件', true); return; }
  const objUrl = URL.createObjectURL(file);
  const img = new Image();
  img.onload = () => {
    URL.revokeObjectURL(objUrl);
    const scale = Math.min(1, LOGO_MAX_PX / Math.max(img.width || 1, img.height || 1));
    const w = Math.max(1, Math.round((img.width || LOGO_MAX_PX) * scale));
    const h = Math.max(1, Math.round((img.height || LOGO_MAX_PX) * scale));
    const cv = document.createElement('canvas');
    cv.width = w;
    cv.height = h;
    cv.getContext('2d').drawImage(img, 0, 0, w, h);
    let data = '';
    try { data = cv.toDataURL('image/webp', 0.9); } catch (e) { data = ''; }
    if (!data.startsWith('data:image/webp')) {
      try { data = cv.toDataURL('image/png'); } catch (e) { vaultHint('❌ 图片读取失败', true); return; }
    }
    setCardLogo(card, data);
  };
  img.onerror = () => { URL.revokeObjectURL(objUrl); vaultHint('❌ 无法解析这张图片', true); };
  img.src = objUrl;
}

function pickLogoFile(card) {
  const inp = document.createElement('input');
  inp.type = 'file';
  inp.accept = 'image/*';
  inp.onchange = () => { if (inp.files && inp.files[0]) readLogoFile(card, inp.files[0]); };
  inp.click();
}

// showLogoMenu 在图标下方弹出 logo 选择器：内置图标 / 按网址取图标 / 上传 / 首字母
function showLogoMenu(anchor, card) {
  document.querySelectorAll('.logo-menu').forEach((m) => m.remove());
  const menu = document.createElement('div');
  menu.className = 'logo-menu';
  menu.innerHTML = `<div class="lm-title">选一个内置图标</div>
    <div class="lm-grid">${LOGO_PRESETS.map(([n, u]) =>
      `<button type="button" class="lm-item" data-logo="${esc(u)}" title="${esc(n)}"><img src="${esc(u)}" alt="${esc(n)}" onerror="this.style.opacity=.2"></button>`).join('')}</div>
    <div class="lm-sep"></div>
    <button type="button" class="lm-row" data-act="favicon">🔗 按网址自动取该站图标</button>
    <button type="button" class="lm-row" data-act="upload">🖼️ 上传图片（也可直接拖到图标上）</button>
    <button type="button" class="lm-row" data-act="none">🔤 用名称首字母</button>`;
  placePopMenu(menu, anchor);
  menu.addEventListener('click', (e) => {
    const item = e.target.closest('[data-logo],[data-act]');
    if (!item) return;
    const act = item.dataset.act;
    if (act === 'favicon') {
      const u = faviconFromUrl(card.querySelector('.v-url').value);
      if (!u) { vaultHint('请先填写网址，再自动取图标'); menu.remove(); return; }
      setCardLogo(card, u);
    } else if (act === 'upload') {
      pickLogoFile(card);
    } else if (act === 'none') {
      setCardLogo(card, '');
    } else {
      setCardLogo(card, item.dataset.logo);
    }
    menu.remove();
  });
  setTimeout(() => {
    const close = (ev) => {
      if (!menu.contains(ev.target)) { menu.remove(); document.removeEventListener('click', close); }
    };
    document.addEventListener('click', close);
  }, 0);
}

// toggleVaultNote 📝 展开 / 收起该账号的备注行（收起不清内容，📝 亮着表示「有备注」）
function toggleVaultNote(btn) {
  const row = btn.closest('.acc-item');
  const inp = row.querySelector('.acc-note');
  if (row.classList.contains('show-note')) {
    inp.blur();
    row.classList.remove('show-note');
    return;
  }
  row.classList.add('show-note');
  inp.focus();
}

async function exportVault() {
  const btn = $('#btn-export-vault');
  if (!(state.config.vault || []).length) { vaultHint('账号库还是空的，没有可导出的内容'); return; }
  btn.disabled = true;
  vaultHint('正在准备导出…（在弹出的「另存为」里选位置）');
  try {
    const res = await fetch('/api/vault/export', { method: 'POST' });
    const data = await res.json().catch(() => ({}));
    if (data.cancelled) vaultHint('已取消导出');
    else if (!res.ok) vaultHint('❌ ' + (data.error || ('HTTP ' + res.status)), true);
    else vaultHint('✓ 已导出到 ' + (data.path || ''), true);
  } catch (e) {
    vaultHint('❌ 导出失败：' + e.message, true);
  } finally {
    btn.disabled = false;
  }
}

function onVaultClick(e) {
  const logo = e.target.closest('.v-logo');
  if (logo) { showLogoMenu(logo, logo.closest('.vault-card')); return; }
  const eye = e.target.closest('.btn-acc-eye');
  if (eye) { toggleVaultPass(eye); return; }
  const both = e.target.closest('.btn-acc-copy-both');
  if (both) { copyVaultBoth(both); return; }
  const note = e.target.closest('.btn-acc-note');
  if (note) { toggleVaultNote(note); return; }
  const copy = e.target.closest('.btn-acc-copy');
  if (copy) { copyVaultField(copy); return; }
  const del = e.target.closest('.btn-acc-del');
  if (del) { removeVaultAccount(del); return; }
  const add = e.target.closest('.btn-add-acc');
  if (add) { addVaultAccount(add.closest('.vault-card')); return; }
  const open = e.target.closest('.v-open');
  if (open) { openVaultSiteUrl(open.closest('.vault-card')); return; }
  const vdel = e.target.closest('.btn-v-del');
  if (vdel) { deleteVaultSite(vdel.closest('.vault-card')); }
}

// bindVaultOrder 站点卡片拖拽排序（从左侧 ⋮⋮ 手柄发起，顺序即保存顺序）
function bindVaultOrder(grid) {
  let dragEl = null;
  grid.addEventListener('mousedown', (e) => {
    const card = e.target.closest('.vault-card');
    if (card) card.draggable = !!e.target.closest('.v-drag');
  });
  grid.addEventListener('dragstart', (e) => {
    const card = e.target.closest('.vault-card');
    if (!card || !card.draggable || hasFileTransfer(e)) return;
    dragEl = card;
    card.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    try { e.dataTransfer.setData('text/plain', ''); } catch (err) {}
  });
  grid.addEventListener('dragover', (e) => {
    if (!dragEl || hasFileTransfer(e)) return;
    const t = e.target.closest('.vault-card');
    if (!t || t === dragEl) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    grid.querySelectorAll('.vault-card').forEach((x) => x.classList.toggle('drag-over', x === t));
  });
  grid.addEventListener('drop', (e) => {
    if (!dragEl) return;
    const t = e.target.closest('.vault-card');
    if (!t || t === dragEl) return;
    e.preventDefault();
    const rect = t.getBoundingClientRect();
    if (e.clientY > rect.top + rect.height / 2) t.after(dragEl); else t.before(dragEl);
    debouncedSaveVault(); // 拖完即按新的 DOM 顺序落库
  });
  grid.addEventListener('dragend', () => {
    grid.querySelectorAll('.dragging,.drag-over').forEach((x) => x.classList.remove('dragging', 'drag-over'));
    dragEl = null;
  });
}

function setupVault() {
  const grid = $('#vault-grid');
  if (!grid) return;
  $('#btn-add-site').addEventListener('click', addVaultSite);
  $('#btn-export-vault').addEventListener('click', exportVault);
  $('#vault-search').addEventListener('input', applyVaultFilter);
  // 事件委托绑在网格容器上：卡片重建后依然生效
  grid.addEventListener('click', onVaultClick);
  grid.addEventListener('dblclick', (e) => {
    const tag = e.target.closest('.acc-rename');
    if (tag) startAccLabelRename(tag);
  });
  grid.addEventListener('input', (e) => {
    if (e.target.closest('.acc-name-input')) return; // 就地重命名自己负责保存
    const t = e.target;
    if (t.classList.contains('acc-pass')) {
      t.closest('.acc-item').classList.toggle('no-pass', !t.value); // 没密码就别摆 👁/📋
    } else if (t.classList.contains('acc-note')) {
      t.closest('.acc-item').querySelector('.btn-acc-note').classList.toggle('on', !!t.value.trim());
    } else if (t.classList.contains('v-name') || t.classList.contains('v-url')) {
      const card = t.closest('.vault-card');
      if (!card.dataset.logo) renderCardLogo(card); // 首字母跟着名称走，名称空则跟着域名
    }
    debouncedSaveVault();
  });
  bindVaultOrder(grid);
  grid.addEventListener('dragover', (e) => {
    const box = e.target.closest('.v-logo');
    if (!box || !hasFileTransfer(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
    box.classList.add('drop-on');
  });
  grid.addEventListener('dragleave', (e) => {
    const box = e.target.closest('.v-logo');
    if (box) box.classList.remove('drop-on');
  });
  grid.addEventListener('drop', (e) => {
    const box = e.target.closest('.v-logo');
    if (!box) return;
    e.preventDefault();
    box.classList.remove('drop-on');
    const f = e.dataTransfer.files && e.dataTransfer.files[0];
    if (f) readLogoFile(box.closest('.vault-card'), f);
  });
  // 图片落在图标之外时不要导航到本地文件（页面会被换成一张图）
  document.addEventListener('drop', (e) => { if (hasFileTransfer(e)) e.preventDefault(); });
  // 窗口失焦 = 人可能要走开了，明文密码先收回遮罩
  window.addEventListener('blur', maskVaultPasswords);
}

// ---------- 页签 / 筛选 / 主题 / 标题栏 ----------
function setupTabs() {
  $$('.nav-item').forEach((t) => {
    t.addEventListener('click', () => {
      switchToTab(t.dataset.tab);
      // 管理面板页签：切过去时自动启动边车并在内嵌 iframe 中加载面板
      if (t.dataset.tab === 'panel') {
        loadCliPanel();
      }
    });
  });
  $$('.pill').forEach((p) => {
    p.addEventListener('click', () => {
      $$('.pill').forEach((x) => x.classList.remove('active'));
      p.classList.add('active');
      state.filter = p.dataset.filter;
      renderGrid();
    });
  });
  $('#search-provider').addEventListener('input', renderGrid);
  $('#only-keyed').addEventListener('change', renderGrid);
  $('#group-filter').addEventListener('change', renderGrid);
  $('#btn-add-custom').addEventListener('click', () => {
    $('#custom-grid-title').classList.remove('hidden');
    const grid = $('#custom-grid');
    const tmp = document.createElement('div');
    tmp.innerHTML = newCustomCardHtml();
    const card = tmp.firstElementChild;
    grid.prepend(card);
    bindCardEvents(card);
  });
}

function currentTheme() {
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

function applyTheme(theme) {
  const set = () => {
    if (theme === 'dark') {
      document.documentElement.setAttribute('data-theme', 'dark');
    } else {
      document.documentElement.removeAttribute('data-theme');
    }
    updateThemeBtn();
    try { localStorage.setItem('theme', theme); } catch (e) {}
  };
  if (document.startViewTransition) {
    document.startViewTransition(set);
  } else {
    set();
  }
}

function updateThemeBtn() {
  const dark = currentTheme() === 'dark';
  const ico = $('#theme-ico');
  const txt = $('#theme-text');
  if (ico) ico.textContent = dark ? '☀️' : '🌙';
  if (txt) txt.textContent = dark ? '日间' : '深色';
}

function setupTheme() {
  const btn = $('#theme-toggle');
  if (!btn) return;
  btn.addEventListener('click', () => {
    applyTheme(currentTheme() === 'dark' ? 'light' : 'dark');
  });
  updateThemeBtn();
}

// 跟随 LifeSystem 主题（内嵌时轮询 /api/theme 同步文件）
let lastLifeTheme = null;

// syncLifeTheme 被 LifeSystem 内嵌时跟随它的主题。
// 只在「那边真的变了」时动手：以前每 1.5 秒无条件掰一次，
// 用户在 ApiCluster 里手动点深色/浅色，1.5 秒后就被打回原形。
async function syncLifeTheme() {
  try {
    const res = await fetch('/api/theme');
    if (!res.ok) return;
    const data = await res.json();
    if (data.theme !== 'dark' && data.theme !== 'light') return;
    if (data.theme === lastLifeTheme) return;
    lastLifeTheme = data.theme;
    if (currentTheme() !== data.theme) applyTheme(data.theme);
  } catch (e) {}
}

// ---------- 侧边栏收起/展开 ----------
function setupSidebarToggle() {
  const btn = $('#sidebar-toggle');
  const layout = document.querySelector('.layout');
  if (!btn || !layout) return;
  const apply = (collapsed) => {
    layout.classList.toggle('sidebar-collapsed', collapsed);
    btn.textContent = collapsed ? '›' : '‹';
    btn.title = collapsed ? '展开侧边栏' : '收起侧边栏';
    try { localStorage.setItem('sidebar-collapsed', collapsed ? '1' : '0'); } catch (e) {}
  };
  btn.addEventListener('click', () => {
    apply(!layout.classList.contains('sidebar-collapsed'));
  });
  // 恢复上次的收起状态
  try { if (localStorage.getItem('sidebar-collapsed') === '1') apply(true); } catch (e) {}
}

// ---------- 订阅账号（内置 CLIProxyAPI 边车） ----------
function fmtDur(s) {
  s = Number(s) || 0;
  if (s < 60) return s + ' 秒';
  if (s < 3600) return Math.floor(s / 60) + ' 分 ' + (s % 60) + ' 秒';
  return Math.floor(s / 3600) + ' 时 ' + Math.floor((s % 3600) / 60) + ' 分';
}

// cliMgmt 调用 ApiCluster 转发出去的边车管理接口
async function cliMgmt(path, method, body) {
  try {
    const opts = { method: method || 'GET' };
    if (body !== undefined) {
      opts.headers = { 'Content-Type': 'application/json' };
      opts.body = JSON.stringify(body);
    }
    const res = await fetch('/api/cliproxy/mgmt?path=' + encodeURIComponent(path), opts);
    const text = await res.text();
    let data;
    try { data = JSON.parse(text); } catch (e) { data = { raw: text }; }
    // 401 = 管理密钥与边车 config.yaml 里的哈希不是同一把，提示一键修复；
    // 其它失败（502 抖动、未启动）不动这个标记，免得修复条一闪就没
    if (res.status === 401) setCliAuthFailed(true);
    return { ok: res.ok, status: res.status, data };
  } catch (e) {
    return { ok: false, status: 0, data: { error: e.message } };
  }
}

// ===== 管理密钥失配（面板 401 invalid management key）=====
let cliAuthFailed = false;

function setCliAuthFailed(failed) {
  if (cliAuthFailed === !!failed) return;
  cliAuthFailed = !!failed;
  renderKeyBanner();
}

function renderKeyBanner() {
  $$('.js-auth-banner').forEach((el) => {
    if (!cliAuthFailed) {
      el.classList.add('hidden');
      el.innerHTML = '';
      return;
    }
    el.classList.remove('hidden');
    el.innerHTML = '<span>⚠ 管理密钥与边车不一致（401 invalid management key），面板登录和账号列表都会鉴权失败。</span>' +
      '<button class="mini-btn btn-fix-key" type="button">🔑 一键修复</button>';
  });
}

// fixCliKey 让后端把明文密钥写回 config.yaml 并重启边车，成功后把新密钥放进剪贴板
async function fixCliKey() {
  $$('.btn-fix-key, #cli-fix-key').forEach((b) => { b.disabled = true; b.textContent = '修复中…'; });
  const box = $('#cli-status-error');
  let msg = '';
  try {
    const res = await fetch('/api/cliproxy/fix-mgmt-key', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}'
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      msg = '❌ ' + (data.error || ('HTTP ' + res.status));
    } else {
      if (data.mgmt_key) await copyText(data.mgmt_key);
      cliAuthFailed = false;
      renderKeyBanner();
      msg = data.verified
        ? '✅ ' + (data.note || '已修复') + '；管理密钥已复制到剪贴板，在面板登录框 Ctrl+V 粘贴。'
        : '✅ ' + (data.note || '配置已重写') + '；密钥已复制到剪贴板。';
    }
  } catch (e) {
    msg = '❌ 修复失败: ' + e.message;
  } finally {
    $$('.btn-fix-key, #cli-fix-key').forEach((b) => {
      b.disabled = false;
      b.textContent = b.id === 'cli-fix-key' ? '🔑 修复管理密钥' : '🔑 一键修复';
    });
  }
  if (box) { box.textContent = msg; box.dataset.busy = ''; }
  await fetchCliStatus();
  await fetchCliAccounts();
  const frame = $('#cli-panel-frame');
  if (frame && frame.src) frame.src = frame.src; // 重新加载内嵌面板
  return msg;
}

// renderOrphanBanner 端口上残留一台不归当前 ApiCluster 管的边车时必须说出来：
// 否则界面按「我有没有启动它」显示成已停止，而端口实际还在被用。
function renderOrphanBanner(orphan, running) {
  const el = $('#cli-orphan-banner');
  if (!el) return;
  if (!orphan || !orphan.port_busy || running) {
    el.classList.add('hidden');
    el.innerHTML = '';
    return;
  }
  el.classList.remove('hidden');
  el.innerHTML = '<span>⚠ 端口 ' + orphan.port + ' 上确实有边车在跑（PID ' + orphan.pid +
    (orphan.name ? '，' + esc(orphan.name) : '') + '），但它不是当前 ApiCluster 启动的' +
    (orphan.ours ? '（是上一轮被强杀后残留的自家实例）' : '（可能是你自己另开的一份 CLIProxyAPI）') +
    '，所以这里显示「已停止」。' +
    (orphan.ours ? '' : '要么在下方换一个端口，要么自行处理该进程；确认是残留实例时这里才能替你结束它。') +
    '</span>' + (orphan.ours ? '<button class="mini-btn btn-kill-orphan" type="button">结束残留进程</button>' : '');
}

async function killCliOrphan(btn) {
  if (btn) { btn.disabled = true; btn.textContent = '结束中…'; }
  const box = $('#cli-status-error');
  try {
    const res = await fetch('/api/cliproxy/kill-orphan', { method: 'POST' });
    const data = await res.json().catch(() => ({}));
    if (!res.ok || data.error) {
      if (box) { box.dataset.busy = '1'; box.textContent = '❌ ' + (data.error || '结束失败'); }
    } else if (box) {
      box.textContent = '';
    }
  } catch (e) {
    if (box) box.textContent = '❌ ' + e.message;
  }
  await fetchCliStatus();
  await fetchCliAccounts();
}

async function fetchCliStatus() {
  try {
    const res = await fetch('/api/cliproxy/status');
    if (!res.ok) return;
    renderCliStatus(await res.json());
  } catch (e) { /* 忽略 */ }
}

function renderCliStatus(d) {
  const st = d.status || {};
  const running = !!st.running;
  const ready = !!st.ready;
  cliRunning = ready;
  setCliAuthFailed(!!st.auth_failed); // 密钥失配时在页面上挂出「一键修复」条
  renderOrphanBanner(d.orphan, running);
  const dot = $('#cli-dot');
  dot.classList.toggle('on', ready);
  dot.classList.toggle('warn', running && !ready);
  $('#cli-status-text').textContent = ready ? '运行中' : (running ? '启动中…' : '已停止');
  $('#cli-status-sub').textContent = st.exe_found ? '' : '未找到 CLIProxyAPI.exe';
  const errBox = $('#cli-status-error');
  if (st.last_error) {
    errBox.textContent = '⚠ ' + st.last_error;
  } else if (!errBox.dataset.busy) {
    errBox.textContent = '';
  }

  const rows = [
    ['监听端口', st.port],
    ['进程 PID', st.pid || '-'],
    ['运行时长', st.uptime_seconds ? fmtDur(st.uptime_seconds) : '-'],
    ['自动重启', (st.restarts || 0) + ' 次'],
    ['可执行文件', st.exe_path || '未找到'],
    ['配置文件', st.config_path || '-'],
    ['凭证目录', st.auth_dir || '-'],
  ];
  $('#cli-status-grid').innerHTML = rows.map(([k, v]) =>
    `<div class="cli-kv"><span class="cli-k">${esc(k)}</span><span class="cli-v" title="${esc(v)}">${esc(v)}</span></div>`
  ).join('');

  $('#cli-endpoints').innerHTML = (d.endpoints || []).map((e) =>
    `<div class="cli-ep"><span class="cli-ep-name">${esc(e.name)}</span><code>${esc(e.path)}</code>` +
    `<span class="muted ep-note">${esc(e.note)}</span>` +
    `<button class="eye-mini btn-ep-copy" data-url="${esc(e.path)}" title="复制这个 Base URL，粘给客户端">📋</button></div>`
  ).join('');
  $('#cli-apikey').textContent = st.api_key || '-';
  $('#cli-mgmtkey').textContent = st.mgmt_key || '-';

  const box = $('#cli-providers');
  box.innerHTML = (d.providers || []).map((p) =>
    `<button class="mini-btn btn-cli-login" data-provider="${esc(p.id)}" title="${esc(p.hint)}">🔑 ${esc(p.name)}</button>`
  ).join('');
  box.querySelectorAll('.btn-cli-login').forEach((b) => {
    b.addEventListener('click', () => cliLogin(b.dataset.provider, b));
  });

  // 用户正在编辑设置时（有未保存修改）不覆盖输入，避免轮询把改动重置回去
  if (!cliSettingsDirty) {
    $('#cli-enabled').checked = !!st.enabled;
    $('#cli-port').value = st.port || 8317;
    $('#cli-exepath').value = st.exe_path_setting || '';
  }
  $('#cli-exepath').placeholder = st.exe_path ? ('自动查找：' + st.exe_path) : '自动查找';
}

// cliBusy 启停过程中禁用这三个按钮：连点会让状态与进程来回打架
let cliBusy = false;

function setCliBusy(on, label) {
  cliBusy = on;
  [['#cli-start', '启动'], ['#cli-stop', '停止'], ['#cli-restart', '重启']].forEach(([sel, text]) => {
    const b = $(sel);
    if (!b) return;
    b.disabled = on;
    b.textContent = on && b.textContent.includes(label) ? text + '中…' : text;
  });
}

async function cliAction(action) {
  if (cliBusy) return;
  const errBox = $('#cli-status-error');
  const label = { start: '启动', stop: '停止', restart: '重启' }[action] || action;
  setCliBusy(true, label);
  errBox.dataset.busy = '1';
  errBox.textContent = '正在' + label + '…';
  try {
    const res = await fetch('/api/cliproxy/action', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action })
    });
    const data = await res.json();
    if (!res.ok) errBox.textContent = '❌ ' + (data.error || ('HTTP ' + res.status));
    else errBox.textContent = '';
  } catch (e) {
    errBox.textContent = '❌ ' + e.message;
  } finally {
    delete errBox.dataset.busy;
    setCliBusy(false);
  }
  // 边车停了以后，内嵌面板那张 iframe 会留在一个连不上的错误页上，直接清掉更诚实
  if (action === 'stop') {
    const frame = $('#cli-panel-frame');
    if (frame) {
      frame.removeAttribute('src');
      cliPanelUrl = '';
      $('#cli-panel-hint').textContent = '订阅账号服务已停止，面板也随之不可用；要看面板点上方「启动」或「🌐 管理面板」。';
    }
  }
  await fetchCliStatus();
  await fetchCliAccounts();
  await fetchCliLog();
}

let cliPanelUrl = '';

// dropBlankVaultSites 离开账号库时丢弃「点了新建却什么都没填」的空卡片（等价于取消）
function dropBlankVaultSites() {
  const grid = $('#vault-grid');
  if (!grid) return;
  const blanks = Array.from(grid.querySelectorAll('.vault-card')).filter(isBlankSiteCard);
  if (!blanks.length) return;
  blanks.forEach((c) => c.remove());
  saveVault();
}

// switchToTab 切换到指定页签（高亮导航 + 显示对应面板）
function switchToTab(tab) {
  maskVaultPasswords(); // 离开账号库时别把明文密码留在屏幕上
  if (tab !== 'vault' && $('#tab-vault') && $('#tab-vault').classList.contains('active')) dropBlankVaultSites();
  $$('.nav-item').forEach((x) => x.classList.remove('active'));
  $$('.panel').forEach((x) => x.classList.remove('active'));
  const nav = document.querySelector('.nav-item[data-tab="' + tab + '"]');
  if (nav) nav.classList.add('active');
  const panel = $('#tab-' + tab);
  if (panel) panel.classList.add('active');
}

// loadCliPanel 在内嵌 iframe 中加载管理面板（不再打开系统浏览器）
async function loadCliPanel() {
  const frame = $('#cli-panel-frame');
  const hint = $('#cli-panel-hint');
  try {
    hint.textContent = '正在连接订阅账号服务…';
    const res = await fetch('/api/cliproxy/open-panel', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) {
      hint.textContent = '❌ ' + (data.error || ('HTTP ' + res.status));
      return;
    }
    if (data.need_start) { showPanelStartPrompt(); return; }
    cliPanelUrl = data.panel_url || '';
    if (!cliPanelUrl) {
      hint.textContent = '❌ 未获取到管理面板地址';
      return;
    }
    frame.src = cliPanelUrl;
    hint.innerHTML = '面板已内嵌加载' + (data.pid ? '（边车运行中，PID ' + esc(data.pid) + '）' : '') +
      '。首次使用需登录：管理密钥已复制到剪贴板，在面板登录框 Ctrl+V 粘贴即可。';
    if (data.mgmt_key) await copyText(data.mgmt_key);
    // 面板里那些请求是 iframe 直连边车的，密钥失配时 ApiCluster 并不知道；
    // 这里替它探一次，好在面板页上方也挂出「一键修复」条
    await cliMgmt('/auth-files', 'GET');
  } catch (e) {
    hint.textContent = '❌ 打开管理面板失败: ' + e.message;
  }
}

// showPanelStartPrompt 边车没跑且用户关着「随 ApiCluster 启动」：
// 给一个明确按钮，不在翻页签时替他偷偷打开。
function showPanelStartPrompt() {
  const frame = $('#cli-panel-frame');
  if (frame) frame.removeAttribute('src');
  cliPanelUrl = '';
  $('#cli-panel-hint').innerHTML =
    '订阅账号服务<b>没有运行</b>（你关着「随 ApiCluster 启动」，切到这个页面不会替你擅自启动）。' +
    '<button class="mini-btn btn-panel-start" type="button">▶ 启动并加载面板</button>';
}

async function startPanelSidecar() {
  $('#cli-panel-hint').textContent = '正在启动订阅账号服务…（首次加载面板还要联网取 management.html）';
  await cliAction('start');
  await loadCliPanel();
}

async function openCliPanel() {
  switchToTab('panel');
  await loadCliPanel();
}

async function openCliTerminal() {
  if (!cliRunning) {
    if (!confirm('订阅账号服务当前没有运行，TUI 要连上它才有数据。\n点「确定」先启动服务再打开 TUI。')) return;
    await cliAction('start');
  }
  try {
    const res = await fetch('/api/cliproxy/open-terminal', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) {
      alert('❌ ' + (data.error || ('HTTP ' + res.status)));
    }
  } catch (e) {
    alert('❌ 打开终端失败: ' + e.message);
  }
}

let cliPollTimer = null;
let cliRunning = false; // 边车是否就绪（由 renderCliStatus 更新）

async function cliLogin(provider, btn) {
  const hint = $('#cli-login-hint');
  hint.textContent = '正在获取授权地址…';
  btn.disabled = true;
  try {
    const res = await fetch('/api/cliproxy/login?provider=' + encodeURIComponent(provider));
    const data = await res.json();
    if (!res.ok) {
      hint.textContent = '❌ ' + (data.error || ('HTTP ' + res.status));
      return;
    }
    hint.textContent = '✅ 已打开浏览器授权页，请完成登录…（成功后本页会自动刷新）';
    pollCliLogin(data.state);
  } catch (e) {
    hint.textContent = '❌ ' + e.message;
  } finally {
    btn.disabled = false;
  }
}

function pollCliLogin(state) {
  if (cliPollTimer) clearInterval(cliPollTimer);
  if (!state) return;
  const hint = $('#cli-login-hint');
  let n = 0;
  cliPollTimer = setInterval(async () => {
    n++;
    if (n > 150) {
      clearInterval(cliPollTimer); cliPollTimer = null;
      hint.textContent = '⏱ 授权超时，请重新点击登录';
      return;
    }
    const r = await cliMgmt('/get-auth-status?state=' + encodeURIComponent(state), 'GET');
    const st = r.data && r.data.status;
    if (st === 'ok') {
      clearInterval(cliPollTimer); cliPollTimer = null;
      hint.textContent = '✅ 登录成功，账号已加入池中';
      await fetchCliAccounts();
      await fetchCliStatus();
    } else if (st === 'error') {
      clearInterval(cliPollTimer); cliPollTimer = null;
      hint.textContent = '❌ ' + ((r.data && r.data.error) || '登录失败');
    }
  }, 2000);
}

async function fetchCliAccounts() {
  // 边车未就绪时跳过管理接口轮询，避免控制台反复出现 502 错误噪音
  if (!cliRunning) {
    const box = $('#cli-accounts');
    if (box && !box.innerHTML) box.innerHTML = '<div class="muted">服务未启动。点击上方「启动」后自动加载账号列表。</div>';
    return;
  }
  const box = $('#cli-accounts');
  const r = await cliMgmt('/auth-files', 'GET');
  if (!r.ok) {
    box.innerHTML = '<div class="muted">' + esc((r.data && r.data.error) || '账号列表获取失败（服务可能未启动）') + '</div>';
    return;
  }
  renderCliAccounts((r.data && r.data.files) || []);
}

function renderCliAccounts(files) {
  const box = $('#cli-accounts');
  if (!files.length) {
    box.innerHTML = '<div class="muted">还没有登录任何账号。点击上方按钮用订阅账号登录。</div>';
    return;
  }
  box.innerHTML = files.map((f) => {
    const name = f.name || '';
    const type = f.type || f.provider || '';
    const label = f.email || f.label || name;
    const disabled = !!f.disabled;
    const status = f.status || (disabled ? 'disabled' : 'active');
    const ok = !disabled && !f.unavailable;
    return `<div class="cli-account">
      <div class="cli-account-main">
        <span class="cli-acc-type">${esc(type)}</span>
        <span class="cli-acc-name" title="${esc(name)}">${esc(label)}</span>
        <span class="tag ${ok ? 'ft-free' : 'ft-paid'}">${esc(disabled ? '已禁用' : status)}</span>
      </div>
      <div class="cli-account-actions">
        <button class="mini-btn" data-act="toggle" data-name="${esc(name)}" data-disabled="${disabled ? '1' : '0'}">${disabled ? '启用' : '禁用'}</button>
        <button class="mini-btn" data-act="refresh" data-name="${esc(name)}">刷新令牌</button>
        <button class="mini-btn cli-danger" data-act="delete" data-name="${esc(name)}">删除</button>
      </div>
    </div>`;
  }).join('');
  box.querySelectorAll('button[data-act]').forEach((b) => {
    b.addEventListener('click', () => cliAccountAction(b.dataset.act, b.dataset.name, b.dataset.disabled === '1', b));
  });
}

async function cliAccountAction(act, name, disabled, btn) {
  btn.disabled = true;
  try {
    if (act === 'toggle') {
      await cliMgmt('/auth-files/status', 'PATCH', { name, disabled: !disabled });
    } else if (act === 'refresh') {
      await cliMgmt('/auth-files/refresh', 'POST', { name });
    } else if (act === 'delete') {
      if (!confirm('确认删除账号「' + name + '」？')) return;
      await cliMgmt('/auth-files?name=' + encodeURIComponent(name), 'DELETE');
    }
    await fetchCliAccounts();
  } finally {
    btn.disabled = false;
  }
}

async function fetchCliLog() {
  const el = $('#cli-log');
  if (!el) return;
  try {
    // 用户在往上翻着看时不要把滚动条抢回底部
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    const res = await fetch('/api/cliproxy/log?lines=200');
    const txt = await res.text();
    el.textContent = txt.trim() ? txt : '（暂无日志）';
    if (atBottom) el.scrollTop = el.scrollHeight;
  } catch (e) { /* 忽略 */ }
}

let cliSettingsDirty = false;

// browseCliExe 用系统文件对话框挑 CLIProxyAPI.exe（和「远程隧道 → 浏览私钥」同一套做法），
// 省得手动敲一长串路径。
async function browseCliExe() {
  const hint = $('#cli-settings-hint');
  try {
    const res = await fetch('/api/cliproxy/browse-exe', { method: 'POST' });
    const data = await res.json().catch(() => ({}));
    if (data.cancelled) return;
    if (!res.ok || !data.path) { hint.textContent = '❌ ' + (data.error || ('HTTP ' + res.status)); return; }
    $('#cli-exepath').value = data.path;
    cliSettingsDirty = true;
    await saveCliSettings();
  } catch (e) {
    hint.textContent = '❌ ' + e.message;
  }
}

async function saveCliSettings() {
  const body = {
    enabled: $('#cli-enabled').checked,
    port: Number($('#cli-port').value) || 8317,
    exe_path: $('#cli-exepath').value.trim()
  };
  const hint = $('#cli-settings-hint');
  try {
    const res = await fetch('/api/cliproxy/settings', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
    });
    const d = await res.json().catch(() => ({}));
    if (res.ok) {
      cliSettingsDirty = false;
      // 关掉常驻会顺带停掉正在跑的边车，这件事必须说出口，别让人以为它还在跑
      hint.textContent = d.stopped
        ? '✓ 已保存：不再随启动常驻，并已停止当前正在跑的边车（要再用点上方「启动」）。'
        : '✓ 已保存。修改端口后请点上方「重启」生效。';
      setTimeout(() => { hint.textContent = '修改自动保存'; }, 5000);
    } else {
      hint.textContent = '❌ ' + (d.error || ('HTTP ' + res.status));
    }
  } catch (e) {
    hint.textContent = '❌ ' + e.message;
  }
  await fetchCliStatus();
}

function setupCliProxy() {
  const navBtn = document.querySelector('.nav-item[data-tab="cliproxy"]');
  if (navBtn) {
    navBtn.addEventListener('click', () => {
      // 先刷新状态（更新 cliRunning），再按需拉取账号列表
      fetchCliStatus().then(fetchCliAccounts);
      fetchCliLog();
    });
  }
  $('#cli-start').addEventListener('click', () => cliAction('start'));
  $('#cli-stop').addEventListener('click', () => cliAction('stop'));
  $('#cli-restart').addEventListener('click', () => cliAction('restart'));
  $('#cli-open-panel').addEventListener('click', openCliPanel);
  $('#cli-open-tui').addEventListener('click', openCliTerminal);
  const panelRefresh = $('#cli-panel-refresh');
  if (panelRefresh) {
    panelRefresh.addEventListener('click', () => {
      const frame = $('#cli-panel-frame');
      if (frame && frame.src) frame.src = frame.src; // 重新加载 iframe
    });
  }
  const panelExternal = $('#cli-panel-external');
  if (panelExternal) {
    panelExternal.addEventListener('click', () => {
      if (cliPanelUrl) openExternal(cliPanelUrl);
    });
  }
  $('#cli-refresh-accounts').addEventListener('click', fetchCliAccounts);
  $('#cli-refresh-log').addEventListener('click', fetchCliLog);
  $('#cli-fix-key').addEventListener('click', fixCliKey);
  // 复制 Base URL / 本地 API Key / 管理密钥
  $('#cli-endpoints').addEventListener('click', (e) => {
    const b = e.target.closest('.btn-ep-copy');
    if (b) copyTo(b, b.dataset.url);
  });
  $('#cli-copy-key').addEventListener('click', async (e) => {
    const v = $('#cli-apikey').textContent;
    await copyTo(e.currentTarget, v === '-' ? '' : v);
  });
  $('#cli-copy-mgmtkey').addEventListener('click', async (e) => {
    const v = $('#cli-mgmtkey').textContent;
    if (v === '-' || !v) return;
    const ok = await copyTo(e.currentTarget, v);
    if (ok) alert('管理密钥已复制到剪贴板，在管理面板登录框 Ctrl+V 粘贴即可。');
    else alert('复制失败，请手动选中上方密钥文本复制。');
  });
  $('#cli-browse-exe').addEventListener('click', browseCliExe);
  // 修复条、「启动并加载面板」、「结束残留进程」都是动态生成的，用委托绑定
  document.addEventListener('click', (e) => {
    if (e.target.closest('.btn-fix-key')) { fixCliKey(); return; }
    const ko = e.target.closest('.btn-kill-orphan');
    if (ko) { killCliOrphan(ko); return; }
    if (e.target.closest('.btn-panel-start')) startPanelSidecar();
  });
  // CLI 设置变化即自动保存（防抖）
  const debouncedSaveCli = debounce(() => saveCliSettings(), 800);
  ['cli-enabled', 'cli-port', 'cli-exepath'].forEach((id) => {
    const el = $('#' + id);
    el.addEventListener('input', () => { cliSettingsDirty = true; debouncedSaveCli(); });
    el.addEventListener('change', () => { cliSettingsDirty = true; debouncedSaveCli(); });
  });

  fetchCliStatus();
  fetchCliAccounts();
  fetchCliLog();
  // 停在本页时状态、账号列表（含冷却/禁用态）和日志一起刷，不用手点刷新
  setInterval(() => {
    const panel = $('#tab-cliproxy');
    if (!panel || !panel.classList.contains('active')) return;
    fetchCliStatus();
    fetchCliAccounts();
    fetchCliLog();
  }, 5000);
}

// ---------- 远程隧道（SSH 反向隧道） ----------
let tunSettingsDirty = false;

// renderTunOrphans 有「像我们这条隧道但不是当前管理器起的」ssh 进程时提示出来：
// 这种残留会在远程占着端口转发，新隧道就会反复 remote port forwarding failed，
// 而界面上只显示「已断开」，谁都看不出原因。
function renderTunOrphans(orphans, running) {
  const box = $('#tun-orphan-banner');
  if (!box) return;
  const list = (orphans || []).filter((x) => x && x.pid);
  if (!list.length) { box.classList.add('hidden'); box.innerHTML = ''; return; }
  box.classList.remove('hidden');
  box.innerHTML = '<span>⚠ 检测到 ' + list.length + ' 个与当前隧道参数相同的 ssh 进程'
    + (running ? '（不是本程序启动的那条）' : '')
    + '：' + list.map((x) => 'PID ' + esc(x.pid) + (x.ours ? '（上一轮遗留）' : '')).join('、')
    + '。它们会在服务器上占着端口。'
    + list.map((x) => '<button class="mini-btn btn-tun-kill" data-pid="' + esc(x.pid) + '" type="button">结束 ' + esc(x.pid) + '</button>').join('');
}

async function killTunOrphan(btn) {
  const pid = Number(btn.dataset.pid);
  if (!pid) return;
  if (!confirm('结束 PID ' + pid + '？\n后端会再核对一次命令行，确认是这条隧道的进程才会动手。')) return;
  btn.disabled = true;
  try {
    const res = await fetch('/api/tunnel/kill-orphan', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ pid })
    });
    const d = await res.json().catch(() => ({}));
    if (!res.ok || d.error) toast('❌ ' + (d.error || '结束失败'), true);
    else toast('已结束 PID ' + pid);
  } catch (e) {
    toast('❌ ' + e.message, true);
  }
  await fetchTunnelStatus();
}

// probeTunnel 让远程那侧自己回话：ssh 进程活着 ≠ 反向端口真的能访问
async function probeTunnel(btn) {
  btn.disabled = true;
  const old = btn.textContent;
  btn.textContent = '探测中…';
  const out = $('#tun-probe-result');
  if (out) out.textContent = '';
  try {
    const res = await fetch('/api/tunnel/probe', { method: 'POST' });
    const d = await res.json().catch(() => ({}));
    if (out) {
      out.textContent = d.ok
        ? '✅ 远程 127.0.0.1:' + d.remote_port + ' 可访问' + (d.http && d.http !== 'OPEN' ? '（healthz HTTP ' + d.http + '）' : '')
        : '❌ ' + (d.error || '探测失败');
      out.classList.toggle('bad', !d.ok);
    }
  } catch (e) {
    if (out) { out.textContent = '❌ 探测请求失败：' + e.message; out.classList.add('bad'); }
  }
  btn.disabled = false;
  btn.textContent = old;
}

async function fetchTunnelStatus() {
  try {
    const res = await fetch('/api/tunnel/status');
    if (!res.ok) return;
    const body = await res.json();
    renderTunnelStatus(body.status || {}, body);
  } catch (e) { /* 忽略 */ }
}

function renderTunnelStatus(st, body) {
  body = body || {};
  const running = !!st.running;
  const ready = !!st.ready;
  const dot = $('#tun-dot');
  dot.classList.toggle('on', ready);
  dot.classList.toggle('warn', running && !ready);
  $('#tun-status-text').textContent = ready ? '隧道已建立' : (running ? '连接中…' : '已断开');
  const sub = $('#tun-status-sub');
  if (ready && st.remote_url) {
    // 「localhost」是相对于远程那台机器而言的，写死容易被误读成本机地址
    sub.textContent = '在远程机器上访问：' + st.remote_url + '（不是你这台电脑）';
  } else {
    sub.textContent = st.pem_path && st.host ? ('目标：' + st.user + '@' + st.host) : '尚未配置';
  }
  // 改了端口/主机但没重启隧道时，必须说清楚界面显示的是「启动那一刻生效的值」
  const drift = $('#tun-drift');
  if (drift) {
    drift.classList.toggle('hidden', !st.needs_restart);
    if (st.needs_restart) {
      drift.innerHTML = '<span>⚠ 配置已修改，但隧道还在用启动时的旧参数（上面的端口/主机即实际生效值）。'
        + '点右上「重启」才会切过去。</span>';
    }
  }
  renderTunOrphans(body.orphans, !!st.running);
  const errBox = $('#tun-status-error');
  if (st.last_error) {
    errBox.textContent = '⚠ ' + st.last_error;
  } else if (!errBox.dataset.busy) {
    errBox.textContent = '';
  }

  const rows = [
    ['进程 PID', st.pid || '-'],
    ['运行时长', st.uptime_seconds ? fmtDur(st.uptime_seconds) : '-'],
    ['自动重连', (st.restarts || 0) + ' 次'],
    ['私钥文件', st.pem_path || '未指定（将用默认 ~/.ssh）'],
    ['远程目标', (st.host && st.user) ? (st.user + '@' + st.host) : '-'],
    ['本地端口', st.local_port || '-'],
    ['远程端口', st.remote_port || '-'],
    ['日志文件', st.log_path || '-'],
  ];
  $('#tun-status-grid').innerHTML = rows.map(([k, v]) =>
    `<div class="cli-kv"><span class="cli-k">${esc(k)}</span><span class="cli-v" title="${esc(v)}">${esc(v)}</span></div>`
  ).join('');

  // 用户正在编辑设置时不覆盖输入，避免轮询把改动重置回去
  if (!tunSettingsDirty) {
    $('#tun-pem').value = st.pem_path || '';
    $('#tun-host').value = st.host || '';
    $('#tun-user').value = st.user || '';
    $('#tun-remote-port').value = st.remote_port || '';
    $('#tun-local-port').value = st.local_port || '';
    $('#tun-enabled').checked = !!st.enabled;
    $('#tun-autoreconnect').checked = !st.disable_auto_reconnect;
  }
}

async function tunAction(action) {
  const errBox = $('#tun-status-error');
  const label = { start: '启动', stop: '停止', restart: '重启' }[action] || action;
  errBox.dataset.busy = '1';
  errBox.textContent = '正在' + label + '…';
  try {
    // 启动/重启前先保存当前表单（确保用的是最新输入）；「停止」直接立即中断，不做任何延迟
    if (action !== 'stop') await saveTunnelSettings(true);
    const res = await fetch('/api/tunnel/action', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action })
    });
    const data = await res.json();
    if (!res.ok) errBox.textContent = '❌ ' + (data.error || ('HTTP ' + res.status));
    else errBox.textContent = '';
  } catch (e) {
    errBox.textContent = '❌ ' + e.message;
  } finally {
    delete errBox.dataset.busy;
  }
  await fetchTunnelStatus();
}

async function saveTunnelSettings(silent) {
  const body = {
    enabled: $('#tun-enabled').checked,
    pem_path: $('#tun-pem').value.trim(),
    host: $('#tun-host').value.trim(),
    user: $('#tun-user').value.trim(),
    remote_port: Number($('#tun-remote-port').value) || 0,
    local_port: Number($('#tun-local-port').value) || 0,
    disable_auto_reconnect: !$('#tun-autoreconnect').checked
  };
  const hint = $('#tun-settings-hint');
  try {
    const res = await fetch('/api/tunnel/settings', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
    });
    if (res.ok) {
      tunSettingsDirty = false;
      if (!silent) {
        hint.textContent = '✓ 已保存';
        setTimeout(() => { hint.textContent = ''; }, 2000);
      }
    } else {
      const d = await res.json();
      hint.textContent = '❌ ' + (d.error || ('HTTP ' + res.status));
    }
  } catch (e) {
    hint.textContent = '❌ ' + e.message;
  }
}

async function browsePem() {
  const hint = $('#tun-settings-hint');
  try {
    const res = await fetch('/api/tunnel/browse-pem', { method: 'POST' });
    const data = await res.json();
    if (res.ok && data.ok && data.path) {
      $('#tun-pem').value = data.path;
      tunSettingsDirty = true;
      saveTunnelSettings(true);
    } else if (res.ok && data.cancelled) {
      // 用户取消选择，不提示
    } else {
      hint.textContent = '❌ ' + (data.error || ('HTTP ' + res.status));
    }
  } catch (e) {
    hint.textContent = '❌ ' + e.message;
  }
}

async function fetchTunnelLog() {
  try {
    const res = await fetch('/api/tunnel/log?lines=200');
    const txt = await res.text();
    const el = $('#tun-log');
    el.textContent = txt.trim() ? txt : '（暂无日志）';
    el.scrollTop = el.scrollHeight;
  } catch (e) { /* 忽略 */ }
}

function setupTunnel() {
  const navBtn = document.querySelector('.nav-item[data-tab="tunnel"]');
  if (navBtn) {
    navBtn.addEventListener('click', () => {
      fetchTunnelStatus();
      fetchTunnelLog();
    });
  }
  $('#tun-start').addEventListener('click', () => tunAction('start'));
  $('#tun-stop').addEventListener('click', () => tunAction('stop'));
  $('#tun-restart').addEventListener('click', () => tunAction('restart'));
  $('#tun-refresh-log').addEventListener('click', fetchTunnelLog);
  $('#tun-browse-pem').addEventListener('click', browsePem);
  // 设置变化即自动保存（防抖）
  const debouncedSaveTunnel = debounce(() => saveTunnelSettings(false), 800);
  ['tun-enabled', 'tun-autoreconnect', 'tun-pem', 'tun-host', 'tun-user', 'tun-remote-port', 'tun-local-port'].forEach((id) => {
    const el = $('#' + id);
    el.addEventListener('input', () => { tunSettingsDirty = true; debouncedSaveTunnel(); });
    el.addEventListener('change', () => { tunSettingsDirty = true; debouncedSaveTunnel(); });
  });

  fetchTunnelStatus();
  fetchTunnelLog();
  document.addEventListener('click', (e) => {
    const k = e.target.closest('.btn-tun-kill');
    if (k) killTunOrphan(k);
  });
  const probe = $('#tun-probe');
  if (probe) probe.addEventListener('click', () => probeTunnel(probe));
  // 停在本页时状态、日志一起刷：断线原因不该只躺在文件里
  setInterval(() => {
    const panel = $('#tab-tunnel');
    if (!panel || !panel.classList.contains('active')) return;
    fetchTunnelStatus();
    fetchTunnelLog();
  }, 5000);
}

// ---------- 启动 ----------
document.addEventListener('DOMContentLoaded', () => {
  // 各模块独立初始化：某个 setup 抛错不影响其余模块和数据加载
  [setupTabs, setupChat, setupSettings, setupRouteConfig, setupCliProxy, setupTunnel, setupVault, setupTheme, setupSidebarToggle].forEach((fn) => {
    try { fn(); } catch (e) { console.error('[init]', fn.name, e); }
  });
  fetchConfig();
  setInterval(fetchConfig, 20000);
  setInterval(syncLifeTheme, 1500);
});
