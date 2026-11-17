const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'bulk-select.js'), 'utf8');

// A small DOM/event harness keeps capture ordering: HTMX sees submit on the
// form before bubble listeners on the container, but after the capture guard.
function world(rows = [{ id: 'one' }]) {
    let document;
    class Element {
        constructor(tag = 'div', attrs = {}) {
            this.tagName = tag.toUpperCase();
            this.attrs = { ...attrs };
            this.dataset = {};
            for (const [k, v] of Object.entries(attrs)) {
                if (k.startsWith('data-')) this.dataset[k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = v;
            }
            this.children = [];
            this.listeners = {};
            this.value = attrs.value || '';
            this.checked = false;
            this.disabled = false;
            this.computedStyle = {};
            this.textContent = '';
            const classes = new Set((attrs.class || '').split(' '));
            this.classList = {
                contains: c => classes.has(c),
                add: c => classes.add(c),
                remove: c => classes.delete(c),
                toggle(c, force) {
                    const on = force === undefined ? !classes.has(c) : force;
                    if (on) classes.add(c); else classes.delete(c);
                },
            };
        }
        setAttribute(k, v) { this.attrs[k] = String(v); }
        getAttribute(k) { return this.attrs[k] ?? null; }
        hasAttribute(k) { return k in this.attrs; }
        removeAttribute(k) { delete this.attrs[k]; }
        get type() { return this.attrs.type || (this.tagName === 'INPUT' ? 'text' : ''); }
        set type(value) { this.attrs.type = value; }
        get name() { return this.attrs.name || ''; }
        set name(value) { this.attrs.name = value; }
        get form() { return this.closest('form'); }
        get tabIndex() { return this.hasAttribute('tabindex') ? Number(this.attrs.tabindex) : (['INPUT', 'BUTTON'].includes(this.tagName) ? 0 : -1); }
        getClientRects() {
            for (let el = this; el; el = el.parent) {
                if (el.classList.contains('hidden') || el.hasAttribute('hidden') || el.computedStyle.display === 'none') return [];
            }
            return this.type === 'hidden' ? [] : [{}];
        }
        appendChild(e) { e.parent = this; this.children.push(e); return e; }
        remove() { this.parent.children = this.parent.children.filter(e => e !== this); }
        matches(selector) {
            return selector.split(',').some(part => {
                part = part.trim();
                if (part === '.hidden') return this.classList.contains('hidden');
                if (part === ':disabled') {
                    for (let el = this; el; el = el.parent) {
                        if (el.disabled && (el === this || el.tagName === 'FIELDSET')) return true;
                    }
                    return false;
                }
                if (/^[a-z]+$/.test(part)) return this.tagName.toLowerCase() === part;
                const attrs = [...part.matchAll(/\[([^\]=]+)(?:="([^"]*)")?\]/g)];
                return attrs.length > 0 && attrs.every(([, k, v]) => this.hasAttribute(k) && (v === undefined || this.getAttribute(k) === v));
            });
        }
        closest(s) { return this.matches(s) ? this : this.parent?.closest(s) || null; }
        querySelectorAll(s) { return this.children.flatMap(e => [...(e.matches(s) ? [e] : []), ...e.querySelectorAll(s)]); }
        querySelector(s) { return this.querySelectorAll(s)[0] || null; }
        focus() { document.activeElement = this; }
        addEventListener(type, fn, capture = false) {
            (this.listeners[type] ||= []).push({ fn, capture: capture === true });
        }
        dispatchEvent(event) {
            event.target ||= this;
            event.preventDefault ||= function () { this.defaultPrevented = true; };
            event.stopPropagation ||= function () { this.stopped = true; };
            event.stopImmediatePropagation ||= function () { this.immediate = this.stopped = true; };
            const ancestors = [];
            for (let e = this; e; e = e.parent) ancestors.push(e);
            const call = (e, capture) => {
                for (const l of e.listeners[event.type] || []) {
                    if (l.capture === capture) l.fn(event);
                    if (event.immediate) break;
                }
            };
            for (const e of [...ancestors].reverse()) { call(e, true); if (event.stopped) return !event.defaultPrevented; }
            for (const e of ancestors) { call(e, false); if (event.stopped || event.bubbles === false) break; }
            return !event.defaultPrevented;
        }
        requestSubmit() { this.dispatchEvent({ type: 'submit' }); }
    }
    const body = new Element('body');
    const container = body.appendChild(new Element('div', { 'data-bulk-select': '' }));
    const add = (parent, tag, attrs) => parent.appendChild(new Element(tag, attrs));
    const toggle = add(container, 'button', { 'data-bulk-toggle-mode': '' });
    add(container, 'input', { 'data-bulk-toggle-all': '' });
    add(container, 'span', { 'data-bulk-count': '' });
    add(container, 'span', { 'data-bulk-count-num': '' });
    const boxes = rows.map(row => {
        const el = add(container, 'article', { 'data-tx-id': row.id, 'data-is-series': row.series ? 'true' : 'false' });
        return add(el, 'input', { 'data-tx-checkbox': '', 'data-ignore-select': '', value: row.id });
    });
    const bar = add(container, 'div', { 'data-bulk-bar': '' });
    const form = add(bar, 'form', { 'data-bulk-delete-form': '', 'data-hx-after': 'resetBulkSelectionDel' });
    add(form, 'input', { 'data-bulk-form-ids': '', 'data-action': 'delete', name: 'ids[]' });
    const scope = add(form, 'input', { 'data-bulk-delete-scope': '', name: 'bulk_scope', value: 'selected_only' });
    const open = add(form, 'button', { 'data-bulk-delete-btn': '' });
    const modal = add(container, 'div', { 'data-bulk-modal': '', class: 'hidden' });
    const dialog = add(modal, 'div', { role: 'dialog', tabindex: '-1' });
    const title = add(dialog, 'h3', { 'data-bulk-delete-title': '' });
    add(dialog, 'p', { 'data-bulk-delete-description': '' });
    const count = add(dialog, 'strong', { 'data-bulk-modal-count': '' });
    const error = add(dialog, 'p', { 'data-bulk-delete-error': '', class: 'hidden' });
    const scopes = add(dialog, 'fieldset', { 'data-bulk-delete-scopes': '' });
    const radios = ['selected_only', 'with_future'].map(value => add(scopes, 'input', { type: 'radio', name: 'bulk_delete_scope', 'data-bulk-delete-choice': '', value }));
    const cancel = add(dialog, 'button', { 'data-action': 'cancel-delete' });
    const confirm = add(dialog, 'button', { 'data-action': 'confirm-delete' });
    document = {
        readyState: 'complete', body, activeElement: body,
        querySelector: s => body.querySelector(s),
        querySelectorAll: s => body.querySelectorAll(s),
        createElement: tag => new Element(tag),
        addEventListener() {},
    };
    const requests = [];
    // Registered before the application, as the HTMX form listener can be.
    form.addEventListener('submit', e => {
        if (!e.defaultPrevented) requests.push({
            ids: form.querySelectorAll('[data-bulk-id-input]').map(e => e.value),
            scope: scope.value,
        });
    });
    const context = vm.createContext({ document, window: {
        matchMedia: () => ({ matches: false }),
        getComputedStyle(el) {
            for (let ancestor = el; ancestor; ancestor = ancestor.parent) {
                if (ancestor.computedStyle.visibility) return { visibility: ancestor.computedStyle.visibility };
            }
            return { visibility: 'visible' };
        },
    }, navigator: {}, setTimeout, clearTimeout,
        CustomEvent: function (type, options) { return { type, ...options }; } });
    vm.runInContext(source, context);
    vm.runInContext(fs.readFileSync(path.join(__dirname, 'hx-events.js'), 'utf8'), context);
    const click = el => {
        if (el.type === 'radio' && !el.disabled) radios.forEach(radio => { radio.checked = radio === el; });
        el.dispatchEvent({ type: 'click' });
    };
    // Simulate only the radio arrow default action. Tab's native navigation is
    // checked in a browser; edge tests below require the guard to prevent it.
    const arrow = key => {
        const event = { type: 'keydown', key };
        document.activeElement.dispatchEvent(event);
        if (!event.defaultPrevented) {
            const current = radios.indexOf(document.activeElement);
            const next = radios[(current + (key === 'ArrowRight' || key === 'ArrowDown' ? 1 : radios.length - 1)) % radios.length];
            next.focus(); click(next);
        }
        return event;
    };
    const choose = indices => { click(toggle); for (const i of indices) { boxes[i].checked = true; click(boxes[i]); } };
    const finish = successful => form.dispatchEvent({ type: 'htmx:afterRequest', detail: { elt: form, successful } });
    return { document, container, form, open, modal, dialog, confirm, cancel, boxes, title, count, radios, scopes, scope, error, requests, click, choose, finish, arrow };
}

for (const [name, rows] of [
    ['one standalone', [{ id: 'a' }]],
    ['several standalones', [{ id: 'a' }, { id: 'b' }]],
    ['mixed selection', [{ id: 'a' }, { id: 'rec-1', series: true }]],
    ['recurring occurrences', [{ id: 'rec-1', series: true }, { id: 'rec-2', series: true }]],
]) {
    test(`${name}: opening never submits; explicit confirmation sends exactly the displayed IDs`, () => {
        const ui = world(rows);
        ui.choose(rows.map((_, i) => i));
        ui.click(ui.open);
        assert.equal(ui.requests.length, 0);
        assert.equal(ui.modal.classList.contains('hidden'), false);
        assert.equal(Number(ui.count.textContent), rows.length);
        assert.equal(ui.document.activeElement, ui.cancel);
        assert.equal(ui.scopes.classList.contains('hidden'), !rows.some(r => r.series));
        ui.click(ui.confirm);
        assert.deepEqual(ui.requests, [{ ids: rows.map(r => r.id), scope: 'selected_only' }]);
    });
}

for (const how of ['cancel', 'Escape', 'backdrop']) {
    test(`${how} closes without a request and restores focus`, () => {
        const ui = world();
        ui.choose([0]); ui.click(ui.open);
        if (how === 'Escape') ui.cancel.dispatchEvent({ type: 'keydown', key: 'Escape' });
        else ui.click(how === 'cancel' ? ui.cancel : ui.modal);
        assert.equal(ui.requests.length, 0);
        assert.equal(ui.modal.classList.contains('hidden'), true);
        assert.equal(ui.document.activeElement, ui.open);
        ui.click(ui.confirm);
        assert.equal(ui.requests.length, 0);
    });
}

test('implicit form submissions are stopped before the HTMX listener', () => {
    const ui = world(); ui.choose([0]);
    ui.form.requestSubmit();
    ui.click(ui.open);
    ui.form.requestSubmit();
    ui.radios[0].dispatchEvent({ type: 'keydown', key: 'Enter' });
    assert.equal(ui.requests.length, 0);
});

test('confirmation is consumed once, including double click and concurrent submit', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open);
    ui.click(ui.confirm); ui.click(ui.confirm); ui.form.requestSubmit();
    assert.equal(ui.requests.length, 1);
    assert.equal(ui.confirm.disabled, true);
});

