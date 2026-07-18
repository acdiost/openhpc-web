(function () {
  var menuButton = document.querySelector('[data-menu]');
  var sidebar = document.getElementById('sidebar');
  if (menuButton && sidebar) {
    menuButton.addEventListener('click', function () {
      sidebar.classList.toggle('open');
    });
  }

  var search = document.querySelector('.search input');
  document.addEventListener('keydown', function (event) {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && search) {
      event.preventDefault();
      search.focus();
    }
  });
}());
