/* 小青柠 LimeLink · 电脑端界面逻辑（原生 JS，无依赖） */
'use strict';

const $ = (sel, root) => (root || document).querySelector(sel);
const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

let STATE = null;
let currentPage = 'home';
let filterCat = 'all';
let searchText = '';
let knownIds = new Set();
let firstRender = true;

const PAGE_META = {
  home: { title: '通知流', sub: '手机来什么通知，这里就有什么' },
  devices: { title: '设备', sub: '已配对的手机会自动重连，不用每次输入 IP' },
  pair: { title: '配对', sub: '同一个网络里，两步就能连上' },
  settings: { title: '设置', sub: '本机名字、数据与连接排查' },
};

const CAT_LABEL = { wechat: '微信', sms: '短信', app: '应用', system: '系统' };

/* ---------------- 基础工具 ---------------- */

async function api(path, body) {
  const opt = body === undefined
    ? { method: 'GET' }
    : {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      };
  const res = await fetch(path, opt);
  let data = null;
  try { data = await res.json(); } catch (_) { data = null; }
  if (!res.ok) throw new Error((data && data.error) || ('请求失败 ' + res.status));
  return data;
}

function el(tag, cls, text) {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined && text !== null) node.textContent = text;
  return node;
}

function catOf(rec) {
  const raw = (rec.icon_type || rec.app_label || 'app').toLowerCase();
  return CAT_LABEL[raw] ? raw : 'app';
}

function fmtTime(iso) {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
  if (sameDay) return hm;
  return (d.getMonth() + 1) + '/' + d.getDate() + ' ' + hm;
}

function fmtUptime(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return { v: String(h), u: 'h' };
  return { v: String(m), u: 'm' };
}

function toast(title, text, isErr) {
  const box = $('#toasts');
  const node = el('div', 'toast' + (isErr ? ' is-err' : ''));
  node.appendChild(el('b', null, title));
  if (text) node.appendChild(el('span', null, text));
  box.appendChild(node);
  setTimeout(() => {
    node.style.opacity = '0';
    node.style.transform = 'translateY(6px)';
    setTimeout(() => node.remove(), 220);
  }, isErr ? 5200 : 3200);
}

async function copyText(text, okMsg) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制', okMsg || text);
  } catch (_) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); toast('已复制', okMsg || text); }
    catch (e) { toast('复制失败', '请手动选中复制', true); }
    ta.remove();
  }
}

function modal(opts) {
  return new Promise((resolve) => {
    const box = $('#modal');
    $('#modalTitle').textContent = opts.title || '确认';
    $('#modalBody').textContent = opts.body || '';
    const pre = $('#modalCmd');
    if (opts.cmd) { pre.hidden = false; pre.textContent = opts.cmd; }
    else { pre.hidden = true; pre.textContent = ''; }
    const okBtn = $('#modalOk');
    okBtn.textContent = opts.okText || '确定';
    okBtn.className = 'btn ' + (opts.danger ? 'btn--danger' : 'btn--ink');
    box.hidden = false;
    const close = (val) => {
      box.hidden = true;
      okBtn.onclick = null;
      $('#modalCancel').onclick = null;
      document.removeEventListener('keydown', onKey);
      resolve(val);
    };
    const onKey = (e) => { if (e.key === 'Escape') close(false); };
    okBtn.onclick = () => close(true);
    $('#modalCancel').onclick = () => close(false);
    document.addEventListener('keydown', onKey);
    okBtn.focus();
  });
}

/* ---------------- 数据 ---------------- */

async function refresh() {
  try {
    STATE = await api('/api/state');
    render();
  } catch (e) {
    toast('界面连接中断', e.message, true);
  }
}

function connectEvents() {
  const es = new EventSource('/api/events');
  es.onmessage = (e) => {
    let ev = null;
    try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.type === 'notify' && ev.record) {
      handleLiveNotification(ev.record);
    } else if (ev.type === 'pair-request') {
      toast('新的连接请求', '有手机想连上这台电脑，去「设备」里点允许');
      refresh();
    } else {
      refresh();
    }
  };
  es.onerror = () => {
    // EventSource 自带重连；这里只做兜底轮询
  };
}

