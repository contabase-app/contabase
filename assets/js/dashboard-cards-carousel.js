(function () {
  function cardStep(rail) {
    const card = rail.querySelector('.card-cc');
    const style = window.getComputedStyle(rail);
    const gap = parseFloat(style.columnGap || style.gap || '0') || 16;
    return card ? card.getBoundingClientRect().width + gap : rail.clientWidth * 0.8;
  }

  function initCarousel(carousel) {
    if (!carousel || carousel.dataset.cardsCarouselInit === 'true') {
      return;
    }

    const section = carousel.closest('#dashboard-cards');
    const rail = carousel.querySelector('.cb-cards-rail');
    const prev = section ? section.querySelector('[data-cards-scroll="prev"]') : null;
    const next = section ? section.querySelector('[data-cards-scroll="next"]') : null;

    if (!section || !rail) {
      return;
    }

    carousel.dataset.cardsCarouselInit = 'true';

    const update = function () {
      const maxScroll = Math.max(0, rail.scrollWidth - rail.clientWidth - 1);
      const isScrollable = maxScroll > 1;

      section.classList.toggle('is-scrollable', isScrollable);

      if (prev) {
        prev.disabled = !isScrollable || rail.scrollLeft <= 4;
      }

      if (next) {
        next.disabled = !isScrollable || rail.scrollLeft >= maxScroll - 4;
      }
    };

    if (prev) {
      prev.addEventListener('click', function () {
        rail.scrollBy({ left: -cardStep(rail), behavior: 'smooth' });
      });
    }

    if (next) {
      next.addEventListener('click', function () {
        rail.scrollBy({ left: cardStep(rail), behavior: 'smooth' });
      });
    }

    let dragging = false;
    let moved = false;
    let startX = 0;
    let startLeft = 0;

    rail.addEventListener('pointerdown', function (event) {
      if (event.pointerType !== 'mouse' || window.matchMedia('(max-width: 620px)').matches) {
        return;
      }

      dragging = true;
      moved = false;
      startX = event.clientX;
      startLeft = rail.scrollLeft;
      rail.classList.add('is-dragging');
      rail.setPointerCapture(event.pointerId);
    });

    rail.addEventListener('dragstart', function (event) {
      if (dragging) {
        event.preventDefault();
      }
    });

    rail.addEventListener('pointermove', function (event) {
      if (!dragging) {
        return;
      }

      const delta = event.clientX - startX;
      if (Math.abs(delta) > 3) {
        moved = true;
      }
      rail.scrollLeft = startLeft - delta;
    });

    function stopDragging() {
      if (!dragging) {
        return;
      }
      dragging = false;
      rail.classList.remove('is-dragging');
      update();
    }

    rail.addEventListener('pointerup', stopDragging);
    rail.addEventListener('pointercancel', stopDragging);
    rail.addEventListener('mouseleave', stopDragging);

    rail.addEventListener('click', function (event) {
      if (!moved) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      moved = false;
    }, true);

    let ticking = false;
    rail.addEventListener('scroll', function () {
      if (ticking) {
        return;
      }
      ticking = true;
      window.requestAnimationFrame(function () {
        update();
        ticking = false;
      });
    }, { passive: true });

    window.addEventListener('resize', update);
    update();
    window.setTimeout(update, 120);
  }

  function initDashboardCardsCarousel(root) {
    const scope = root || document;
    if (scope.matches && scope.matches('#dashboard-cards .cb-cards-carousel')) {
      initCarousel(scope);
    }
    scope.querySelectorAll('#dashboard-cards .cb-cards-carousel').forEach(initCarousel);
  }

  window.initDashboardCardsCarousel = initDashboardCardsCarousel;

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () {
      initDashboardCardsCarousel(document);
    });
  } else {
    initDashboardCardsCarousel(document);
  }

  document.body.addEventListener('htmx:afterSettle', function (event) {
    initDashboardCardsCarousel(event.target || document);
  });
})();
