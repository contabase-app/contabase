const test = require('node:test');
const assert = require('node:assert/strict');
const preview = require('./installment-preview.js');

test('describes R$ 20.000,00 split into 3 using backend remainder rule', () => {
    assert.equal(
        preview.describe(2000000, 3),
        '3 parcelas: 1x de R$ 6.666,68 e 2x de R$ 6.666,66'
    );
});

test('describes R$ 100,00 split into 3', () => {
    assert.equal(
        preview.describe(10000, 3),
        '3 parcelas: 1x de R$ 33,34 e 2x de R$ 33,33'
    );
});

test('describes R$ 10,00 split into 3', () => {
    assert.equal(
        preview.describe(1000, 3),
        '3 parcelas: 1x de R$ 3,34 e 2x de R$ 3,33'
    );
});

test('describes one installment without losing cents', () => {
    assert.equal(
        preview.describe(2000000, 1),
        'Parcelado em 1x de R$ 20.000,00'
    );
});

test('recalculates when installment count or amount changes', () => {
    assert.equal(preview.describe(2000000, 2), 'Parcelado em 2x de R$ 10.000,00');
    assert.equal(preview.describe(30000, 3), 'Parcelado em 3x de R$ 100,00');
});

test('does not produce a preview for missing or invalid values', () => {
    assert.equal(preview.describe(0, 3), '');
    assert.equal(preview.describe(2000000, 0), '');
});
