const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function runtime() {
    const listeners = new Map();
    const document = {
        addEventListener(name,fn) { if (!listeners.has(name)) listeners.set(name,[]); listeners.get(name).push(fn); },
        querySelectorAll() { return []; }, querySelector() { return null; },
    };
    vm.runInNewContext(fs.readFileSync(require.resolve('./investment-create.js'),'utf8'),{document});
    function field(mode = 'positive') {
        return { value:'', selectionStart:0, selectionEnd:0, dataset:{investmentMoney:mode},
            matches: s => s === '[data-investment-money]',
            setCustomValidity(message) { this.message = message; },
            setSelectionRange(start,end) { this.selectionStart=start; this.selectionEnd=end; },
        };
    }
    function send(el, value, inputType = 'insertFromPaste', data = null, cursor = value.length) {
        el.value=value; el.selectionStart=cursor; el.selectionEnd=cursor;
        for (const fn of listeners.get('input')) fn({target:el,inputType,data});
        return el.value;
    }
    function type(el,chars) {
        for (const char of chars) {
            const start=el.selectionStart, end=el.selectionEnd;
            send(el,el.value.slice(0,start)+char+el.value.slice(end),'insertText',char,start+1);
        }
        return el.value;
    }
    function blur(el) { for (const fn of listeners.get('blur')) fn({target:el}); return el.value; }
    return {field,send,type,blur};
}

test('digits are reais, comma decimals remain exact, blur pads without changing amount',()=>{
    const r=runtime();
    for (const [raw,want] of [['3000','R$ 3.000,00'],['300,00','R$ 300,00'],['1.234,56','R$ 1.234,56'],['300,0','R$ 300,00'],['0,01','R$ 0,01'],['300','R$ 300,00'],['300.001','R$ 300.001,00']]) {
        const el=r.field(); r.send(el,raw); assert.equal(r.blur(el),want); assert.equal(el.message,'');
        assert.equal(r.blur(el),want);
    }
    const el=r.field(); assert.equal(r.type(el,'3000'),'R$ 3.000'); assert.equal(r.type(el,',56'),'R$ 3.000,56'); assert.equal(r.blur(el),'R$ 3.000,56');
});

test('invalid external formats stay invalid through input, deletion and blur',()=>{
    const r=runtime();
    for (const raw of ['300,001','300.00','1,234.56','1.23,45','1.23,','-300','1e3','92233720368547758,08']) {
        const el=r.field(); assert.equal(r.send(el,raw),raw); assert.ok(el.message); assert.equal(r.blur(el),raw); assert.ok(el.message);
        // Adding a third digit repairs the BR thousands group in 300.00.
        if (raw === '300.00') {
            assert.equal(r.send(el,raw+'1','insertText','1'),'R$ 300.001');
            assert.equal(el.message,'');
            assert.equal(r.blur(el),'R$ 300.001,00');
        } else {
            assert.equal(r.send(el,raw+'1','insertText','1'),raw+'1'); assert.ok(el.message);
        }
    }
    const el=r.field(); r.type(el,'300,001'); assert.equal(el.value,'R$ 300,001'); assert.ok(el.message);
    r.send(el,el.value.slice(0,-1),'deleteContentBackward'); assert.equal(el.value,'R$ 300,00'); assert.equal(el.message,'');
});

test('selection, middle edits, deletion and cursor remain natural',()=>{
    const r=runtime(), el=r.field(); r.type(el,'1234');
    assert.equal(el.value,'R$ 1.234'); assert.equal(el.selectionStart,8);
    el.selectionStart=5; el.selectionEnd=5; r.type(el,'9'); assert.equal(el.value,'R$ 19.234'); assert.equal(el.selectionStart,5);
    el.selectionStart=0;el.selectionEnd=el.value.length;r.type(el,'300');assert.equal(el.value,'R$ 300');
    r.send(el,'','deleteContentBackward');assert.equal(el.value,'');assert.equal(el.message,'');
    r.send(el,'   ');assert.equal(el.value,'');
});

test('zero is allowed only for position/final value; max int64 stays exact',()=>{
    const r=runtime();
    const principal=r.field(), final=r.field('zero');
    r.send(principal,'0,00');assert.ok(principal.message);
    r.send(final,'0,00');assert.equal(final.value,'R$ 0,00');assert.equal(final.message,'');
    r.send(final,'92.233.720.368.547.758,07');assert.equal(final.value,'R$ 92.233.720.368.547.758,07');assert.equal(final.message,'');
    r.send(final,'0001234,56');assert.equal(final.value,'R$ 1.234,56');assert.equal(final.message,'');
});
