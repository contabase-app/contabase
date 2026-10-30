(function () {
    // Keep rejection local to this input. A replacement input inserted by HTMX
    // starts fresh, and an editing inputType cannot make rejected text dial.
    var strictInvalidInputs = new WeakSet();
    document.addEventListener('input', function (e) {
        var el = e.target.closest('[data-money-mask]');
        if (!el) return;
        if (el.getAttribute('data-money-mask') === 'signed') {
            // Rejected text stays as typed and marks the control invalid, so the
            // browser blocks the submit instead of sending a converted amount.
            // The message is surfaced by native validation on submit; reporting
            // it on blur would swallow clicks on the next field. External text
            // is read as an amount; rejected text stays strict through edits.
            var strict = strictInvalidInputs.has(el) || !window.moneyMaskInputIsTyping(e.inputType, e.data);
            var state = window.evaluateMoneyInput(el.value, {
                allowNegative: true,
                exact: true,
                strict: strict,
            });
            el.value = state.value;
            if (strict) {
                if (state.valid) {
                    strictInvalidInputs.delete(el);
                } else {
                    strictInvalidInputs.add(el);
                }
            }
            if (typeof el.setCustomValidity === 'function') {
                el.setCustomValidity(state.valid ? '' : state.message);
            }
            return;
        }
        el.value = window.maskMoneyRealTime(el.value);
    });
})();
