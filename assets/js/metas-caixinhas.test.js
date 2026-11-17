const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Contratos de interação dos cards de Limites:
// - um único menu aberto por vez;
// - ESC devolve o foco ao gatilho somente quando o foco estava no menu;
// - clique externo fecha sem roubar o foco do novo alvo;
// - HTMX inicializa apenas os cards novos, uma vez;
// - o chip de progresso é posicionado uma vez por barra e com clamp 0..100;
// - o sheet de histórico prende o foco e o devolve ao gatilho.
const source = fs.readFileSync(path.join(__dirname, 'metas-caixinhas.js'), 'utf8');

function classList(initial = []) {
  const values = new Set(initial);
  return {
    add: (...names) => names.forEach((name) => values.add(name)),
    remove: (...names) => names.forEach((name) => values.delete(name)),
    contains: (name) => values.has(name),
    toggle: (name, force) => {
      const enabled = force === undefined ? !values.has(name) : !!force;
      if (enabled) values.add(name);
      else values.delete(name);
      return enabled;
    },
  };
}

function attrs(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getAttribute: (name) => (values.has(name) ? values.get(name) : null),
    setAttribute: (name, value) => values.set(name, String(value)),
    removeAttribute: (name) => values.delete(name),
    hasAttribute: (name) => values.has(name),
  };
}

function focusTarget() {
  const api = attrs();
  let focusCalls = 0;
  const target = {
    ...api,
    classList: classList(),
    listeners: {},
    addEventListener(name, fn) {
      (target.listeners[name] = target.listeners[name] || []).push(fn);
    },
    focusCalls: () => focusCalls,
    focus() {
      focusCalls += 1;
      currentDocument.activeElement = target;
    },
  };
  return target;
}

let currentDocument;

function makeDropdown(name = 'menu-1') {
  const link = {
    ...attrs(),
    classList: classList(),
    closest: () => card,
  };
  const panel = {
    ...attrs(),
    classList: classList(['hidden']),
    contains: (target) => panel.items.includes(target),
    items: [],
  };
  const toggle = focusTarget();
  toggle.closest = () => card;
  const dropdown = {
    ...attrs(),
    classList: classList(),
    dataset: {},
    listeners: {},
    contains: (target) => target === panel || panel.contains(target) || target === toggle,
    addEventListener(name, fn) {
      (dropdown.listeners[name] = dropdown.listeners[name] || []).push(fn);
    },
    querySelector(selector) {
      if (selector === '[data-ck-panel]') return panel;
      if (selector === '[data-ck-toggle]') return toggle;
      return null;
    },
    querySelectorAll() {
      return [];
    },
    closest(selector) {
      return selector === '[data-meta-card]' ? card : null;
    },
  };
  const card = {
    querySelector(selector) {
      return selector === '[data-meta-card-link]' ? link : null;
    },
  };
  dropdown.name = name;
  return { dropdown, link, panel, toggle, card };
}

function makeProgress(value) {
  return {
    dataset: { metasP: String(value) },
    attrs: attrs(),
  };
}

