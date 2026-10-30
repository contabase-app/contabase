function getCookieValue(name) {
    var escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    var match = document.cookie.match(new RegExp('(?:^|; )' + escaped + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : '';
}

function csrfToken() {
    return getCookieValue('contabase_csrf');
}

function centerActiveMonth() {
    document.querySelectorAll('[data-month-active="true"]').forEach(function(active) {
        active.scrollIntoView({ behavior: 'auto', block: 'nearest', inline: 'center' });
    });
}

function showGlobalFeedback(message, tone) {
    var box = document.getElementById('global-feedback');
    if (!box) return;
    box.classList.remove('hidden');
    if (tone === 'success') {
        box.className = box.className.replace(/border-rose-[^ ]+/g, '').replace(/bg-rose-[^ ]+/g, '').replace(/text-rose-[^ ]+/g, '');
        box.classList.add('border-emerald-400/30', 'bg-emerald-500/15', 'text-emerald-50');
    } else {
        box.className = box.className.replace(/border-emerald-[^ ]+/g, '').replace(/bg-emerald-[^ ]+/g, '').replace(/text-emerald-[^ ]+/g, '');
        box.classList.add('border-rose-400/30', 'bg-rose-500/15', 'text-rose-50');
    }
    box.textContent = message;
    window.setTimeout(function() {
        if (box.textContent === message) box.classList.add('hidden');
    }, 2600);
}

// Rebuilds the currency string from the typed digit sequence alone, so no point
// arithmetic can round the value that is submitted back to the server.
function formatExactBRLDigits(digits, negative) {
    var normalized = digits.replace(/^0+/, '');
    if (normalized === '') {
        normalized = '0';
    }
    var centavos = normalized.length > 2 ? normalized.slice(-2) : normalized;
    var reais = normalized.length > 2 ? normalized.slice(0, -2) : '0';
    while (centavos.length < 2) {
        centavos = '0' + centavos;
    }
    var grouped = '';
    for (var i = 0; i < reais.length; i++) {
        if (i > 0 && (reais.length - i) % 3 === 0) {
            grouped += '.';
        }
        grouped += reais.charAt(i);
    }
    return (negative ? '-' : '') + 'R$ ' + grouped + ',' + centavos;
}

// A signed money field only rewrites text it can read as an amount: one optional
// sign, one optional "R$" prefix and a body of digits with a single decimal
// comma. Grouping and fraction width stay free while typing, but letters, a
// repeated prefix, a repeated sign or a doubled separator are left exactly as
// typed so malformed input can never become a different balance.
var moneyInputInvalidMessage = 'Valor inválido. Digite apenas números (os dois últimos são centavos) e "-" para saldo negativo.';

function readsAsMoneyBody(body) {
    if (body === '') return false;
    var commas = 0;
    var separatorPending = true;
    for (var i = 0; i < body.length; i++) {
        var ch = body.charAt(i);
        if (ch === ',' || ch === '.') {
            if (ch === ',') {
                commas += 1;
                if (commas > 1) return false;
            }
            if (separatorPending) return false;
            separatorPending = true;
            continue;
        }
        if (ch < '0' || ch > '9') return false;
        separatorPending = false;
    }
    return !separatorPending;
}

// Shapes an already formatted amount may have, matching the grouping rules
// parseAccountBalance applies: plain digits with an optional comma decimal, BR
// grouping (dot groups of three) and US grouping (comma groups of three with a
// dot decimal). A dot is never read as the decimal separator on its own, so a
// malformed thousands group such as "1.23" is rejected instead of being stripped
// into a different amount.
var strictMoneyBodyPattern = /^(?:[0-9]+(?:,[0-9]{1,2})?|[1-9][0-9]{0,2}(?:\.[0-9]{3})+(?:,[0-9]{1,2})?|[1-9][0-9]{0,2}(?:,[0-9]{3})+(?:\.[0-9]{1,2})?)$/;

// Editing operations treated as keystrokes: there the mask owns the digits and
// re-reads its own output plus or minus one typed character. Anything else
// (paste, drop, redo, autofill, unknown origin) is a formatted amount and must
// pass the structural shapes above. Chrome reports these values in event.inputType.
var moneyTypingInputTypes = {
    insertText: true,
    insertCompositionText: true,
    deleteContentBackward: true,
    deleteContentForward: true,
    deleteByCut: true,
    deleteByDrag: true,
    deleteWordBackward: true,
    deleteWordForward: true,
    deleteSoftLineBackward: true,
    deleteSoftLineForward: true,
};

function moneyInputIsTyping(inputType, data) {
    if (moneyTypingInputTypes[inputType] !== true) return false;
    if (inputType === 'insertText' || inputType === 'insertCompositionText') {
        return data === null || data === undefined || data.length <= 1;
    }
    return true;
}

window.moneyMaskInputIsTyping = moneyInputIsTyping;

function readExactMoneyInput(value, strict) {
    var text = value === null || value === undefined ? '' : value.toString().trim();
    if (text === '') return { negative: false, digits: '' };
    var index = 0;
    var negative = false;
    var prefix = 0;
    while (index < text.length) {
        var ch = text.charAt(index);
        if (ch === ' ') {
            index += 1;
            continue;
        }
        if (ch === '-') {
            if (negative) return null;
            negative = true;
            index += 1;
            continue;
        }
        if ((ch === 'R' || ch === 'r') && text.charAt(index + 1) === '$') {
            prefix += 1;
            if (prefix > 1) return null;
            index += 2;
            continue;
        }
        break;
    }
    var body = text.slice(index);
    if (strict === true ? !strictMoneyBodyPattern.test(body) : !readsAsMoneyBody(body)) return null;
    return { negative: negative, digits: body.replace(/\D/g, '') };
}

// Single entry point for the signed mask: reports the text the field must show
// and whether native validation has to block the submit. A rejected reading
// keeps the typed text untouched.
window.evaluateMoneyInput = function(value, options) {
    var opts = options || {};
    var text = value === null || value === undefined ? '' : value.toString();
    if (opts.exact !== true) {
        return { value: text, valid: true, message: '' };
    }
    var reading = readExactMoneyInput(text, opts.strict === true);
    if (reading === null) {
        return { value: text, valid: false, message: moneyInputInvalidMessage };
    }
    if (reading.digits === '') {
        return { value: '', valid: true, message: '' };
    }
    return { value: formatExactBRLDigits(reading.digits, reading.negative), valid: true, message: '' };
};

// options.allowNegative keeps a leading sign; options.exact formats without
// float. Both are opt-in so every existing money field keeps its output.
window.maskMoneyRealTime = function(v, options) {
    if (!v) return '';
    var opts = options || {};
    var raw = v.toString();
    if (opts.exact === true) {
        return window.evaluateMoneyInput(raw, opts).value;
    }
    var negative = opts.allowNegative === true && /^\s*(?:r\$)?\s*-/i.test(raw);
    var val = raw.replace(/\D/g, '');
    if (!val) return negative ? '-' : '';
    var floatVal = parseFloat(val) / 100;
    return floatVal.toLocaleString('pt-BR', { style: 'currency', currency: 'BRL' });
};
