const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Locks the contract between the invoice/dashboard markup and show-more.js:
// the toggle container lives inside [data-show-more-group], the control lives
// inside that container, and expansion reveals every item at/after the initial
// index exactly once — on first paint and after every HTMX swap.
const source = fs.readFileSync(path.join(__dirname, 'show-more.js'), 'utf8');

function el(attrs = {}) {
    const map = new Map(Object.entries(attrs));
    const classes = new Set();
    return {
        classList: {
            add: function () {
                Array.prototype.forEach.call(arguments, function (c) { classes.add(c); });
            },
            remove: function () {
                Array.prototype.forEach.call(arguments, function (c) { classes.delete(c); });
            },
            contains: (c) => classes.has(c),
            toggle: function (c, force) {
                const on = force === undefined ? !classes.has(c) : !!force;
                if (on) classes.add(c); else classes.delete(c);
                return on;
            },
        },
        getAttribute: (k) => (map.has(k) ? map.get(k) : null),
        setAttribute: (k, v) => map.set(k, String(v)),
        hasAttribute: (k) => map.has(k),
        _classes: classes,
        _attrs: map,
    };
}

function buildGroup(options = {}) {
    const initial = options.initial === undefined ? 5 : options.initial;
    const count = options.count === undefined ? 0 : options.count;
    const withToggle = options.withToggle !== false;
    const oneWay = options.oneWay === true;

    const group = el({
        'data-show-more-group': options.name || 'group',
        'data-show-more-initial': String(initial),
    });
    if (oneWay) group.setAttribute('data-show-more-one-way', '');

    const items = [];
    for (let i = 0; i < count; i++) {
        // Every financial row keeps its privacy hook; show-more must not touch it.
        items.push(el({ 'data-show-more-item': String(i), 'data-sensitive': '' }));
    }

    const toggleContainer = el({ 'data-show-more-toggle-container': '' });
    const button = el({ 'data-show-more-toggle': '', 'data-onclick-ev': 'toggleShowMore' });
    const expandLabel = el({ 'data-show-more-label-expand': '' });
    const collapseLabel = el({ 'data-show-more-label-collapse': '' });
    collapseLabel.classList.add('hidden');

    group.querySelectorAll = (sel) => {
        if (sel === '[data-show-more-item]') return items;
        if (sel === '[data-show-more-toggle]') return withToggle ? [button] : [];
        return [];
    };
    group.querySelector = (sel) => {
        if (sel === '[data-show-more-toggle-container]') return withToggle ? toggleContainer : null;
        if (sel === '[data-show-more-label-expand]') return expandLabel;
        if (sel === '[data-show-more-label-collapse]') return collapseLabel;
        return null;
    };
    button.closest = (sel) => (sel === '[data-show-more-toggle]' ? button : sel === '[data-show-more-group]' ? group : null);

    return { group, items, toggleContainer, button, expandLabel, collapseLabel, initial, oneWay };
}

function createDriver(initialGroups = []) {
    const groups = initialGroups.slice();
    const documentListeners = {};
    const bodyListeners = {};
    const fetchCalls = [];

    const document = {
        readyState: 'complete',
        querySelectorAll: (sel) => (sel === '[data-show-more-group]' ? groups.slice() : []),
        addEventListener: (name, fn) => {
            (documentListeners[name] = documentListeners[name] || []).push(fn);
        },
        body: {
            addEventListener: (name, fn) => {
                (bodyListeners[name] = bodyListeners[name] || []).push(fn);
            },
        },
    };
    const window = {};
    const context = {
        document,
        window,
        fetch: function () {
            fetchCalls.push(Array.prototype.slice.call(arguments));
            throw new Error('show-more must not issue network requests');
        },
    };

    vm.runInNewContext(source, context);

    return {
        window,
        groups,
        documentListeners,
        bodyListeners,
        fetchCalls,
        replaceGroups(next) {
            groups.splice(0, groups.length, ...next);
        },
        settle() {
            (bodyListeners['htmx:afterSettle'] || []).forEach((fn) => fn({}));
        },
        click(button) {
            window.toggleShowMore({ target: button });
        },
    };
}

function hiddenFlags(items) {
    return items.map((item) => item.classList.contains('hidden'));
}

test('initializes by hiding items after the initial index', () => {
    const g = buildGroup({ initial: 2, count: 4, oneWay: true });
    const driver = createDriver([g.group]);

    assert.deepEqual(hiddenFlags(g.items), [false, false, true, true]);
    assert.equal(g.toggleContainer.classList.contains('hidden'), false);
    assert.equal(g.button.getAttribute('aria-expanded'), 'false');
    assert.ok(g.group.hasAttribute('data-show-more-initialized'));
    assert.equal(driver.fetchCalls.length, 0);
});

test('one-way expansion reveals all hidden items, hides the footer and marks aria-expanded', () => {
    const g = buildGroup({ initial: 2, count: 4, oneWay: true });
    const driver = createDriver([g.group]);

    driver.click(g.button);

    assert.deepEqual(hiddenFlags(g.items), [false, false, false, false]);
    assert.equal(g.toggleContainer.classList.contains('hidden'), true);
    assert.equal(g.button.getAttribute('aria-expanded'), 'true');
    assert.equal(driver.fetchCalls.length, 0);
});