function runtime(options = {}) {
  const dropdowns = (options.dropdowns || []).map((entry) => entry.dropdown);
  const progressWraps = (options.progressWraps || []).map((entry) => entry);
  const documentListeners = {};
  const bodyListeners = {};
  const windowListeners = {};
  const motionCalls = [];
  const byID = new Map();
  let currentSheet = null;
  let restoredTrigger = null;
  let closeCalls = 0;
  let overlayCloseCalls = 0;

  const document = {
    readyState: 'complete',
    activeElement: null,
    body: {
      addEventListener(name, fn) {
        (bodyListeners[name] = bodyListeners[name] || []).push(fn);
      },
    },
    addEventListener(name, fn) {
      (documentListeners[name] = documentListeners[name] || []).push(fn);
    },
    getElementById(id) {
      return byID.get(id) || null;
    },
    querySelector(selector) {
      if (selector === '#bottom-sheet-container [data-metas-sheet]' || selector === '#bottom-sheet-container [data-metas-history]') {
        return currentSheet;
      }
      if (restoredTrigger && selector.indexOf('[aria-controls=') === 0) {
        return restoredTrigger;
      }
      return null;
    },
    querySelectorAll(selector) {
      if (selector.includes('metas-progress-wrap')) {
        return progressWraps.filter((wrap) => !wrap.dataset.pReady);
      }
      if (selector.includes(':not([data-ck-ready])')) {
        return dropdowns.filter((dropdown) => !dropdown.dataset.ckReady);
      }
      if (selector.includes('.is-open')) {
        return dropdowns.filter((dropdown) => dropdown.classList.contains('is-open'));
      }
      return [];
    },
    emit(name, event) {
      (documentListeners[name] || []).forEach((fn) => fn(event));
    },
    emitBody(name, event) {
      (bodyListeners[name] || []).forEach((fn) => fn(event));
    },
  };
  currentDocument = document;

  const window = {
    addEventListener(name, fn) {
      (windowListeners[name] = windowListeners[name] || []).push(fn);
    },
    cbRuntimeMotion: options.withMotion === false ? undefined : {
      hold(wrap, vars) {
        motionCalls.push({ wrap, vars });
      },
    },
    closeBottomSheet() {
      closeCalls += 1;
      currentSheet = null;
    },
    overlayDidClose() {
      overlayCloseCalls += 1;
    },
  };
  const context = vm.createContext({ document, window, console });
  vm.runInNewContext(source, context, { filename: 'metas-caixinhas.js' });

  return {
    document,
    dropdowns,
    progressWraps,
    documentListeners,
    bodyListeners,
    windowListeners,
    motionCalls,
    window,
    byID,
    addDropdown(entry) {
      dropdowns.push(entry.dropdown);
    },
    addProgress(entry) {
      progressWraps.push(entry);
    },
    setSheet(entry) {
      currentSheet = entry ? entry.sheet : null;
    },
    setRestoredTrigger(entry) {
      restoredTrigger = entry;
    },
    closeBottomSheet() {
      window.closeBottomSheet();
    },
    closeCalls() {
      return closeCalls;
    },
    overlayCloseCalls() {
      return overlayCloseCalls;
    },
    click(element) {
      const event = {
        target: element,
        preventDefault() {},
        stopPropagation() {},
        closest: element.closest ? element.closest.bind(element) : () => null,
      };
      (element.listeners?.click || []).forEach((fn) => fn(event));
    },
    clickOutside(element) {
      document.emit('click', { target: element, closest: element.closest || (() => null) });
    },
    pressEscape() {
      document.emit('keydown', { key: 'Escape' });
    },
    settle() {
      document.emitBody('htmx:afterSettle', {});
    },
    settleWithTarget(target) {
      document.emitBody('htmx:afterSettle', { detail: { target } });
    },
  };
}

function makeHistorySheet() {
  const close = focusTarget();
  const body = focusTarget();
  const panel = {
    listeners: {},
    addEventListener(name, fn) {
      (panel.listeners[name] = panel.listeners[name] || []).push(fn);
    },
    focus() {
      currentDocument.activeElement = panel;
    },
  };
  const sheet = {
    dataset: {},
    contains: (target) => target === close || target === body || target === panel,
    querySelector(selector) {
      if (selector === '[data-metas-history-panel]') return panel;
      if (selector === '[data-metas-history-close]') return close;
      return null;
    },
    querySelectorAll() {
      return [close, body];
    },
  };
  return { sheet, panel, close, body };
}

