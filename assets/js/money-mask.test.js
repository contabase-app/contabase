const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// The scripts are browser globals with no module wrapper; load the shipped files
// in a sandbox with a minimal document so the real listener is exercised.
function runtime() {
    const listeners = new Map();
    const document = {
        addEventListener: (name, fn) => {
            if (!listeners.has(name)) listeners.set(name, []);
            listeners.get(name).push(fn);
        },
    };
    const window = {};
    const context = vm.createContext({ window, document });
    for (const file of ['./core-utils.js', './money-mask.js']) {
        vm.runInContext(fs.readFileSync(require.resolve(file), 'utf8'), context, { filename: file });
    }

    // The signed path formats through evaluateMoneyInput and the legacy path
    // through maskMoneyRealTime: count both to prove one pass per event.
    let signedCalls = 0;
    let legacyCalls = 0;
    const mask = window.maskMoneyRealTime;
    const evaluate = window.evaluateMoneyInput;
    window.maskMoneyRealTime = (value, options) => {
        legacyCalls += 1;
        return mask(value, options);
    };
    window.evaluateMoneyInput = (value, options) => {
        signedCalls += 1;
        return evaluate(value, options);
    };

    const listenersFor = (name) => listeners.get(name) || [];
    // Mirrors HTMLInputElement: a custom error keeps the control invalid until
    // the message is cleared, which is what blocks the form submit.
    function field(attribute, required = false) {
        const el = {
            value: '',
            required,
            validationMessage: '',
            customError: false,
            closest: () => el,
            getAttribute: (name) => (name === 'data-money-mask' ? attribute : null),
            setCustomValidity: (message) => {
                el.validationMessage = message;
                el.customError = message !== '';
            },
            checkValidity: () => el.validationMessage === '' && (!el.required || el.value !== ''),
        };
        return el;
    }
    // Chrome reports one inputType per editing operation: typing fires insertText
    // with a single character, paste fires insertFromPaste with the whole text.
    function send(el, value, inputType, data) {
        el.value = value;
        listenersFor('input').forEach((fn) => fn({ target: el, inputType, data }));
        return el.value;
    }
    function type(el, value) {
        return send(el, value, 'insertText', value.slice(-1));
    }
    function paste(el, value) {
        return send(el, value, 'insertFromPaste', value);
    }
    function redo(el, value) {
        return send(el, value, 'historyRedo', value);
    }
    function form(el) {
        let posts = 0;
        let adjustments = 0;
        return {
            checkValidity: () => el.checkValidity(),
            submit: () => {
                if (!el.checkValidity()) return false;
                posts += 1;
                adjustments += 1;
                return true;
            },
            counts: () => ({ posts, adjustments }),
        };
    }
    return {
        mask,
        inputIsTyping: window.moneyMaskInputIsTyping,
        field,
        type,
        paste,
        redo,
        send,
        form,
        inputListeners: () => listenersFor('input').length,
        blurListeners: () => listenersFor('blur').length,
        resetCounters: () => {
            signedCalls = 0;
            legacyCalls = 0;
        },
        signedPasses: () => signedCalls,
        legacyPasses: () => legacyCalls,
    };
}

const SIGNED = { allowNegative: true, exact: true };
const normalize = (value) => value.replace(/\u00a0/g, ' ');

test('signed mode formats typed digits as centavos with pt-BR separators', () => {
    const { mask } = runtime();
    const cases = [
        ['0', 'R$ 0,00'],
        ['0,00', 'R$ 0,00'],
        ['0,01', 'R$ 0,01'],
        ['0,10', 'R$ 0,10'],
        ['1', 'R$ 0,01'],
        ['1,00', 'R$ 1,00'],
        ['1,01', 'R$ 1,01'],
        ['1.234,56', 'R$ 1.234,56'],
        ['-1,00', '-R$ 1,00'],
        ['-0,01', '-R$ 0,01'],
        ['-1.234,56', '-R$ 1.234,56'],
    ];
    for (const [input, want] of cases) {
        assert.equal(mask(input, SIGNED), want, `mask(${JSON.stringify(input)})`);
    }
});

