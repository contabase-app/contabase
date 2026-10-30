const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Contratos de interação dos cards de Metas:
// - um único menu aberto por vez;
// - ESC devolve o foco ao gatilho somente quando o foco estava no menu;
// - clique externo fecha sem roubar o foco do novo alvo;
// - HTMX inicializa apenas os cards novos, uma vez;
// - o chip de progresso é posicionado uma vez por barra e com clamp 0..100.
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
        querySelectorAll(selector) {
            return selector === '[data-ck-mode-panel]' ? [] : [];
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
    let tabs = options.tabs || [];
    const documentListeners = {};
    const bodyListeners = {};
    const windowListeners = {};
    const motionCalls = [];
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
        querySelector(selector) {
            if (selector === '#bottom-sheet-container [data-metas-sheet]' || selector === '#bottom-sheet-container [data-metas-history]') {
                return currentSheet;
            }
            if (restoredTrigger && selector.indexOf('[aria-controls=') === 0) {
                return restoredTrigger;
            }
            if (selector === '#metas-tabs [role="tab"][aria-selected="true"]') {
                return tabs.find((tab) => tab.getAttribute('aria-selected') === 'true') || null;
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
            if (selector === '#metas-tabs [role="tab"]') {
                return tabs;
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
        tabs,
        setTabs(nextTabs) {
            tabs = nextTabs;
        },
        documentListeners,
        bodyListeners,
        windowListeners,
        motionCalls,
        window,
        addDropdown(entry) {
            dropdowns.push(entry.dropdown);
        },
        addProgress(entry) {
            progressWraps.push(entry);
        },
        setHistorySheet(entry) {
            currentSheet = entry ? entry.sheet : null;
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
            const event = {
                target: element,
                closest: element.closest || (() => null),
            };
            document.emit('click', event);
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

function makeHistoryTrigger() {
    const trigger = focusTarget();
    trigger.closest = (selector) => (selector.includes('/metas/caixinha/historico') ? trigger : null);
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
    const item = { label: 'Aportar' };
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
    const rt = runtime({
        progressWraps: [high, low, invalid],
        withMotion: true,
    });

    assert.deepEqual(rt.motionCalls.map((call) => call.vars['--metas-p']), ['100', '0', '0']);
});

test('progress initialization tolerates missing runtime motion', () => {
    const high = makeProgress(150);
    const rt = runtime({
        progressWraps: [high],
        withMotion: false,
    });

    assert.equal(high.dataset.pReady, 'true');
    assert.doesNotThrow(() => rt.settle());
    assert.equal(rt.motionCalls.length, 0);
});

test('history sheet focuses its close button and traps Tab once', () => {
    const rt = runtime();
    const history = makeHistorySheet();

    rt.setHistorySheet(history);
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

    assert.equal(pressTab(history.panel, false), false);
    assert.equal(history.close.focusCalls(), 2);
});

test('history close restores the trigger and reopening keeps one bridge', () => {
    const rt = runtime();
    const trigger = makeHistoryTrigger();
    const first = makeHistorySheet();

    rt.clickOutside(trigger);
    rt.setHistorySheet(first);
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
    rt.setHistorySheet(second);
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

    rt.setHistorySheet(history);
    rt.settle();
    rt.document.activeElement = outside;
    rt.closeBottomSheet();

    assert.equal(rt.closeCalls(), 1);
    assert.equal(rt.document.activeElement, outside);
    assert.equal(history.close.focusCalls(), 1);
});

function makeMoneyTrigger(kind) {
    const trigger = focusTarget();
    trigger.closest = (selector) => (selector.includes('/metas/caixinha/' + kind + '/sheet') ? trigger : null);
    return trigger;
}

function makeMoneySheet(kind, options = {}) {
    const close = focusTarget();
    const amount = focusTarget();
    const cancel = focusTarget();
    const submit = focusTarget();
    const box = focusTarget();
    box.value = 'box-1';
    amount.id = kind + 'Amount';
    amount.name = 'amount';
    amount.value = '';
    amount.closest = () => null;
    amount.setAttribute('aria-invalid', 'false');
    const error = {
        classList: classList(['hidden']),
        textContent: '',
        dataset: {},
    };
    const formAttrs = new Map([
        ['data-metas-money-form', ''],
        ['data-metas-form-kind', kind],
    ]);
    const form = {
        closest: (selector) => (selector.indexOf('form[data-metas-money-form]') >= 0 ? form : null),
        hasAttribute: (name) => formAttrs.has(name),
        getAttribute: (name) => (formAttrs.has(name) ? formAttrs.get(name) : null),
        querySelector(selector) {
            if (selector === '[name="amount"]') return amount;
            if (selector === '[name="box_id"]') return box;
            if (selector === '.metas-form__field-error') return error;
            return null;
        },
    };
    amount.closest = (selector) => (selector.indexOf('data-metas-money-form') >= 0 ? form : null);
    const panel = {
        dataset: {},
        listeners: {},
        addEventListener(name, fn) {
            (panel.listeners[name] = panel.listeners[name] || []).push(fn);
        },
        focus() {
            currentDocument.activeElement = panel;
        },
    };
    const controls = [close, amount, cancel, submit];
    const sheet = {
        dataset: {},
        contains: (target) => controls.indexOf(target) >= 0 || target === panel || target === error,
        querySelector(selector) {
            if (selector === '[data-metas-history-panel]') return null;
            if (selector === '[data-metas-form-panel]') return panel;
            if (selector === '[data-metas-history-close]') return null;
            if (selector === '.metas-sheet__close') return close;
            if (selector === '[data-metas-form-initial]') return amount;
            if (selector === 'dialog[open]') return options.openDialog ? { open: true } : null;
            if (selector.indexOf('form[data-metas-money-form]') === 0) return form;
            return null;
        },
        querySelectorAll() {
            return controls;
        },
    };
    return { sheet, panel, close, amount, cancel, submit, box, error, form };
}

test('money sheet focuses the amount, traps Tab and restores the trigger once', () => {
    const rt = runtime();
    const trigger = makeMoneyTrigger('aporte');
    const sheet = makeMoneySheet('aporte');

    rt.clickOutside(trigger);
    rt.setSheet(sheet);
    rt.settle();

    assert.equal(sheet.amount.focusCalls(), 1);
    assert.equal(rt.document.activeElement, sheet.amount);
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
    assert.equal(rt.document.activeElement, trigger);

    rt.closeBottomSheet();
    assert.equal(trigger.focusCalls(), 1);
    assert.equal(rt.window.closeBottomSheet, wrappedClose);
});

test('native icon dialog owns the focus while open', () => {
    const rt = runtime();
    const sheet = makeMoneySheet('aporte', { openDialog: true });

    rt.setSheet(sheet);
    rt.settle();
    rt.document.activeElement = sheet.submit;

    assert.equal(pressTab(sheet.panel, false), false);
    assert.equal(rt.document.activeElement, sheet.submit);
});

test('successful OOB close releases the overlay and restores the trigger', () => {
    const rt = runtime();
    const trigger = makeMoneyTrigger('resgate');
    const sheet = makeMoneySheet('resgate');

    rt.clickOutside(trigger);
    rt.setSheet(sheet);
    rt.settle();
    assert.equal(sheet.amount.focusCalls(), 1);

    rt.setSheet(null);
    rt.settleWithTarget({ id: 'bottom-sheet-container', innerHTML: '' });

    assert.equal(rt.overlayCloseCalls(), 1);
    assert.equal(trigger.focusCalls(), 1);
    assert.equal(rt.document.activeElement, trigger);
});

test('money form blocks a zero amount and restores the typed value on server error', () => {
    const rt = runtime();
    const first = makeMoneySheet('aporte');
    rt.setSheet(first);
    rt.settle();

    const focusBeforeSubmit = first.amount.focusCalls();
    let prevented = false;
    const event = {
        target: first.form,
        preventDefault() { prevented = true; },
        stopImmediatePropagation() {},
    };
    (rt.documentListeners.submit || []).forEach((fn) => fn(event));

    assert.equal(prevented, true);
    assert.equal(first.error.classList.contains('hidden'), false);
    assert.equal(first.error.textContent, 'Informe um valor maior que zero.');
    assert.equal(first.amount.getAttribute('aria-invalid'), 'true');
    assert.equal(first.amount.focusCalls(), focusBeforeSubmit + 1);

    first.amount.value = 'R$ 50,00';
    (rt.bodyListeners['htmx:beforeRequest'] || []).forEach((fn) => fn({ detail: { elt: first.form } }));

    const second = makeMoneySheet('aporte');
    rt.setSheet(second);
    rt.settle();

    assert.equal(second.amount.value, 'R$ 50,00');
    assert.equal(second.amount.focusCalls() >= 1, true);
    assert.equal(second.error.classList.contains('hidden'), true);
});

test('money form accepts a positive value without client-side error', () => {
    const rt = runtime();
    const sheet = makeMoneySheet('resgate');
    sheet.amount.value = 'R$ 0,01';
    rt.setSheet(sheet);
    rt.settle();

    let prevented = false;
    const event = {
        target: sheet.form,
        preventDefault() { prevented = true; },
        stopImmediatePropagation() {},
    };
    (rt.documentListeners.submit || []).forEach((fn) => fn(event));

    assert.equal(prevented, false);
    assert.equal(sheet.error.classList.contains('hidden'), true);
    assert.equal(sheet.amount.getAttribute('aria-invalid'), 'false');
});

test('menu mode selector does not swallow item clicks (M6 trigger bridge)', () => {
    assert.match(source, /closest\('button\[data-ck-mode\]'\)/);
    assert.doesNotMatch(source, /closest\('\[data-ck-mode\]'\)/);
});

test('Escape inside the native icon dialog does not close the sheet', () => {
    const rt = runtime();
    const sheet = makeMoneySheet('aporte', { openDialog: true });
    rt.setSheet(sheet);
    rt.settle();

    let stopped = false;
    const event = {
        key: 'Escape',
        stopImmediatePropagation() { stopped = true; },
    };
    (rt.windowListeners.keydown || []).forEach((fn) => fn(event));

    assert.equal(stopped, true);
    assert.equal(rt.document.querySelector('#bottom-sheet-container [data-metas-sheet]'), sheet.sheet);
});

test('OOB success restores focus to the replacement trigger after the tabs swap', () => {
    const rt = runtime();
    const trigger = focusTarget();
    trigger.setAttribute('aria-controls', 'menu-1');
    trigger.closest = (selector) => (selector.includes('/metas/caixinha/aporte/sheet') ? trigger : null);
    const replacement = focusTarget();
    rt.setRestoredTrigger(replacement);
    const sheet = makeMoneySheet('aporte');

    rt.clickOutside(trigger);
    rt.setSheet(sheet);
    rt.settle();

    rt.setSheet(null);
    rt.settleWithTarget({ id: 'bottom-sheet-container', innerHTML: '' });

    assert.equal(replacement.focusCalls(), 1);
    assert.equal(rt.document.activeElement, replacement);
});

function makeMetasTab(id, selected, tabindex) {
    const tab = focusTarget();
    tab.id = id;
    tab.setAttribute('role', 'tab');
    tab.setAttribute('aria-selected', selected ? 'true' : 'false');
    tab.setAttribute('tabindex', tabindex);
    tab.closest = (selector) => (selector.includes('#metas-tabs [role="tab"]') ? tab : null);
    return tab;
}

function pressTabKey(rt, key, target) {
    let prevented = false;
    rt.document.emit('keydown', {
        key,
        target,
        ctrlKey: false,
        metaKey: false,
        altKey: false,
        preventDefault() { prevented = true; },
    });
    return prevented;
}

test('metas tabs use roving tabindex and manual arrow navigation', () => {
    const limites = makeMetasTab('metas-tab-limites', true, '0');
    const reservas = makeMetasTab('metas-tab-reservas', false, '-1');
    const rt = runtime({ tabs: [limites, reservas] });

    assert.equal(pressTabKey(rt, 'ArrowRight', limites), true);
    assert.equal(rt.document.activeElement, reservas);
    assert.equal(limites.getAttribute('tabindex'), '-1');
    assert.equal(reservas.getAttribute('tabindex'), '0');
    assert.equal(limites.getAttribute('aria-selected'), 'true');
    assert.equal(reservas.getAttribute('aria-selected'), 'false');

    assert.equal(pressTabKey(rt, 'ArrowLeft', reservas), true);
    assert.equal(rt.document.activeElement, limites);
    assert.equal(limites.getAttribute('tabindex'), '0');
    assert.equal(reservas.getAttribute('tabindex'), '-1');

    assert.equal(pressTabKey(rt, 'End', limites), true);
    assert.equal(rt.document.activeElement, reservas);
    assert.equal(pressTabKey(rt, 'Home', reservas), true);
    assert.equal(rt.document.activeElement, limites);
});

test('metas tab click restores focus to the replacement active tab after HTMX swap', () => {
    const limites = makeMetasTab('metas-tab-limites', true, '0');
    const reservas = makeMetasTab('metas-tab-reservas', false, '-1');
    const rt = runtime({ tabs: [limites, reservas] });

    rt.document.emit('click', {
        target: limites,
        closest: (selector) => (selector === '#metas-tabs [role="tab"]' ? limites : null),
    });

    const nextLimites = makeMetasTab('metas-tab-limites', true, '0');
    const nextReservas = makeMetasTab('metas-tab-reservas', false, '-1');
    rt.setTabs([nextLimites, nextReservas]);
    rt.settle();

    assert.equal(nextLimites.focusCalls(), 1);
    assert.equal(rt.document.activeElement, nextLimites);
});
