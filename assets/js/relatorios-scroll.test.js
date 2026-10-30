const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// R8 — comportamento do indicador de rolagem horizontal de Relatórios.
// Harness mínimo, no mesmo estilo dos testes de privacy/metas: um DOM falso
// com regiões roláveis, dicas e listeners de resize/HTMX.
function runtime(regions) {
    const documentListeners = [];
    const bodyListeners = [];
    const windowListeners = [];

    const document = {
        readyState: 'complete',
        querySelectorAll: () => regions,
        addEventListener: (name, fn) => documentListeners.push({ name, fn }),
        body: {
            addEventListener: (name, fn) => bodyListeners.push({ name, fn }),
        },
    };
    const window = {
        addEventListener: (name, fn) => windowListeners.push({ name, fn }),
        setTimeout: (fn) => fn(),
    };

    const context = vm.createContext({ document, window, console });
    let source = fs.readFileSync(require.resolve('./relatorios-scroll.js'), 'utf8');
    source = source.replace(/\}\)\(\);\s*$/, 'window.__scroll = { update, init, updateAll };\n})();');
    vm.runInContext(source, context, { filename: 'relatorios-scroll.js' });

    return {
        api: window.__scroll,
        documentListeners,
        bodyListeners,
        windowListeners,
        fire: (list, name) => {
            const entry = list.find((item) => item.name === name);
            if (entry) entry.fn({});
        },
    };
}

function region({ scrollWidth, clientWidth, scrollLeft = 0, hint = true }) {
    const hintEl = {
        hidden: false,
        hasAttribute: (name) => hint && name === 'data-scroll-hint',
    };
    const el = {
        scrollWidth,
        clientWidth,
        scrollLeft,
        dataset: {},
        previousElementSibling: hintEl,
        scrollListeners: [],
        addEventListener: (name, fn) => {
            if (name === 'scroll') el.scrollListeners.push(fn);
        },
    };
    return { el, hintEl };
}

test('hides the cue when the table fits', () => {
    const { el, hintEl } = region({ scrollWidth: 300, clientWidth: 300 });
    const rt = runtime([el]);

    assert.equal(hintEl.hidden, true);
});

test('shows the cue at the start of a real overflow', () => {
    const { el, hintEl } = region({ scrollWidth: 900, clientWidth: 300 });
    const rt = runtime([el]);

    assert.equal(hintEl.hidden, false);
});

test('hides the cue at the end and shows it again when scrolling back', () => {
    const { el, hintEl } = region({ scrollWidth: 900, clientWidth: 300 });
    const rt = runtime([el]);
    assert.equal(hintEl.hidden, false);

    el.scrollLeft = 600;
    el.scrollListeners[0]();
    assert.equal(hintEl.hidden, true);

    el.scrollLeft = 0;
    el.scrollListeners[0]();
    assert.equal(hintEl.hidden, false);
});

test('recalculates on resize without adding scroll listeners', () => {
    const { el, hintEl } = region({ scrollWidth: 900, clientWidth: 300 });
    const rt = runtime([el]);
    assert.equal(hintEl.hidden, false);

    el.clientWidth = 900;
    rt.fire(rt.windowListeners, 'resize');
    assert.equal(hintEl.hidden, true);
    assert.equal(el.scrollListeners.length, 1);
});

test('reinitializes after HTMX without duplicate listeners', () => {
    const { el, hintEl } = region({ scrollWidth: 900, clientWidth: 300 });
    const rt = runtime([el]);

    rt.fire(rt.bodyListeners, 'htmx:afterSwap');
    rt.fire(rt.bodyListeners, 'htmx:afterSwap');

    assert.equal(el.scrollListeners.length, 1);
    assert.equal(el.dataset.scrollHintReady, 'true');
    assert.equal(hintEl.hidden, false);
});

test('initializes a newly swapped region once', () => {
    const first = region({ scrollWidth: 300, clientWidth: 300 });
    const regions = [first.el];
    const rt = runtime(regions);
    const second = region({ scrollWidth: 900, clientWidth: 300 });
    regions.push(second.el);

    rt.fire(rt.bodyListeners, 'htmx:afterSwap');

    assert.equal(second.el.scrollListeners.length, 1);
    assert.equal(second.hintEl.hidden, false);
    rt.fire(rt.bodyListeners, 'htmx:afterSwap');
    assert.equal(second.el.scrollListeners.length, 1);
});
