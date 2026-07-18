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

  var resourceModal = document.getElementById('job-resource-modal');
  var resourceClose = document.querySelector('[data-resource-close]');
  var resourceStatus = document.querySelector('[data-resource-status]');
  var resourceContent = document.querySelector('[data-resource-content]');
  var resourceTimer = null;
  var resourceController = null;
  var resourceRequestID = 0;
  var resourceJobID = '';
  var resourceTrigger = null;
  var resourceHistory = [];
  var resourcePollDelay = 5000;

  var cancelResourcePolling = function () {
    resourceRequestID++;
    if (resourceTimer) window.clearTimeout(resourceTimer);
    if (resourceController) resourceController.abort();
    resourceTimer = null;
    resourceController = null;
  };

  var formatDuration = function (seconds) {
    var days = Math.floor(seconds / 86400);
    var remaining = seconds % 86400;
    var hours = Math.floor(remaining / 3600);
    var minutes = Math.floor((remaining % 3600) / 60);
    var secs = remaining % 60;
    var clock = [hours, minutes, secs].map(function (value) { return String(value).padStart(2, '0'); }).join(':');
    return days > 0 ? days + '-' + clock : clock;
  };

  var formatBytes = function (bytes) {
    if (!bytes) return '0 B';
    var units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    var unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    var value = bytes / Math.pow(1024, unit);
    return (value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)) + ' ' + units[unit];
  };

  var chartPoints = function (history, field) {
    var maximum = history.reduce(function (current, sample) { return Math.max(current, sample[field]); }, 0) || 1;
    return history.map(function (sample, index) {
      var x = history.length === 1 ? 360 : index * 720 / (history.length - 1);
      var y = 200 - sample[field] * 180 / maximum;
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
  };

  var renderResourceUsage = function (usage) {
    var cpu = resourceModal.querySelector('[data-resource-cpu]');
    var memory = resourceModal.querySelector('[data-resource-memory]');
    var sampled = resourceModal.querySelector('[data-resource-sampled]');
    var cpuLine = resourceModal.querySelector('[data-resource-cpu-line]');
    var memoryLine = resourceModal.querySelector('[data-resource-memory-line]');
    var chart = resourceModal.querySelector('[data-resource-chart]');
    var chartDescription = resourceModal.querySelector('[data-resource-chart-description]');
    var tableBody = resourceModal.querySelector('[data-sstat-body]');
    var sampledTime = new Date(usage.sampled_at).toLocaleTimeString();
    var cpuText = formatDuration(usage.total_cpu_seconds);
    var memoryText = formatBytes(usage.max_rss_bytes);
    cpu.textContent = cpuText;
    memory.textContent = memoryText;
    sampled.textContent = sampledTime;
    resourceHistory = resourceHistory.concat([{
      cpu: usage.total_cpu_seconds,
      memory: usage.max_rss_bytes
    }]).slice(-24);
    cpuLine.setAttribute('points', chartPoints(resourceHistory, 'cpu'));
    memoryLine.setAttribute('points', chartPoints(resourceHistory, 'memory'));
    chart.setAttribute('data-sample-count', String(resourceHistory.length));
    var resourceSummary = resourceModal.getAttribute('data-resource-sampled-label') + ' ' + sampledTime + '; ' +
      resourceModal.getAttribute('data-resource-cpu-label') + ' ' + cpuText + '; ' +
      resourceModal.getAttribute('data-resource-memory-label') + ' ' + memoryText;
    chartDescription.textContent = resourceSummary;
    while (tableBody.firstChild) tableBody.removeChild(tableBody.firstChild);
    usage.steps.forEach(function (step) {
      var row = document.createElement('tr');
      [step.step, step.ave_cpu || '—', step.total_cpu || '—', step.ave_rss || '—', step.max_rss || '—', step.max_vm_size || '—'].forEach(function (value) {
        var cell = document.createElement('td');
        cell.textContent = value;
        row.appendChild(cell);
      });
      tableBody.appendChild(row);
    });
    resourceStatus.textContent = usage.steps.length ? resourceSummary : resourceModal.getAttribute('data-resource-empty');
    resourceContent.hidden = false;
  };

  var pollResourceUsage = function () {
    cancelResourcePolling();
    var requestID = resourceRequestID;
    resourceController = new AbortController();
    fetch('/slurm/jobs/' + encodeURIComponent(resourceJobID) + '/resources', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      signal: resourceController.signal
    })
      .then(function (response) {
        if (!response.ok) throw new Error('resource request failed');
        return response.json();
      })
      .then(function (usage) {
        if (requestID !== resourceRequestID || !resourceModal.open) return;
        renderResourceUsage(usage);
      })
      .catch(function (error) {
        if (error.name === 'AbortError' || requestID !== resourceRequestID || !resourceModal.open) return;
        resourceStatus.textContent = resourceModal.getAttribute('data-resource-error');
      })
      .then(function () {
        if (requestID !== resourceRequestID || !resourceModal.open) return;
        resourceController = null;
        resourceTimer = window.setTimeout(pollResourceUsage, resourcePollDelay);
      });
  };

  if (resourceModal && resourceClose && resourceStatus && resourceContent) {
    document.addEventListener('click', function (event) {
      var trigger = event.target.closest('[data-job-resource]');
      if (!trigger) return;
      resourceTrigger = trigger;
      resourceJobID = trigger.getAttribute('data-job-resource');
      resourceHistory = [];
      resourceContent.hidden = true;
      resourceStatus.textContent = resourceModal.getAttribute('data-resource-loading');
      resourceModal.querySelector('[data-resource-job-label]').textContent = '#' + resourceJobID;
      document.body.classList.add('modal-open');
      resourceModal.showModal();
      resourceClose.focus();
      pollResourceUsage();
    });
    resourceClose.addEventListener('click', function () { resourceModal.close(); });
    resourceModal.addEventListener('click', function (event) {
      if (event.target === resourceModal) resourceModal.close();
    });
    resourceModal.addEventListener('close', function () {
      cancelResourcePolling();
      resourceHistory = [];
      document.body.classList.remove('modal-open');
      if (resourceTrigger) resourceTrigger.focus();
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
