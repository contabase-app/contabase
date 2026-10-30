const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require.resolve('./faturas-index.js'), 'utf8');

function classList(initial = []) {
    const values = new Set(initial);
    return {
        add: (key) => values.add(key),
        remove: (key) => values.delete(key),
        contains: (key) => values.has(key),
        toggle: (key, force) => {
            const next = force === undefined ? !values.has(key) : Boolean(force);
            if (next) values.add(key);
            else values.delete(key);
            return next;
        },
    };
}

function node(initialClasses = []) {
    const listeners = new Map();
    const attrs = new Map();
    return {
        dataset: {},
        classList: classList(initialClasses),
        setAttribute: (key, value) => attrs.set(key, String(value)),
        getAttribute: (key) => (attrs.has(key) ? attrs.get(key) : null),
        removeAttribute: (key) => attrs.delete(key),
        addEventListener(name, fn) {
            if (!listeners.has(name)) listeners.set(name, []);
            listeners.get(name).push(fn);
        },
        emit(name, event = {}) {
            (listeners.get(name) || []).forEach((fn) => fn(event));
        },
        listenerCount(name) {
            return (listeners.get(name) || []).length;
        },
    };
}

function createWorld({ count, cardWidth = 300, gap = 16, clientWidth = 900, selectedIndex = 0, reducedMotion = false } = {}) {
    const timers = new Map();
    let timerID = 0;
    let sections = [];
    let lastBehavior = null;
    const requests = [];
    const aborts = [];
    const target = Object.assign(node(), { id: 'faturas-index-detail' });
    const monthLink = { href: 'http://localhost/faturas?ano=2026&cartao=xp&mes=10&ordem=asc' };

    const world = {
        timers,
        requests,
        aborts,
        get lastBehavior() { return lastBehavior; },
        get sections() { return sections; },
        flush() {
            const pending = [...timers.values()];
            timers.clear();
            pending.forEach((fn) => fn());
        },
    };

    function buildSection(cardCount, opts = {}) {
        const width = opts.cardWidth ?? cardWidth;
        const spacing = opts.gap ?? gap;
        const viewport = opts.clientWidth ?? clientWidth;
        const activeIndex = opts.selectedIndex ?? selectedIndex;

        const section = Object.assign(node(), {
            id: 'faturas-index-card-rail',
            isConnected: true,
        });
        const rail = Object.assign(node(), {
            scrollLeft: 0,
            clientWidth: viewport,
            scrollWidth: cardCount * width + Math.max(0, cardCount - 1) * spacing,
            getBoundingClientRect: () => ({ left: 0, right: viewport, width: viewport, top: 0, bottom: 0, height: 0 }),
        });
        rail.scrollTo = ({ left, behavior }) => {
            lastBehavior = behavior;
            const max = Math.max(0, rail.scrollWidth - rail.clientWidth);
            rail.scrollLeft = Math.max(0, Math.min(left, max));
            rail.emit('scroll');
        };

        const cards = Array.from({ length: cardCount }, (_, index) => {
            const card = Object.assign(node(index === activeIndex ? ['is-selected'] : []), {
                href: `http://localhost/faturas?ano=2026&cartao=card-${index}&mes=11`,
                dataset: {
                    faturasCardId: `card-${index}`,
                    faturasContentUrl: `/faturas/conteudo?ano=2026&cartao=card-${index}&mes=11`,
                },
                getBoundingClientRect: () => {
                    const left = index * (width + spacing) - rail.scrollLeft;
                    return { left, right: left + width, width, top: 0, bottom: 0, height: 0 };
                },
            });
            if (index === activeIndex) card.setAttribute('aria-current', 'page');
            return card;
        });
        const dots = cards.map((_, index) => node(index === activeIndex ? ['is-active'] : []));
        const previous = Object.assign(node(), { disabled: true });
        const next = Object.assign(node(), { disabled: true });

        section.querySelector = (selector) => {
            if (selector === '[data-faturas-card-rail]') return rail;
            if (selector === '[data-faturas-card-prev]') return previous;
            if (selector === '[data-faturas-card-next]') return next;
            return null;
        };
        section.querySelectorAll = (selector) => {
            if (selector === '[data-faturas-card]') return cards;
            if (selector === '[data-faturas-carousel-dots] span') return dots;
            return [];
        };

        return { section, rail, cards, dots, previous, next };
    }

    const first = buildSection(count);
    sections = [first.section];

    const documentListeners = new Map();
    const bodyListeners = new Map();
    const body = {
        addEventListener(name, fn) {
            if (!bodyListeners.has(name)) bodyListeners.set(name, []);
            bodyListeners.get(name).push(fn);
        },
        emit(name, event = {}) {
            (bodyListeners.get(name) || []).forEach((fn) => fn(event));
        },
    };
    const document = {
        body,
        addEventListener(name, fn) {
            if (!documentListeners.has(name)) documentListeners.set(name, []);
            documentListeners.get(name).push(fn);
        },
        emit(name, event = {}) {
            (documentListeners.get(name) || []).forEach((fn) => fn(event));
        },
        querySelectorAll(selector) {
            if (selector === '#faturas-index-card-rail') return sections;
            if (selector === '[data-month-selector] a') return [monthLink];
            return [];
        },
        getElementById(id) {
            return id === 'faturas-index-detail' ? target : null;
        },
    };

    const windowListeners = new Map();
    const window = {
        location: { origin: 'http://localhost' },
        getComputedStyle: () => ({ scrollPaddingLeft: '0px', scrollPaddingRight: '0px' }),
        matchMedia: () => ({ matches: reducedMotion }),
        setTimeout: (fn) => {
            timers.set(++timerID, fn);
            return timerID;
        },
        clearTimeout: (id) => timers.delete(id),
        addEventListener(name, fn) {
            if (!windowListeners.has(name)) windowListeners.set(name, []);
            windowListeners.get(name).push(fn);
        },
        emit(name, event = {}) {
            (windowListeners.get(name) || []).forEach((fn) => fn(event));
        },
        htmx: {
            trigger: (element, name) => aborts.push([element, name]),
            ajax: (method, url, options) => {
                requests.push({ method, url, options });
                return Promise.resolve();
            },
        },
    };

    vm.runInNewContext(source, { document, window, URL });
    document.emit('DOMContentLoaded');

    return Object.assign(world, {
        document,
        window,
        body,
        target,
        monthLink,
        section: first.section,
        rail: first.rail,
        cards: first.cards,
        dots: first.dots,
        previous: first.previous,
        next: first.next,
        replaceSection(opts = {}) {
            first.section.isConnected = false;
            const nextSection = buildSection(opts.count ?? count, opts);
            sections = [nextSection.section];
            body.emit('htmx:afterSwap', { target: nextSection.section });
            return nextSection;
        },
        emitAfterSwap() {
            body.emit('htmx:afterSwap', { target: sections[0] });
        },
        selectedIndexes() {
            return this.cards.map((card, index) => (card.classList.contains('is-selected') ? index : -1)).filter((index) => index >= 0);
        },
    });
}

