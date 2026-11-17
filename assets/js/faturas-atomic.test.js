const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function fixture() {
    const listeners = new Map();
    const body = {
        addEventListener(name, fn) {
            if (!listeners.has(name)) listeners.set(name, []);
            listeners.get(name).push(fn);
        },
        emit(name, detail) {
            const event = { detail, prevented: false, preventDefault() { this.prevented = true; } };
            for (const fn of listeners.get(name) || []) fn(event);
            return event;
        },
    };
    const documentListeners = new Map();
    const document = {
        body,
        addEventListener(name, fn) { documentListeners.set(name, fn); },
        querySelector: () => ({}), querySelectorAll: () => [],
    };
    vm.runInNewContext(fs.readFileSync(require.resolve('./faturas-index.js'), 'utf8'), { document, window: {} });
    function request(verb, path) {
        const xhr = {};
        const event = body.emit('htmx:beforeRequest', { xhr, requestConfig: { verb, path } });
        return { xhr, event };
    }
    return { body, request, documentListeners };
}

test('a late card or full snapshot response cannot overwrite newer selection or history', () => {
    const ui = fixture();
    const old = ui.request('get', '/faturas?partial=cards&mes=11&ano=2026');
    const current = ui.request('get', '/faturas/conteudo?cartao=b&mes=11&ano=2026');
    assert.equal(ui.body.emit('htmx:beforeOnLoad', { xhr: old.xhr }).prevented, true);
    assert.equal(ui.body.emit('htmx:beforeOnLoad', { xhr: current.xhr }).prevented, false);
});

test('payment invalidates older reads and prevents duplicate submissions until completion', () => {
    const ui = fixture();
    const old = ui.request('get', '/faturas/conteudo?cartao=a');
    const payment = ui.request('post', '/cartoes/faturas/pagar');
    assert.equal(payment.event.prevented, false);
    assert.equal(ui.body.emit('htmx:beforeOnLoad', { xhr: old.xhr }).prevented, true);
    assert.equal(ui.request('post', '/cartoes/faturas/pagar').event.prevented, true);
    assert.equal(ui.request('get', '/faturas?partial=cards').event.prevented, true);
    assert.equal(ui.body.emit('htmx:beforeOnLoad', { xhr: payment.xhr }).prevented, false);
    ui.body.emit('htmx:afterRequest', { xhr: payment.xhr });
    assert.equal(ui.request('get', '/faturas/conteudo?cartao=b').event.prevented, false);
});

test('a failed mutation releases the request guard without running another payment', () => {
    const ui = fixture();
    const payment = ui.request('post', '/cartoes/faturas/pagar');
    ui.body.emit('htmx:afterRequest', { xhr: payment.xhr, failed: true });
    assert.equal(ui.request('get', '/faturas?partial=cards').event.prevented, false);
});

test('form reads and unrelated requests do not invalidate an invoice mutation response', () => {
    const ui = fixture();
    const payment = ui.request('post', '/cartoes/faturas/pagar');
    assert.equal(ui.request('get', '/transacoes/nova').event.prevented, false);
    assert.equal(ui.body.emit('htmx:beforeOnLoad', { xhr: payment.xhr }).prevented, false);
});

test('snapshot event refreshes other financial regions without starting another hub GET', () => {
    const ui = fixture();
    const event = ui.body.emit('htmx:beforeRequest', {
        xhr: {},
        requestConfig: { verb: 'get', path: '/faturas?partial=cards', triggeringEvent: { detail: { invoiceSnapshot: true } } },
    });
    assert.equal(event.prevented, true);
    assert.equal(ui.request('get', '/faturas?partial=cards').event.prevented, false);
});

test('duplicate submit is stopped in capture before HTMX can enqueue another payment', () => {
    const ui = fixture();
    const payment = ui.request('post', '/cartoes/faturas/pagar');
    function submit() {
        const event = { prevented: false, stopped: false, preventDefault() { this.prevented = true; }, stopImmediatePropagation() { this.stopped = true; } };
        ui.documentListeners.get('submit')(event);
        return event;
    }
    assert.equal(submit().prevented, true);
    assert.equal(submit().stopped, true);
    ui.body.emit('htmx:afterRequest', { xhr: payment.xhr });
    assert.equal(submit().prevented, false);
});
