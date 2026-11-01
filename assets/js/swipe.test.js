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

function runAction(row, side) {
    const calls = [];
    const scopeCalls = [];
    const context = {
        console,
        document: { querySelectorAll: () => [] },
        window: {
            cbRuntimeMotion: { supported: true },
            htmx: {
                ajax: (method, url, options) => calls.push({ method, url, options }),
            },
            openGlobalConfirm: (message, onConfirm) => onConfirm(),
            openInvoiceMoveScopeFromRow: (sourceRow) => scopeCalls.push(sourceRow),
        },
    };
    context.htmx = context.window.htmx;
    vm.createContext(context);
    vm.runInContext(source, context);
    context.runSwipeAction(row, side);
    return { calls, scopeCalls };
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
