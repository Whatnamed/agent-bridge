(() => {
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? "—").replace(/[&<>"']/g, (c) => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" }[c]));
  const finiteNumber = (value) => value != null && Number.isFinite(Number(value));
  const num = (value) => finiteNumber(value) ? Number(value).toLocaleString("zh-CN") : "—";
  const exact = (value) => finiteNumber(value) ? Number(value).toLocaleString("zh-CN") : "不可用";
  const compactToken = (value) => {
    if (!finiteNumber(value)) return "—";
    const number = Number(value), absolute = Math.abs(number);
    if (absolute >= 1000000000) return (number / 1000000000).toFixed(1).replace(/\.0$/, "") + "B";
    if (absolute >= 1000000) return (number / 1000000).toFixed(1).replace(/\.0$/, "") + "M";
    if (absolute >= 1000) return (number / 1000).toFixed(1).replace(/\.0$/, "") + "K";
    return String(number);
  };
  const formatDuration = (value, empty = "—") => {
    if (!finiteNumber(value)) return empty;
    const milliseconds = Math.max(0, Math.round(Number(value)));
    return milliseconds < 1000 ? milliseconds + " ms" : (milliseconds / 1000).toFixed(2) + " 秒";
  };
  const formatBytes = (value) => finiteNumber(value) ? (Number(value) / 1024 / 1024).toFixed(1) + " MB" : "—";
  const time = (value) => value ? new Date(value).toLocaleTimeString("zh-CN") : "—";
  const date = (value) => value ? new Date(value).toLocaleString("zh-CN") : "—";
  const yesNo = (value) => value == null ? "—" : (value ? "是" : "否");
  const statusLabel = (value) => ({ success: "成功", failed: "失败", cancelled: "已取消" }[value] || value || "未知");
  const rangeLabel = (value) => ({ today: "今天", "1h": "最近 1 小时", "6h": "最近 6 小时", "24h": "最近 24 小时", "7d": "最近 7 天", "30d": "最近 30 天" }[value] || value || "当前范围");
  const sourceKey = (item) => {
    if (String(item?.source || "").toLowerCase() === "codex" || String(item?.client_type || "").toLowerCase() === "codex") return "Codex";
    const value = String(item?.client_type || "Unknown");
    return ["ZCode", "DSH", "Unknown"].includes(value) ? value : "Unknown";
  };
  const sourceValueLabel = (value) => ({ All: "全部", all: "全部", Unknown: "未知", unknown: "未知" }[String(value)] || value || "—");
  const sourceLabel = (item) => sourceValueLabel(sourceKey(item));
  const sourceClass = (item) => sourceKey(item).toLowerCase();
  const providerKey = (item) => {
    const value = String(item?.provider || "").toLowerCase();
    if (value === "codex") return "Codex";
    if (value === "antigravity") return "Antigravity";
    if (String(item?.source || "").toLowerCase() === "codex") return "Codex";
    return "Unknown";
  };
  const providerValueLabel = (value) => ({ All: "全部", all: "全部", Codex: "Codex", codex: "Codex", Antigravity: "Antigravity", antigravity: "Antigravity", Unknown: "未知", unknown: "未知" }[String(value)] || value || "—");
  const providerLabel = (item) => providerValueLabel(providerKey(item));
  const recordKindLabel = (value) => ({ request: "请求", turn: "轮次" }[String(value)] || value || "—");
  const secondarySourceLabel = (value) => ({ cli: "命令行", user: "用户", subagent: "子智能体", Unknown: "未知", unknown: "未知" }[String(value)] || value || "—");
  const directionLabel = (value) => ({ upstream: "上游", downstream: "下游" }[String(value)] || value || "—");
  const eventTypeLabel = (value) => ({
    encrypted_reasoning: "加密推理",
    function_call: "函数调用",
    function_call_output: "函数调用结果",
    reasoning: "推理",
    summary_delta: "摘要增量",
    tool_call: "工具调用",
    tool_call_output: "工具调用结果"
  }[String(value)] || value || "—");
  const sourceFilter = () => $("source-filter")?.value || "all";
  const providerFilter = () => $("provider-filter")?.value || "all";
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
  const tokenRatioText = (secondary, denominator) => {
    if (!finiteNumber(secondary)) return "";
    const value = Number(secondary);
    if (value === 0) return "0%";
    if (!finiteNumber(denominator) || Number(denominator) <= 0) return "";
    return (value * 100 / Number(denominator)).toFixed(1) + "%";
  };
  const tokenCell = (primary, secondary, secondaryLabel) => {
    const primaryText = compactToken(primary), secondaryText = compactToken(secondary);
    const ratio = tokenRatioText(secondary, primary);
    return '<span class="cell-main" title="精确值：' + esc(exact(primary)) + '">' + esc(primaryText) + '</span><span class="cell-sub" title="精确值：' + esc(exact(secondary)) + '">' + esc(secondaryLabel + ' ' + secondaryText + (ratio ? ' · ' + ratio : '')) + '</span>';
  };
  const ratioText = (value) => finiteNumber(value) ? Number(value).toFixed(1) + "%" : "—";
  const durationForItem = (item) => item.duration_available === false && !Number(item.request_duration_ms) ? null : item.request_duration_ms;

  let activeTab = "overview";
  function showTab(name) {
    if (!["overview", "requests"].includes(name)) name = "overview";
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

  function renderCodexCollector(snapshot) {
    const data = snapshot || {};
    const enabled = data.enabled === true;
    $("codex-collector-status").textContent = enabled ? "运行中" : "未启用";
    $("codex-tracked-files").textContent = enabled ? num(data.tracked_files) : "—";
    $("codex-imported-turns").textContent = enabled ? num(data.imported_turns) : "—";
    $("codex-parse-unknown").textContent = enabled ? num(data.parse_errors) + " / " + num(data.unknown_events_ignored) : "—";
    const last = enabled && data.last_sync ? time(data.last_sync) : "—";
    const lag = enabled && finiteNumber(data.collector_lag_ms) ? formatDuration(data.collector_lag_ms) : "—";
    $("codex-last-sync").textContent = last + " / " + lag;
  }

  let overviewInFlight = false;
  async function loadOverview() {
    if (overviewInFlight) return;
    overviewInFlight = true;
    try {
      const range = $("range")?.value || "today";
      const query = new URLSearchParams({ range, source: sourceFilter(), provider: providerFilter() });
      const data = await json("/dashboard/api/overview?" + query.toString());
      $("bridge-status").textContent = data.bridge_status === "running" ? "运行中" : (data.bridge_status || "未知");
      $("quota-status").textContent = ({ fresh: "最新", stale: "较旧", pending: "读取中", unavailable: "不可用" }[data.quota_status] || data.quota_status || "不可用");
      $("active-requests").textContent = num(data.active_requests);
      $("dropped").textContent = num(data.dropped_telemetry_count);
      $("writer-errors").textContent = num(data.telemetry_writer_errors);
      $("last-quota").textContent = data.last_quota_update ? time(data.last_quota_update) : "未获取";
      $("selected-range-label").textContent = "当前范围：" + rangeLabel(data.range || range) + " · 来源：" + sourceValueLabel(data.source || "All") + " · 提供方：" + providerValueLabel(data.provider || "All");
      renderQuota(data.quota);
      renderCodexCollector(data.codex);
      const stats = data.stats || {};
      const values = [
        ["请求 / 轮次", num(stats.requests), num(stats.requests)], ["成功率", ratioText(stats.success_rate), ratioText(stats.success_rate)],
        ["输入 Token", compactToken(stats.input_tokens), exact(stats.input_tokens)], ["缓存 Token", compactToken(stats.cached_input_tokens), exact(stats.cached_input_tokens)],
        ["请求缓存命中率", ratioText(stats.request_cache_hit_percent), ratioText(stats.request_cache_hit_percent)], ["Token 缓存占比", ratioText(stats.token_cache_ratio_percent), ratioText(stats.token_cache_ratio_percent)],
        ["输出 Token", compactToken(stats.output_tokens), exact(stats.output_tokens)], ["推理 Token", compactToken(stats.reasoning_tokens), exact(stats.reasoning_tokens)],
        ["TTFT 中位数", formatDuration(stats.median_ttft_ms), formatDuration(stats.median_ttft_ms)], ["TTFT P95", formatDuration(stats.p95_ttft_ms), formatDuration(stats.p95_ttft_ms)],
        ["总耗时中位数", formatDuration(stats.median_duration_ms), formatDuration(stats.median_duration_ms)], ["总耗时 P95", formatDuration(stats.p95_duration_ms), formatDuration(stats.p95_duration_ms)]
      ];
      $("summary-grid").innerHTML = values.map(([label, value, precise]) => '<div class="summary-item"><span>' + label + '</span><strong title="精确值：' + esc(precise) + '">' + esc(value) + '</strong></div>').join("");
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
      return '<tr data-id="' + esc(item.internal_request_id) + '"><td>' + esc(time(item.started_at)) + '</td><td><span class="source-badge source-' + sourceClass(item) + '">' + esc(sourceLabel(item)) + '</span><span class="cell-sub">' + esc(recordKindLabel(item.record_kind || "request")) + ' · ' + esc(providerLabel(item)) + '</span></td><td title="' + esc(item.model || "不可用") + '">' + esc(item.model || "—") + '</td><td>' + esc(effortDisplay(item)) + '</td><td>' + tokenCell(item.input_tokens, item.cached_input_tokens, "缓存") + '</td><td>' + tokenCell(item.output_tokens, item.reasoning_tokens, "推理") + '</td><td>' + formatDuration(item.first_upstream_event_ms) + '</td><td>' + formatDuration(item.ttft_ms, "无文本") + '</td><td>' + formatDuration(durationForItem(item)) + '</td><td class="' + statusClass + '">' + esc(statusLabel(item.outcome)) + '</td></tr>';
    }).join("") : '<tr><td colspan="10" class="empty">当前范围没有请求或轮次。</td></tr>';
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
    const query = new URLSearchParams({ range: $("range").value, source: sourceFilter(), provider: providerFilter(), limit: String(requestPageSize), offset: String(requestOffset), sort: $("sort-filter").value });
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
      ["入站 Session Hash", hashValue(item.incoming_session_id_hash)], ["上游 Session Hash", hashValue(item.upstream_session_id_hash)],
      ["入站 Thread Hash", hashValue(item.incoming_thread_id_hash)], ["上游 Thread Hash", hashValue(item.upstream_thread_id_hash)],
      ["入站 x-client-request-id Hash", hashValue(item.incoming_client_request_id_hash)], ["上游 x-client-request-id Hash", hashValue(item.upstream_client_request_id_hash)],
      ["入站工具集 Hash", hashValue(item.tools_hash)], ["上游工具集 Hash", hashValue(item.prepared_tools_hash)],
      ["入站指令 Hash", hashValue(item.instructions_hash)], ["上游指令 Hash", hashValue(item.prepared_instructions_hash)],
      ["入站请求形状 Hash", hashValue(item.request_shape_hash)], ["上游请求形状 Hash", hashValue(item.prepared_request_shape_hash)],
      ["previous_response_id", yesNo(item.previous_response_id_present)]
    ];
    return '<details class="cache-diagnostics"><summary>缓存诊断</summary><p class="muted">这里只展示存在性和 hash 前缀，不展示原始 ID、prompt 或工具 schema，也不据此推断命中原因。</p><div class="detail-grid">' + rows.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("") + '</div></details>';
  }
  function samplingTable(item) {
    const samples = Array.isArray(item.sampling) ? item.sampling : [];
    if (!samples.length) return '<div class="empty">没有可用的采样用量</div>';
    const rows = samples.map((sample) => '<tr><td>' + esc(sample.index ?? "—") + '</td><td title="精确值：' + esc(exact(sample.input_tokens)) + '">' + esc(compactToken(sample.input_tokens)) + '</td><td title="精确值：' + esc(exact(sample.cached_input_tokens)) + '">' + esc(compactToken(sample.cached_input_tokens)) + '</td><td title="精确值：' + esc(exact(sample.output_tokens)) + '">' + esc(compactToken(sample.output_tokens)) + '</td><td title="精确值：' + esc(exact(sample.reasoning_tokens)) + '">' + esc(compactToken(sample.reasoning_tokens)) + '</td><td title="精确值：' + esc(exact(sample.total_tokens)) + '">' + esc(compactToken(sample.total_tokens)) + '</td></tr>').join("");
    return '<div class="table-wrap sampling-table"><table><thead><tr><th>#</th><th>输入</th><th>缓存</th><th>输出</th><th>推理</th><th>总计</th></tr></thead><tbody>' + rows + '</tbody></table></div>';
  }
  let detailGeneration = 0;
  function openDrawer() {
    $("detail-drawer").classList.add("open");
    $("detail-drawer").setAttribute("aria-hidden", "false");
    $("drawer-backdrop").hidden = false;
    document.body.classList.add("drawer-open");
  }
  function closeDrawer() {
    $("detail-drawer").classList.remove("open");
    $("detail-drawer").setAttribute("aria-hidden", "true");
    $("drawer-backdrop").hidden = true;
    document.body.classList.remove("drawer-open");
  }
  async function loadDetail(id) {
    const generation = ++detailGeneration;
    openDrawer();
    $("detail").innerHTML = '<div class="empty">正在读取详情…</div>';
    try {
      const item = await json("/dashboard/api/requests/" + encodeURIComponent(id));
      if (generation !== detailGeneration) return;
      const toolTypes = Object.entries(item.tool_types || {}).map(([key, value]) => key + ": " + value).join(", ") || "—";
      const cells = [
        ["来源", sourceLabel(item)], ["记录类型", recordKindLabel(item.record_kind)], ["开始", date(item.started_at)], ["完成", date(item.completed_at)],
        ["rollout Hash", hashValue(item.rollout_id_hash)], ["session Hash", hashValue(item.session_id_hash)], ["turn Hash", hashValue(item.turn_id_hash)], ["次级来源", secondarySourceLabel(item.secondary_source)],
        ["模型", item.model], ["请求模型", item.requested_model], ["实际上游模型", item.actual_upstream_model], ["提供方", item.provider], ["上下文窗口", num(item.context_window)], ["子智能体", yesNo(item.subagent)],
        ["结果", statusLabel(item.outcome)], ["客户端", sourceValueLabel(item.client_type)], ["接口", item.endpoint], ["HTTP", String(item.http_method || "—") + " " + String(item.http_status ?? "—")],
        ["SSE 流式", yesNo(item.stream)], ["请求推理强度", item.requested_reasoning_effort], ["上游推理强度", item.upstream_reasoning_effort], ["强度判定", effortDisplay(item)],
        ["输入 Token", exact(item.input_tokens)], ["缓存 Token", exact(item.cached_input_tokens)], ["缓存写入 Token", exact(item.cache_write_tokens)], ["输出 Token", exact(item.output_tokens)], ["推理 Token", exact(item.reasoning_tokens)], ["总 Token", exact(item.total_tokens)],
        ["采样次数", item.sampling_count || (item.sampling || []).length], ["首个上游事件", formatDuration(item.first_upstream_event_ms)], ["首个推理", formatDuration(item.first_reasoning_event_ms)], ["首个工具调用", formatDuration(item.first_tool_call_ms)], ["TTFT", formatDuration(item.ttft_ms, "无文本")], ["总耗时", formatDuration(durationForItem(item))],
        ["推理条目", item.reasoning_item_count], ["可读摘要条目", item.readable_reasoning_summary_item_count], ["摘要增量", item.reasoning_summary_delta_count], ["函数调用次数", item.function_call_count], ["工具调用次数", item.tool_call_count], ["上游事件", item.upstream_event_count], ["下游事件", item.downstream_event_count],
        ["加密推理", yesNo(item.upstream_encrypted_reasoning_enabled)], ["客户端断开", yesNo(item.client_disconnected)], ["上游流错误", yesNo(item.upstream_stream_error)]
      ];
      const detailCells = cells.map(([label, value]) => '<div class="detail-cell"><span>' + label + '</span><strong>' + esc(value) + '</strong></div>').join("");
      const timeline = (item.timeline || []).map((event) => '<div class="timeline-row"><span>' + formatDuration(event.relative_ms) + '</span><span>' + esc(directionLabel(event.direction)) + '</span><strong>' + esc(eventTypeLabel(event.type)) + '</strong><span>' + esc(event.item_type ? eventTypeLabel(event.item_type) : (event.item_id_hash || "")) + '</span></div>').join("");
      const sampling = item.source === "codex" ? '<div class="card-heading"><span>Codex 采样明细</span><span class="muted">仅用量，不含 rollout 内容</span></div>' + samplingTable(item) : "";
      $("detail").innerHTML = '<div class="detail-grid">' + detailCells + '</div>' + sampling + cacheDiagnostics(item) + '<div class="card-heading"><span>事件时间线元数据</span><span class="muted">不包含文本 payload</span></div><div class="timeline">' + (timeline || '<div class="empty">该记录没有保留内存事件</div>') + '</div>';
    } catch (error) { if (generation === detailGeneration) $("detail").textContent = String(error); }
  }

  function resetRequestPage() { requestOffset = 0; requestGeneration++; loadRequests({ force: true }); }
  $("refresh-overview").addEventListener("click", loadOverview);
  $("refresh-requests").addEventListener("click", () => loadRequests({ force: true }));
  $("range").addEventListener("change", () => { loadOverview(); resetRequestPage(); });
  $("source-filter").addEventListener("change", () => { loadOverview(); resetRequestPage(); });
  $("provider-filter").addEventListener("change", () => { loadOverview(); resetRequestPage(); });
  $("model-filter").addEventListener("keydown", (event) => { if (event.key === "Enter") resetRequestPage(); });
  $("effort-filter").addEventListener("change", resetRequestPage);
  $("outcome-filter").addEventListener("change", resetRequestPage);
  $("endpoint-filter").addEventListener("change", resetRequestPage);
  $("sort-filter").addEventListener("change", resetRequestPage);
  $("request-prev").addEventListener("click", () => { requestOffset = Math.max(0, requestOffset - requestPageSize); requestGeneration++; loadRequests({ force: true }); });
  $("request-next").addEventListener("click", () => { requestOffset += requestPageSize; requestGeneration++; loadRequests({ force: true }); });
  $("detail-close").addEventListener("click", closeDrawer);
  $("drawer-backdrop").addEventListener("click", closeDrawer);
  document.addEventListener("keydown", (event) => { if (event.key === "Escape" && $("detail-drawer").classList.contains("open")) closeDrawer(); });
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") { loadOverview(); if (requestsAutoRefreshEligible()) loadRequests({ auto: true }); } });
  loadOverview();
  setInterval(() => { if (document.visibilityState === "visible") loadOverview(); }, 15000);
  setInterval(() => { if (requestsAutoRefreshEligible()) loadRequests({ auto: true }); }, 5000);
})();