test('signed mode stays exact beyond float precision', () => {
    const { mask } = runtime();
    assert.equal(mask('9223372036854775807', SIGNED), 'R$ 92.233.720.368.547.758,07');
    assert.equal(mask('-9223372036854775808', SIGNED), '-R$ 92.233.720.368.547.758,08');
    assert.equal(mask('123456789012345678', SIGNED), 'R$ 1.234.567.890.123.456,78');
    // The legacy path divides by 100 in floating point, so at int64 scale it
    // returns a different amount: the reconciliation field opts out of it.
    assert.notEqual(normalize(mask('9223372036854775807')), 'R$ 92.233.720.368.547.758,07');
});

test('pasted pt-BR money strings keep sign and centavos', () => {
    const rt = runtime();
    for (const input of ['1234,56', '1.234,56', 'R$ 1.234,56', '-R$ 1.234,56', 'R$ -1.234,56', ' -1.234,56 ']) {
        const el = rt.field('signed');
        rt.paste(el, input);
        assert.equal(el.checkValidity(), true, `${JSON.stringify(input)} must stay valid`);
    }
    assert.equal(rt.paste(rt.field('signed'), '1.234,56'), 'R$ 1.234,56');
    assert.equal(rt.paste(rt.field('signed'), '-R$ 1.234,56'), '-R$ 1.234,56');
    assert.equal(rt.paste(rt.field('signed'), 'R$ -1.234,56'), '-R$ 1.234,56');
    // US grouping is part of the server contract (parseAccountBalance accepts it).
    assert.equal(rt.paste(rt.field('signed'), '1,234.56'), 'R$ 1.234,56');
});

test('grouping and extra fraction digits keep working while typing', () => {
    const { mask } = runtime();
    // Measured in Chrome: every keystroke appends one character to the mask's own
    // output, so these interim readings must stay valid and be reformatted.
    assert.equal(mask('R$ 1.234,567', SIGNED), 'R$ 12.345,67');
    assert.equal(mask('R$ 12.345,678', SIGNED), 'R$ 123.456,78');
    assert.equal(mask('R$ 0,012', SIGNED), 'R$ 0,12');
    assert.equal(mask('R$ 1.2234,56', SIGNED), 'R$ 12.234,56');
    assert.equal(mask('R$ 0,001', SIGNED), 'R$ 0,01');
});

test('keystrokes dial while paste, drop, redo and unknown origins validate', () => {
    const rt = runtime();
    assert.equal(rt.inputIsTyping('insertText', '5'), true);
    assert.equal(rt.inputIsTyping('deleteContentBackward', null), true);
    assert.equal(rt.inputIsTyping('deleteByCut', null), true);
    assert.equal(rt.inputIsTyping('insertText', '150000'), false, 'a batch insertion is not a keystroke');
    assert.equal(rt.inputIsTyping('insertFromPaste', '1.234,56'), false);
    assert.equal(rt.inputIsTyping('insertFromDrop', '1.234,56'), false);
    assert.equal(rt.inputIsTyping('historyRedo', '1.23,45'), false);
    assert.equal(rt.inputIsTyping(undefined, undefined), false);
});

