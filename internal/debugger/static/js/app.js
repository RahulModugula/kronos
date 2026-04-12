document.addEventListener('alpine:init', () => {
  Alpine.data('runDetail', (runId) => ({
    selectedStep: null,
    activeTab: 'input',
    playheadSeq: -1,
    events: [],
    steps: [],
    connected: false,

    init() {
      this.loadEvents();
      if (this.$refs.status && this.$refs.status.dataset.status === 'running') {
        this.connectSSE();
      }
    },

    async loadEvents() {
      try {
        const resp = await fetch(`/debugger/runs/${runId}/events`);
        this.events = await resp.json();
        this.buildSteps();
      } catch (e) {
        console.error('Failed to load events:', e);
      }
    },

    buildSteps() {
      const steps = {};
      for (const evt of this.events) {
        const name = evt.payload?.step_name;
        if (!name) continue;
        if (!steps[name]) steps[name] = { name, status: 'pending', input: null, output: null, error: null, duration_ms: null, attempt: 0 };
        switch (evt.event_type) {
          case 'step_started': steps[name].status = 'running'; steps[name].attempt = evt.payload?.attempt || 0; break;
          case 'step_completed': steps[name].status = 'completed'; break;
          case 'step_failed': steps[name].status = 'failed'; steps[name].error = evt.payload?.error; break;
          case 'step_input_recorded': steps[name].input = evt.payload?.input; break;
          case 'step_output_recorded': steps[name].output = evt.payload?.output; steps[name].duration_ms = evt.payload?.duration_ms; break;
        }
      }
      this.steps = Object.values(steps);
    },

    selectStep(name) {
      this.selectedStep = this.steps.find(s => s.name === name) || null;
      this.activeTab = 'input';
    },

    seekTo(seq) {
      this.playheadSeq = seq;
      const evt = this.events.find(e => e.seq === seq);
      if (evt && evt.payload?.step_name) {
        this.selectStep(evt.payload.step_name);
      }
    },

    timelineStyle() {
      if (this.events.length === 0) return 'width:0%;left:0%';
      const total = this.events.length;
      const markers = [];
      let i = 0;
      for (const evt of this.events) {
        const left = (i / (total - 1 || 1)) * 100;
        let color = 'var(--gray)';
        if (evt.event_type === 'step_completed') color = 'var(--green)';
        else if (evt.event_type === 'step_failed') color = 'var(--red)';
        else if (evt.event_type === 'step_started' || evt.event_type === 'step_output_recorded') color = 'var(--yellow)';
        else if (evt.event_type === 'run_started' || evt.event_type === 'run_completed') color = 'var(--accent)';
        markers.push(`<div class="timeline-marker" style="left:${left}%;background:${color}" @click="seekTo(${evt.seq})"></div>`);
        i++;
      }
      return markers.join('');
    },

    formatJSON(data) {
      if (!data) return '';
      try {
        const obj = typeof data === 'string' ? JSON.parse(data) : data;
        return syntaxHighlight(JSON.stringify(obj, null, 2));
      } catch {
        return String(data);
      }
    },

    connectSSE() {
      const source = new EventSource(`/debugger/runs/${runId}/live`);
      this.connected = true;
      source.onmessage = (e) => {
        const evt = JSON.parse(e.data);
        this.events.push(evt);
        this.buildSteps();
      };
      source.onerror = () => {
        this.connected = false;
        source.close();
      };
    },

    async forkFrom(stepName) {
      try {
        const resp = await fetch(`/debugger/runs/${runId}/fork`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ from_step: stepName })
        });
        const data = await resp.json();
        if (data.forked_run_id) {
          window.location.href = `/debugger/runs/${data.forked_run_id}`;
        }
      } catch (e) {
        alert('Fork failed: ' + e.message);
      }
    }
  }));

  Alpine.data('runsList', () => ({
    runs: [],
    loading: false,
    filterWorkflow: '',
    filterStatus: '',

    init() {
      this.loadRuns();
    },

    async loadRuns() {
      this.loading = true;
      const params = new URLSearchParams();
      if (this.filterWorkflow) params.set('workflow', this.filterWorkflow);
      if (this.filterStatus) params.set('status', this.filterStatus);
      try {
        const resp = await fetch(`/debugger/runs?${params}`);
        this.runs = await resp.json();
      } catch (e) {
        console.error('Failed to load runs:', e);
      }
      this.loading = false;
    }
  }));
});

function syntaxHighlight(json) {
  json = json.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  return json.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, (match) => {
    let cls = 'json-number';
    if (/^"/.test(match)) {
      cls = /:$/.test(match) ? 'json-key' : 'json-string';
    } else if (/true|false/.test(match)) {
      cls = 'json-bool';
    } else if (/null/.test(match)) {
      cls = 'json-null';
    }
    return '<span class="' + cls + '">' + match + '</span>';
  });
}