let liveTimer = null;
function handleLiveNotification(rec) {
  if (!STATE) { refresh(); return; }
  STATE.records.unshift(rec);
  if (STATE.records.length > 200) STATE.records.pop();
  STATE.today_count = (STATE.today_count || 0) + 1;
  STATE.notify_count = (STATE.notify_count || 0) + 1;
  renderStats();
  renderFeed();
  const title = rec.title || rec.app_name || '新通知';
  toast(rec.app_name || '通知', title + (rec.text ? ' · ' + rec.text : ''));
  clearTimeout(liveTimer);
  liveTimer = setTimeout(refresh, 2500); // 稍后拉一次快照，校准在线状态
}

/* ---------------- 渲染 ---------------- */

function render() {
  if (!STATE) return;
  const meta = PAGE_META[currentPage] || PAGE_META.home;
  $('#pageTitle').textContent = meta.title;
  $('#pageSub').textContent = meta.sub;

  const paired = STATE.paired_count || 0;
  const online = STATE.online_count || 0;
  const dot = $('#pillPair .dot');
  dot.classList.toggle('is-on', online > 0);
  $('#pillPairTxt').textContent = paired === 0 ? '未配对' : (online > 0 ? online + ' 台在线' : paired + ' 台离线');
  const ip = (STATE.ips && STATE.ips[0]) ? STATE.ips[0].ip : '无网络';
  $('#pillAddr').textContent = ip + (STATE.port ? ':' + STATE.port : '');

  $('#railOnline').textContent = online;
  $('#railToday').textContent = STATE.today_count || 0;
  $('#railVer').textContent = 'v' + (STATE.version || '1.3.0');

  renderStats();
  renderCode($('#codeMini'));
  renderCode($('#codeBig'));
  renderFeed(true);
  renderDevices();
  renderPending();
  renderAddrs();
  renderSelf();
  renderSettings();
  firstRender = false;
}

function renderStats() {
  if (!STATE) return;
  $('#stToday').textContent = STATE.today_count || 0;
  $('#stOnline').textContent = STATE.online_count || 0;
  $('#stPaired').textContent = STATE.paired_count || 0;
  const up = fmtUptime(STATE.uptime_sec);
  const box = $('#stUptime');
  box.textContent = '';
  box.appendChild(document.createTextNode(up.v));
  const small = el('small', null, up.u);
  box.appendChild(small);
}

function renderCode(target) {
  if (!target) return;
  const code = (STATE && STATE.code) || '';
  target.textContent = '';
  target.classList.toggle('is-empty', !code);
  const chars = code ? code.split('') : ['-', '-', '-', '-', '-', '-'];
  chars.forEach((c) => target.appendChild(el('i', null, c)));
}

function filteredRecords() {
  const recs = (STATE && STATE.records) || [];
  const q = searchText.trim().toLowerCase();
  return recs.filter((r) => {
    if (filterCat !== 'all' && catOf(r) !== filterCat) return false;
    if (!q) return true;
    return (r.title || '').toLowerCase().includes(q)
      || (r.text || '').toLowerCase().includes(q)
      || (r.app_name || '').toLowerCase().includes(q);
  });
}

function renderFeed(force) {
  const feed = $('#feed');
  if (!feed) return;
  const list = filteredRecords();
  const prevKnown = knownIds;

  if (!force && list.length && prevKnown.has(list[0].id || '')) {
    // 没有新东西，跳过重排
    if (feed.dataset.count === String(list.length)) return;
  }

  feed.textContent = '';
  feed.dataset.count = String(list.length);

  if (!list.length) {
    feed.appendChild(emptyState());
    knownIds = new Set();
    return;
  }

  const frag = document.createDocumentFragment();
  const nextKnown = new Set();
  list.forEach((r, idx) => {
    const id = r.id || (r.received_at + idx);
    nextKnown.add(id);
    frag.appendChild(feedItem(r, !prevKnown.has(id) && !firstRender));
  });
  knownIds = nextKnown;
  feed.appendChild(frag);
}