test('toggle mode expands and collapses labels and aria-expanded state', () => {
    const g = buildGroup({ initial: 6, count: 7 });
    const driver = createDriver([g.group]);
    const last = g.items[6];

    assert.equal(last.classList.contains('hidden'), true);
    assert.equal(g.expandLabel.classList.contains('hidden'), false);
    assert.equal(g.collapseLabel.classList.contains('hidden'), true);
    assert.equal(g.button.getAttribute('aria-expanded'), 'false');

    driver.click(g.button);

    assert.equal(last.classList.contains('hidden'), false);
    assert.equal(g.expandLabel.classList.contains('hidden'), true);
    assert.equal(g.collapseLabel.classList.contains('hidden'), false);
    assert.equal(g.button.getAttribute('aria-expanded'), 'true');
    assert.ok(g.group.classList.contains('show-more-expanded'));

    driver.click(g.button);

    assert.equal(last.classList.contains('hidden'), true);
    assert.equal(g.expandLabel.classList.contains('hidden'), false);
    assert.equal(g.collapseLabel.classList.contains('hidden'), true);
    assert.equal(g.button.getAttribute('aria-expanded'), 'false');
    assert.equal(g.group.classList.contains('show-more-expanded'), false);
});

test('handles empty, one-item, below-limit, exactly-at-limit and above-limit groups', () => {
    const empty = buildGroup({ name: 'empty', initial: 5, count: 0 });
    const one = buildGroup({ name: 'one', initial: 5, count: 1 });
    const below = buildGroup({ name: 'below', initial: 5, count: 4 });
    const exact = buildGroup({ name: 'exact', initial: 5, count: 5 });
    const above = buildGroup({ name: 'above', initial: 5, count: 6 });

    createDriver([empty.group, one.group, below.group, exact.group, above.group]);

    [empty, one, below, exact].forEach((g) => {
        assert.equal(g.toggleContainer.classList.contains('hidden'), true);
        assert.equal(g.items.every((item) => item.classList.contains('hidden') === false), true);
    });

    assert.equal(above.items[4].classList.contains('hidden'), false);
    assert.equal(above.items[5].classList.contains('hidden'), true);
    assert.equal(above.toggleContainer.classList.contains('hidden'), false);
});

test('multiple groups keep independent limits and expansion state', () => {
    const invoices = buildGroup({ name: 'faturas', initial: 2, count: 5, oneWay: true });
    const cards = buildGroup({ name: 'cards', initial: 3, count: 4 });
    const driver = createDriver([invoices.group, cards.group]);

    assert.deepEqual(hiddenFlags(invoices.items), [false, false, true, true, true]);
    assert.deepEqual(hiddenFlags(cards.items), [false, false, false, true]);

    driver.click(cards.button);

    assert.deepEqual(hiddenFlags(invoices.items), [false, false, true, true, true]);
    assert.deepEqual(hiddenFlags(cards.items), [false, false, false, false]);
    assert.equal(cards.button.getAttribute('aria-expanded'), 'true');
    assert.equal(invoices.button.getAttribute('aria-expanded'), 'false');
});

test('htmx settle re-initialization is idempotent and does not hide expanded rows again', () => {
    const g = buildGroup({ initial: 2, count: 4, oneWay: true });
    const driver = createDriver([g.group]);

    assert.equal(driver.bodyListeners['htmx:afterSettle'].length, 1);

    driver.settle();
    driver.settle();
    assert.equal(driver.bodyListeners['htmx:afterSettle'].length, 1);
    assert.deepEqual(hiddenFlags(g.items), [false, false, true, true]);

    driver.click(g.button);
    driver.settle();
    driver.settle();
    driver.settle();

    assert.deepEqual(hiddenFlags(g.items), [false, false, false, false]);
    assert.equal(g.toggleContainer.classList.contains('hidden'), true);
    assert.equal(driver.fetchCalls.length, 0);
});

test('repeated swaps initialize only the replacement groups once', () => {
    const first = buildGroup({ name: 'swap-0', initial: 2, count: 4, oneWay: true });
    const driver = createDriver([first.group]);

    for (let round = 1; round <= 5; round++) {
        const next = buildGroup({ name: 'swap-' + round, initial: 2, count: 4, oneWay: true });
        driver.replaceGroups([next.group]);
        driver.settle();

        assert.ok(next.group.hasAttribute('data-show-more-initialized'));
        assert.deepEqual(hiddenFlags(next.items), [false, false, true, true]);
        assert.equal(driver.bodyListeners['htmx:afterSettle'].length, 1);

        driver.click(next.button);
        driver.settle();

        assert.deepEqual(hiddenFlags(next.items), [false, false, false, false]);
    }

    assert.equal(driver.fetchCalls.length, 0);
});

test('privacy hooks survive expansion unchanged', () => {
    const g = buildGroup({ initial: 1, count: 3, oneWay: true });
    const driver = createDriver([g.group]);

    driver.click(g.button);

    assert.deepEqual(hiddenFlags(g.items), [false, false, false]);
    g.items.forEach((item) => {
        assert.equal(item.hasAttribute('data-sensitive'), true);
        assert.equal(item.classList.contains('hidden'), false);
    });
    assert.equal(driver.fetchCalls.length, 0);
});

test('ignores clicks outside a toggle or group without throwing', () => {
    const g = buildGroup({ initial: 1, count: 2, oneWay: true });
    const driver = createDriver([g.group]);
    const outside = el({});
    outside.closest = () => null;

    assert.doesNotThrow(() => driver.click(outside));
    assert.deepEqual(hiddenFlags(g.items), [false, true]);
    assert.equal(driver.fetchCalls.length, 0);
});
