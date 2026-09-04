const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// Exercise the shipped browser script with scroll events that can occur after
// layout/scroll-snap as well as deliberate user gestures.
function fixture(selectedIndex, width = 900) {
    function node() {
        const listeners = new Map();
        const classes = new Set();
        const attrs = new Map();
        return {
            dataset: {}, isConnected: true,
            classList: {
                contains: (key) => classes.has(key),
                toggle: (key, value) => value ? classes.add(key) : classes.delete(key),
            },
            setAttribute: (key, value) => attrs.set(key, value),
            getAttribute: (key) => attrs.get(key),
            removeAttribute: (key) => attrs.delete(key),
            addEventListener: (name, fn) => {
                if (!listeners.has(name)) listeners.set(name, []);
                listeners.get(name).push(fn);
            },
            emit: (name, event = {}) => (listeners.get(name) || []).forEach((fn) => fn(event)),
        };
    }
    const rail = Object.assign(node(), { scrollLeft: 0, clientWidth: width, scrollWidth: 1500 });
    rail.getBoundingClientRect = () => ({ left: 0 });
    rail.scrollTo = ({ left }) => { rail.scrollLeft = left; };
    const cards = ['itau', 'xp', 'sem-fatura', 'inter', 'nubank'].map((id, index) => {
        const card = node();
        card.href = `http://localhost/faturas?ano=2026&cartao=${id}&mes=9`;
        card.dataset = { faturasCardId: id, faturasContentUrl: `/faturas/conteudo?ano=2026&cartao=${id}&mes=9` };
        card.classList.toggle('is-selected', index === selectedIndex);
        card.getBoundingClientRect = () => ({ left: index * 300 - rail.scrollLeft, width: 280 });
        return card;
    });
    const dots = cards.map(node);
    const section = node();
    section.querySelector = (selector) => selector === '[data-faturas-card-rail]' ? rail : null;
    section.querySelectorAll = (selector) => selector === '[data-faturas-card]' ? cards : dots;
    const target = Object.assign(node(), { id: 'faturas-index-detail' });
    const monthLink = { href: 'http://localhost/faturas?ano=2026&cartao=xp&mes=10&ordem=asc' };
    const document = Object.assign(node(), {
        body: node(),
        querySelectorAll: (selector) => selector === '#faturas-index-card-rail' ? [section] : [monthLink],
        querySelector: () => null,
        getElementById: () => target,
    });
    const timers = new Map();
    let timerID = 0;
    const requests = [];
    const aborts = [];
    const window = {
        location: { origin: 'http://localhost' },
        setTimeout: (fn) => { timers.set(++timerID, fn); return timerID; },
        clearTimeout: (id) => timers.delete(id),
        htmx: {
            trigger: (element, name) => aborts.push([element, name]),
            ajax: (method, url, options) => { requests.push({ method, url, options }); return Promise.resolve(); },
        },
    };
    vm.runInNewContext(fs.readFileSync(require.resolve('./faturas-index.js'), 'utf8'), { document, window, URL });
    document.emit('DOMContentLoaded');
    const flush = () => { const pending = [...timers.values()]; timers.clear(); pending.forEach((fn) => fn()); };
    const selected = () => cards.filter((card) => card.classList.contains('is-selected')).map((card) => card.dataset.faturasCardId);
    return { rail, cards, dots, requests, aborts, target, monthLink, flush, selected };
}

test('URL selection survives desktop layout scroll with XP in the center', () => {
    const ui = fixture(0);
    ui.rail.emit('scroll');
    ui.flush();
    assert.deepEqual(ui.selected(), ['itau']);
    assert.equal(ui.requests.length, 0);
    assert.equal(ui.cards[0].getAttribute('aria-current'), 'page');
    assert.equal(ui.dots[0].classList.contains('is-active'), true);
});

test('a card without an invoice is selected and revealed on mobile', () => {
    const ui = fixture(2, 324);
    assert.equal(ui.rail.scrollLeft, 600);
    ui.rail.emit('scroll');
    ui.flush();
    assert.deepEqual(ui.selected(), ['sem-fatura']);
    assert.equal(ui.requests.length, 0);
    assert.equal(ui.cards[2].getAttribute('aria-current'), 'page');
    assert.equal(ui.dots[2].classList.contains('is-active'), true);
});

test('explicit click is not overridden by its programmatic scroll', () => {
    const ui = fixture(1);
    ui.cards[0].emit('click', { preventDefault() {} });
    ui.rail.emit('scroll');
    ui.flush();
    assert.deepEqual(ui.selected(), ['itau']);
    assert.equal(ui.requests.length, 1);
    assert.match(ui.requests[0].url, /cartao=itau/);
    assert.equal(ui.requests[0].options.source, ui.target);
});

test('repeated swipe events load the settled card exactly once', () => {
    const ui = fixture(0, 324);
    ui.rail.emit('pointerdown');
    ui.rail.scrollLeft = 300;
    ui.rail.emit('scroll');
    ui.rail.emit('scroll');
    assert.deepEqual(ui.selected(), ['itau']);
    ui.flush();
    assert.deepEqual(ui.selected(), ['xp']);
    assert.equal(ui.requests.length, 1);
    assert.match(ui.requests[0].url, /cartao=xp/);
});

test('month navigation keeps the clicked card and cancels the previous detail request', () => {
    const ui = fixture(0);
    ui.cards[1].emit('click', { preventDefault() {} });
    ui.cards[2].emit('click', { preventDefault() {} });
    const url = new URL(ui.monthLink.href, 'http://localhost');
    assert.equal(url.searchParams.get('cartao'), 'sem-fatura');
    assert.equal(url.searchParams.get('mes'), '10');
    assert.equal(url.searchParams.get('ordem'), 'asc');
    assert.equal(ui.aborts.length, 2);
    assert.equal(ui.aborts[1][0], ui.target);
    assert.equal(ui.aborts[1][1], 'htmx:abort');
    assert.deepEqual(ui.selected(), ['sem-fatura']);
});
