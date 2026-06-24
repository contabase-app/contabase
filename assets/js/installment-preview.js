(function(root, factory) {
    var api = factory();
    if (typeof module === 'object' && module.exports) {
        module.exports = api;
    }
    if (root) {
        root.ContaBaseInstallmentPreview = api;
    }
})(typeof window !== 'undefined' ? window : null, function() {
    var currencyFormatter = new Intl.NumberFormat('pt-BR', {
        style: 'currency',
        currency: 'BRL'
    });

    function formatCents(cents) {
        return currencyFormatter.format(cents / 100);
    }

    function describe(amountCents, totalInstallments) {
        var cents = Number(amountCents);
        var installments = Number(totalInstallments);
        if (!Number.isSafeInteger(cents) || cents <= 0) return '';
        if (!Number.isSafeInteger(installments) || installments <= 0) return '';

        var regularAmount = Math.floor(cents / installments);
        var remainder = cents % installments;
        if (remainder === 0) {
            return 'Parcelado em ' + installments + 'x de ' + formatCents(regularAmount);
        }

        var firstAmount = regularAmount + remainder;
        if (installments === 1) {
            return 'Parcelado em 1x de ' + formatCents(firstAmount);
        }
        return installments + ' parcelas: 1x de ' + formatCents(firstAmount)
            + ' e ' + (installments - 1) + 'x de ' + formatCents(regularAmount);
    }

    return {
        describe: describe,
        formatCents: formatCents
    };
});