test('one card keeps both controls disabled', () => {
    const ui = createWorld({ count: 1, clientWidth: 900 });
    assert.equal(ui.previous.disabled, true);
    assert.equal(ui.next.disabled, true);
    assert.equal(ui.rail.scrollWidth, 300);
    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 0);
    assert.deepEqual(ui.selectedIndexes(), [0]);
    assert.equal(ui.requests.length, 0);
});

test('two cards: next reaches the end and prev returns to the start', () => {
    const ui = createWorld({ count: 2, clientWidth: 400 });
    assert.equal(ui.previous.disabled, true);
    assert.equal(ui.next.disabled, false);

    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 216);
    assert.equal(ui.next.disabled, true);
    assert.equal(ui.previous.disabled, false);

    ui.previous.emit('click');
    assert.equal(ui.rail.scrollLeft, 0);
    assert.equal(ui.previous.disabled, true);
    assert.equal(ui.next.disabled, false);
});

test('three cards advance one snap position at a time without selecting', () => {
    const ui = createWorld({ count: 3, clientWidth: 400 });
    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 316);
    assert.equal(ui.next.disabled, false);
    assert.equal(ui.previous.disabled, false);

    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 532);
    assert.equal(ui.next.disabled, true);
    assert.deepEqual(ui.selectedIndexes(), [0]);
    assert.equal(ui.requests.length, 0);
});