function feedItem(rec, isNew) {
  const cat = catOf(rec);
  const node = el('article', 'item' + (isNew ? ' is-new' : ''));
  node.appendChild(el('div', 'item-rail')).dataset.cat = cat;

  const name = rec.app_name || CAT_LABEL[cat] || '通知';
  const avatar = el('div', 'avatar', (name.trim()[0] || '通').toUpperCase());
  avatar.dataset.cat = cat;
  node.appendChild(avatar);

  const body = el('div', 'item-body');
  const top = el('div', 'item-top');
  top.appendChild(el('span', 'item-app', name));
  const title = el('span', 'item-title', rec.title || '(无标题)');
  top.appendChild(title);
  body.appendChild(top);

  if (rec.text) body.appendChild(el('p', 'item-text', rec.text));
  else if (rec.hidden) body.appendChild(el('p', 'item-text', '（内容已隐藏）'));

  if (rec.device_name) {
    body.appendChild(el('div', 'item-note', '来自 ' + rec.device_name));
  }
  node.appendChild(body);

  const time = el('div', 'item-time', fmtTime(rec.received_at));
  node.appendChild(time);
  return node;
}

function emptyState() {
  const wrap = el('div', 'empty');
  wrap.innerHTML = `
    <svg viewBox="0 0 220 120" aria-hidden="true">
      <rect x="6" y="26" width="58" height="88" fill="#fffdf6" stroke="#0b0b0f" stroke-width="4"/>
      <rect x="14" y="40" width="42" height="8" fill="#c8ff2e" stroke="#0b0b0f" stroke-width="3"/>
      <rect x="14" y="56" width="42" height="8" fill="#e7e1d2" stroke="#0b0b0f" stroke-width="3"/>
      <rect x="14" y="72" width="30" height="8" fill="#e7e1d2" stroke="#0b0b0f" stroke-width="3"/>
      <path d="M78 64h54" stroke="#0b0b0f" stroke-width="5" stroke-dasharray="10 7"/>
      <path d="M132 64l-12-8v16z" fill="#0b0b0f"/>
      <rect x="142" y="34" width="70" height="52" fill="#7b5bff" stroke="#0b0b0f" stroke-width="4"/>
      <rect x="150" y="42" width="54" height="30" fill="#fffdf6" stroke="#0b0b0f" stroke-width="3"/>
      <rect x="168" y="92" width="18" height="6" fill="#0b0b0f"/>
    </svg>`;
  wrap.appendChild(el('b', null, '还没有通知'));
  wrap.appendChild(el('span', null, '手机配对并开启通知读取后，来一条微信、短信或系统通知，这里会立刻出现。'));
  return wrap;
}

function renderDevices() {
  const grid = $('#deviceGrid');
  if (!grid) return;
  grid.textContent = '';
  const devs = (STATE && STATE.devices) || [];
  if (!devs.length) {
    const card = el('div', 'card');
    card.appendChild(el('div', 'card-tag', '还没有配对设备'));
    card.appendChild(el('p', 'hint', '手机打开小青柠，会在这台电脑的同一网络里看到它。点「附近的电脑」→ 连接，然后在这台电脑上点「允许」。'));
    const btn = el('button', 'btn btn--lime', '去看配对码');
    btn.style.marginTop = '12px';
    btn.onclick = () => goto('pair');
    card.appendChild(btn);
    grid.appendChild(card);
    return;
  }
  devs.forEach((d) => {
    const card = el('div', 'dev');
    const head = el('div', 'dev-head');
    const ico = el('div', 'dev-ico');
    ico.innerHTML = `<svg class="ico" viewBox="0 0 24 24"><rect x="7" y="2" width="10" height="20" rx="2"/><path d="M10 5h4"/></svg>`;
    head.appendChild(ico);

    const info = el('div');
    info.appendChild(el('div', 'dev-name', d.name || '手机'));
    info.appendChild(el('div', 'dev-meta', (d.ip || '未知地址') + ' · ' + (d.last_seen_text || '从未上线')));
    head.appendChild(info);

    const state = el('div', 'dev-state' + (d.online ? ' is-on' : ''), d.online ? '在线' : '离线');
    head.appendChild(state);
    card.appendChild(head);

    const acts = el('div', 'dev-actions');

    const sendBtn = el('button', 'btn btn--violet', '发条文字');
    sendBtn.onclick = async () => {
      const text = await askText('发到 ' + (d.name || '手机'), '手机下次心跳就会弹出来（≤5 秒）。');
      if (!text) return;
      try {
        await api('/api/send', { device_id: d.id, text });
        toast('已排队', '手机会在 5 秒内收到');
      } catch (e) { toast('发送失败', e.message, true); }
    };

    const kickBtn = el('button', 'btn btn--ghost', '断开');
    kickBtn.onclick = async () => {
      const ok = await modal({
        title: '解绑「' + (d.name || '手机') + '」？',
        body: '解绑后这台手机需要重新配对才能同步通知。',
        okText: '解绑',
        danger: true,
      });
      if (!ok) return;
      try { await api('/api/kick', { device_id: d.id }); toast('已解绑', d.name || ''); refresh(); }
      catch (e) { toast('操作失败', e.message, true); }
    };

    acts.appendChild(sendBtn);
    acts.appendChild(kickBtn);
    card.appendChild(acts);
    grid.appendChild(card);
  });
}