test('with_future is preserved for a series, and is reset on reopening', () => {
    const ui = world([{ id: 'rec', series: true }]); ui.choose([0]); ui.click(ui.open);
    ui.click(ui.radios[1]);
    ui.click(ui.cancel); ui.click(ui.open);
    assert.equal(ui.radios[0].checked, true);
    ui.click(ui.radios[1]); ui.click(ui.confirm);
    assert.deepEqual(ui.requests, [{ ids: ['rec'], scope: 'with_future' }]);
});

test('standalone selection cannot inherit a previous series scope', () => {
    const ui = world([{ id: 'rec', series: true }, { id: 'one' }]);
    ui.choose([0]); ui.click(ui.open); ui.click(ui.radios[1]); ui.click(ui.cancel);
    ui.click(ui.boxes[0]); ui.click(ui.boxes[1]); ui.click(ui.open);
    ui.click(ui.radios[1]); ui.click(ui.confirm);
    assert.deepEqual(ui.requests, [{ ids: ['one'], scope: 'selected_only' }]);
});

test('changed selection invalidates confirmation instead of submitting unseen IDs', () => {
    const ui = world([{ id: 'a' }, { id: 'b' }]); ui.choose([0]); ui.click(ui.open);
    ui.click(ui.boxes[1]);
    assert.equal(Number(ui.count.textContent), 1);
    ui.click(ui.confirm);
    assert.equal(ui.requests.length, 0);
    assert.equal(ui.modal.classList.contains('hidden'), true);
});