test('eight cards stop exactly at the real rail end', () => {
    const ui = createWorld({ count: 8, clientWidth: 900 });
    for (let i = 0; i < 10; i++) ui.next.emit('click');
    const max = 8 * 300 + 7 * 16 - 900;
    assert.equal(ui.rail.scrollLeft, max);
    assert.equal(ui.next.disabled, true);
    assert.equal(ui.previous.disabled, false);
    assert.deepEqual(ui.selectedIndexes(), [0]);
    assert.equal(ui.requests.length, 0);
});

test('when every card fits both controls stay disabled', () => {
    const ui = createWorld({ count: 3, clientWidth: 1200 });
    assert.equal(ui.previous.disabled, true);
    assert.equal(ui.next.disabled, true);
    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 0);
});

test('arrows never change selection, aria-current, refresh URL or HTMX requests', () => {
    const ui = createWorld({ count: 5, clientWidth: 900, selectedIndex: 1 });
    const hxGet = ui.section.getAttribute('hx-get');
    const monthHref = ui.monthLink.href;
    assert.deepEqual(ui.selectedIndexes(), [1]);
    assert.equal(ui.cards[1].getAttribute('aria-current'), 'page');

    ui.next.emit('click');
    ui.next.emit('click');
    ui.flush();

    assert.deepEqual(ui.selectedIndexes(), [1]);
    assert.equal(ui.cards[1].getAttribute('aria-current'), 'page');
    assert.equal(ui.section.getAttribute('hx-get'), hxGet);
    assert.equal(ui.monthLink.href, monthHref);
    assert.equal(ui.requests.length, 0);
    assert.equal(ui.aborts.length, 0);
});

test('dots follow the visible rail position while selection stays untouched', () => {
    const ui = createWorld({ count: 5, clientWidth: 900, selectedIndex: 0 });
    ui.next.emit('click');
    assert.equal(ui.dots[1].classList.contains('is-active'), true);
    assert.equal(ui.dots[0].classList.contains('is-active'), false);
    assert.deepEqual(ui.selectedIndexes(), [0]);
});

test('resize recalculates the real overflow boundaries', () => {
    const ui = createWorld({ count: 8, clientWidth: 900 });
    assert.equal(ui.next.disabled, false);

    ui.rail.clientWidth = 3000;
    ui.window.emit('resize');
    assert.equal(ui.previous.disabled, true);
    assert.equal(ui.next.disabled, true);

    ui.rail.clientWidth = 900;
    ui.window.emit('resize');
    assert.equal(ui.next.disabled, false);
});

test('HTMX replacement binds the new rail and leaves the old one alone', () => {
    const ui = createWorld({ count: 2, clientWidth: 400 });
    const oldRail = ui.rail;
    const nextSection = ui.replaceSection({ count: 6, clientWidth: 500 });

    assert.equal(nextSection.previous.disabled, true);
    assert.equal(nextSection.next.disabled, false);
    assert.equal(nextSection.next.listenerCount('click'), 1);

    nextSection.next.emit('click');
    assert.equal(nextSection.rail.scrollLeft, 316);
    assert.equal(oldRail.scrollLeft, 0);
    assert.equal(nextSection.cards[0].classList.contains('is-selected'), true);
    assert.equal(ui.requests.length, 0);
});