function askText(title, hint) {
  return new Promise((resolve) => {
    const box = $('#modal');
    $('#modalTitle').textContent = title;
    const body = $('#modalBody');
    body.textContent = '';
    if (hint) body.appendChild(el('p', 'hint', hint));
    const ta = document.createElement('textarea');
    ta.rows = 3;
    ta.placeholder = '输入要发送的文字…';
    ta.style.marginTop = '10px';
    body.appendChild(ta);
    $('#modalCmd').hidden = true;
    const okBtn = $('#modalOk');
    okBtn.textContent = '发送';
    okBtn.className = 'btn btn--ink';
    box.hidden = false;
    setTimeout(() => ta.focus(), 30);
    const close = (val) => {
      box.hidden = true;
      okBtn.onclick = null;
      $('#modalCancel').onclick = null;
      document.removeEventListener('keydown', onKey);
      resolve(val);
    };
    const onKey = (e) => { if (e.key === 'Escape') close(''); };
    okBtn.onclick = () => close(ta.value.trim());
    $('#modalCancel').onclick = () => close('');
    document.addEventListener('keydown', onKey);
  });
}

function renderPending() {
  const wrap = $('#pendingWrap');
  if (!wrap) return;
  wrap.textContent = '';
  wrap.className = (STATE && STATE.pending && STATE.pending.length) ? 'pending' : '';
  (STATE && STATE.pending ? STATE.pending : []).forEach((p) => {
    const card = el('div', 'pending-card');
    const tag = el('div', 'pending-tag', '待确认');
    card.appendChild(tag);

    const info = el('div', 'grow');
    info.appendChild(el('div', 'dev-name', p.name || '未知手机'));
    info.appendChild(el('div', 'dev-meta', (p.ip || '') + ' · ' + (p.wait_text || '') + '发起请求'));
    card.appendChild(info);

    const allow = el('button', 'btn btn--lime', '允许连接');
    allow.onclick = async () => {
      try {
        await api('/api/approve', { req_id: p.id });
        toast('已允许', (p.name || '手机') + ' 已加入设备列表');
        refresh();
      } catch (e) { toast('允许失败', e.message, true); }
    };
    const deny = el('button', 'btn btn--ghost', '拒绝');
    deny.onclick = async () => {
      try { await api('/api/deny', { req_id: p.id }); toast('已拒绝'); refresh(); }
      catch (e) { toast('拒绝失败', e.message, true); }
    };
    card.appendChild(allow);
    card.appendChild(deny);
    wrap.appendChild(card);
  });
}

function renderAddrs() {
  const box = $('#addrList');
  if (!box) return;
  box.textContent = '';
  const ips = (STATE && STATE.ips) || [];
  if (!ips.length) {
    box.appendChild(el('p', 'hint', '未找到可用局域网地址，请检查网络连接。'));
    return;
  }
  ips.forEach((a) => {
    const node = el('div', 'addr');
    const left = el('div');
    left.appendChild(el('div', 'addr-ip', a.ip + ':' + (STATE.port || 8787)));
    left.appendChild(el('div', 'addr-if', a.iface));
    node.appendChild(left);
    const kind = el('div', 'addr-kind', a.kind === 'hotspot' ? '热点' : (a.kind === 'wan' ? '公网' : '局域网'));
    kind.dataset.kind = a.kind;
    node.appendChild(kind);
    node.onclick = () => copyText(a.ip, '手机端可手动填写 ' + a.ip);
    box.appendChild(node);
  });
}