test('a removed checkbox invalidates the selection being confirmed', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open);
    ui.boxes[0].remove(); ui.click(ui.confirm);
    assert.equal(ui.requests.length, 0);
});

test('server/network failure keeps selection, announces uncertainty and prevents stale retries', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open); ui.click(ui.confirm);
    ui.finish(false);
    assert.equal(ui.modal.classList.contains('hidden'), false);
    assert.equal(ui.error.classList.contains('hidden'), false);
    assert.match(ui.error.textContent, /Recarregue/);
    assert.equal(ui.confirm.disabled, true);
    assert.equal(ui.boxes[0].checked, true);
    ui.click(ui.confirm); ui.click(ui.cancel); ui.click(ui.open); ui.click(ui.confirm);
    assert.equal(ui.requests.length, 1);
});

test('only a successful response closes the modal and resets selection', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open); ui.click(ui.confirm); ui.finish(true);
    assert.equal(ui.modal.classList.contains('hidden'), true);
    assert.equal(ui.boxes[0].checked, false);
});

test('reset from other bulk actions does not move focus to the delete button', () => {
    const ui = world(); ui.choose([0]);
    ui.boxes[0].focus();
    ui.container.dispatchEvent({ type: 'contabase:bulk-reset', bubbles: false });
    assert.equal(ui.document.activeElement, ui.boxes[0]);
    assert.equal(ui.boxes[0].checked, false);
});

