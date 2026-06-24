(function () {
    document.addEventListener('input', function (e) {
        var el = e.target.closest('[data-money-mask]');
        if (!el) return;
        el.value = window.maskMoneyRealTime(el.value);
    });
})();