test('SEP01-SEP05: malformed thousands groups are rejected as amounts', () => {
    const rt = runtime();
    const cases = [
        ['1.23,45', 'R$ 123,45'],
        ['1.2.3', 'R$ 1,23'],
        ['1..234', 'R$ 12,34'],
        ['12.34,56', 'R$ 1.234,56'],
        ['123.45,67', 'R$ 12.345,67'],
    ];
    // Every origin that is not a keystroke (paste, drop, redo, autofill) reads the
    // text as a formatted amount and validates its structure.
    for (const [input, transformed] of cases) {
        for (const path of ['paste', 'redo']) {
            const el = rt.field('signed');
            rt[path](el, input);
            assert.equal(el.value, input, `${path} ${JSON.stringify(input)} must keep the text`);
            assert.equal(el.checkValidity(), false, `${path} ${JSON.stringify(input)} must stay invalid`);
            assert.notEqual(el.value, transformed, `${path} ${JSON.stringify(input)} leaked an amount`);
        }
    }
    // A dot is never read as the decimal separator on its own.
    for (const input of ['1.23', '12.34', '1234.56', '1.2345', '1.2345,67', '.123', '123.']) {
        const el = rt.field('signed');
        rt.paste(el, input);
        assert.equal(el.value, input, `${JSON.stringify(input)} must keep the text`);
        assert.equal(el.checkValidity(), false, `${JSON.stringify(input)} must stay invalid`);
    }
});

test('typing dials centavos while a pasted amount keeps its structure', () => {
    // Keystrokes own the digits, so 1234 typed is 1234 centavos and the mask
    // rewrites that into R$ 12,34; the same digits pasted with separators are read
    // as an amount already formatted. Reachability matters: typing re-formats on
    // every keystroke, so a malformed grouping can only arrive through paste.
    const rt = runtime();
    assert.equal(rt.type(rt.field('signed'), '1234'), 'R$ 12,34');
    assert.equal(rt.type(rt.field('signed'), '1.234,56'), 'R$ 1.234,56');
    assert.equal(rt.paste(rt.field('signed'), '1.234,56'), 'R$ 1.234,56');
    assert.equal(rt.paste(rt.field('signed'), '1.234'), 'R$ 12,34');
});

test('SEP06-SEP11: valid grouping is accepted on both paths', () => {
    const rt = runtime();
    const cases = [
        ['1.234,56', 'R$ 1.234,56'],
        ['12.345,67', 'R$ 12.345,67'],
        ['123.456,78', 'R$ 123.456,78'],
        ['1.234.567,89', 'R$ 1.234.567,89'],
        ['1.234', 'R$ 12,34'],
        ['1234,56', 'R$ 1.234,56'],
        ['123456', 'R$ 1.234,56'],
    ];
    for (const [input, want] of cases) {
        for (const path of ['paste', 'type']) {
            const el = rt.field('signed');
            assert.equal(rt[path](el, input), want, `${path} ${JSON.stringify(input)}`);
            assert.equal(el.checkValidity(), true, `${path} ${JSON.stringify(input)} must stay valid`);
        }
    }
});

test('SEP12: correcting a malformed grouping clears the custom validity', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.paste(el, '1.23,45');
    assert.equal(el.value, '1.23,45');
    assert.equal(el.validationMessage === '', false);
    rt.paste(el, '1.234,56');
    assert.equal(el.value, 'R$ 1.234,56');
    assert.equal(el.validationMessage, '');
    assert.equal(el.checkValidity(), true);
});

test('INVALID01-INVALID06: malformed input is preserved and never converted', () => {
    const rt = runtime();
    const cases = [
        ['abc0', 'R$ 0,00'],
        ['1,2,3', 'R$ 1,23'],
        ['1e2', 'R$ 0,12'],
        ['1..000', 'R$ 10,00'],
        ['R$ R$ 10,00', 'R$ 10,00'],
        ['--', '-R$ 0,00'],
        ['1234-56', 'R$ 1.234,56'],
    ];
    for (const [input, transformed] of cases) {
        for (const path of ['paste', 'type']) {
            const el = rt.field('signed');
            rt[path](el, input);
            assert.equal(el.value, input, `${path} ${JSON.stringify(input)} must keep the typed text`);
            assert.equal(el.checkValidity(), false, `${path} ${JSON.stringify(input)} must stay invalid`);
            assert.notEqual(el.value, transformed, `${path} ${JSON.stringify(input)} leaked a converted amount`);
        }
    }
});

