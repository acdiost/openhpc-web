(function () {
  var menuButton = document.querySelector('[data-menu]');
  var sidebar = document.getElementById('sidebar');
  if (menuButton && sidebar) {
    var setMenuOpen = function (isOpen) {
      sidebar.classList.toggle('open', isOpen);
      menuButton.setAttribute('aria-expanded', String(isOpen));
    };
    menuButton.addEventListener('click', function () {
      setMenuOpen(!sidebar.classList.contains('open'));
    });
  }

  var search = document.querySelector('.search input');
  document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape' && menuButton && sidebar && sidebar.classList.contains('open')) {
      setMenuOpen(false);
      menuButton.focus();
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && search) {
      event.preventDefault();
      search.focus();
    }
  });
}());
