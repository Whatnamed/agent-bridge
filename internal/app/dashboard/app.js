(() => {
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? "—").replace(/[&<>"']/g, (c) => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" }[c]));
  const num = (value) => value == null ? "—" : Number(value).toLocaleString();
  const ms = (value) => value == null ? "—" : Number(value).toFixed(0) + " ms";
  const time = (value) => value ? new Date(value).toLocaleTimeString() : "—";
  const json = async (path) => {
    const response = await fetch(path, { cache: "no-store" });
    if (!response.ok) throw new Error("HTTP " + response.status);
    return response.json();
  };
  function showTab(name) {
    document.querySelectorAll(".tab").forEach((button) => button.classList.toggle("active", button.dataset.tab === name));
    document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.toggle("active", panel.id === name));
    if (name === "requests") loadRequests();
  }
  document.querySelectorAll(".tab").forEach((button) => button.addEventListener("click", () => showTab(button.dataset.tab)));
  function renderQuota(quota) {
    const root = $("quota-content");
    if (!quota) { root.innerHTML = '<div class="empty">官方额度尚未返回或不可用</div>'; return; }
    const windows = ["primary", "secondary"].filter((key) => quota[key]);
    root.innerHTML = windows.map((key) => {
      const item = quota[key];
      const duration = item.window_duration_mins == null ? "duration unavailable" : String(item.window_duration_mins) + " min";
      return '<div class="quota-window"><h3>' + (key[0].toUpperCase() + key.slice(1)) + ' Window</h3><div class="quota-number">' + esc(item.used_percent ?? "—") + '% used</div><div class="quota-meta">' + esc(duration) + ' · reset ' + esc(item.resets_at ?? "unavailable") + '</div></div>';
    }).join("") || '<div class="empty">额度接口返回了可解析但没有窗口字段的快照</div>';
    if (quota.credits) root.insertAdjacentHTML("beforeend", '<div class="quota-window"><h3>Credits</h3><div class="quota-number">' + esc(quota.credits.balance ?? "—") + '</div><div class="quota-meta">' + esc(quota.plan_type ?? "plan unavailable") + '</div></div>');
  }
  async function loadOverview() {
    try {
      const data = await json("/dashboard/api/overview?range=" + encodeURIComponent($("range")?.value || "today"));
      $("bridge-status").textContent = data.bridge_status || "Unknown";
      $("quota-status").textContent = data.quota_status || "unavailable";
      $("active-requests").textContent = num(data.active_requests);
      $("dropped").textContent = num(data.dropped_telemetry_count);
      $("writer-errors").textContent = num(data.telemetry_writer_errors);
      $("last-quota").textContent = data.last_quota_update ? time(data.last_quota_update) : "not fetched";
      renderQuota(data.quota);
      const stats = data.stats || {};
      const values = [
        ["Requests", num(stats.requests)], ["Success", Number(stats.success_rate || 0).toFixed(1) + "%"],
        ["Input", num(stats.input_tokens)], ["Cached", num(stats.cached_input_tokens)], ["Cache hit", Number(stats.cache_hit_percent || 0).toFixed(1) + "%"],
        ["Output", num(stats.output_tokens)], ["Reasoning", num(stats.reasoning_tokens)], ["Median TTFT", ms(stats.median_ttft_ms)],
        ["P95 TTFT", ms(stats.p95_ttft_ms)], ["P95 duration", ms(stats.p95_duration_ms)]
      ];
      $("summary-grid").innerHTML = values.map(([label, value]) => '<div class="summary-item"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
    } catch (error) {
      $("bridge-status").textContent = "Dashboard error";
      $("quota-status").textContent = String(error);
    }
  }
  async function loadRequests() {
    const query = new URLSearchParams({ range: $("range").value, limit: "100", sort: "newest" });
    const model = $("model-filter").value.trim(), status = $("outcome-filter").value;
    if (model) query.set("model", model); if (status) query.set("status", status);
    try {
      const data = await json("/dashboard/api/requests?" + query.toString());
      const rows = data.data || [];
      $("request-table").innerHTML = rows.length ? rows.map((item) => {
        const statusClass = item.outcome === "success" ? "ok" : item.outcome === "cancelled" ? "warn" : "bad";
        return '<tr data-id="' + esc(item.internal_request_id) + '"><td>' + esc(time(item.started_at)) + '</td><td>' + esc(item.model) + '</td><td>' + esc(item.requested_reasoning_effort) + '</td><td>' + num(item.input_tokens) + '</td><td>' + num(item.cached_input_tokens) + '</td><td>' + num(item.output_tokens) + '</td><td>' + num(item.reasoning_tokens) + '</td><td>' + ms(item.ttft_ms) + '</td><td>' + ms(item.request_duration_ms) + '</td><td class="' + statusClass + '">' + esc(item.outcome) + '</td></tr>';
      }).join("") : '<tr><td colspan="10" class="empty">No requests in this range.</td></tr>';
      document.querySelectorAll("#request-table tr[data-id]").forEach((row) => row.addEventListener("click", () => loadDetail(row.dataset.id)));
    } catch (error) {
      $("request-table").innerHTML = '<tr><td colspan="10" class="empty">' + esc(error) + '</td></tr>';
    }
  }
  async function loadDetail(id) {
    showTab("diagnostics");
    try {
      const item = await json("/dashboard/api/requests/" + encodeURIComponent(id));
      const cells = [
        ["ID", item.internal_request_id], ["Endpoint", item.endpoint], ["Model", item.model], ["Outcome", item.outcome],
        ["Requested effort", item.requested_reasoning_effort], ["Upstream effort", item.upstream_reasoning_effort],
        ["Summary requested", item.requested_reasoning_summary], ["Upstream summary", item.upstream_reasoning_summary],
        ["Reasoning items", item.reasoning_item_count], ["Readable summaries", item.readable_reasoning_summary_item_count],
        ["Encrypted items", item.encrypted_reasoning_item_count], ["Tool calls", item.tool_call_count],
        ["Upstream events", item.upstream_event_count], ["Downstream events", item.downstream_event_count],
        ["First upstream", ms(item.first_upstream_event_ms)], ["First reasoning", ms(item.first_reasoning_event_ms)],
        ["TTFT", ms(item.ttft_ms)], ["Longest SSE gap", ms(item.longest_sse_gap_ms)]
      ];
      const timeline = (item.timeline || []).map((event) => '<div class="timeline-row"><span>' + ms(event.relative_ms) + '</span><span>' + esc(event.direction) + '</span><strong>' + esc(event.type) + '</strong><span>' + esc(event.item_type || event.item_id_hash || "") + '</span></div>').join("");
      const detailCells = cells.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
      $("detail").innerHTML = '<div class="detail-grid">' + detailCells + '</div><div class="card-heading"><span>Event timeline metadata</span><span class="muted">text payloads omitted</span></div><div class="timeline">' + (timeline || '<div class="empty">No in-memory events</div>') + '</div>';
    } catch (error) { $("detail").textContent = String(error); }
  }
  $("refresh-overview").addEventListener("click", loadOverview);
  $("refresh-requests").addEventListener("click", loadRequests);
  $("range").addEventListener("change", () => { loadOverview(); loadRequests(); });
  $("model-filter").addEventListener("keydown", (event) => { if (event.key === "Enter") loadRequests(); });
  $("outcome-filter").addEventListener("change", loadRequests);
  loadOverview();
  setInterval(loadOverview, 15000);
})();