test('INVALID07: rejected input leaves the control invalid', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.paste(el, 'abc0');
    assert.equal(el.value, 'abc0');
    assert.equal(el.validationMessage === '', false, 'custom validity must explain the rejection');
    assert.equal(el.checkValidity(), false);
    for (const input of ['-', ',', 'R$', '1,']) {
        const interim = rt.field('signed');
        rt.type(interim, input);
        assert.equal(interim.checkValidity(), false, `${JSON.stringify(input)} must stay invalid`);
        assert.equal(interim.value, input, `${JSON.stringify(input)} must stay visible`);
    }
});

test('INVALID08: correcting the field clears the custom validity', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.paste(el, 'abc0');
    assert.equal(el.checkValidity(), false);
    rt.type(el, '1234,56');
    assert.equal(el.value, 'R$ 1.234,56');
    assert.equal(el.validationMessage, '');
    assert.equal(el.checkValidity(), true);
});

test('INVALID09: an invalid paste never produces a financial value', () => {
    const rt = runtime();
    const converted = { abc0: 'R$ 0,00', '1e2': 'R$ 0,12', 'R$ R$ 10,00': 'R$ 10,00', '1,2,3': 'R$ 1,23' };
    for (const input of Object.keys(converted)) {
        const el = rt.field('signed');
        rt.paste(el, input);
        assert.equal(el.value, input, `${JSON.stringify(input)} must stay verbatim`);
        assert.notEqual(el.value, converted[input]);
        assert.equal(el.checkValidity(), false, `${JSON.stringify(input)} must stay invalid`);
        // Re-running the mask over the rejected text cannot drift into a value.
        rt.paste(el, el.value);
        assert.equal(el.value, input);
        assert.equal(el.checkValidity(), false);
    }
});

test('STATE01: Backspace cannot dial an invalid pasted grouping into money', () => {
    const rt = runtime();
    const el = rt.field('signed', true);
    const form = rt.form(el);
    assert.equal(rt.paste(el, '1.23,45'), '1.23,45');
    assert.equal(el.customError, true);
    assert.equal(rt.send(el, '1.23,4', 'deleteContentBackward'), '1.23,4');
    assert.equal(el.customError, true);
    assert.equal(form.checkValidity(), false);
    assert.equal(form.submit(), false);
    assert.deepEqual(form.counts(), { posts: 0, adjustments: 0 });
});

test('STATE02: a digit cannot dial an invalid pasted grouping into money', () => {
    const rt = runtime();
    const el = rt.field('signed', true);
    const form = rt.form(el);
    rt.paste(el, '1.23,45');
    assert.equal(rt.send(el, '1.23,459', 'insertText', '9'), '1.23,459');
    assert.equal(el.customError, true);
    assert.equal(form.submit(), false);
    assert.deepEqual(form.counts(), { posts: 0, adjustments: 0 });
});

test('STATE03: malformed multiple grouping remains strict after deletion', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.paste(el, '1.2.3');
    assert.equal(rt.send(el, '1.2.', 'deleteContentBackward'), '1.2.');
    assert.equal(el.customError, true);
    assert.equal(rt.send(el, '1.2.4', 'insertText', '4'), '1.2.4');
    assert.equal(el.customError, true);
});

test('STATE04: strict invalid clears only after the current text is structurally valid', () => {
    const rt = runtime();
    const el = rt.field('signed', true);
    const form = rt.form(el);
    rt.paste(el, '1.23,45');
    assert.equal(rt.send(el, '1.23,4', 'deleteContentBackward'), '1.23,4');
    assert.equal(el.customError, true);
    assert.equal(rt.send(el, '1.23,5', 'insertText', '5'), '1.23,5');
    assert.equal(el.customError, true);
    assert.equal(rt.send(el, '1.23,56', 'insertText', '6'), '1.23,56');
    assert.equal(el.customError, true);
    assert.equal(rt.send(el, '1.234,56', 'insertText', '4'), 'R$ 1.234,56');
    assert.equal(el.customError, false);
    assert.equal(form.checkValidity(), true);
    assert.equal(form.submit(), true);
    assert.deepEqual(form.counts(), { posts: 1, adjustments: 1 });
});

