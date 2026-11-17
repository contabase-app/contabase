const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'notification-swipe.js'), 'utf8');

function world() {
    const listeners = {};
    let deleteClicks = 0;
    const delBtn = { click() { deleteClicks += 1; } };
    const card = {
        dataset: {},
        addEventListener(type, fn) { listeners[type] = fn; },
        querySelector(selector) { return selector === '[data-swipe-delete]' ? delBtn : null; },
    };
    const motion = {
        supported: true,
        cancel() {},
        hold() {},
        toBase() {},
        exit(element, base, options) { if (options && options.complete) options.complete(); },
    };
    const document = {
        readyState: 'complete',
        querySelectorAll(selector) { return selector === '[data-swipe-notification]' ? [card] : []; },
        body: { addEventListener() {} },
    };
    const context = { console, document, window: { cbRuntimeMotion: motion } };
    vm.createContext(context);
    vm.runInContext(source, context);
    return { listeners, deleteClicks: () => deleteClicks };
}

function touch(x) {
    return { touches: [{ clientX: x }] };
}

function linkClick() {
    return {
        target: { closest: (selector) => (selector === '[data-notification-open]' ? {} : null) },
        preventDefault() { this.prevented = true; },
        stopPropagation() { this.stopped = true; },
    };
}

function controlClick() {
    return {
        target: { closest: () => null },
        preventDefault() { this.prevented = true; },
        stopPropagation() { this.stopped = true; },
    };
}

test('drag below delete threshold suppresses the card navigation click', () => {
    const { listeners, deleteClicks } = world();
    listeners.touchstart(touch(200));
    listeners.touchmove(touch(150));
    listeners.touchend({});

    const click = linkClick();
    listeners.click(click);

    assert.equal(click.prevented, true);
    assert.equal(click.stopped, true);
    assert.equal(deleteClicks(), 0);
});

test('the next genuine tap still opens the card after a cancelled drag', () => {
    const { listeners } = world();
    listeners.touchstart(touch(200));
    listeners.touchmove(touch(150));
    listeners.touchend({});
    listeners.click(linkClick());

    listeners.touchstart(touch(200));
    listeners.touchend({});
    const click = linkClick();
    listeners.click(click);

    assert.equal(click.prevented, undefined);
});

test('a completed swipe deletes and never navigates through the stretched link', () => {
    const { listeners, deleteClicks } = world();
    listeners.touchstart(touch(240));
    listeners.touchmove(touch(60));
    listeners.touchend({});

    assert.equal(deleteClicks(), 1);

    const link = linkClick();
    listeners.click(link);
    assert.equal(link.prevented, true);
});

test('a control click after a swipe keeps its own handler', () => {
    const { listeners } = world();
    listeners.touchstart(touch(200));
    listeners.touchmove(touch(150));
    listeners.touchend({});

    const control = controlClick();
    listeners.click(control);
    assert.equal(control.prevented, undefined);
    assert.equal(control.stopped, undefined);
});
