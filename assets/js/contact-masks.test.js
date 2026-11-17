const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function runtime() {
    const windowStub = {};
    const documentStub = {
        readyState: 'complete',
        querySelectorAll: () => [],
        addEventListener: () => {},
    };
    const context = vm.createContext({ window: windowStub, document: documentStub });
    const source = fs.readFileSync(require.resolve('./contact-masks.js'), 'utf8');
    vm.runInContext(source, context, { filename: 'contact-masks.js' });
    return {
        window: windowStub,
        loadAgain: () => vm.runInContext(source, context, { filename: 'contact-masks.js' }),
    };
}

function input(value, caret) {
    const el = {
        value,
        selectionStart: caret === undefined ? value.length : caret,
        selectionEnd: caret === undefined ? value.length : caret,
        setSelectionRange(start, end) {
            this.selectionStart = start;
            this.selectionEnd = end;
        },
    };
    return el;
}

test('loads once and keeps a single global mask pair', () => {
    const rt = runtime();
    const documentMask = rt.window.maskCnpjCpfContato;
    const phoneMask = rt.window.maskPhoneContato;
    rt.loadAgain();
    assert.equal(rt.window.maskCnpjCpfContato, documentMask);
    assert.equal(rt.window.maskPhoneContato, phoneMask);
});

test('formats CPF, CNPJ and both Brazilian phone lengths', () => {
    const rt = runtime();

    const cpf = input('12345678900');
    rt.window.maskCnpjCpfContato(cpf);
    assert.equal(cpf.value, '123.456.789-00');

    const cnpj = input('12345678000190');
    rt.window.maskCnpjCpfContato(cnpj);
    assert.equal(cnpj.value, '12.345.678/0001-90');

    const mobile = input('11999998888');
    rt.window.maskPhoneContato(mobile);
    assert.equal(mobile.value, '(11) 99999-8888');

    const landline = input('1133334444');
    rt.window.maskPhoneContato(landline);
    assert.equal(landline.value, '(11) 3333-4444');
});

test('normalizes pasted values and truncates excess digits', () => {
    const rt = runtime();

    const pasted = input('CPF 123.456.789-00 extra');
    rt.window.maskCnpjCpfContato(pasted);
    assert.equal(pasted.value, '123.456.789-00');

    const longDocument = input('123456780001905');
    rt.window.maskCnpjCpfContato(longDocument);
    assert.equal(longDocument.value, '12.345.678/0001-90');

    const longPhone = input('55 11 99999-8888');
    rt.window.maskPhoneContato(longPhone);
    assert.equal(longPhone.value, '(55) 11999-9988');
});

test('keeps the caret anchored to the same digit after an edit', () => {
    const rt = runtime();

    const cpf = input('123.456.789-00', 6);
    rt.window.maskCnpjCpfContato(cpf);
    assert.equal(cpf.value, '123.456.789-00');
    assert.equal(cpf.selectionStart, 6, 'caret stays after the third digit');

    const phone = input('(11) 39999-8888', 5);
    rt.window.maskPhoneContato(phone);
    assert.equal(phone.value, '(11) 39999-8888');
    assert.equal(phone.selectionStart, 3, 'caret stays after the second digit');
});
