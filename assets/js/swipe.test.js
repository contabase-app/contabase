const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Locks the target contract used by programmatic swipe actions. Invoice rows use
// `invoice-tx-*`; assuming `tx-*` made htmx abort with targetError before the
// request reached the existing delete/move endpoints.
const source = fs.readFileSync(path.join(__dirname, 'swipe.js'), 'utf8');

function buildRow(overrides = {}) {
    return Object.assign({
        id: 'invoice-tx-1',
        dataset: {
            txId: 'tx-1',
            swipeLeftUrl: '/transacoes/tx-1?invoice_id=invoice-1',
            swipeLeftMethod: 'DELETE',
            swipeLeftSwap: 'none',
            swipeLeftConfirm: 'Remover este lançamento?',
        },
        querySelector: () => null,
    }, overrides);
}

function runAction(row, side, options = {}) {
    const calls = [];
    const scopeCalls = [];
    const confirmCalls = [];
    const confirmMessages = [];
    const context = {
        console,
        document: { querySelectorAll: () => [] },
        window: {
            cbRuntimeMotion: { supported: true },
            htmx: {
                ajax: (method, url, options) => calls.push({ method, url, options }),
            },
            openGlobalConfirm: (message, onConfirm) => {
                confirmMessages.push(message);
                if (options.autoConfirm !== false) onConfirm();
            },
            openInvoiceMoveScopeFromRow: (sourceRow) => scopeCalls.push(sourceRow),
            openPaymentConfirmModalFromElement: (sourceRow) => confirmCalls.push(sourceRow),
        },
    };
    context.htmx = context.window.htmx;
    vm.createContext(context);
    vm.runInContext(source, context);
    context.runSwipeAction(row, side);
    return { calls, scopeCalls, confirmCalls, confirmMessages };
}

test('invoice swipe targets the row that exists on the page', () => {
    const { calls } = runAction(buildRow(), 'left');

    assert.equal(calls.length, 1);
    assert.equal(calls[0].method, 'DELETE');
    assert.equal(calls[0].url, '/transacoes/tx-1?invoice_id=invoice-1');
    assert.equal(calls[0].options.target, '#invoice-tx-1');
    assert.equal(calls[0].options.swap, 'none');
});

test('rows that already use the legacy tx prefix keep their target', () => {
    const row = buildRow({
        id: 'tx-7',
        dataset: {
            txId: '7',
            swipeLeftUrl: '/transacoes/7?escopo=single',
            swipeLeftMethod: 'DELETE',
            swipeLeftSwap: 'delete',
            swipeLeftConfirm: 'Remover este lançamento?',
        },
    });
    const { calls } = runAction(row, 'left');

    assert.equal(calls[0].options.target, '#tx-7');
    assert.equal(calls[0].options.swap, 'delete');
});

test('invoice swipe move sends the existing optimistic values once', () => {
    const values = {
        expected_invoice_id: 'invoice-1',
        expected_destination_reference: '2026-12',
        expected_updated_at: '123',
        expected_move_scope: 'single',
    };
    const row = buildRow({
        dataset: {
            txId: 'tx-1',
            swipeRightUrl: '/transacoes/tx-1/mover-fatura',
            swipeRightMethod: 'POST',
            swipeRightSwap: 'none',
            swipeRightVals: JSON.stringify(values),
        },
    });
    const { calls } = runAction(row, 'right');

    assert.equal(calls.length, 1);
    assert.equal(calls[0].method, 'POST');
    assert.equal(calls[0].url, '/transacoes/tx-1/mover-fatura');
    assert.equal(calls[0].options.target, '#invoice-tx-1');
    assert.deepEqual(JSON.parse(JSON.stringify(calls[0].options.values)), values);
});

test('installment swipe opens the scope choice without sending a request', () => {
    const row = buildRow({
        dataset: {
            txId: 'tx-1',
            swipeRightUrl: '/transacoes/tx-1/mover-fatura',
            swipeRightMoveScope: 'true',
            installmentLabel: '3/6',
        },
    });
    const { calls, scopeCalls } = runAction(row, 'right');

    assert.equal(calls.length, 0);
    assert.equal(scopeCalls.length, 1);
    assert.equal(scopeCalls[0], row);
});

test('pending transfer follows the standard swipe directions', () => {
    const row = buildRow({
        id: 'transfer-tx-1',
        dataset: {
            txId: 'tx-1',
            swipeLeftUrl: '/transacoes/tx-1?escopo=single',
            swipeLeftMethod: 'DELETE',
            swipeLeftTarget: '#transfer-tx-1',
            swipeLeftSwap: 'delete',
            swipeLeftConfirm: 'Remover esta transferência?',
            swipeRightPaymentConfirm: 'true',
        },
    });

    // Right swipe (side left) must ask before deleting anything.
    const held = runAction(row, 'left', { autoConfirm: false });
    assert.equal(held.calls.length, 0);
    assert.deepEqual(held.confirmMessages, ['Remover esta transferência?']);

    const confirmed = runAction(row, 'left');
    assert.equal(confirmed.calls.length, 1);
    assert.equal(confirmed.calls[0].method, 'DELETE');
    assert.equal(confirmed.calls[0].url, '/transacoes/tx-1?escopo=single');
    assert.equal(confirmed.calls[0].options.target, '#transfer-tx-1');
    assert.equal(confirmed.calls[0].options.swap, 'delete');

    // Left swipe (side right) opens the shared transfer confirmation.
    const payment = runAction(row, 'right');
    assert.equal(payment.calls.length, 0);
    assert.equal(payment.confirmCalls.length, 1);
    assert.equal(payment.confirmCalls[0], row);
});