test('STATE05: select all and replace with a digit exits strict invalid', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.paste(el, '1.23,45');
    assert.equal(rt.send(el, '1', 'insertText', '1'), 'R$ 0,01');
    assert.equal(el.customError, false);
    assert.equal(el.checkValidity(), true);
    assert.equal(rt.send(el, 'R$ 0,012', 'insertText', '2'), 'R$ 0,12');
    assert.equal(el.customError, false);
});

test('STATE06-STATE07: clearing invalid text leaves required empty, then dialing resumes', () => {
    const rt = runtime();
    const el = rt.field('signed', true);
    const form = rt.form(el);
    rt.paste(el, '1.23,45');
    assert.equal(rt.send(el, '', 'deleteContentForward'), '');
    assert.equal(el.customError, false);
    assert.equal(form.checkValidity(), false);
    assert.equal(form.submit(), false);
    assert.deepEqual(form.counts(), { posts: 0, adjustments: 0 });
    for (const [raw, want] of [['1', 'R$ 0,01'], ['R$ 0,012', 'R$ 0,12'], ['R$ 0,123', 'R$ 1,23']]) {
        assert.equal(rt.send(el, raw, 'insertText', raw.slice(-1)), want);
        assert.equal(el.customError, false);
    }
    assert.equal(form.checkValidity(), true);
});

test('STATE08-STATE09: composition and forward deletion cannot escape invalid state', () => {
    const rt = runtime();
    for (const [inputType, raw, data] of [
        ['insertCompositionText', '1.23,459', '9'],
        ['deleteContentForward', '1.23,4', null],
    ]) {
        const el = rt.field('signed');
        rt.paste(el, '1.23,45');
        assert.equal(rt.send(el, raw, inputType, data), raw);
        assert.equal(el.customError, true);
        assert.equal(el.checkValidity(), false);
    }
});

test('STATE10: an HTMX replacement input does not inherit strict invalid state', () => {
    const rt = runtime();
    const old = rt.field('signed');
    rt.paste(old, '1.23,45');
    assert.equal(old.customError, true);
    const replacement = rt.field('signed');
    assert.equal(rt.send(replacement, '1', 'insertText', '1'), 'R$ 0,01');
    assert.equal(replacement.customError, false);
    assert.equal(rt.inputListeners(), 1);
});

test('all external invalid origins activate the same persistent strict state', () => {
    const rt = runtime();
    for (const [origin, data] of [
        ['insertFromPaste', '1.23,45'],
        ['insertFromDrop', '1.23,45'],
        ['insertReplacementText', '1.23,45'],
        ['historyRedo', '1.23,45'],
        ['insertText', '1.23,45'],
        [undefined, undefined],
    ]) {
        const el = rt.field('signed');
        rt.send(el, '1.23,45', origin, data);
        assert.equal(el.customError, true, String(origin));
        assert.equal(rt.send(el, '1.23,4', 'deleteContentBackward'), '1.23,4', String(origin));
        assert.equal(el.customError, true, String(origin));
    }
});

test('ordinary digit dialing stays unchanged without prior strict invalid input', () => {
    const rt = runtime();
    const el = rt.field('signed');
    for (const [raw, want] of [
        ['1', 'R$ 0,01'],
        ['R$ 0,012', 'R$ 0,12'],
        ['R$ 0,123', 'R$ 1,23'],
        ['R$ 1,234', 'R$ 12,34'],
        ['R$ 12,345', 'R$ 123,45'],
        ['R$ 123,456', 'R$ 1.234,56'],
    ]) {
        assert.equal(rt.send(el, raw, 'insertText', raw.slice(-1)), want);
        assert.equal(el.customError, false);
    }
});