test('Tab and Shift+Tab stay within the confirmation controls', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open);
    ui.cancel.dispatchEvent({ type: 'keydown', key: 'Tab', shiftKey: true });
    assert.equal(ui.document.activeElement, ui.confirm);
    ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.cancel);
});

for (const selected of [0, 1]) {
    test(`recurring radio ${selected + 1}: both Tab edges stay inside without submitting`, () => {
        const ui = world([{ id: 'rec', series: true }]);
        ui.choose([0]); ui.click(ui.open); ui.click(ui.radios[selected]);
        ui.radios[selected].focus();
        const backward = { type: 'keydown', key: 'Tab', shiftKey: true };
        ui.radios[selected].dispatchEvent(backward);
        assert.equal(backward.defaultPrevented, true);
        assert.equal(ui.document.activeElement, ui.confirm);
        const forward = { type: 'keydown', key: 'Tab' };
        ui.confirm.dispatchEvent(forward);
        assert.equal(forward.defaultPrevented, true);
        assert.equal(ui.document.activeElement, ui.radios[selected]);
        const interior = { type: 'keydown', key: 'Tab' };
        ui.radios[selected].dispatchEvent(interior);
        assert.notEqual(interior.defaultPrevented, true);
        assert.equal(ui.requests.length, 0);
        ui.click(ui.confirm); ui.click(ui.confirm);
        assert.deepEqual(ui.requests, [{ ids: ['rec'], scope: selected ? 'with_future' : 'selected_only' }]);
    });
}

test('radio arrows retain native selection and the next Tab edge follows that selection', () => {
    const ui = world([{ id: 'rec', series: true }]);
    ui.choose([0]); ui.click(ui.open); ui.radios[0].focus();
    for (const [key, selected] of [['ArrowRight', 1], ['ArrowLeft', 0], ['ArrowDown', 1], ['ArrowUp', 0]]) {
        const event = ui.arrow(key);
        assert.notEqual(event.defaultPrevented, true);
        assert.equal(ui.document.activeElement, ui.radios[selected]);
        assert.equal(ui.radios[selected].checked, true);
        assert.equal(ui.scope.value, selected ? 'with_future' : 'selected_only');
    }
    ui.arrow('ArrowRight');
    const backward = { type: 'keydown', key: 'Tab', shiftKey: true };
    ui.radios[1].dispatchEvent(backward);
    assert.equal(backward.defaultPrevented, true);
    assert.equal(ui.document.activeElement, ui.confirm);
    assert.equal(ui.requests.length, 0);
});

for (const [name, hide, restore] of [
    ['hidden class on fieldset', ui => ui.scopes.classList.add('hidden'), ui => ui.scopes.classList.remove('hidden')],
    ['hidden attribute on fieldset', ui => ui.scopes.setAttribute('hidden', ''), ui => ui.scopes.removeAttribute('hidden')],
    ['display:none ancestor', ui => { ui.scopes.computedStyle.display = 'none'; }, ui => { ui.scopes.computedStyle.display = ''; }],
    ['visibility:hidden ancestor', ui => { ui.scopes.computedStyle.visibility = 'hidden'; }, ui => { ui.scopes.computedStyle.visibility = ''; }],
    ['disabled fieldset', ui => { ui.scopes.disabled = true; }, ui => { ui.scopes.disabled = false; }],
    ['inert ancestor', ui => ui.scopes.setAttribute('inert', ''), ui => ui.scopes.removeAttribute('inert')],
    ['negative tabindex', ui => ui.radios.forEach(r => r.setAttribute('tabindex', '-1')), ui => ui.radios.forEach(r => r.removeAttribute('tabindex'))],
]) {
    test(`${name}: dynamic exclusions and restoration change the Tab edges`, () => {
        const ui = world([{ id: 'rec', series: true }]);
        ui.choose([0]); ui.click(ui.open); ui.click(ui.radios[1]);
        hide(ui); ui.cancel.focus();
        const backward = { type: 'keydown', key: 'Tab', shiftKey: true };
        ui.cancel.dispatchEvent(backward);
        assert.equal(backward.defaultPrevented, true);
        assert.equal(ui.document.activeElement, ui.confirm);
        ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
        assert.equal(ui.document.activeElement, ui.cancel);
        restore(ui); ui.confirm.focus();
        ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
        assert.equal(ui.document.activeElement, ui.radios[1]);
        assert.equal(ui.requests.length, 0);
    });
}