test('repeated HTMX notifications do not duplicate control listeners', () => {
    const ui = createWorld({ count: 5, clientWidth: 400 });
    ui.emitAfterSwap();
    ui.emitAfterSwap();
    ui.emitAfterSwap();
    assert.equal(ui.next.listenerCount('click'), 1);
    assert.equal(ui.previous.listenerCount('click'), 1);

    ui.next.emit('click');
    assert.equal(ui.rail.scrollLeft, 316);
});

test('reduced motion uses an immediate scroll', () => {
    const ui = createWorld({ count: 3, clientWidth: 400, reducedMotion: true });
    ui.next.emit('click');
    assert.equal(ui.lastBehavior, 'auto');
});

test('clicking an already visible card does not move the rail', () => {
    const ui = createWorld({ count: 4, clientWidth: 900, selectedIndex: 0 });
    ui.cards[1].emit('click', { preventDefault() {} });

    assert.equal(ui.rail.scrollLeft, 0);
    assert.equal(ui.lastBehavior, null);
    assert.deepEqual(ui.selectedIndexes(), [1]);
    assert.equal(ui.requests.length, 1);
    assert.match(ui.requests[0].url, /card-1/);
});

test('clicking a card clipped on the right moves only the needed amount smoothly', () => {
    const ui = createWorld({ count: 4, clientWidth: 400, selectedIndex: 0 });
    ui.cards[1].emit('click', { preventDefault() {} });

    assert.equal(ui.rail.scrollLeft, 216);
    assert.equal(ui.lastBehavior, 'smooth');
    assert.deepEqual(ui.selectedIndexes(), [1]);
    assert.equal(ui.requests.length, 1);
});

test('clicking a card clipped on the left moves only the needed amount smoothly', () => {
    const ui = createWorld({ count: 4, clientWidth: 400, selectedIndex: 0 });
    ui.rail.scrollLeft = 400;
    ui.cards[0].emit('click', { preventDefault() {} });

    assert.equal(ui.rail.scrollLeft, 0);
    assert.equal(ui.lastBehavior, 'smooth');
    assert.deepEqual(ui.selectedIndexes(), [0]);
    assert.equal(ui.requests.length, 1);
});

test('card click respects reduced motion for the minimal reveal', () => {
    const ui = createWorld({ count: 4, clientWidth: 400, selectedIndex: 0, reducedMotion: true });
    ui.cards[1].emit('click', { preventDefault() {} });

    assert.equal(ui.rail.scrollLeft, 216);
    assert.equal(ui.lastBehavior, 'auto');
});

test('initial selected card reveal is minimal and does not jump to the end', () => {
    const ui = createWorld({ count: 5, clientWidth: 400, selectedIndex: 2 });
    assert.equal(ui.rail.scrollLeft, 532);
    assert.equal(ui.lastBehavior, 'auto');
});

test('minimal card reveal suspends mandatory snap until a real gesture', () => {
    const ui = createWorld({ count: 4, clientWidth: 400, selectedIndex: 0 });
    ui.cards[1].emit('click', { preventDefault() {} });
    assert.equal(ui.rail.scrollLeft, 216);
    assert.equal(ui.rail.classList.contains('is-snap-suspended'), true);

    ui.rail.emit('wheel');
    assert.equal(ui.rail.classList.contains('is-snap-suspended'), false);
});

test('arrow navigation restores snap before scrolling', () => {
    const ui = createWorld({ count: 4, clientWidth: 400, selectedIndex: 0 });
    ui.cards[1].emit('click', { preventDefault() {} });
    assert.equal(ui.rail.classList.contains('is-snap-suspended'), true);

    ui.next.emit('click');
    assert.equal(ui.rail.classList.contains('is-snap-suspended'), false);
});

test('initial selected reveal suspends snap so it is not pulled back', () => {
    const ui = createWorld({ count: 5, clientWidth: 400, selectedIndex: 2 });
    assert.equal(ui.rail.classList.contains('is-snap-suspended'), true);
});
