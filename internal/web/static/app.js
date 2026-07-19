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

  var platformUserCreateTrigger = document.querySelector('[data-platform-user-create]');
  var platformUserCreateModal = document.getElementById('platform-user-create-modal');
  var platformUserCreateInput = document.getElementById('platform-username');
  if (platformUserCreateTrigger && platformUserCreateModal) {
    var openPlatformUserCreateModal = function () {
      if (platformUserCreateModal.open) platformUserCreateModal.close();
      platformUserCreateModal.showModal();
      document.body.classList.add('modal-open');
      platformUserCreateTrigger.setAttribute('aria-expanded', 'true');
      if (platformUserCreateInput) platformUserCreateInput.focus();
    };
    platformUserCreateTrigger.addEventListener('click', function (event) {
      event.preventDefault();
      openPlatformUserCreateModal();
    });
    platformUserCreateModal.querySelectorAll('[data-platform-user-create-close]').forEach(function (button) {
      button.addEventListener('click', function (event) {
        event.preventDefault();
        platformUserCreateModal.close();
      });
    });
    platformUserCreateModal.addEventListener('click', function (event) {
      if (event.target === platformUserCreateModal) platformUserCreateModal.close();
    });
    platformUserCreateModal.addEventListener('close', function () {
      document.body.classList.remove('modal-open');
      platformUserCreateTrigger.setAttribute('aria-expanded', 'false');
      platformUserCreateTrigger.focus();
    });
    if (platformUserCreateModal.hasAttribute('data-platform-user-create-open')) openPlatformUserCreateModal();
  }

  var confirmationForms = document.querySelectorAll('form[data-confirm]');
  confirmationForms.forEach(function (form) {
    form.addEventListener('submit', function (event) {
      if (!window.confirm(form.getAttribute('data-confirm'))) {
        event.preventDefault();
        return;
      }
      var confirmedInput = document.createElement('input');
      confirmedInput.type = 'hidden';
      confirmedInput.name = 'confirmed';
      confirmedInput.value = 'true';
      form.appendChild(confirmedInput);
    });
  });

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
  var normalizeOutputLineEndings = function (content) {
    return content.replace(/\r\n?/g, '\n');
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
            outputContent.textContent = normalizeOutputLineEndings(result.content);
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

  var chartPoints = function (samples, field) {
    var values = samples.filter(function (sample) { return Number.isFinite(sample[field]); });
    if (!values.length) return '';
    var maximum = values.reduce(function (current, sample) { return Math.max(current, sample[field]); }, 0) || 1;
    var firstTime = values[0].time;
    var lastTime = values[values.length - 1].time;
    var timeRange = lastTime > firstTime ? lastTime - firstTime : 0;
    return values.map(function (sample, index) {
      var x = timeRange ? 52 + (sample.time - firstTime) * 652 / timeRange : (values.length === 1 ? 378 : 52 + index * 652 / (values.length - 1));
      var y = 120 - sample[field] * 100 / maximum;
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
  };

  var chartMaximum = function (samples, field) {
    return samples.reduce(function (current, sample) {
      return Number.isFinite(sample[field]) ? Math.max(current, sample[field]) : current;
    }, 0);
  };

  var renderResourceChart = function (line, axisMaximum, description, samples, field, formatter, empty) {
    var maximum = chartMaximum(samples, field);
    var hasSamples = samples.some(function (sample) { return Number.isFinite(sample[field]); });
    line.setAttribute('points', chartPoints(samples, field));
    axisMaximum.textContent = hasSamples ? formatter(maximum) : '—';
    description.textContent = hasSamples ? formatter(maximum) : empty;
  };

  var renderResourceUsage = function (usage) {
    var cpu = resourceModal.querySelector('[data-resource-cpu]');
    var memory = resourceModal.querySelector('[data-resource-memory]');
    var sampled = resourceModal.querySelector('[data-resource-sampled]');
    var cpuRate = resourceModal.querySelector('[data-resource-cpu-rate]');
    var cpuLine = resourceModal.querySelector('[data-resource-cpu-line]');
    var memoryLine = resourceModal.querySelector('[data-resource-memory-line]');
    var cpuAxisMaximum = resourceModal.querySelector('[data-resource-cpu-axis-max]');
    var memoryAxisMaximum = resourceModal.querySelector('[data-resource-memory-axis-max]');
    var cpuDescription = resourceModal.querySelector('[data-resource-cpu-chart-description]');
    var memoryDescription = resourceModal.querySelector('[data-resource-memory-chart-description]');
    var memoryCurrent = resourceModal.querySelector('[data-resource-memory-current]');
    var tableBody = resourceModal.querySelector('[data-sstat-body]');
    var sampledTime = new Date(usage.sampled_at).toLocaleTimeString();
    var cpuText = formatDuration(usage.total_cpu_seconds);
    var memoryText = formatBytes(usage.max_rss_bytes);
    cpu.textContent = cpuText;
    memory.textContent = memoryText;
    sampled.textContent = sampledTime;
    var sampledAt = Date.parse(usage.sampled_at);
    resourceHistory = resourceHistory.concat([{
      cpu: usage.total_cpu_seconds,
      memory: usage.max_rss_bytes,
      time: Number.isFinite(sampledAt) ? sampledAt : Date.now()
    }]).slice(-24);
    var cpuSamples = resourceHistory.map(function (sample, index) {
      if (index === 0) return { time: sample.time, cores: NaN };
      var previous = resourceHistory[index - 1];
      var elapsedSeconds = (sample.time - previous.time) / 1000;
      var deltaCPU = sample.cpu - previous.cpu;
      return { time: sample.time, cores: elapsedSeconds > 0 && deltaCPU >= 0 ? deltaCPU / elapsedSeconds : NaN };
    });
    var latestCPU = cpuSamples[cpuSamples.length - 1].cores;
    cpuRate.textContent = Number.isFinite(latestCPU) ? latestCPU.toFixed(latestCPU >= 10 ? 0 : 1) : '—';
    memoryCurrent.textContent = memoryText;
    renderResourceChart(cpuLine, cpuAxisMaximum, cpuDescription, cpuSamples, 'cores', function (value) { return value.toFixed(value >= 10 ? 0 : 1); }, resourceModal.getAttribute('data-resource-empty'));
    renderResourceChart(memoryLine, memoryAxisMaximum, memoryDescription, resourceHistory, 'memory', formatBytes, resourceModal.getAttribute('data-resource-empty'));
    var resourceSummary = resourceModal.getAttribute('data-resource-sampled-label') + ' ' + sampledTime + '; ' +
      resourceModal.getAttribute('data-resource-cpu-label') + ' ' + cpuText + '; ' +
      resourceModal.getAttribute('data-resource-memory-label') + ' ' + memoryText;
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

  var jobCancelModal = document.getElementById('job-cancel-modal');
  var jobCancelForm = document.querySelector('[data-job-cancel-form]');
  var jobCancelName = document.querySelector('[data-job-cancel-name]');
  var jobCancelTrigger = null;

  if (jobCancelModal && jobCancelForm && jobCancelName) {
    document.addEventListener('click', function (event) {
      var trigger = event.target.closest('[data-job-cancel]');
      if (!trigger) return;
      var sourceForm = trigger.closest('form');
      if (!sourceForm) return;
      event.preventDefault();
      jobCancelTrigger = trigger;
      jobCancelForm.setAttribute('action', sourceForm.getAttribute('action'));
      jobCancelName.textContent = '#' + trigger.getAttribute('data-job-cancel');
      document.body.classList.add('modal-open');
      jobCancelModal.showModal();
      jobCancelModal.querySelector('[data-job-cancel-close]').focus();
    });
    jobCancelModal.addEventListener('click', function (event) {
      if (event.target === jobCancelModal || event.target.closest('[data-job-cancel-close]')) jobCancelModal.close();
    });
    jobCancelModal.addEventListener('close', function () {
      document.body.classList.remove('modal-open');
      if (jobCancelTrigger) jobCancelTrigger.focus();
    });
  }

  var nodeStateModal = document.getElementById('node-state-modal');
  var nodeStateTitle = document.querySelector('[data-node-state-title]');
  var nodeStateName = document.querySelector('[data-node-state-name]');
  var nodeStateValue = document.querySelector('[data-node-state-value]');
  var nodeStateReason = document.querySelector('[data-node-state-reason]');
  var nodeStateSubmit = document.querySelector('[data-node-state-submit]');
  var nodeStateTrigger = null;

  if (nodeStateModal && nodeStateTitle && nodeStateName && nodeStateValue && nodeStateReason && nodeStateSubmit) {
    document.addEventListener('click', function (event) {
      var trigger = event.target.closest('[data-node-state-trigger]');
      if (!trigger) return;
      var state = trigger.getAttribute('data-node-state-trigger');
      nodeStateTrigger = trigger;
      nodeStateTitle.textContent = trigger.getAttribute('data-node-action-label') + ': ' + trigger.getAttribute('data-node-name');
      nodeStateName.value = trigger.getAttribute('data-node-name');
      nodeStateValue.value = state;
      nodeStateReason.value = '';
      nodeStateSubmit.className = state === 'down' ? 'node-offline-button' : 'node-drain-button';
      document.body.classList.add('modal-open');
      nodeStateModal.showModal();
      nodeStateReason.focus();
    });
    nodeStateModal.addEventListener('click', function (event) {
      if (event.target === nodeStateModal || event.target.closest('[data-node-state-close]')) nodeStateModal.close();
    });
    nodeStateModal.addEventListener('close', function () {
      nodeStateReason.value = '';
      document.body.classList.remove('modal-open');
      if (nodeStateTrigger) nodeStateTrigger.focus();
    });
  }

  var partitionEditorModal = document.getElementById('partition-editor-modal');
  var partitionDeleteModal = document.getElementById('partition-delete-modal');
  var partitionEditorForm = document.querySelector('[data-partition-editor-form]');
  var partitionNameInput = document.querySelector('[data-partition-name-input]');
  var partitionEditorTitle = document.querySelector('[data-partition-editor-title]');
  var partitionSubmit = document.querySelector('[data-partition-submit]');
  var partitionDeleteInput = document.querySelector('[data-partition-delete-input]');
  var partitionDeleteName = document.querySelector('[data-partition-delete-name]');
  var partitionEditorTrigger = null;
  var partitionDeleteTrigger = null;

  var openPartitionModal = function (modal) {
    document.body.classList.add('modal-open');
    if (!modal.open) modal.showModal();
  };

  if (partitionEditorModal && partitionDeleteModal && partitionEditorForm && partitionNameInput && partitionEditorTitle && partitionSubmit && partitionDeleteInput && partitionDeleteName) {
    var setPartitionEditor = function (name, nodeNames, isEditing) {
      partitionEditorForm.reset();
      partitionNameInput.value = name;
      partitionNameInput.readOnly = isEditing;
      partitionEditorTitle.textContent = isEditing ? partitionEditorModal.getAttribute('data-partition-update-label') : partitionEditorModal.getAttribute('data-partition-create-label');
      partitionSubmit.textContent = partitionEditorTitle.textContent;
      var selectedNames = nodeNames.reduce(function (names, nodeName) {
        names[nodeName] = true;
        return names;
      }, {});
      partitionEditorForm.querySelectorAll('[data-partition-node]').forEach(function (node) {
        node.checked = Boolean(selectedNames[node.value]);
      });
    };

    document.addEventListener('click', function (event) {
      var createTrigger = event.target.closest('[data-partition-create]');
      if (createTrigger) {
        event.preventDefault();
        partitionEditorTrigger = createTrigger;
        setPartitionEditor('', [], false);
        openPartitionModal(partitionEditorModal);
        partitionNameInput.focus();
        return;
      }

      var editTrigger = event.target.closest('[data-partition-edit]');
      if (editTrigger) {
        event.preventDefault();
        partitionEditorTrigger = editTrigger;
        var nodes = editTrigger.getAttribute('data-partition-nodes').split(', ').filter(Boolean);
        setPartitionEditor(editTrigger.getAttribute('data-partition-name'), nodes, true);
        openPartitionModal(partitionEditorModal);
        partitionNameInput.focus();
        return;
      }

      var deleteTrigger = event.target.closest('[data-partition-delete]');
      if (deleteTrigger) {
        event.preventDefault();
        partitionDeleteTrigger = deleteTrigger;
        var partitionName = deleteTrigger.getAttribute('data-partition-name');
        partitionDeleteInput.value = partitionName;
        partitionDeleteName.textContent = partitionName;
        openPartitionModal(partitionDeleteModal);
        partitionDeleteModal.querySelector('button[type="submit"]').focus();
      }
    });

    document.querySelectorAll('[data-partition-modal-close]').forEach(function (button) {
      button.addEventListener('click', function (event) {
        event.preventDefault();
        button.closest('dialog').close();
      });
    });

    [partitionEditorModal, partitionDeleteModal].forEach(function (modal) {
      modal.addEventListener('click', function (event) {
        if (event.target === modal) modal.close();
      });
      modal.addEventListener('close', function () {
        document.body.classList.remove('modal-open');
        var trigger = modal === partitionEditorModal ? partitionEditorTrigger : partitionDeleteTrigger;
        if (trigger) trigger.focus();
      });
    });

    if (partitionEditorModal.hasAttribute('data-partition-open')) {
      if (partitionEditorModal.open) partitionEditorModal.close();
      openPartitionModal(partitionEditorModal);
      partitionNameInput.focus();
    }
  }

  var terminalPage = document.querySelector('[data-terminal-page]');
  var terminalForm = document.querySelector('[data-terminal-connect]');
  if (terminalPage && terminalForm) {
    var terminalStatus = terminalPage.querySelector('[data-terminal-status]');
    var terminalOutput = terminalPage.querySelector('[data-terminal-output]');
    var terminalInput = terminalPage.querySelector('[data-terminal-input]');
    var terminalOpen = terminalPage.querySelector('[data-terminal-open]');
    var terminalSocket = null;
    var appendTerminalOutput = function (text) {
      terminalOutput.textContent = (terminalOutput.textContent + text).slice(-131072);
      terminalOutput.scrollTop = terminalOutput.scrollHeight;
    };
    var setTerminalStatus = function (text) { terminalStatus.textContent = text; };
    var closeTerminal = function () {
      if (terminalSocket) terminalSocket.close();
      terminalSocket = null;
      terminalInput.disabled = true;
    };
    terminalForm.addEventListener('submit', function (event) {
      event.preventDefault();
      closeTerminal();
      terminalOutput.textContent = '';
      terminalOpen.disabled = true;
      setTerminalStatus('Connecting...');
      fetch('/terminal/sessions', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'X-CSRF-Token': terminalForm.querySelector('input[name="_csrf"]').value },
        body: new FormData(terminalForm)
      })
        .then(function (response) {
          if (!response.ok) throw new Error('terminal connection failed');
          return response.json();
        })
        .then(function (payload) {
          terminalForm.reset();
          var scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
          terminalSocket = new WebSocket(scheme + '//' + window.location.host + '/terminal/sessions/' + encodeURIComponent(payload.session_id) + '/socket');
          terminalSocket.addEventListener('open', function () {
            terminalInput.disabled = false;
            terminalInput.focus();
            setTerminalStatus('Connected');
          });
          terminalSocket.addEventListener('message', function (message) { appendTerminalOutput(String(message.data)); });
          terminalSocket.addEventListener('close', function () {
            terminalInput.disabled = true;
            setTerminalStatus('Disconnected');
          });
          terminalSocket.addEventListener('error', function () { setTerminalStatus('Connection failed'); });
        })
        .catch(function () { setTerminalStatus('Connection failed'); })
        .then(function () { terminalOpen.disabled = false; });
    });
    terminalInput.addEventListener('keydown', function (event) {
      if (event.key !== 'Enter' || event.shiftKey || !terminalSocket || terminalSocket.readyState !== WebSocket.OPEN) return;
      event.preventDefault();
      terminalSocket.send(terminalInput.value + '\n');
      terminalInput.value = '';
    });
    window.addEventListener('beforeunload', closeTerminal);
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