test('no checked radio uses the first eligible radio; hidden and disabled members are skipped', () => {
    const ui = world([{ id: 'rec', series: true }]);
    ui.choose([0]); ui.click(ui.open);
    ui.radios.forEach(radio => { radio.checked = false; });
    ui.confirm.focus(); ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.radios[0]);
    ui.radios[0].setAttribute('hidden', '');
    ui.confirm.focus(); ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.radios[1]);
    ui.radios[0].removeAttribute('hidden'); ui.radios[0].disabled = true;
    ui.confirm.focus(); ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.radios[1]);
    assert.equal(ui.requests.length, 0);
});

test('positive tabindex order and dynamically unavailable focused controls stay contained', () => {
    const ui = world([{ id: 'rec', series: true }]);
    ui.choose([0]); ui.click(ui.open); ui.click(ui.radios[1]);
    ui.confirm.setAttribute('tabindex', '1'); ui.cancel.setAttribute('tabindex', '2');
    ui.confirm.focus(); ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab', shiftKey: true });
    assert.equal(ui.document.activeElement, ui.radios[1]);
    ui.radios[1].dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.confirm);
    ui.confirm.disabled = true;
    ui.confirm.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.cancel);
    assert.equal(ui.requests.length, 0);
});

test('with no available controls Tab stays on the dialog until controls return', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open);
    ui.cancel.disabled = true; ui.confirm.disabled = true;
    const event = { type: 'keydown', key: 'Tab' };
    ui.cancel.dispatchEvent(event);
    assert.equal(event.defaultPrevented, true);
    assert.equal(ui.document.activeElement, ui.dialog);
    ui.cancel.disabled = false;
    ui.dialog.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.cancel);
    assert.equal(ui.requests.length, 0);
});

test('confirming keeps focus in the dialog while the confirmation button is disabled', () => {
    const ui = world(); ui.choose([0]); ui.click(ui.open);
    ui.confirm.focus(); ui.click(ui.confirm);
    assert.equal(ui.confirm.disabled, true);
    assert.equal(ui.document.activeElement, ui.dialog);
    ui.dialog.dispatchEvent({ type: 'keydown', key: 'Tab' });
    assert.equal(ui.document.activeElement, ui.cancel);
    ui.cancel.dispatchEvent({ type: 'keydown', key: 'Tab', shiftKey: true });
    assert.equal(ui.document.activeElement, ui.cancel);
    ui.click(ui.confirm);
    assert.equal(ui.requests.length, 1);
});

for (const how of ['cancel', 'Escape']) {
    test(`recurring second radio: ${how} restores the trigger without submitting`, () => {
        const ui = world([{ id: 'rec', series: true }]);
        ui.choose([0]); ui.click(ui.open); ui.click(ui.radios[1]); ui.radios[1].focus();
        if (how === 'Escape') ui.radios[1].dispatchEvent({ type: 'keydown', key: 'Escape' });
        else ui.click(ui.cancel);
        assert.equal(ui.modal.classList.contains('hidden'), true);
        assert.equal(ui.document.activeElement, ui.open);
        assert.equal(ui.requests.length, 0);
    });
}

test('modal retains destructive warning, count and explicit non-submit buttons', () => {
    const template = fs.readFileSync(path.join(__dirname, '../../templates/pages/lancamentos.html'), 'utf8');
    assert.match(template, /Esta ação exclui os lançamentos e não pode ser desfeita/);
    assert.match(template, /data-bulk-modal-count/);
    assert.match(template, /type="button"[^>]+data-action="confirm-delete"/);
    assert.match(template, /type="button"[^>]+data-action="cancel-delete"/);
});
