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
  var jobModal = document.getElementById('job-detail-modal');
  var jobModalBody = document.querySelector('[data-job-modal-body]');
  var jobModalClose = document.querySelector('[data-modal-close]');
  var jobModalTrigger = null;
  var outputRequestID = 0;
  var outputAbortController = null;
  var cancelOutputRequest = function () {
    outputRequestID++;
    if (outputAbortController) outputAbortController.abort();
    outputAbortController = null;
  };

  if (jobModal && jobModalBody && jobModalClose) {
    document.addEventListener('click', function (event) {
      var outputButton = event.target.closest('[data-job-output]');
      if (outputButton && jobModal.contains(outputButton)) {
        var outputPanel = jobModalBody.querySelector('[data-job-output-preview]');
        var outputTitle = jobModalBody.querySelector('[data-job-output-title]');
        var outputContent = jobModalBody.querySelector('[data-job-output-content]');
        if (!outputPanel || !outputTitle || !outputContent) return;

        cancelOutputRequest();
        outputButton.disabled = true;
        var requestID = outputRequestID;
        outputAbortController = new AbortController();
        outputPanel.hidden = false;
        outputTitle.textContent = outputButton.getAttribute('data-output-label');
        outputContent.textContent = jobModal.getAttribute('data-output-loading');
        var outputURL = '/slurm/jobs/' + encodeURIComponent(outputButton.getAttribute('data-job-id')) +
          '/output/' + encodeURIComponent(outputButton.getAttribute('data-output-stream'));

        fetch(outputURL, {
          credentials: 'same-origin',
          headers: { Accept: 'text/plain' },
          signal: outputAbortController.signal
        })
          .then(function (response) {
            if (!response.ok) throw new Error('output request failed');
            var truncated = response.headers.get('X-Content-Truncated') === 'true';
            return response.text().then(function (content) {
              return { content: content, truncated: truncated };
            });
          })
          .then(function (result) {
            if (requestID !== outputRequestID || !jobModal.open) return;
            if (result.truncated) {
              outputTitle.textContent += ' · ' + jobModal.getAttribute('data-output-truncated');
            }
            outputContent.textContent = result.content;
            outputContent.focus();
          })
          .catch(function () {
            if (requestID !== outputRequestID || !jobModal.open) return;
            outputContent.textContent = jobModal.getAttribute('data-output-error');
          })
          .then(function () {
            if (document.body.contains(outputButton)) outputButton.disabled = false;
            if (requestID === outputRequestID) outputAbortController = null;
          });
        return;
      }

      var trigger = event.target.closest('[data-job-detail]');
      if (!trigger) return;
      var detailTemplate = document.getElementById(trigger.getAttribute('data-job-detail'));
      if (!detailTemplate) return;

      cancelOutputRequest();
      while (jobModalBody.firstChild) jobModalBody.removeChild(jobModalBody.firstChild);
      jobModalBody.appendChild(detailTemplate.content.cloneNode(true));
      jobModalTrigger = trigger;
      document.body.classList.add('modal-open');
      jobModal.showModal();
      jobModalClose.focus();
    });

    jobModalClose.addEventListener('click', function () { jobModal.close(); });
    jobModal.addEventListener('click', function (event) {
      if (event.target === jobModal) jobModal.close();
    });
    jobModal.addEventListener('keydown', function (event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        jobModal.close();
      }
    });
    jobModal.addEventListener('close', function () {
      cancelOutputRequest();
      document.body.classList.remove('modal-open');
      if (jobModalTrigger) jobModalTrigger.focus();
    });
  }

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
