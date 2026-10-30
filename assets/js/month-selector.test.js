const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// The shared month selector is the single owner of competence-rail centering.
// These tests lock the rect-based math that keeps it correct for every page.
function run(opts = {}) {
    const {
        viewportScrollLeft = 0,
        activeLeft = 400,
        viewportLeft = 100,
        clientWidth = 260,
        activeWidth = 50,
        reducedMotion = false,
        readyState = 'complete',
    } = opts;

    const calls = [];
    const viewport = {
        clientWidth,
        scrollLeft: viewportScrollLeft,
        getBoundingClientRect: () => ({ left: viewportLeft }),
        scrollTo: (options) => {
            calls.push(options);
            viewport.scrollLeft = options.left;
        },
    };
    const active = {
        getBoundingClientRect: () => ({ left: activeLeft, width: activeWidth }),
    };
    const selector = {
        querySelector: (sel) => {
            if (sel === '[data-month-rail-viewport]') return viewport;
            if (sel === '[data-month-active="true"]') return active;
            return null;
        },
    };
    const listeners = {};
    const document = {
        readyState,
        querySelectorAll: (sel) => (sel === '[data-month-selector]' ? [selector] : []),
        addEventListener: (name, fn) => { listeners[name] = fn; },
        body: {
            addEventListener: (name, fn) => { listeners['body:' + name] = fn; },
        },
    };
    const window = {
        matchMedia: (query) => ({ matches: reducedMotion && query.includes('prefers-reduced-motion') }),
        requestAnimationFrame: (fn) => fn(),
    };

    vm.runInNewContext(fs.readFileSync(require.resolve('./month-selector.js'), 'utf8'), { document, window });
    if (readyState !== 'loading' && listeners.DOMContentLoaded) listeners.DOMContentLoaded();
    return { calls, viewport, listeners };
}

test('centers the active month relative to the rail viewport, not the page', () => {
    const { calls, viewport } = run();
    assert.equal(calls.length, 1);
    // 0 + (400 - 100) - ((260 - 50) / 2) = 195
    assert.equal(calls[0].left, 195);
    assert.equal(calls[0].behavior, 'smooth');
    assert.equal(viewport.scrollLeft, 195);
});

test('accounts for an already scrolled viewport', () => {
    const { calls } = run({ viewportScrollLeft: 100, activeLeft: 350, viewportLeft: 100 });
    // 100 + 250 - 105 = 245
    assert.equal(calls[0].left, 245);
});

test('never scrolls to a negative position', () => {
    const { calls } = run({ activeLeft: 100, viewportLeft: 100 });
    assert.equal(calls[0].left, 0);
});

test('uses instant scrolling when reduced motion is preferred', () => {
    const { calls } = run({ reducedMotion: true });
    assert.equal(calls[0].behavior, 'auto');
});

test('re-centers after HTMX swaps', () => {
    const { calls, listeners } = run();
    assert.equal(calls.length, 1);
    listeners['body:htmx:afterSwap']();
    assert.equal(calls.length, 2);
});

test('waits for DOMContentLoaded when the document is still loading', () => {
    const { calls, listeners } = run({ readyState: 'loading' });
    assert.equal(calls.length, 0);
    listeners.DOMContentLoaded();
    assert.equal(calls.length, 1);
});
