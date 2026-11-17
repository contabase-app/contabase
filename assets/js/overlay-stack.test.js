const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require.resolve('./overlay-stack.js'), 'utf8');

function classList(initial = []) {
    const values = new Set(initial);
    return {
        add: (key) => values.add(key),
        remove: (key) => values.delete(key),
        contains: (key) => values.has(key),
        toggle: (key, force) => {
            const next = force === undefined ? !values.has(key) : Boolean(force);
            if (next) values.add(key);
            else values.delete(key);
            return next;
        },
    };
}

function overlay(id, hidden = false) {
    const attributes = new Map();
    return {
        id,
        classList: classList(hidden ? ['hidden'] : []),
        innerHTML: '',
        getAttribute: (name) => (attributes.has(name) ? attributes.get(name) : null),
        hasAttribute: (name) => attributes.has(name),
        setAttribute: (name, value) => attributes.set(name, String(value)),
    };
}

function createWorld(initialOverlays = []) {
    let overlays = [...initialOverlays];
    const bodyListeners = new Map();
    const body = {
        addEventListener(name, fn) {
            if (!bodyListeners.has(name)) bodyListeners.set(name, []);
            bodyListeners.get(name).push(fn);
        },
        dispatch(name, event = {}) {
            (bodyListeners.get(name) || []).forEach((fn) => fn(event));
        },
    };
    const documentElement = { classList: classList(initialOverlays.length ? ['cb-overlay-open'] : []) };
    const legacy = new Map();
    const document = {
        body,
        documentElement,
        querySelectorAll(selector) {
            if (selector === '[data-contabase-overlay]') return overlays;
            return [];
        },
        getElementById(id) {
            return legacy.get(id) || null;
        },
    };
    const history = {
        state: initialOverlays.length ? { contabaseOverlay: true } : null,
        backCalls: 0,
        pushCalls: 0,
        replaceCalls: 0,
        lastURL: '',
        back() { this.backCalls += 1; },
        pushState(state, _title, url) {
            this.state = state;
            this.pushCalls += 1;
            this.lastURL = url;
        },
        replaceState(state, _title, url) {
            this.state = state;
            this.replaceCalls += 1;
            this.lastURL = url;
        },
    };
    const window = {
        location: { pathname: '/cartoes/card-test/faturas', search: '?mes=8&ano=2026' },
        getComputedStyle: (el) => ({ display: el.classList.contains('hidden') ? 'none' : 'grid' }),
        addEventListener() {},
    };
    const localStorage = { getItem: () => null };

    vm.runInNewContext(source, { document, window, history, localStorage, console });

    return {
        body,
        window,
        document,
        history,
        documentElement,
        get overlays() { return overlays; },
        removeOverlay(id) {
            overlays = overlays.filter((item) => item.id !== id);
            return overlays;
        },
        removeAllOverlays() {
            overlays = [];
        },
    };
}

test('a dialog removed by HTMX drops its sentinel without navigating', () => {
    const dialog = overlay('invoice-partial-payment-modal');
    const ui = createWorld([dialog]);
    ui.window.overlayDidOpen('partial-payment');
    assert.equal(ui.history.pushCalls, 1);
    assert.equal(ui.documentElement.classList.contains('cb-overlay-open'), true);

    ui.removeAllOverlays();
    ui.body.dispatch('htmx:afterSettle', { detail: {} });

    assert.equal(ui.documentElement.classList.contains('cb-overlay-open'), false);
    assert.equal(ui.history.backCalls, 0);
    assert.equal(ui.history.replaceCalls, 1);
    assert.equal(ui.history.state?.contabaseOverlay, undefined);
    assert.equal(ui.window.overlayReconcile(), true);
    assert.equal(ui.history.replaceCalls, 1);
});

test('a legitimate overlay keeps the scroll lock and its sentinel', () => {
    const payment = overlay('invoice-partial-payment-modal');
    const drawer = overlay('workspaceDrawer');
    const ui = createWorld([payment, drawer]);
    ui.window.overlayDidOpen('partial-payment');

    ui.removeOverlay(payment.id);
    ui.body.dispatch('htmx:afterSettle', { detail: {} });

    assert.equal(ui.overlays.length, 1);
    assert.equal(ui.documentElement.classList.contains('cb-overlay-open'), true);
    assert.equal(ui.history.backCalls, 0);
    assert.equal(ui.history.replaceCalls, 0);
    assert.equal(ui.window.overlayReconcile(), false);
});

test('reconcile does not consume the opening of a newly inserted overlay', () => {
    const sheet = overlay('bottom-sheet-container');
    const ui = createWorld([sheet]);

    assert.equal(ui.window.overlayReconcile(), false);
    ui.window.overlayDidOpen('htmx:afterSwap(bottom-sheet-container)');

    assert.equal(ui.history.pushCalls, 1);
    assert.equal(ui.documentElement.classList.contains('cb-overlay-open'), true);
});

test('reconcile is idempotent after the dialog and sentinel are gone', () => {
    const ui = createWorld([overlay('invoice-settle-payment-modal')]);
    ui.window.overlayDidOpen('settle-payment');
    ui.removeAllOverlays();

    assert.equal(ui.window.overlayReconcile(), true);
    assert.equal(ui.window.overlayReconcile(), true);
    assert.equal(ui.window.overlayReconcile(), true);
    assert.equal(ui.history.replaceCalls, 1);
    assert.equal(ui.history.backCalls, 0);
    assert.equal(ui.documentElement.classList.contains('cb-overlay-open'), false);
});