function renderSelf() {
  const card = $('#selfCard');
  if (!card || !STATE) return;
  card.textContent = '';
  const ico = el('div', 'dev-ico');
  ico.innerHTML = `<svg class="ico" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="12" rx="1"/><path d="M8 20h8M12 16v4"/></svg>`;
  card.appendChild(ico);
  const info = el('div');
  info.appendChild(el('div', 'dev-name', STATE.name || 'LimeLink-PC'));
  const ip = (STATE.ips && STATE.ips[0]) ? STATE.ips[0].ip : '无网络';
  info.appendChild(el('div', 'dev-meta', '本机 · 端口 ' + STATE.port + ' · ' + ip));
  card.appendChild(info);
  const tag = el('div', 'dev-state is-on', '这台电脑');
  card.appendChild(tag);
}

function renderSettings() {
  if (!STATE) return;
  const nameInput = $('#nameInput');
  if (nameInput && document.activeElement !== nameInput) nameInput.value = STATE.name || '';
  const openStore = $('#openStore');
  if (openStore) {
    openStore.textContent = '数据目录：' + (STATE.store_dir || '默认');
  }
  const about = $('#aboutCard');
  if (!about) return;
  about.textContent = '';
  about.appendChild(el('div', 'card-tag', '关于'));
  const grid = el('div', 'about-grid');
  const items = [
    ['版本', 'v' + (STATE.version || '1.3.0')],
    ['协议', 'LIMELINK3'],
    ['端口', String(STATE.port)],
    ['启动时间', STATE.started_at ? new Date(STATE.started_at).toLocaleString('zh-CN', { hour12: false }) : '—'],
    ['累计通知', String(STATE.notify_count || 0)],
    ['被拒请求', String(STATE.notify_rejects || 0)],
  ];
  items.forEach(([k, v]) => {
    const box = el('div', 'about-item');
    box.appendChild(el('span', null, k));
    box.appendChild(el('b', null, v));
    grid.appendChild(box);
  });
  about.appendChild(grid);
  about.appendChild(el('p', 'hint', '小青柠把手机通知镜像到 Windows 弹窗；不联网、不中转，全部走局域网。'));
}

/* ---------------- 交互 ---------------- */

function goto(page) {
  currentPage = page;
  $$('.nav-item').forEach((b) => b.classList.toggle('is-active', b.dataset.page === page));
  $$('.page').forEach((p) => { p.hidden = p.dataset.page !== page; });
  const meta = PAGE_META[page] || PAGE_META.home;
  $('#pageTitle').textContent = meta.title;
  $('#pageSub').textContent = meta.sub;
  const main = $('.page[data-page="' + page + '"]');
  if (main) { main.style.animation = 'none'; void main.offsetWidth; main.style.animation = ''; }
}

