(() => {
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? "—").replace(/[&<>"']/g, (c) => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" }[c]));
  const num = (value) => {
    if (value == null || !Number.isFinite(Number(value))) return "—";
    return Number(value).toLocaleString("zh-CN");
  };
  const formatDuration = (value, empty = "—") => {
    if (value == null || !Number.isFinite(Number(value))) return empty;
    const milliseconds = Math.max(0, Math.round(Number(value)));
    return milliseconds < 1000 ? milliseconds + " ms" : (milliseconds / 1000).toFixed(2) + " 秒";
  };
  const formatBytes = (value) => {
    if (value == null || !Number.isFinite(Number(value))) return "—";
    return (Number(value) / 1024 / 1024).toFixed(1) + " MB";
  };
  const time = (value) => value ? new Date(value).toLocaleTimeString("zh-CN") : "—";
  const date = (value) => value ? new Date(value).toLocaleString("zh-CN") : "—";
  const yesNo = (value) => value == null ? "—" : (value ? "是" : "否");
  const statusLabel = (value) => ({ success: "成功", failed: "失败", cancelled: "已取消" }[value] || value || "未知");
  const rangeLabel = (value) => ({ today: "今天", "1h": "最近 1 小时", "6h": "最近 6 小时", "24h": "最近 24 小时", "7d": "最近 7 天", "30d": "最近 30 天" }[value] || value || "当前范围");
  const windowDuration = (minutes) => {
    if (minutes == null) return "周期不可用";
    const value = Number(minutes);
    if (!Number.isFinite(value)) return "周期不可用";
    if (value === 10080) return "每周";
    if (value === 1440) return "每天";
    if (value % 60 === 0) return String(value / 60) + " 小时";
    return String(value) + " 分钟";
  };
  const json = async (path) => {
    const response = await fetch(path, { cache: "no-store" });
    if (!response.ok) throw new Error("HTTP " + response.status);
    return response.json();
  };
  const hashValue = (value) => value ? "有（" + esc(String(value).slice(0, 12)) + "…）" : "无";
  const presenceHash = (present, hash) => present ? hashValue(hash || "present") : "无";
  const effortDisplay = (item) => {
    const requested = item.requested_reasoning_effort || "—";
    const upstream = item.upstream_reasoning_effort;
    if (!upstream) return requested;
    if (requested === upstream) return requested + " ✓";
    return requested + " → " + upstream + " ⚠";
  };

  let activeTab = "overview";
  function showTab(name) {
    activeTab = name;
    document.querySelectorAll(".tab").forEach((button) => button.classList.toggle("active", button.dataset.tab === name));
    document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.toggle("active", panel.id === name));
    if (name === "requests") loadRequests({ force: true });
  }
  document.querySelectorAll(".tab").forEach((button) => button.addEventListener("click", () => showTab(button.dataset.tab)));

  function renderQuota(quota) {
    const root = $("quota-content");
    if (!quota) { root.innerHTML = '<div class="empty">官方额度尚未返回或不可用</div>'; return; }
    const windows = ["primary", "secondary"].filter((key) => quota[key]);
    root.innerHTML = windows.map((key) => {
      const item = quota[key];
      return '<div class="quota-window"><h3>' + (key === "primary" ? "主窗口" : "次窗口") + '</h3><div class="quota-stats"><div><span>已使用</span><strong>' + esc(item.used_percent ?? "—") + '%</strong></div><div><span>剩余</span><strong>' + esc(item.remaining_percent ?? "—") + '%</strong></div></div><div class="quota-meta">' + esc(windowDuration(item.window_duration_mins)) + ' · 重置 ' + esc(item.resets_at ?? "不可用") + '</div></div>';
    }).join("") || '<div class="empty">额度接口没有返回可解析窗口</div>';
    if (quota.credits) root.insertAdjacentHTML("beforeend", '<div class="quota-window"><h3>Credits 额度</h3><div class="quota-number">' + esc(quota.credits.balance ?? "—") + '</div><div class="quota-meta">' + esc(quota.plan_type ?? "计划不可用") + '</div></div>');
  }

  let overviewInFlight = false;
  async function loadOverview() {
    if (overviewInFlight) return;
    overviewInFlight = true;
    try {
      const range = $("range")?.value || "today";
      const data = await json("/dashboard/api/overview?range=" + encodeURIComponent(range));
      $("bridge-status").textContent = data.bridge_status === "running" ? "运行中" : (data.bridge_status || "未知");
      $("quota-status").textContent = ({ fresh: "最新", stale: "较旧", pending: "读取中", unavailable: "不可用" }[data.quota_status] || data.quota_status || "不可用");
      $("active-requests").textContent = num(data.active_requests);
      $("dropped").textContent = num(data.dropped_telemetry_count);
      $("writer-errors").textContent = num(data.telemetry_writer_errors);
      $("last-quota").textContent = data.last_quota_update ? time(data.last_quota_update) : "未获取";
      $("selected-range-label").textContent = "当前范围：" + rangeLabel(data.range || range);
      renderQuota(data.quota);
      const stats = data.stats || {};
      const values = [
        ["请求数", num(stats.requests)], ["成功率", Number(stats.success_rate || 0).toFixed(1) + "%"],
        ["输入 Token", num(stats.input_tokens)], ["缓存 Token", num(stats.cached_input_tokens)],
        ["请求缓存命中率", Number(stats.request_cache_hit_percent || 0).toFixed(1) + "%"],
        ["Token 缓存占比", Number(stats.token_cache_ratio_percent || 0).toFixed(1) + "%"],
        ["输出 Token", num(stats.output_tokens)], ["推理 Token", num(stats.reasoning_tokens)],
        ["TTFT 中位数", formatDuration(stats.median_ttft_ms)], ["TTFT P95", formatDuration(stats.p95_ttft_ms)],
        ["总耗时中位数", formatDuration(stats.median_duration_ms)], ["总耗时 P95", formatDuration(stats.p95_duration_ms)]
      ];
      $("summary-grid").innerHTML = values.map(([label, value]) => '<div class="summary-item"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
      const memory = data.memory || {};
      $("heap-alloc").textContent = formatBytes(memory.heap_alloc_bytes);
      $("heap-inuse").textContent = formatBytes(memory.heap_inuse_bytes);
      $("heap-sys").textContent = formatBytes(memory.heap_sys_bytes);
      $("response-store-items").textContent = num(memory.response_store_items);
      $("chat-store-items").textContent = num(memory.chat_store_items);
      $("telemetry-records").textContent = num(memory.telemetry_records);
      $("num-gc").textContent = num(memory.num_gc);
    } catch (error) {
      $("bridge-status").textContent = "面板错误";
      $("quota-status").textContent = String(error);
    } finally { overviewInFlight = false; }
  }

  let requestOffset = 0;
  const requestPageSize = 50;
  let requestInFlight = false;
  let requestGeneration = 0;
  let requestQueued = false;
  function requestsAutoRefreshEligible() {
    return activeTab === "requests" && document.visibilityState === "visible" && requestOffset === 0 && $("sort-filter").value === "newest";
  }
  function updatePagination(data) {
    $("request-page").textContent = data.total ? (String(data.offset + 1) + "–" + String(data.offset + data.data.length) + " / " + String(data.total)) : "0 / 0";
    $("request-prev").disabled = data.offset <= 0;
    $("request-next").disabled = !data.has_more;
  }
  function renderRequests(data) {
    const rows = data.data || [];
    $("request-table").innerHTML = rows.length ? rows.map((item) => {
      const statusClass = item.outcome === "success" ? "ok" : item.outcome === "cancelled" ? "warn" : "bad";
      return '<tr data-id="' + esc(item.internal_request_id) + '"><td>' + esc(time(item.started_at)) + '</td><td>' + esc(item.model) + '</td><td>' + esc(effortDisplay(item)) + '</td><td>' + num(item.input_tokens) + '</td><td>' + num(item.cached_input_tokens) + '</td><td>' + num(item.output_tokens) + '</td><td>' + num(item.reasoning_tokens) + '</td><td>' + formatDuration(item.first_upstream_event_ms) + '</td><td>' + formatDuration(item.ttft_ms, "无文本") + '</td><td>' + formatDuration(item.request_duration_ms) + '</td><td class="' + statusClass + '">' + esc(statusLabel(item.outcome)) + '</td></tr>';
    }).join("") : '<tr><td colspan="11" class="empty">当前范围没有请求。</td></tr>';
    document.querySelectorAll("#request-table tr[data-id]").forEach((row) => row.addEventListener("click", () => loadDetail(row.dataset.id)));
    updatePagination(data);
  }
  async function loadRequests({ force = false, auto = false } = {}) {
    if (requestInFlight) {
      if (force && !auto) requestQueued = true;
      return;
    }
    if (auto && !requestsAutoRefreshEligible()) return;
    const generation = requestGeneration;
    const query = new URLSearchParams({ range: $("range").value, limit: String(requestPageSize), offset: String(requestOffset), sort: $("sort-filter").value });
    const model = $("model-filter").value.trim(), effort = $("effort-filter").value, status = $("outcome-filter").value, endpoint = $("endpoint-filter").value;
    if (model) query.set("model", model); if (effort) query.set("effort", effort); if (status) query.set("status", status); if (endpoint) query.set("endpoint", endpoint);
    requestInFlight = true;
    $("request-refresh-state").textContent = auto ? "自动刷新…" : "读取中…";
    try {
      const data = await json("/dashboard/api/requests?" + query.toString());
      if (generation === requestGeneration) {
        renderRequests(data);
        $("request-refresh-state").textContent = "已更新 · " + new Date().toLocaleTimeString("zh-CN");
      }
    } catch (error) {
      // Keep the last successful rows in place to avoid list flicker on a
      // transient dashboard request failure.
      $("request-refresh-state").textContent = "读取失败：" + String(error);
    } finally {
      requestInFlight = false;
      if (requestQueued) { requestQueued = false; loadRequests({ force: true }); }
    }
  }

  function cacheDiagnostics(item) {
    const rows = [
      ["入站 prompt_cache_key", presenceHash(item.incoming_prompt_cache_key_present, item.incoming_prompt_cache_key_hash)],
      ["上游 prompt_cache_key", presenceHash(item.upstream_prompt_cache_key_present, item.upstream_prompt_cache_key_hash)],
      ["入站 Session hash", hashValue(item.incoming_session_id_hash)], ["上游 Session hash", hashValue(item.upstream_session_id_hash)],
      ["入站 Thread hash", hashValue(item.incoming_thread_id_hash)], ["上游 Thread hash", hashValue(item.upstream_thread_id_hash)],
      ["入站 x-client-request-id hash", hashValue(item.incoming_client_request_id_hash)], ["上游 x-client-request-id hash", hashValue(item.upstream_client_request_id_hash)],
      ["入站 Toolset hash", hashValue(item.tools_hash)], ["上游 Toolset hash", hashValue(item.prepared_tools_hash)],
      ["入站 Instructions hash", hashValue(item.instructions_hash)], ["上游 Instructions hash", hashValue(item.prepared_instructions_hash)],
      ["入站 Request shape hash", hashValue(item.request_shape_hash)], ["上游 Request shape hash", hashValue(item.prepared_request_shape_hash)],
      ["previous_response_id", yesNo(item.previous_response_id_present)]
    ];
    return '<details class="cache-diagnostics"><summary>缓存诊断</summary><p class="muted">这里只展示存在性和 hash 前缀，不展示原始 ID、prompt 或工具 schema，也不据此推断命中原因。</p><div class="detail-grid">' + rows.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("") + '</div></details>';
  }
  async function loadDetail(id) {
    showTab("diagnostics");
    try {
      const item = await json("/dashboard/api/requests/" + encodeURIComponent(id));
      const toolTypes = Object.entries(item.tool_types || {}).map(([key, value]) => key + ": " + value).join(", ") || "—";
      const cells = [
        ["ID", item.internal_request_id], ["开始", date(item.started_at)], ["完成", date(item.completed_at)], ["接口", item.endpoint],
        ["HTTP", String(item.http_method || "—") + " " + String(item.http_status ?? "—")], ["模型", item.model], ["结果", statusLabel(item.outcome)], ["客户端", item.client_type],
        ["SSE 流式", yesNo(item.stream)], ["请求推理强度", item.requested_reasoning_effort], ["上游推理强度", item.upstream_reasoning_effort], ["强度判定", effortDisplay(item)],
        ["请求 summary", item.requested_reasoning_summary], ["上游 summary", item.upstream_reasoning_summary], ["文本 verbosity", item.text_verbosity],
        ["请求字节", num(item.request_bytes)], ["响应字节", num(item.response_bytes)], ["工具数", item.tool_count], ["工具类型", toolTypes], ["并行工具", yesNo(item.parallel_tool_calls)],
        ["输入 Token", num(item.input_tokens)], ["缓存 Token", num(item.cached_input_tokens)], ["Cache write", num(item.cache_write_tokens)], ["输出 Token", num(item.output_tokens)], ["推理 Token", num(item.reasoning_tokens)], ["总 Token", num(item.total_tokens)],
        ["首个上游事件", formatDuration(item.first_upstream_event_ms)], ["首个 reasoning", formatDuration(item.first_reasoning_event_ms)], ["首个工具调用", formatDuration(item.first_tool_call_ms)], ["TTFT", formatDuration(item.ttft_ms, "无文本")], ["总耗时", formatDuration(item.request_duration_ms)], ["最长 SSE 间隔", formatDuration(item.longest_sse_gap_ms)],
        ["Reasoning 条目", item.reasoning_item_count], ["可读 summary 条目", item.readable_reasoning_summary_item_count], ["Summary delta", item.reasoning_summary_delta_count], ["Raw reasoning 条目", item.raw_reasoning_text_item_count], ["Encrypted 条目", item.encrypted_reasoning_item_count],
        ["Function call 数", item.function_call_count], ["Tool call 数", item.tool_call_count], ["Completed 事件", item.response_completed_count], ["Failed 事件", item.response_failed_count], ["Incomplete 事件", item.response_incomplete_count], ["上游事件", item.upstream_event_count], ["下游事件", item.downstream_event_count],
        ["Encrypted reasoning", yesNo(item.upstream_encrypted_reasoning_enabled)], ["客户端断开", yesNo(item.client_disconnected)], ["上游流错误", yesNo(item.upstream_stream_error)]
      ];
      const timeline = (item.timeline || []).map((event) => '<div class="timeline-row"><span>' + formatDuration(event.relative_ms) + '</span><span>' + esc(event.direction) + '</span><strong>' + esc(event.type) + '</strong><span>' + esc(event.item_type || event.item_id_hash || "") + '</span></div>').join("");
      const detailCells = cells.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
      $("detail").innerHTML = '<div class="detail-grid">' + detailCells + '</div>' + cacheDiagnostics(item) + '<div class="card-heading"><span>事件时间线元数据</span><span class="muted">不包含文本 payload</span></div><div class="timeline">' + (timeline || '<div class="empty">该历史记录没有保留内存事件</div>') + '</div>';
    } catch (error) { $("detail").textContent = String(error); }
  }

  function resetRequestPage() { requestOffset = 0; requestGeneration++; loadRequests({ force: true }); }
  $("refresh-overview").addEventListener("click", loadOverview);
  $("refresh-requests").addEventListener("click", () => loadRequests({ force: true }));
  $("range").addEventListener("change", () => { loadOverview(); resetRequestPage(); });
  $("model-filter").addEventListener("keydown", (event) => { if (event.key === "Enter") resetRequestPage(); });
  $("effort-filter").addEventListener("change", resetRequestPage);
  $("outcome-filter").addEventListener("change", resetRequestPage);
  $("endpoint-filter").addEventListener("change", resetRequestPage);
  $("sort-filter").addEventListener("change", resetRequestPage);
  $("request-prev").addEventListener("click", () => { requestOffset = Math.max(0, requestOffset - requestPageSize); requestGeneration++; loadRequests({ force: true }); });
  $("request-next").addEventListener("click", () => { requestOffset += requestPageSize; requestGeneration++; loadRequests({ force: true }); });
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") { loadOverview(); if (requestsAutoRefreshEligible()) loadRequests({ auto: true }); } });
  loadOverview();
  setInterval(() => { if (document.visibilityState === "visible") loadOverview(); }, 15000);
  setInterval(() => { if (requestsAutoRefreshEligible()) loadRequests({ auto: true }); }, 5000);
})();
