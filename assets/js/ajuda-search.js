(function() {
  function normalize(str) {
    return (str || '').normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase();
  }

  function initHelpSearch() {
    var input = document.getElementById('help-search');
    if (!input) return;
    if (input.dataset.helpSearchReady) return;
    input.dataset.helpSearchReady = 'true';

    var sections = document.querySelectorAll('main > div > section');
    if (!sections.length) {
      delete input.dataset.helpSearchReady;
      return;
    }

    var indexedSections = Array.prototype.map.call(sections, function(sec) {
      return {
        section: sec,
        text: normalize(sec.innerText),
      };
    });

    input.addEventListener('input', function() {
      var q = normalize(input.value.trim());
      indexedSections.forEach(function(entry) {
        entry.section.classList.toggle('hidden', q.length > 0 && entry.text.indexOf(q) === -1);
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initHelpSearch);
  } else {
    initHelpSearch();
  }

  document.body.addEventListener('htmx:afterSettle', function() {
    initHelpSearch();
  });
})();