function bind() {
  $$('.nav-item').forEach((btn) => {
    btn.addEventListener('click', () => goto(btn.dataset.page));
  });
  $$('[data-goto]').forEach((btn) => {
    btn.addEventListener('click', () => goto(btn.dataset.goto));
  });

  $('#filters').addEventListener('click', (e) => {
    const chip = e.target.closest('.chip');
    if (!chip) return;
    filterCat = chip.dataset.cat;
    $$('.chip').forEach((c) => c.classList.toggle('is-on', c === chip));
    renderFeed(true);
  });

  $('#search').addEventListener('input', (e) => {
    searchText = e.target.value;
    renderFeed(true);
  });

  $('#copyCode').addEventListener('click', () => copyText((STATE && STATE.code) || '', '配对码已复制'));
  $('#codeMini').addEventListener('click', () => copyText((STATE && STATE.code) || '', '配对码已复制'));

  $('#resetPair').addEventListener('click', async () => {
    const ok = await modal({
      title: '重置配对？',
      body: '会生成新的配对码，并清空已配对的手机。手机需要重新连接。',
      okText: '重置',
      danger: true,
    });
    if (!ok) return;
    try { await api('/api/reset', {}); toast('已重置', '新的配对码已生成'); refresh(); }
    catch (e) { toast('重置失败', e.message, true); }
  });

  $('#clearRecords').addEventListener('click', async () => {
    const ok = await modal({ title: '清空通知记录？', body: '只会删除本机保存的记录，不影响同步。', okText: '清空', danger: true });
    if (!ok) return;
    try { await api('/api/clear', {}); toast('已清空'); refresh(); }
    catch (e) { toast('清空失败', e.message, true); }
  });

  $('#sendBtn').addEventListener('click', async () => {
    const ta = $('#sendText');
    const text = ta.value.trim();
    if (!text) { toast('写点什么', '输入要发到手机的文字', true); return; }
    try {
      await api('/api/send', { text });
      ta.value = '';
      toast('已排队', '手机会在 5 秒内收到');
    } catch (e) { toast('发送失败', e.message, true); }
  });

  $('#nameSave').addEventListener('click', async () => {
    const name = $('#nameInput').value.trim();
    if (!name) { toast('名字不能为空', '', true); return; }
    try { await api('/api/name', { name }); toast('已保存', '手机下次刷新可见新名字'); refresh(); }
    catch (e) { toast('保存失败', e.message, true); }
  });

  $('#openStore').addEventListener('click', async () => {
    const path = (STATE && STATE.store_dir) || '';
    try {
      await api('/api/reveal', {});
      toast('已打开数据目录', path);
    } catch (e) {
      copyText(path, '已复制数据目录路径');
    }
  });

  $('#fwHelp').addEventListener('click', () => {
    modal({
      title: '放行防火墙端口',
      body: '如果手机能看到电脑但连不上：用管理员身份运行程序目录里的 fix-firewall.ps1，或手动执行下面的命令（只需一次）。',
      cmd: 'netsh advfirewall firewall add rule name="LimeLink TCP 8787" dir=in action=allow protocol=TCP localport=8787\nnetsh advfirewall firewall add rule name="LimeLink UDP 8788" dir=in action=allow protocol=UDP localport=8788',
      okText: '复制命令',
    }).then((ok) => {
      if (!ok) return;
      copyText('netsh advfirewall firewall add rule name="LimeLink TCP 8787" dir=in action=allow protocol=TCP localport=8787\nnetsh advfirewall firewall add rule name="LimeLink UDP 8788" dir=in action=allow protocol=UDP localport=8788', '命令已复制，右键管理员运行终端后粘贴');
    });
  });

  $('#copyDebug').addEventListener('click', () => {
    if (!STATE) return;
    const snap = {
      version: STATE.version,
      name: STATE.name,
      port: STATE.port,
      paired: STATE.paired_count,
      online: STATE.online_count,
      devices: STATE.devices.map((d) => ({ id: d.id, name: d.name, ip: d.ip, online: d.online, last: d.last_seen_text })),
      ips: STATE.ips,
      uptime_sec: STATE.uptime_sec,
      notify_count: STATE.notify_count,
      rejects: STATE.notify_rejects,
      store: STATE.store_dir,
    };
    copyText(JSON.stringify(snap, null, 2), '诊断信息已复制');
  });

  document.addEventListener('keydown', (e) => {
    const tag = (e.target.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea' || e.metaKey || e.ctrlKey || e.altKey) return;
    const map = { '1': 'home', '2': 'devices', '3': 'pair', '4': 'settings' };
    if (map[e.key]) { e.preventDefault(); goto(map[e.key]); }
  });

  document.addEventListener('click', (e) => {
    if (e.target.id === 'modal') { /* 点遮罩不关闭，避免误触 */ }
  });
}

/* ---------------- 启动 ---------------- */

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** 启动流程：首次启动端口可能还没 bind 好（防火墙放行要几秒），这里轮询等待。 */
async function bootApp() {
  const boot = $('#boot');
  for (let i = 0; i < 150; i++) {
    try {
      STATE = await api('/api/state');
      break;
    } catch (_) {
      await sleep(600);
    }
  }
  if (STATE) {
    render();
    connectEvents();
    boot.classList.add('is-done');
    setTimeout(() => { boot.hidden = true; }, 260);
    setInterval(refresh, 20000);
  } else {
    $('#bootTitle').textContent = '连不上本地服务';
    $('#bootHint').textContent = '关闭窗口后重新启动小青柠；如果反复出现，检查端口 8787 是否被占用。';
    $('.boot-bar').style.display = 'none';
  }
}

bind();
bootApp();

// 支持 ?page=devices 直接进某一页（托盘菜单 / 截图调试用）
(function deepLink() {
  const page = new URLSearchParams(location.search).get('page');
  if (page && PAGE_META[page]) goto(page);
})();
