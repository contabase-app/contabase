(function () {
    function cleanPercent(v) {
        // Strip everything except digits, comma, and period
        v = v.replace(/[^\d,.]/g, '');
        // Normalize: replace period with comma for BR visual consistency
        // (backend parseMonthlyYieldRate handles both via strings.ReplaceAll(raw, ",", "."))
        v = v.replace(/\./g, ',');
        // Ensure only one comma (keep the first one)
        var firstComma = v.indexOf(',');
        if (firstComma >= 0) {
            v = v.substring(0, firstComma + 1) + v.substring(firstComma + 1).replace(/,/g, '');
        }
        // Limit to 2 decimal places after comma
        var commaIdx = v.indexOf(',');
        if (commaIdx >= 0 && v.length > commaIdx + 3) {
            v = v.substring(0, commaIdx + 3);
        }
        return v;
    }

    document.addEventListener('input', function (e) {
        var el = e.target.closest('[data-percent-mask]');
        if (!el) return;
        var original = el.value;
        if (!original) {
            el.setCustomValidity('');
            return;
        }
        var cleaned = cleanPercent(original);
        el.value = cleaned;
        if (!cleaned) {
            el.setCustomValidity('Digite um valor percentual, ex: 0,8');
        } else {
            el.setCustomValidity('');
        }
    });

    document.addEventListener('blur', function (e) {
        var el = e.target.closest('[data-percent-mask]');
        if (!el) return;
        if (el.validationMessage) {
            el.reportValidity();
        }
    }, true);
})();