// Invoice rows follow the same direction contract as the rest of the app:
// dragging left to right runs the destructive DELETE; dragging right to left
// opens the existing move-to-next-invoice scope choice.
function buildGestureHarness(dataset) {
    const calls = [];
    const scopeCalls = [];
    const confirmMessages = [];
    const listeners = {};
    const content = {};
    const leftBg = {};
    const rightBg = {};
    let confirmEnabled = true;
    const row = {
        id: 'invoice-tx-1',
        dataset: Object.assign({}, dataset),
        addEventListener: (type, handler) => {
            listeners[type] = listeners[type] || [];
            listeners[type].push(handler);
        },
        dispatchEvent: () => {},
        querySelector: (selector) => {
            if (selector === '[data-swipe-content]') return content;
            if (selector === '[data-swipe-bg-left]') return leftBg;
            if (selector === '[data-swipe-bg-right]') return rightBg;
            return null;
        },
    };
    const context = {
        console,
        CustomEvent: class {
            constructor(type, options = {}) {
                this.type = type;
                this.detail = options.detail;
                this.bubbles = options.bubbles === true;
            }
        },
        document: { querySelectorAll: () => [] },
        window: {
            cbRuntimeMotion: {
                supported: true,
                cancel: () => {},
                hold: () => {},
                toBase: () => {},
            },
            htmx: {
                ajax: (method, url, options) => calls.push({ method, url, options }),
            },
            openGlobalConfirm: (message, onConfirm) => {
                confirmMessages.push(message);
                if (confirmEnabled) onConfirm();
            },
            openInvoiceMoveScopeFromRow: (sourceRow) => scopeCalls.push(sourceRow),
        },
    };
    context.htmx = context.window.htmx;
    vm.createContext(context);
    vm.runInContext(source, context);
    context.initSwipeRow(row);

    function touch(type, clientX) {
        (listeners[type] || []).forEach((handler) => handler({
            touches: [{ clientX, clientY: 10 }],
            cancelable: true,
            preventDefault: () => {},
        }));
    }

    return {
        row,
        calls,
        scopeCalls,
        confirmMessages,
        setAutoConfirm: (value) => { confirmEnabled = value; },
        drag: (fromX, toX) => {
            touch('touchstart', fromX);
            touch('touchmove', toX);
            (listeners.touchend || []).forEach((handler) => handler({}));
        },
    };
}

const invoiceGestureDataset = {
    txId: 'tx-1',
    swipeLeftUrl: '/transacoes/tx-1?invoice_id=invoice-1',
    swipeLeftMethod: 'DELETE',
    swipeLeftSwap: 'none',
    swipeLeftConfirm: 'Remover este lançamento?',
    swipeRightUrl: '/transacoes/tx-1/mover-fatura',
    swipeRightMethod: 'POST',
    swipeRightSwap: 'none',
    swipeRightMoveScope: 'true',
};

test('invoice gesture left to right asks to delete and uses the delete endpoint', () => {
    const held = buildGestureHarness(invoiceGestureDataset);
    held.setAutoConfirm(false);
    held.drag(10, 190);

    assert.deepEqual(held.confirmMessages, ['Remover este lançamento?']);
    assert.equal(held.calls.length, 0, 'the gesture must not delete before confirmation');
    assert.equal(held.scopeCalls.length, 0);

    const confirmed = buildGestureHarness(invoiceGestureDataset);
    confirmed.drag(10, 190);

    assert.equal(confirmed.confirmMessages.length, 1);
    assert.equal(confirmed.calls.length, 1);
    assert.equal(confirmed.calls[0].method, 'DELETE');
    assert.equal(confirmed.calls[0].url, '/transacoes/tx-1?invoice_id=invoice-1');
    assert.equal(confirmed.calls[0].options.target, '#invoice-tx-1');
    assert.equal(confirmed.calls[0].options.swap, 'none');
    assert.equal(confirmed.scopeCalls.length, 0, 'delete gesture must never open the move scope');
});

test('invoice gesture right to left opens the move scope and never deletes', () => {
    const harness = buildGestureHarness(invoiceGestureDataset);
    harness.drag(190, 10);

    assert.equal(harness.scopeCalls.length, 1);
    assert.equal(harness.calls.length, 0, 'move gesture must never call the delete endpoint');
});