// The frontend verdicts below must match parseAccountBalance for every shared
// sample; the Go side is pinned by TestReconciliationSeparatorContractSamples.
test('frontend and backend share the same separator frontier', () => {
    const rt = runtime();
    const shared = [
        ['1.234,56', true],
        ['12.345,67', true],
        ['1234,56', true],
        ['123456', true],
        ['0,01', true],
        ['1.23,45', false],
        ['1.2.3', false],
        ['1..234', false],
        ['12.34,56', false],
        ['123.45,67', false],
        ['1.2345', false],
        ['abc0', false],
        ['1e2', false],
    ];
    for (const [input, valid] of shared) {
        const el = rt.field('signed');
        rt.paste(el, input);
        assert.equal(el.checkValidity(), valid, `${JSON.stringify(input)} frontend verdict`);
    }
});

test('a sign typed first stays usable until its digits arrive', () => {
    const rt = runtime();
    const el = rt.field('signed');
    rt.type(el, '-');
    assert.equal(el.value, '-');
    assert.equal(el.checkValidity(), false);
    rt.type(el, '-0,01');
    assert.equal(el.value, '-R$ 0,01');
    assert.equal(el.checkValidity(), true);
});

test('empty input stays empty so required reports it, not the mask', () => {
    const rt = runtime();
    for (const input of ['', ' ', '   ']) {
        const el = rt.field('signed');
        rt.type(el, input);
        assert.equal(el.value, '');
        assert.equal(el.customError, false, 'no custom error on the empty state');
        assert.equal(el.validationMessage, '');
    }
});

test('the mask never reports on blur: the native submit path does', () => {
    // reportValidity() on blur shows the browser bubble, which then swallows the
    // click the user aimed at the next field (and reappears on every attempt).
    // The message stays on the control for the browser to show on submit.
    const rt = runtime();
    assert.equal(rt.blurListeners(), 0);
    const invalid = rt.field('signed');
    rt.paste(invalid, 'abc0');
    assert.equal(invalid.validationMessage === '', false);
});

test('legacy mode keeps its historical output for every other money field', () => {
    const rt = runtime();
    const { mask } = rt;
    assert.equal(normalize(mask('123456')), 'R$ 1.234,56');
    assert.equal(normalize(mask('0')), 'R$ 0,00');
    assert.equal(mask(''), '');
    // Historical behaviour drops sign-only and unsigned negatives.
    assert.equal(normalize(mask('-1')), 'R$ 0,01');
    // Junk is still stripped exactly as before and never marked invalid.
    const el = rt.field('');
    rt.paste(el, 'abc0');
    assert.equal(el.validationMessage, '');
    const unsigned = rt.field('');
    assert.equal(normalize(rt.paste(unsigned, '-1234,56')), 'R$ 1.234,56');
    assert.equal(unsigned.validationMessage, '');
});

test('the input listener masks a swapped-in field exactly once per event', () => {
    const rt = runtime();
    assert.equal(rt.inputListeners(), 1);

    const signed = rt.field('signed');
    rt.resetCounters();
    assert.equal(rt.type(signed, '-1234,56'), '-R$ 1.234,56');
    assert.equal(signed.checkValidity(), true);
    assert.equal(rt.signedPasses(), 1);
    assert.equal(rt.legacyPasses(), 0);

    // Reconciliation re-opened through HTMX inserts a brand new input: the single
    // delegated listener must format it without being registered again and the
    // fresh field must start valid.
    const reopened = rt.field('signed');
    assert.equal(reopened.validationMessage, '');
    rt.resetCounters();
    assert.equal(rt.type(reopened, '1500,00'), 'R$ 1.500,00');
    assert.equal(rt.signedPasses(), 1);
    assert.equal(rt.inputListeners(), 1);
    assert.equal(rt.paste(reopened, '1.23,45'), '1.23,45');
    assert.equal(rt.signedPasses(), 2);
    assert.equal(reopened.checkValidity(), false);

    const legacy = rt.field('');
    rt.resetCounters();
    assert.equal(normalize(rt.type(legacy, '1234,56')), 'R$ 1.234,56');
    assert.equal(rt.legacyPasses(), 1);
    assert.equal(rt.signedPasses(), 0);
});
