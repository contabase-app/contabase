const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// Shared adaptive scheme: one luminance implementation for the Dashboard card
// and the Faturas tile. Values are the same thresholds used before extraction.
function load() {
    const listeners = [];
    const nodes = [];
    function node(color, face = true) {
        const attrs = new Map();
        const classes = new Set(face ? ['cb-card-face'] : []);
        const el = {
            getAttribute: (key) => attrs.get(key) ?? null,
            setAttribute: (key, value) => attrs.set(key, value),
            classList: { contains: (c) => classes.has(c) },
            dataset: {},
        };
        if (color) attrs.set('data-cb-color', color);
        nodes.push(el);
        return el;
    }
    const document = {
        readyState: 'complete',
        querySelectorAll: (sel) => (sel === '[data-cb-color]' ? nodes : []),
        addEventListener: (name, fn) => listeners.push([name, fn]),
        body: { addEventListener: (name, fn) => listeners.push(['body:' + name, fn]) },
    };
    const window = {};
    vm.runInNewContext(fs.readFileSync(require.resolve('./credit-cards.js'), 'utf8'), { document, window });
    return { window, document, listeners, node, nodes };
}

test('luminous institution colors switch to the light scheme', () => {
    const { window } = load();
    assert.equal(window.creditCardSchemeForColor('#ffffff'), 'light');
    assert.equal(window.creditCardSchemeForColor('#FFD400'), 'light');
    assert.equal(window.creditCardSchemeForColor('#e5e7eb'), 'light');
});

test('dark institution colors keep the dark scheme', () => {
    const { window } = load();
    assert.equal(window.creditCardSchemeForColor('#000000'), 'dark');
    assert.equal(window.creditCardSchemeForColor('#8b5cf6'), 'dark');
    assert.equal(window.creditCardSchemeForColor('#0f7a52'), 'dark');
    assert.equal(window.creditCardSchemeForColor('#123456'), 'dark');
});

test('missing or invalid colors fall back to the safe dark scheme', () => {
    const { window } = load();
    assert.equal(window.creditCardSchemeForColor(''), 'dark');
    assert.equal(window.creditCardSchemeForColor(null), 'dark');
    assert.equal(window.creditCardSchemeForColor('invalid'), 'dark');
    assert.equal(window.creditCardSchemeForColor('#12345'), 'dark');
});

test('only card faces receive the scheme attribute', () => {
    const { window, document, node } = load();
    const card = node('#ffffff');
    const other = node('#ffffff', false);
    window.applyCardSchemes(document);
    assert.equal(card.getAttribute('data-card-scheme'), 'light');
    assert.equal(other.getAttribute('data-card-scheme'), null);
});

test('scheme is reapplied after HTMX swaps', () => {
    const { window, document, listeners, node } = load();
    const card = node('#000000');
    const afterSwap = listeners.filter(([name]) => name === 'body:htmx:afterSwap');
    assert.equal(afterSwap.length, 1);
    afterSwap[0][1]();
    assert.equal(card.getAttribute('data-card-scheme'), 'dark');
});