function makeFormSheet(options = {}) {
  const close = focusTarget();
  const initial = focusTarget();
  const submit = focusTarget();
  const panel = {
    listeners: {},
    addEventListener(name, fn) {
      (panel.listeners[name] = panel.listeners[name] || []).push(fn);
    },
    focus() {
      currentDocument.activeElement = panel;
    },
  };
  const controls = [close, initial, submit];
  const sheet = {
    dataset: {},
    contains: (target) => controls.indexOf(target) >= 0 || target === panel,
    querySelector(selector) {
      if (selector === '[data-metas-history-panel]') return null;
      if (selector === '[data-metas-form-panel]') return panel;
      if (selector === '[data-metas-history-close]') return null;
      if (selector === '.metas-sheet__close') return close;
      if (selector === '[data-metas-form-initial]') return initial;
      if (selector === 'dialog[open]') return options.openDialog ? { open: true } : null;
      return null;
    },
    querySelectorAll() {
      return controls;
    },
  };
  return { sheet, panel, close, initial, submit };
}

function makeHistoryTrigger(id = 'limit-history-trigger') {
  const trigger = focusTarget();
  trigger.id = id;
  trigger.closest = (selector) => (selector.includes('/metas/limite/historico') ? trigger : null);
  return trigger;
}

function pressTab(panel, shiftKey = false) {
  let prevented = false;
  const event = {
    key: 'Tab',
    shiftKey,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    preventDefault() {
      prevented = true;
    },
  };
  (panel.listeners.keydown || []).forEach((fn) => fn(event));
  return prevented;
}

function open(rt, entry) {
  rt.click(entry.toggle);
}

test('initializes each dropdown and progress chip once', () => {
  const entry = makeDropdown('one');
  const progress = makeProgress(76);
  const rt = runtime({ dropdowns: [entry], progressWraps: [progress] });

  assert.equal(entry.dropdown.dataset.ckReady, 'true');
  assert.equal(progress.dataset.pReady, 'true');
  assert.equal(rt.motionCalls.length, 1);
  assert.equal(rt.motionCalls[0].vars['--metas-p'], '76');
  assert.equal(rt.documentListeners.click.length, 1);
  assert.equal(rt.documentListeners.keydown.length, 1);
  assert.equal(rt.bodyListeners['htmx:afterSettle'].length, 1);
});

