(() => {
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? "—").replace(/[&<>"']/g, (c) => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" }[c]));
  const num = (value) => value == null ? "—" : Number(value).toLocaleString();
  const ms = (value) => value == null ? "—" : Number(value).toFixed(0) + " ms";
  const time = (value) => value ? new Date(value).toLocaleTimeString() : "—";
  const date = (value) => value ? new Date(value).toLocaleString() : "—";
  const yesNo = (value) => value == null ? "—" : (value ? "Yes" : "No");
  const windowDuration = (minutes) => {
    if (minutes == null) return "duration unavailable";
    const value = Number(minutes);
    if (!Number.isFinite(value)) return "duration unavailable";
    if (value === 10080) return "Weekly";
    if (value === 1440) return "Daily";
    if (value % 60 === 0) return String(value / 60) + "h";
    return String(value) + "m";
  };
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
      const duration = windowDuration(item.window_duration_mins);
      return '<div class="quota-window"><h3>' + (key[0].toUpperCase() + key.slice(1)) + ' Window</h3><div class="quota-stats"><div><span>Used</span><strong>' + esc(item.used_percent ?? "—") + '%</strong></div><div><span>Remaining</span><strong>' + esc(item.remaining_percent ?? "—") + '%</strong></div></div><div class="quota-meta">' + esc(duration) + ' window · reset ' + esc(item.resets_at ?? "unavailable") + '</div></div>';
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
         ["P95 TTFT", ms(stats.p95_ttft_ms)], ["Median duration", ms(stats.median_duration_ms)], ["P95 duration", ms(stats.p95_duration_ms)]
      ];
      $("summary-grid").innerHTML = values.map(([label, value]) => '<div class="summary-item"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
    } catch (error) {
      $("bridge-status").textContent = "Dashboard error";
      $("quota-status").textContent = String(error);
    }
  }
  let requestOffset = 0;
  const requestPageSize = 50;
  function updatePagination(data) {
    $("request-page").textContent = data.total ? (String(data.offset + 1) + "–" + String(data.offset + data.data.length) + " / " + String(data.total)) : "0 / 0";
    $("request-prev").disabled = data.offset <= 0;
    $("request-next").disabled = !data.has_more;
  }
  async function loadRequests() {
    const query = new URLSearchParams({ range: $("range").value, limit: String(requestPageSize), offset: String(requestOffset), sort: $("sort-filter").value });
    const model = $("model-filter").value.trim(), effort = $("effort-filter").value, status = $("outcome-filter").value, endpoint = $("endpoint-filter").value;
    if (model) query.set("model", model); if (effort) query.set("effort", effort); if (status) query.set("status", status); if (endpoint) query.set("endpoint", endpoint);
    try {
      const data = await json("/dashboard/api/requests?" + query.toString());
      const rows = data.data || [];
      $("request-table").innerHTML = rows.length ? rows.map((item) => {
        const statusClass = item.outcome === "success" ? "ok" : item.outcome === "cancelled" ? "warn" : "bad";
        return '<tr data-id="' + esc(item.internal_request_id) + '"><td>' + esc(time(item.started_at)) + '</td><td>' + esc(item.model) + '</td><td>' + esc(item.requested_reasoning_effort) + '</td><td>' + num(item.input_tokens) + '</td><td>' + num(item.cached_input_tokens) + '</td><td>' + num(item.output_tokens) + '</td><td>' + num(item.reasoning_tokens) + '</td><td>' + ms(item.ttft_ms) + '</td><td>' + ms(item.request_duration_ms) + '</td><td class="' + statusClass + '">' + esc(item.outcome) + '</td></tr>';
      }).join("") : '<tr><td colspan="10" class="empty">No requests in this range.</td></tr>';
      document.querySelectorAll("#request-table tr[data-id]").forEach((row) => row.addEventListener("click", () => loadDetail(row.dataset.id)));
      updatePagination(data);
    } catch (error) {
      $("request-table").innerHTML = '<tr><td colspan="10" class="empty">' + esc(error) + '</td></tr>';
      $("request-page").textContent = "—";
    }
  }
  async function loadDetail(id) {
    showTab("diagnostics");
    try {
      const item = await json("/dashboard/api/requests/" + encodeURIComponent(id));
      const toolTypes = Object.entries(item.tool_types || {}).map(([key, value]) => key + ": " + value).join(", ") || "—";
      const cells = [
        ["ID", item.internal_request_id], ["Started", date(item.started_at)], ["Completed", date(item.completed_at)], ["Endpoint", item.endpoint],
        ["HTTP", String(item.http_method || "—") + " " + String(item.http_status ?? "—")], ["Model", item.model], ["Outcome", item.outcome], ["Client", item.client_type],
        ["Stream", yesNo(item.stream)], ["Requested effort", item.requested_reasoning_effort], ["Upstream effort", item.upstream_reasoning_effort],
        ["Summary requested", item.requested_reasoning_summary], ["Upstream summary", item.upstream_reasoning_summary], ["Text verbosity", item.text_verbosity],
        ["Request bytes", num(item.request_bytes)], ["Response bytes", num(item.response_bytes)], ["Tool count", item.tool_count], ["Tool types", toolTypes], ["Parallel tools", yesNo(item.parallel_tool_calls)],
        ["Input tokens", num(item.input_tokens)], ["Cached input", num(item.cached_input_tokens)], ["Cache write", num(item.cache_write_tokens)], ["Output tokens", num(item.output_tokens)], ["Reasoning tokens", num(item.reasoning_tokens)], ["Total tokens", num(item.total_tokens)],
        ["First upstream", ms(item.first_upstream_event_ms)], ["First reasoning", ms(item.first_reasoning_event_ms)], ["First tool", ms(item.first_tool_call_ms)], ["TTFT", ms(item.ttft_ms)], ["Duration", ms(item.request_duration_ms)], ["Longest SSE gap", ms(item.longest_sse_gap_ms)],
        ["Reasoning items", item.reasoning_item_count], ["Readable summaries", item.readable_reasoning_summary_item_count], ["Summary deltas", item.reasoning_summary_delta_count], ["Raw reasoning items", item.raw_reasoning_text_item_count], ["Encrypted items", item.encrypted_reasoning_item_count],
        ["Function calls", item.function_call_count], ["Tool calls", item.tool_call_count], ["Completed events", item.response_completed_count], ["Failed events", item.response_failed_count], ["Incomplete events", item.response_incomplete_count], ["Upstream events", item.upstream_event_count], ["Downstream events", item.downstream_event_count],
        ["Encrypted enabled", yesNo(item.upstream_encrypted_reasoning_enabled)], ["Client disconnected", yesNo(item.client_disconnected)], ["Stream error", yesNo(item.upstream_stream_error)]
      ];
      const timeline = (item.timeline || []).map((event) => '<div class="timeline-row"><span>' + ms(event.relative_ms) + '</span><span>' + esc(event.direction) + '</span><strong>' + esc(event.type) + '</strong><span>' + esc(event.item_type || event.item_id_hash || "") + '</span></div>').join("");
      const detailCells = cells.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
      $("detail").innerHTML = '<div class="detail-grid">' + detailCells + '</div><div class="card-heading"><span>Event timeline metadata</span><span class="muted">text payloads omitted</span></div><div class="timeline">' + (timeline || '<div class="empty">No in-memory events</div>') + '</div>';
    } catch (error) { $("detail").textContent = String(error); }
  }
  $("refresh-overview").addEventListener("click", loadOverview);
  $("refresh-requests").addEventListener("click", loadRequests);
  function resetRequestPage() { requestOffset = 0; loadRequests(); }
  $("range").addEventListener("change", () => { loadOverview(); resetRequestPage(); });
  $("model-filter").addEventListener("keydown", (event) => { if (event.key === "Enter") resetRequestPage(); });
  $("effort-filter").addEventListener("change", resetRequestPage);
  $("outcome-filter").addEventListener("change", resetRequestPage);
  $("endpoint-filter").addEventListener("change", resetRequestPage);
  $("sort-filter").addEventListener("change", resetRequestPage);
  $("request-prev").addEventListener("click", () => { requestOffset = Math.max(0, requestOffset - requestPageSize); loadRequests(); });
  $("request-next").addEventListener("click", () => { requestOffset += requestPageSize; loadRequests(); });
  loadOverview();
  setInterval(loadOverview, 15000);
})();
