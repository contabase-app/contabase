// Input modality: keyboard focus gets an outline; pointer/touch focus keeps the
// quiet neutral border defined by the field components. CSS cannot distinguish
// a mouse-focused text input from a keyboard-focused one, so this single shell
// helper records the modality on <html>.
(function () {
    'use strict';

    if (window.__contabaseInputModalityReady) return;
    window.__contabaseInputModalityReady = true;

    var root = document.documentElement;
    if (!root) return;

    var setMode = function (mode) {
        if (root.getAttribute('data-input-modality') !== mode) {
            root.setAttribute('data-input-modality', mode);
        }
    };

    setMode('pointer');

    document.addEventListener('keydown', function (event) {
        if (!event) return;
        var key = event.key;
        if (key === 'Tab' || key === 'Enter' || key === ' ' || key === 'Spacebar' || key === 'Escape') {
            setMode('keyboard');
        }
    }, true);

    document.addEventListener('pointerdown', function () {
        setMode('pointer');
    }, true);
})();