test('toggle opens and closes with ARIA and shields the card link', () => {
  const entry = makeDropdown('one');
  const rt = runtime({ dropdowns: [entry] });

  open(rt, entry);
  assert.equal(entry.dropdown.classList.contains('is-open'), true);
  assert.equal(entry.panel.classList.contains('hidden'), false);
  assert.equal(entry.toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(entry.link.classList.contains('pointer-events-none'), true);
  assert.equal(entry.link.getAttribute('aria-hidden'), 'true');

  open(rt, entry);
  assert.equal(entry.dropdown.classList.contains('is-open'), false);
  assert.equal(entry.panel.classList.contains('hidden'), true);
  assert.equal(entry.toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(entry.link.classList.contains('pointer-events-none'), false);
  assert.equal(entry.link.hasAttribute('aria-hidden'), false);
});

test('Escape closes an open menu and returns focus to its toggle', () => {
  const entry = makeDropdown('one');
  const rt = runtime({ dropdowns: [entry] });
  const item = { label: 'Editar' };
  entry.panel.items.push(item);

  open(rt, entry);
  rt.document.activeElement = item;
  rt.pressEscape();

  assert.equal(entry.dropdown.classList.contains('is-open'), false);
  assert.equal(entry.toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(entry.toggle.focusCalls(), 1);
  assert.equal(rt.document.activeElement, entry.toggle);
});

test('outside click closes without stealing focus from the new target', () => {
  const first = makeDropdown('first');
  const second = makeDropdown('second');
  const rt = runtime({ dropdowns: [first, second] });
  const outside = { closest: () => null };

  open(rt, first);
  open(rt, second);
  assert.equal(first.dropdown.classList.contains('is-open'), false);
  assert.equal(second.dropdown.classList.contains('is-open'), true);

  rt.document.activeElement = outside;
  rt.clickOutside(outside);

  assert.equal(second.dropdown.classList.contains('is-open'), false);
  assert.equal(second.toggle.focusCalls(), 0);
  assert.equal(rt.document.activeElement, outside);
});

test('Escape with focus outside the menu does not move focus', () => {
  const entry = makeDropdown('one');
  const rt = runtime({ dropdowns: [entry] });
  const outside = { closest: () => null };

  open(rt, entry);
  rt.document.activeElement = outside;
  rt.pressEscape();

  assert.equal(entry.dropdown.classList.contains('is-open'), false);
  assert.equal(entry.toggle.focusCalls(), 0);
  assert.equal(rt.document.activeElement, outside);
});

test('HTMX settle initializes only new cards and is idempotent', () => {
  const first = makeDropdown('first');
  const firstProgress = makeProgress(10);
  const rt = runtime({ dropdowns: [first], progressWraps: [firstProgress] });
  const firstListenerCount = first.dropdown.listeners.click.length;
  const second = makeDropdown('second');
  const secondProgress = makeProgress(120);
  rt.addDropdown(second);
  rt.addProgress(secondProgress);

  rt.settle();
  rt.settle();

  assert.equal(first.dropdown.listeners.click.length, firstListenerCount);
  assert.equal(first.dropdown.dataset.ckReady, 'true');
  assert.equal(second.dropdown.dataset.ckReady, 'true');
  assert.equal(second.dropdown.listeners.click.length, 1);
  assert.equal(firstProgress.dataset.pReady, 'true');
  assert.equal(secondProgress.dataset.pReady, 'true');
  assert.equal(rt.motionCalls.length, 2);
  assert.equal(rt.motionCalls[1].vars['--metas-p'], '100');
});

test('progress chips clamp values between 0 and 100', () => {
  const high = makeProgress(150);
  const low = makeProgress(-4);
  const invalid = makeProgress('abc');
  const rt = runtime({ progressWraps: [high, low, invalid], withMotion: true });

  assert.deepEqual(rt.motionCalls.map((call) => call.vars['--metas-p']), ['100', '0', '0']);
});

test('progress initialization tolerates missing runtime motion', () => {
  const high = makeProgress(150);
  const rt = runtime({ progressWraps: [high], withMotion: false });

  assert.equal(high.dataset.pReady, 'true');
  assert.doesNotThrow(() => rt.settle());
  assert.equal(rt.motionCalls.length, 0);
});

test('history sheet focuses its close button and traps Tab once', () => {
  const rt = runtime();
  const history = makeHistorySheet();

  rt.setSheet(history);
  rt.settle();

  assert.equal(history.close.focusCalls(), 1);
  assert.equal(rt.document.activeElement, history.close);
  assert.equal(history.panel.listeners.keydown.length, 1);

  rt.settle();
  assert.equal(history.close.focusCalls(), 1);
  assert.equal(history.panel.listeners.keydown.length, 1);

  assert.equal(pressTab(history.panel, true), true);
  assert.equal(history.body.focusCalls(), 1);
  assert.equal(rt.document.activeElement, history.body);

  assert.equal(pressTab(history.panel, false), true);
  assert.equal(history.close.focusCalls(), 2);
  assert.equal(rt.document.activeElement, history.close);
});

test('history close restores the trigger and reopening keeps one bridge', () => {
  const rt = runtime();
  const trigger = makeHistoryTrigger();
  rt.byID.set(trigger.id, trigger);
  const first = makeHistorySheet();

  rt.clickOutside(trigger);
  rt.setSheet(first);
  rt.settle();
  assert.equal(rt.document.activeElement, first.close);

  const wrappedClose = rt.window.closeBottomSheet;
  rt.closeBottomSheet();

  assert.equal(rt.closeCalls(), 1);
  assert.equal(trigger.focusCalls(), 1);
  assert.equal(rt.document.activeElement, trigger);

  rt.closeBottomSheet();
  assert.equal(rt.closeCalls(), 2);
  assert.equal(trigger.focusCalls(), 1);
  assert.equal(rt.window.closeBottomSheet, wrappedClose);

  rt.settle();
  assert.equal(trigger.focusCalls(), 1);

  const second = makeHistorySheet();
  rt.clickOutside(trigger);
  rt.setSheet(second);
  rt.settle();

  assert.equal(second.close.focusCalls(), 1);
  assert.equal(second.panel.listeners.keydown.length, 1);
  assert.equal(rt.document.activeElement, second.close);

  rt.closeBottomSheet();
  assert.equal(rt.closeCalls(), 3);
  assert.equal(trigger.focusCalls(), 2);
  assert.equal(rt.document.activeElement, trigger);
});

test('history close without a trigger does not steal focus', () => {
  const rt = runtime();
  const history = makeHistorySheet();
  const outside = { focusCalls: () => 0 };

  rt.setSheet(history);
  rt.settle();
  rt.document.activeElement = outside;
  rt.closeBottomSheet();

  assert.equal(rt.closeCalls(), 1);
  assert.equal(rt.document.activeElement, outside);
  assert.equal(history.close.focusCalls(), 1);
});

test('form sheet focuses the initial field, traps Tab and restores the trigger once', () => {
  const rt = runtime();
  const trigger = makeHistoryTrigger('limit-form-trigger');
  rt.byID.set(trigger.id, trigger);
  const sheet = makeFormSheet();

  rt.clickOutside(trigger);
  rt.setSheet(sheet);
  rt.settle();

  assert.equal(sheet.initial.focusCalls(), 1);
  assert.equal(rt.document.activeElement, sheet.initial);
  assert.equal(sheet.panel.listeners.keydown.length, 1);

  rt.document.activeElement = sheet.submit;
  assert.equal(pressTab(sheet.panel, false), true);
  assert.equal(rt.document.activeElement, sheet.close);

  rt.document.activeElement = sheet.close;
  assert.equal(pressTab(sheet.panel, true), true);
  assert.equal(rt.document.activeElement, sheet.submit);

  const wrappedClose = rt.window.closeBottomSheet;
  rt.closeBottomSheet();
  assert.equal(rt.closeCalls(), 1);
  assert.equal(trigger.focusCalls(), 1);
  assert.equal(rt.window.closeBottomSheet, wrappedClose);
});

test('native icon dialog owns focus while open', () => {
  const rt = runtime();
  const sheet = makeFormSheet({ openDialog: true });

  rt.setSheet(sheet);
  rt.settle();
  rt.document.activeElement = sheet.submit;

  assert.equal(pressTab(sheet.panel, false), false);
  assert.equal(rt.document.activeElement, sheet.submit);
});

test('successful OOB close releases the overlay and restores the trigger', () => {
  const rt = runtime();
  const trigger = makeHistoryTrigger('limit-oob-trigger');
  rt.byID.set(trigger.id, trigger);
  const sheet = makeFormSheet();

  rt.clickOutside(trigger);
  rt.setSheet(sheet);
  rt.settle();
  assert.equal(sheet.initial.focusCalls(), 1);

  rt.setSheet(null);
  rt.settleWithTarget({ id: 'bottom-sheet-container', innerHTML: '' });

  assert.equal(rt.overlayCloseCalls(), 1);
  assert.equal(trigger.focusCalls(), 1);
  assert.equal(rt.document.activeElement, trigger);
});

test('Escape inside the native icon dialog does not close the sheet', () => {
  const rt = runtime();
  const sheet = makeFormSheet({ openDialog: true });
  rt.setSheet(sheet);
  rt.settle();

  let stopped = false;
  const event = { key: 'Escape', stopImmediatePropagation() { stopped = true; } };
  (rt.windowListeners.keydown || []).forEach((fn) => fn(event));

  assert.equal(stopped, true);
  assert.equal(rt.document.querySelector('#bottom-sheet-container [data-metas-sheet]'), sheet.sheet);
});
