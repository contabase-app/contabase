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

test('card copy uses the adaptive foreground tokens with AA contrast', () => {
    const { window } = load();
    const path = require('node:path').resolve(__dirname, '../css/credit-cards.css');
    const css = fs.readFileSync(path, 'utf8');
    const cssRules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
    const expected = {
        '.cb-v2-credit-card-meta strong': 'var(--card-fg-soft)',
        '.cb-v2-credit-card-limit strong': 'var(--card-fg-soft)',
        '.cb-v2-credit-card-pending': 'var(--card-fg-soft)',
        '.cb-v2-credit-card-history': 'var(--card-fg-soft)',
        '.cb-v2-credit-card-history small': 'var(--card-fg-muted)',
        '.cb-v2-credit-card-amount small': 'var(--card-fg-muted)',
    };
    for (const [selector, token] of Object.entries(expected)) {
        const colorRules = cssRules.filter(([, selectors]) => selectors.replace(/\/\*[\s\S]*?\*\//g, '').split(',').some(item => item.trim() === selector))
            .map(([, , declarations]) => declarations.match(/color:\s*([^;]+)/)?.[1]?.trim())
            .filter(Boolean);
        assert.equal(colorRules.at(-1), token, `${selector} must end with ${token}`);
    }

    function linear(channel) {
        const value = channel / 255;
        return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    }
    function luminance(rgb) {
        return 0.2126 * linear(rgb[0]) + 0.7152 * linear(rgb[1]) + 0.0722 * linear(rgb[2]);
    }
    function blend(foreground, background, alpha) {
        return foreground.map((channel, index) => Math.round(channel * alpha + background[index] * (1 - alpha)));
    }
    function contrast(foreground, background) {
        const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
        return (values[0] + 0.05) / (values[1] + 0.05);
    }

    // Captured card surface for Mercado Pago #00A6FF; light-scheme ink on a
    // bright card covers the opposite end of the custom-color range.
    const darkSurface = [0, 113, 185];
    const whiteSoft = blend([255, 255, 255], darkSurface, 0xeb / 0xff);
    const lightSurface = [250, 250, 250];
    const darkSoft = blend([25, 28, 43], lightSurface, 0xf0 / 0xff);
    assert.ok(contrast(whiteSoft, darkSurface) >= 4.5);
    assert.ok(contrast(darkSoft, lightSurface) >= 4.5);
    assert.equal(window.creditCardSchemeForColor('#00A6FF'), 'dark');
    assert.equal(window.creditCardSchemeForColor('#FFFFFF'), 'light');
    assert.equal(window.creditCardSchemeForColor('#FFFF00'), 'light');
    assert.equal(window.creditCardSchemeForColor('#000000'), 'dark');
});
