// Contato masks — CPF/CNPJ and Brazilian phone.
//
// These functions are loaded globally (app shell) because the same contact
// form is rendered in two places: the Contatos bottom sheet and the inline
// "Criar contato" picker used by the Lançamentos form after an HTMX swap.
// Formatting is idempotent and keeps the caret anchored to the same digit
// index, so paste, middle edits and re-opened sheets behave consistently.
(function () {
    if (window.__contactMasksInstalled) return;
    window.__contactMasksInstalled = true;

    function onlyDigits(value) {
        return String(value == null ? '' : value).replace(/\D/g, '');
    }

    function digitCountBeforeCaret(input) {
        if (typeof input.selectionStart !== 'number') return null;
        return onlyDigits(input.value.slice(0, input.selectionStart)).length;
    }

    function caretAfterDigit(value, count) {
        if (count <= 0) return 0;
        var seen = 0;
        for (var i = 0; i < value.length; i++) {
            if (/\d/.test(value.charAt(i))) {
                seen++;
                if (seen === count) return i + 1;
            }
        }
        return value.length;
    }

    function applyMask(input, formatter) {
        if (!input) return;
        var digitsBefore = digitCountBeforeCaret(input);
        var formatted = formatter(onlyDigits(input.value));
        if (input.value !== formatted) input.value = formatted;
        if (digitsBefore === null || typeof input.setSelectionRange !== 'function') return;
        var position = caretAfterDigit(formatted, Math.min(digitsBefore, onlyDigits(formatted).length));
        input.setSelectionRange(position, position);
    }

    function formatCpfCnpj(digits) {
        var clean = digits.substring(0, 14);
        if (clean.length <= 11) {
            return clean
                .replace(/(\d{3})(\d)/, '$1.$2')
                .replace(/(\d{3})\.(\d{3})(\d)/, '$1.$2.$3')
                .replace(/\.(\d{3})(\d{1,2})$/, '.$1-$2');
        }
        return clean
            .replace(/^(\d{2})(\d)/, '$1.$2')
            .replace(/^(\d{2})\.(\d{3})(\d)/, '$1.$2.$3')
            .replace(/\.(\d{3})(\d)/, '.$1/$2')
            .replace(/(\d{4})(\d{1,2})$/, '$1-$2');
    }

    function formatBrazilianPhone(digits) {
        var clean = digits.substring(0, 11);
        if (clean.length <= 10) {
            return clean
                .replace(/^(\d{2})(\d)/, '($1) $2')
                .replace(/(\d{4})(\d{1,4})$/, '$1-$2');
        }
        return clean
            .replace(/^(\d{2})(\d)/, '($1) $2')
            .replace(/(\d{5})(\d{1,4})$/, '$1-$2');
    }

    window.maskCnpjCpfContato = function (input) {
        applyMask(input, formatCpfCnpj);
    };

    window.maskPhoneContato = function (input) {
        applyMask(input, formatBrazilianPhone);
    };

    // Existing contacts are persisted as digits. Format them as soon as the
    // sheet or inline picker is inserted, so reopening the modal shows the
    // same CPF/CNPJ and phone shape the user sees while typing.
    function formatExisting(root) {
        var scope = root && typeof root.querySelectorAll === 'function' ? root : document;
        scope.querySelectorAll('[data-oninput="maskCnpjCpfContato"]').forEach(function (input) {
            if (input.value && input.value.trim()) window.maskCnpjCpfContato(input);
        });
        scope.querySelectorAll('[data-oninput="maskPhoneContato"]').forEach(function (input) {
            if (input.value && input.value.trim()) window.maskPhoneContato(input);
        });
    }

    function formatExistingInEvent(event) {
        var target = event && event.detail ? event.detail.target : null;
        formatExisting(target || document);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function () { formatExisting(document); }, { once: true });
    } else {
        formatExisting(document);
    }
    document.addEventListener('htmx:afterSwap', formatExistingInEvent);
}());
