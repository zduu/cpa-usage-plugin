// cpausage i18n — follows CPA management panel language (cli-proxy-language localStorage key).
// Supports: zh-CN, zh-TW, en, ru. Falls back to zh-CN.

var I18N_MAP = {
  'zh-CN': {
    // ---- HTML static ----
    page_title: '用量统计',
    dashboard_heading: '使用统计',
    time_range: '时间范围',
    range_7h: '最近7小时',
    range_24h: '最近24小时',
    range_7d: '最近7天',
    range_all: '全部',
    export_data: '导出数据',
    import_data: '导入数据',
    refresh_btn: '刷新',
    loading: '正在加载...',

    // ---- stat cards ----
    total_requests: '总请求数',
    success_requests: '成功请求',
    failure_requests: '失败请求',
    avg_latency: '平均用时',
    total_tokens: '总 token 数',
    cached_tokens: '缓存命中 token',
    cache_write_tokens: '缓存创建 token',
    reasoning_tokens: '思考 token',
    rpm: '每分钟请求',
    rpm_meta: '最近30分钟请求',
    total_cost: '总花费',
    cost_meta: '按本页模型价格估算',

    // ---- health panel ----
    health_title: '服务健康监测',
    health_subtle: '最近7天，15分钟一个网格；绿色代表成功率高，红色代表失败较多。',
    success_rate: '成功率',
    success: '成功',
    failure: '失败',
    legend_less: '少',
    legend_more: '多',
    legend_empty: '灰色为无请求',

    // ---- price panel ----
    price_title: '模型价格设置',
    price_unit: '单位：US$/M token',
    model_label: '模型',
    input_price: '输入价格',
    output_price: '输出价格',
    cache_price: '缓存命中价格',
    cache_write_price: '缓存创建价格',
    cache_placeholder: '默认同输入',
    cache_write_placeholder: '未知时默认 0',
    save: '保存',
    edit: '编辑',
    delete: '删除',
    no_prices: '暂无价格设置，设置后会显示估算花费。',

    // ---- API stats ----
    api_stats_title: 'API 详细统计',
    api_stats_subtle: '按调用 CPA 服务的 API key 聚合。',
    sort_requests: '请求次数',
    sort_tokens: 'Token数量',
    sort_cost: '总花费',
    client_api_filter: 'API Key 筛选',
    client_api_selected: '已选中',
    client_api_current_filter: '当前筛选：{0}',
    client_api_filter_failed: 'API Key 筛选数据加载失败，请稍后重试。',
    client_api_filter_compat_unavailable: '兼容数据模式无法可靠应用 API Key 筛选。',
    no_api_data: '暂无 API key 请求数据',

    // ---- upstream API ----
    upstream_title: '上游接口统计',
    upstream_subtle: '按上游提供商和来源聚合',
    upstream_select_none: '暂无上游接口',
    no_upstream_data: '暂无接口数据',

    // ---- upstream detail ----
    upstream_detail_title: '上游接口详情',
    upstream_detail_select_hint: '选择一个上游接口查看模型、来源、错误和最近请求。',
    upstream_api_label: '上游接口',
    input_model_placeholder: '输入或选择模型',
    export_api_csv: '导出当前接口表格',
    export_api_json: '导出当前接口明细',

    // ---- model stats ----
    model_stats_title: '模型统计',
    model_stats_subtle: '请求数、token、用时、成功率、缓存命中、花费和实际单价',

    // ---- events ----
    events_title: '请求事件明细',
    events_subtle: '可按模型、来源、凭证筛选并导出。列表保持滚动查看。',
    clear_filters: '清除筛选',
    export_csv: '导出表格',
    export_json: '导出明细',
    events_latency_unit: '用时 / 首字按时长显示 ms/s',

    // ---- table headers ----
    col_time: '时间',
    col_model: '模型',
    col_source: '来源',
    col_credential: '凭证',
    col_result: '结果',
    col_latency: '用时 / 首字',
    col_input: '非缓存输入',
    col_output: '输出',
    col_thinking: '思考',
    col_cache: '缓存命中',
    col_cache_write: '缓存创建',
    col_total: '总计',
    col_api: '接口',
    col_requests: '请求',
    col_success_rate: '成功率',
    col_tokens: 'token',
    col_avg_latency: '平均用时',
    col_models: '模型',
    col_cost: '花费',
    col_status: '状态码',

    // ---- filters ----
    filter_all_models: '全部模型',
    filter_all_sources: '全部来源',
    filter_all_credentials: '全部凭证',

    // ---- dynamic text ----
    updated_at: '更新于',
    loading_api_detail: '正在加载接口请求明细...',
    loading_source_data: '正在加载来源数据...',
    compat_mode: '兼容模式',
    no_events: '暂无请求事件',
    no_model_data: '暂无模型数据',
    no_source_data: '暂无来源数据',
    no_failures: '暂无失败请求',
    no_detail: '暂无请求明细',
    no_detail_data: '暂无接口详情',
    detail_load_failed: '请求明细加载失败',
    detail_load_failed_msg: '请求明细加载失败：',

    // ---- storage status ----
    storage_enabled: '持久化已开启',
    storage_disabled: '未开启持久化',
    storage_error: '持久化异常',
    write_queue_full: '队列已满',
    write_queue_backlog: '队列积压',
    write_queued: '正在排队',
    write_slow: '写入偏慢',
    write_normal: '正常',
    write_pressure: '写入压力',

    // ---- events export / import ----
    events_count: '共 {0} 条，显示 {1} 条',
    export_truncated: '导出已截断：共 {0} 条，已导出 {1} 条',
    export_failed: '导出失败',
    export_failed_msg: '导出失败：',
    export_job_timeout: '导出任务超时',
    export_job_failed: '导出任务失败',
    export_no_id: '导出任务未返回 ID',
    import_complete: '导入完成：新增 {0}，跳过 {1}，过期忽略 {2}',
    import_failed: '导入失败：',
    import_no_key: '未读取到管理登录状态，请回到管理中心重新登录并勾选记住登录。',

    // ---- errors ----
    load_usage_failed: '加载用量统计失败',
    response_not_json: '响应不是有效 JSON',
    empty_response: '返回空数据',
    request_failed: '请求失败',
    request_failed_colon: '请求失败：',
    unknown_error: '未知错误',
    no_304_cache: '服务端返回 304，但本地没有可复用缓存',
    unknown_api: '未知 API',
    unknown_interface: '未知接口',
    unknown_source: '未知来源',
    credential: '凭证',
    no_body_returned: '未返回错误内容',

    // ---- storage batch ----
    storage_batch_title: '最近批量写入 {0} 条',
    storage_batch_duration: '耗时',
    storage_batch_avg: '平均耗时',
    storage_batch_p95: '耗时 p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: '最长排队',
    storage_batch_avg_wait: '平均排队',
    storage_batch_wait_p95: '排队 p95',
    storage_pending_snapshot: ' 条记录待写入快照',
    storage_pending_flush: ' 条记录待刷新到文件',
    storage_pending_sync: ' 条记录待同步到磁盘',
    storage_pending_queue_no_capacity: ' 条记录等待后台写入',
    storage_pending_queue: ' 条记录等待后台写入，队列容量 {0}',

    // ---- price actions ----
    price_save_failed: '保存价格失败：',
    price_delete_failed: '删除价格失败：',

    // ---- metrics ----
    requests_label: '请求数',
    model_count: '模型数',
    source_count: '来源',
    recent_requests_label: '最近1小时请求',
    total_tokens_label: '总 token 数',
    token_count: '总 token',

    // ---- empty states ----
    no_requests: '无请求',
    no_response: '-',

    // ---- misc ----
    success_label: '成功',
    failure_label: '失败',
    unknown: '未知',
    model_distribution: '模型分布',
    source_distribution: '来源分布',
    error_stats: '错误统计',
    recent_requests: '最近请求',

    // ---- trend chart ----
    trend_title: '用量趋势',
    trend_subtle: '按日聚合的趋势图，可切换查看不同指标。',
    trend_metric_label: '指标',
    trend_daily_cost: '每日成本',
    trend_daily_requests: '每日请求',
    trend_daily_tokens: '每日 token',
    trend_daily_rpm: '每日平均 RPM',
    no_trend_data: '暂无趋势数据',

    // ---- model stats extra ----
    col_cache_rate: '缓存命中率',
    col_cost_per_m: '实际单价',
    cost_per_m_unit: '/M token',
  },

  'zh-TW': {
    page_title: '用量統計',
    dashboard_heading: '使用統計',
    time_range: '時間範圍',
    range_7h: '最近7小時',
    range_24h: '最近24小時',
    range_7d: '最近7天',
    range_all: '全部',
    export_data: '匯出資料',
    import_data: '匯入資料',
    refresh_btn: '重新整理',
    loading: '正在載入...',

    total_requests: '總請求數',
    success_requests: '成功請求',
    failure_requests: '失敗請求',
    avg_latency: '平均用時',
    total_tokens: '總 token 數',
    cached_tokens: '快取命中 token',
    cache_write_tokens: '快取建立 token',
    reasoning_tokens: '思考 token',
    rpm: '每分鐘請求',
    rpm_meta: '最近30分鐘請求',
    total_cost: '總花費',
    cost_meta: '依本頁模型價格估算',

    health_title: '服務健康監測',
    health_subtle: '最近7天，15分鐘一個網格；綠色代表成功率高，紅色代表失敗較多。',
    success_rate: '成功率',
    success: '成功',
    failure: '失敗',
    legend_less: '少',
    legend_more: '多',
    legend_empty: '灰色為無請求',

    price_title: '模型價格設定',
    price_unit: '單位：US$/M token',
    model_label: '模型',
    input_price: '輸入價格',
    output_price: '輸出價格',
    cache_price: '快取命中價格',
    cache_write_price: '快取建立價格',
    cache_placeholder: '預設同輸入',
    cache_write_placeholder: '未知時預設為 0',
    save: '儲存',
    edit: '編輯',
    delete: '刪除',
    no_prices: '暫無價格設定，設定後會顯示估算花費。',

    api_stats_title: 'API 詳細統計',
    api_stats_subtle: '按呼叫 CPA 服務的 API key 聚合。',
    sort_requests: '請求次數',
    sort_tokens: 'Token數量',
    sort_cost: '總花費',
    client_api_filter: 'API Key 篩選',
    client_api_selected: '已選取',
    client_api_current_filter: '目前篩選：{0}',
    client_api_filter_failed: 'API Key 篩選資料載入失敗，請稍後重試。',
    client_api_filter_compat_unavailable: '相容資料模式無法可靠套用 API Key 篩選。',
    no_api_data: '暫無 API key 請求資料',

    upstream_title: '上游介面統計',
    upstream_subtle: '按上游提供商和來源聚合',
    upstream_select_none: '暫無上游介面',
    no_upstream_data: '暫無介面資料',

    upstream_detail_title: '上游介面詳情',
    upstream_detail_select_hint: '選擇一個上游介面查看模型、來源、錯誤和最近請求。',
    upstream_api_label: '上游介面',
    input_model_placeholder: '輸入或選擇模型',
    export_api_csv: '匯出目前介面表格',
    export_api_json: '匯出目前介面明細',

    model_stats_title: '模型統計',
    model_stats_subtle: '請求數、token、用時、成功率、快取命中、花費和實際單價',

    events_title: '請求事件明細',
    events_subtle: '可按模型、來源、憑證篩選並匯出。列表保持捲動查看。',
    clear_filters: '清除篩選',
    export_csv: '匯出表格',
    export_json: '匯出明細',
    events_latency_unit: '用時 / 首字依時長顯示 ms/s',

    col_time: '時間',
    col_model: '模型',
    col_source: '來源',
    col_credential: '憑證',
    col_result: '結果',
    col_latency: '用時 / 首字',
    col_input: '非快取輸入',
    col_output: '輸出',
    col_thinking: '思考',
    col_cache: '快取命中',
    col_cache_write: '快取建立',
    col_total: '總計',
    col_api: '介面',
    col_requests: '請求',
    col_success_rate: '成功率',
    col_tokens: 'token',
    col_avg_latency: '平均用時',
    col_models: '模型',
    col_cost: '花費',
    col_status: '狀態碼',

    filter_all_models: '全部模型',
    filter_all_sources: '全部來源',
    filter_all_credentials: '全部憑證',

    updated_at: '更新於',
    loading_api_detail: '正在載入介面請求明細...',
    loading_source_data: '正在載入來源資料...',
    compat_mode: '相容模式',
    no_events: '暫無請求事件',
    no_model_data: '暫無模型資料',
    no_source_data: '暫無來源資料',
    no_failures: '暫無失敗請求',
    no_detail: '暫無請求明細',
    no_detail_data: '暫無介面詳情',
    detail_load_failed: '請求明細載入失敗',
    detail_load_failed_msg: '請求明細載入失敗：',

    storage_enabled: '持久化已開啟',
    storage_disabled: '未開啟持久化',
    storage_error: '持久化異常',
    write_queue_full: '佇列已滿',
    write_queue_backlog: '佇列積壓',
    write_queued: '正在排隊',
    write_slow: '寫入偏慢',
    write_normal: '正常',
    write_pressure: '寫入壓力',

    events_count: '共 {0} 條，顯示 {1} 條',
    export_truncated: '匯出已截斷：共 {0} 條，已匯出 {1} 條',
    export_failed: '匯出失敗',
    export_failed_msg: '匯出失敗：',
    export_job_timeout: '匯出任務逾時',
    export_job_failed: '匯出任務失敗',
    export_no_id: '匯出任務未返回 ID',
    import_complete: '匯入完成：新增 {0}，跳過 {1}，過期忽略 {2}',
    import_failed: '匯入失敗：',
    import_no_key: '未讀取到管理登入狀態，請回到管理中心重新登入並勾選記住登入。',

    load_usage_failed: '載入用量統計失敗',
    response_not_json: '回應不是有效 JSON',
    empty_response: '返回空資料',
    request_failed: '請求失敗',
    request_failed_colon: '請求失敗：',
    unknown_error: '未知錯誤',
    no_304_cache: '伺服器返回 304，但本地沒有可復用快取',
    unknown_api: '未知 API',
    unknown_interface: '未知介面',
    unknown_source: '未知來源',
    credential: '憑證',
    no_body_returned: '未返回錯誤內容',

    storage_batch_title: '最近批次寫入 {0} 條',
    storage_batch_duration: '耗時',
    storage_batch_avg: '平均耗時',
    storage_batch_p95: '耗時 p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: '最長排隊',
    storage_batch_avg_wait: '平均排隊',
    storage_batch_wait_p95: '排隊 p95',
    storage_pending_snapshot: ' 條記錄待寫入快照',
    storage_pending_flush: ' 條記錄待刷新到檔案',
    storage_pending_sync: ' 條記錄待同步到磁碟',
    storage_pending_queue_no_capacity: ' 條記錄等待背景寫入',
    storage_pending_queue: ' 條記錄等待背景寫入，佇列容量 {0}',

    price_save_failed: '儲存價格失敗：',
    price_delete_failed: '刪除價格失敗：',

    requests_label: '請求數',
    model_count: '模型數',
    source_count: '來源',
    recent_requests_label: '最近1小時請求',
    total_tokens_label: '總 token 數',
    token_count: '總 token',

    no_requests: '無請求',
    no_response: '-',

    success_label: '成功',
    failure_label: '失敗',
    unknown: '未知',
    model_distribution: '模型分布',
    source_distribution: '來源分布',
    error_stats: '錯誤統計',
    recent_requests: '最近請求',

    trend_title: '用量趨勢',
    trend_subtle: '按日聚合的趨勢圖，可切換查看不同指標。',
    trend_metric_label: '指標',
    trend_daily_cost: '每日成本',
    trend_daily_requests: '每日請求',
    trend_daily_tokens: '每日 token',
    trend_daily_rpm: '每日平均 RPM',
    no_trend_data: '暫無趨勢資料',

    col_cache_rate: '快取命中率',
    col_cost_per_m: '實際單價',
    cost_per_m_unit: '/M token',
  },

  'en': {
    page_title: 'Usage Statistics',
    dashboard_heading: 'Usage Statistics',
    time_range: 'Time Range',
    range_7h: 'Last 7 Hours',
    range_24h: 'Last 24 Hours',
    range_7d: 'Last 7 Days',
    range_all: 'All',
    export_data: 'Export',
    import_data: 'Import',
    refresh_btn: 'Refresh',
    loading: 'Loading...',

    total_requests: 'Total Requests',
    success_requests: 'Success requests',
    failure_requests: 'Failure requests',
    avg_latency: 'Avg Duration',
    total_tokens: 'Total Tokens',
    cached_tokens: 'Cache Hit Tokens',
    cache_write_tokens: 'Cache Creation Tokens',
    reasoning_tokens: 'Reasoning Tokens',
    rpm: 'RPM',
    rpm_meta: 'Recent 30 min requests',
    total_cost: 'Total Cost',
    cost_meta: 'Estimated with model prices on this page',

    health_title: 'Service Health',
    health_subtle: 'Last 7 days, 15-min slots. Green = high success rate, red = failures.',
    success_rate: 'Success Rate',
    success: 'Success',
    failure: 'Failure',
    legend_less: 'Few',
    legend_more: 'Many',
    legend_empty: 'Gray = no requests',

    price_title: 'Model Prices',
    price_unit: 'Unit: US$/M tokens',
    model_label: 'Model',
    input_price: 'Input Price',
    output_price: 'Output Price',
    cache_price: 'Cache Hit Price',
    cache_write_price: 'Cache Creation Price',
    cache_placeholder: 'Default to input',
    cache_write_placeholder: 'Defaults to 0 when unknown',
    save: 'Save',
    edit: 'Edit',
    delete: 'Delete',
    no_prices: 'No prices set. Estimated cost will be shown after setting prices.',

    api_stats_title: 'API Stats',
    api_stats_subtle: 'Grouped by the API key used to call CPA.',
    sort_requests: 'Requests',
    sort_tokens: 'Tokens',
    sort_cost: 'Cost',
    client_api_filter: 'Filter by API Key',
    client_api_selected: 'Selected',
    client_api_current_filter: 'Current filter: {0}',
    client_api_filter_failed: 'Failed to load API key filtered data. Please retry.',
    client_api_filter_compat_unavailable: 'API key filtering is unavailable in compatibility data mode.',
    no_api_data: 'No API key request data',

    upstream_title: 'Upstream APIs',
    upstream_subtle: 'Grouped by upstream provider and source',
    upstream_select_none: 'No upstream APIs',
    no_upstream_data: 'No API data',

    upstream_detail_title: 'Upstream API Detail',
    upstream_detail_select_hint: 'Select an upstream API to view models, sources, errors and recent requests.',
    upstream_api_label: 'Upstream API',
    input_model_placeholder: 'Enter or select model',
    export_api_csv: 'Export API Table',
    export_api_json: 'Export API Details',

    model_stats_title: 'Model Stats',
    model_stats_subtle: 'Requests, tokens, duration, success rate, cache hit, cost and unit cost',

    events_title: 'Request Events',
    events_subtle: 'Filter by model, source or credential. Export supported.',
    clear_filters: 'Clear Filters',
    export_csv: 'Export CSV',
    export_json: 'Export JSON',
    events_latency_unit: 'Duration / first token shown in ms/s',

    col_time: 'Time',
    col_model: 'Model',
    col_source: 'Source',
    col_credential: 'Credential',
    col_result: 'Result',
    col_latency: 'Duration / First Token',
    col_input: 'Uncached input',
    col_output: 'Output',
    col_thinking: 'Thinking',
    col_cache: 'Cache Hit',
    col_cache_write: 'Cache Creation',
    col_total: 'Total',
    col_api: 'API',
    col_requests: 'Requests',
    col_success_rate: 'Success Rate',
    col_tokens: 'Tokens',
    col_avg_latency: 'Avg Duration',
    col_models: 'Models',
    col_cost: 'Cost',
    col_status: 'Status',

    filter_all_models: 'All Models',
    filter_all_sources: 'All Sources',
    filter_all_credentials: 'All Credentials',

    updated_at: 'Updated at',
    loading_api_detail: 'Loading API request details...',
    loading_source_data: 'Loading source data...',
    compat_mode: 'Compat Mode',
    no_events: 'No request events',
    no_model_data: 'No model data',
    no_source_data: 'No source data',
    no_failures: 'No failures',
    no_detail: 'No details',
    no_detail_data: 'No API detail',
    detail_load_failed: 'Detail load failed',
    detail_load_failed_msg: 'Detail load failed: ',

    storage_enabled: 'Storage enabled',
    storage_disabled: 'Storage disabled',
    storage_error: 'Storage error',
    write_queue_full: 'Queue full',
    write_queue_backlog: 'Queue backlog',
    write_queued: 'Queued',
    write_slow: 'Write slow',
    write_normal: 'Normal',
    write_pressure: 'Write Pressure',

    events_count: 'Total {0}, showing {1}',
    export_truncated: 'Export truncated: total {0}, exported {1}',
    export_failed: 'Export failed',
    export_failed_msg: 'Export failed: ',
    export_job_timeout: 'Export job timeout',
    export_job_failed: 'Export job failed',
    export_no_id: 'Export job returned no ID',
    import_complete: 'Import complete: added {0}, skipped {1}, ignored {2}',
    import_failed: 'Import failed: ',
    import_no_key: 'No management login state. Return to management panel and log in with "remember me" checked.',

    load_usage_failed: 'Failed to load usage stats',
    response_not_json: 'Response is not valid JSON',
    empty_response: 'Empty response',
    request_failed: 'Request failed',
    request_failed_colon: 'Request failed: ',
    unknown_error: 'Unknown error',
    no_304_cache: 'Server returned 304 but no local cache to reuse',
    unknown_api: 'Unknown API',
    unknown_interface: 'Unknown interface',
    unknown_source: 'Unknown source',
    credential: 'Credential',
    no_body_returned: 'No error body returned',

    storage_batch_title: 'Last batch wrote {0} records',
    storage_batch_duration: 'Duration',
    storage_batch_avg: 'Avg',
    storage_batch_p95: 'p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: 'Max wait',
    storage_batch_avg_wait: 'Avg wait',
    storage_batch_wait_p95: 'Wait p95',
    storage_pending_snapshot: ' records pending snapshot',
    storage_pending_flush: ' records pending flush',
    storage_pending_sync: ' records pending sync',
    storage_pending_queue_no_capacity: ' records pending write',
    storage_pending_queue: ' records pending write, queue capacity {0}',

    price_save_failed: 'Save price failed: ',
    price_delete_failed: 'Delete price failed: ',

    requests_label: 'Requests',
    model_count: 'Models',
    source_count: 'Sources',
    recent_requests_label: 'Last hour requests',
    total_tokens_label: 'Total tokens',
    token_count: 'Total tokens',

    no_requests: 'No requests',
    no_response: '-',

    success_label: 'Success',
    failure_label: 'Failure',
    unknown: 'Unknown',
    model_distribution: 'Model Distribution',
    source_distribution: 'Source Distribution',
    error_stats: 'Error Stats',
    recent_requests: 'Recent Requests',

    trend_title: 'Usage Trends',
    trend_subtle: 'Aggregated trend chart with switchable metrics.',
    trend_metric_label: 'Metric',
    trend_daily_cost: 'Daily Cost',
    trend_daily_requests: 'Daily Requests',
    trend_daily_tokens: 'Daily Tokens',
    trend_daily_rpm: 'Daily Avg RPM',
    no_trend_data: 'No trend data',

    col_cache_rate: 'Cache Hit Rate',
    col_cost_per_m: 'Unit Cost',
    cost_per_m_unit: '/M token',
  },

  'ru': {
    page_title: 'Статистика использования',
    dashboard_heading: 'Статистика использования',
    time_range: 'Период',
    range_7h: 'Последние 7 часов',
    range_24h: 'Последние 24 часа',
    range_7d: 'Последние 7 дней',
    range_all: 'Всё',
    export_data: 'Экспорт',
    import_data: 'Импорт',
    refresh_btn: 'Обновить',
    loading: 'Загрузка...',

    total_requests: 'Всего запросов',
    success_requests: 'Успешно',
    failure_requests: 'Ошибок',
    avg_latency: 'Среднее время',
    total_tokens: 'Всего токенов',
    cached_tokens: 'Токены попадания в кэш',
    cache_write_tokens: 'Токены создания кэша',
    reasoning_tokens: 'Токены рассуждения',
    rpm: 'Запросов/мин',
    rpm_meta: 'За последние 30 мин',
    total_cost: 'Общая стоимость',
    cost_meta: 'Оценка по ценам моделей',

    health_title: 'Здоровье сервиса',
    health_subtle: 'Последние 7 дней, интервалы по 15 мин. Зелёный = высокий успех, красный = много ошибок.',
    success_rate: 'Успешность',
    success: 'Успешно',
    failure: 'Ошибок',
    legend_less: 'Мало',
    legend_more: 'Много',
    legend_empty: 'Серый = нет запросов',

    price_title: 'Цены моделей',
    price_unit: 'Ед.: US$/M токенов',
    model_label: 'Модель',
    input_price: 'Цена ввода',
    output_price: 'Цена вывода',
    cache_price: 'Цена попадания в кэш',
    cache_write_price: 'Цена создания кэша',
    cache_placeholder: 'По умолч. как ввод',
    cache_write_placeholder: 'Если неизвестно, по умолчанию 0',
    save: 'Сохранить',
    edit: 'Ред.',
    delete: 'Удалить',
    no_prices: 'Цены не заданы. Стоимость будет показана после установки цен.',

    api_stats_title: 'Статистика API',
    api_stats_subtle: 'Сгруппировано по API-ключу для вызова CPA.',
    sort_requests: 'Запросы',
    sort_tokens: 'Токены',
    sort_cost: 'Стоимость',
    client_api_filter: 'Фильтр по API-ключу',
    client_api_selected: 'Выбрано',
    client_api_current_filter: 'Текущий фильтр: {0}',
    client_api_filter_failed: 'Не удалось загрузить данные по выбранному API-ключу. Повторите попытку.',
    client_api_filter_compat_unavailable: 'Фильтр по API-ключу недоступен в режиме совместимости.',
    no_api_data: 'Нет данных по API-ключам',

    upstream_title: 'Входящие API',
    upstream_subtle: 'Сгруппировано по провайдеру и источнику',
    upstream_select_none: 'Нет входящих API',
    no_upstream_data: 'Нет данных',

    upstream_detail_title: 'Детали входящего API',
    upstream_detail_select_hint: 'Выберите API для просмотра моделей, источников, ошибок и последних запросов.',
    upstream_api_label: 'Входящий API',
    input_model_placeholder: 'Введите или выберите модель',
    export_api_csv: 'Экспорт таблицы API',
    export_api_json: 'Экспорт деталей API',

    model_stats_title: 'Статистика моделей',
    model_stats_subtle: 'Запросы, токены, время, успешность, кэш, стоимость и удельная цена',

    events_title: 'События запросов',
    events_subtle: 'Фильтр по модели, источнику или учётным данным. Поддерживается экспорт.',
    clear_filters: 'Сбросить',
    export_csv: 'Экспорт CSV',
    export_json: 'Экспорт JSON',
    events_latency_unit: 'Время / первый токен в мс/с',

    col_time: 'Время',
    col_model: 'Модель',
    col_source: 'Источник',
    col_credential: 'Учётные данные',
    col_result: 'Результат',
    col_latency: 'Время / первый токен',
    col_input: 'Ввод без кэша',
    col_output: 'Вывод',
    col_thinking: 'Рассуждение',
    col_cache: 'Попадание в кэш',
    col_cache_write: 'Создание кэша',
    col_total: 'Всего',
    col_api: 'API',
    col_requests: 'Запросы',
    col_success_rate: 'Успешность',
    col_tokens: 'Токены',
    col_avg_latency: 'Ср. время',
    col_models: 'Модели',
    col_cost: 'Стоимость',
    col_status: 'Статус',

    filter_all_models: 'Все модели',
    filter_all_sources: 'Все источники',
    filter_all_credentials: 'Все учётные данные',

    updated_at: 'Обновлено',
    loading_api_detail: 'Загрузка деталей запросов API...',
    loading_source_data: 'Загрузка данных источников...',
    compat_mode: 'Режим совм.',
    no_events: 'Нет событий',
    no_model_data: 'Нет данных по моделям',
    no_source_data: 'Нет данных по источникам',
    no_failures: 'Нет ошибок',
    no_detail: 'Нет деталей',
    no_detail_data: 'Нет деталей API',
    detail_load_failed: 'Ошибка загрузки деталей',
    detail_load_failed_msg: 'Ошибка загрузки деталей: ',

    storage_enabled: 'Персистентность включена',
    storage_disabled: 'Персистентность выключена',
    storage_error: 'Ошибка персистентности',
    write_queue_full: 'Очередь заполнена',
    write_queue_backlog: 'Задолженность очереди',
    write_queued: 'В очереди',
    write_slow: 'Медленная запись',
    write_normal: 'Нормально',
    write_pressure: 'Нагрузка записи',

    events_count: 'Всего {0}, показано {1}',
    export_truncated: 'Экспорт обрезан: всего {0}, экспортировано {1}',
    export_failed: 'Ошибка экспорта',
    export_failed_msg: 'Ошибка экспорта: ',
    export_job_timeout: 'Таймаут задачи экспорта',
    export_job_failed: 'Задача экспорта не удалась',
    export_no_id: 'Задача экспорта не вернула ID',
    import_complete: 'Импорт завершён: добавлено {0}, пропущено {1}, проигнорировано {2}',
    import_failed: 'Ошибка импорта: ',
    import_no_key: 'Состояние входа не найдено. Вернитесь в панель управления и войдите с опцией "запомнить".',

    load_usage_failed: 'Не удалось загрузить статистику',
    response_not_json: 'Ответ не является JSON',
    empty_response: 'Пустой ответ',
    request_failed: 'Ошибка запроса',
    request_failed_colon: 'Ошибка запроса: ',
    unknown_error: 'Неизвестная ошибка',
    no_304_cache: 'Сервер вернул 304, но локальный кэш отсутствует',
    unknown_api: 'Неизвестный API',
    unknown_interface: 'Неизвестный интерфейс',
    unknown_source: 'Неизвестный источник',
    credential: 'Учётные данные',
    no_body_returned: 'Тело ошибки не возвращено',

    storage_batch_title: 'Последняя пакетная запись: {0} записей',
    storage_batch_duration: 'Длит.',
    storage_batch_avg: 'Средн.',
    storage_batch_p95: 'p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: 'Макс. ожидание',
    storage_batch_avg_wait: 'Средн. ожидание',
    storage_batch_wait_p95: 'Ожидание p95',
    storage_pending_snapshot: ' записей ожидают снимка',
    storage_pending_flush: ' записей ожидают сброса',
    storage_pending_sync: ' записей ожидают синхронизации',
    storage_pending_queue_no_capacity: ' записей ожидают записи',
    storage_pending_queue: ' записей ожидают записи, ёмкость очереди {0}',

    price_save_failed: 'Ошибка сохранения цены: ',
    price_delete_failed: 'Ошибка удаления цены: ',

    requests_label: 'Запросы',
    model_count: 'Модели',
    source_count: 'Источники',
    recent_requests_label: 'Запросы за последний час',
    total_tokens_label: 'Всего токенов',
    token_count: 'Всего токенов',

    no_requests: 'Нет запросов',
    no_response: '-',

    success_label: 'Успешно',
    failure_label: 'Ошибка',
    unknown: 'Неизвестно',
    model_distribution: 'Распределение моделей',
    source_distribution: 'Распределение источников',
    error_stats: 'Статистика ошибок',
    recent_requests: 'Последние запросы',

    trend_title: 'Тренды использования',
    trend_subtle: 'Агрегированный тренд с переключаемыми метриками.',
    trend_metric_label: 'Метрика',
    trend_daily_cost: 'Стоимость по дням',
    trend_daily_requests: 'Запросы по дням',
    trend_daily_tokens: 'Токены по дням',
    trend_daily_rpm: 'Средн. RPM по дням',
    no_trend_data: 'Нет данных тренда',

    col_cache_rate: 'Попадание в кэш',
    col_cost_per_m: 'Цена за ед.',
    cost_per_m_unit: '/M токенов',
  },
};

// Format locale mapping
var FORMAT_LOCALES = {
  'zh-CN': 'zh-CN',
  'zh-TW': 'zh-TW',
  'en': 'en-US',
  'ru': 'ru-RU'
};

function getFormatLocale() {
  return FORMAT_LOCALES[I18N_LANG] || 'zh-CN';
}

// ---- i18n runtime ----

var I18N_LANG = 'zh-CN';

function languageFromStorageValue(value) {
  if (!value) return '';
  if (I18N_MAP[value]) return value;
  try {
    var parsed = JSON.parse(value);
    var lang = parsed && parsed.state && parsed.state.language;
    if (!lang && parsed && typeof parsed.language === 'string') lang = parsed.language;
    if (!lang && typeof parsed === 'string') lang = parsed;
    return lang && I18N_MAP[lang] ? lang : '';
  } catch (e) {
    return '';
  }
}

function detectCPALanguage() {
  try {
    if (window.parent && window.parent !== window) {
      try {
        var parentLang = languageFromStorageValue(window.parent.localStorage.getItem('cli-proxy-language'));
        if (parentLang) return parentLang;
      } catch (e) { /* cross-origin */ }
    }
    var stored = languageFromStorageValue(localStorage.getItem('cli-proxy-language'));
    if (stored) return stored;
    var nav = (typeof navigator !== 'undefined' && navigator.language) || 'zh-CN';
    var short = nav.split('-')[0];
    if (nav === 'zh-TW' || nav === 'zh-HK') return 'zh-TW';
    if (short === 'zh') return 'zh-CN';
    if (nav === 'ru' || short === 'ru') return 'ru';
    return 'en';
  } catch (e) { return 'zh-CN'; }
}

function t(key) {
  var args = Array.prototype.slice.call(arguments, 1);
  var dict = I18N_MAP[I18N_LANG] || I18N_MAP['zh-CN'];
  var template = dict[key];
  if (!template) template = I18N_MAP['zh-CN'][key];
  if (!template) return key;
  if (!args.length) return template;
  return template.replace(/\{(\d+)\}/g, function(match, idx) {
    var val = args[Number(idx)];
    return val != null ? String(val) : match;
  });
}

function applyI18N() {
  if (document.documentElement) {
    document.documentElement.lang = I18N_LANG;
  }
  var i, el, key;
  var elements = document.querySelectorAll('[data-i18n]');
  for (i = 0; i < elements.length; i++) {
    el = elements[i];
    key = el.getAttribute('data-i18n');
    if (key) el.textContent = t(key);
  }
  var placeholders = document.querySelectorAll('[data-i18n-placeholder]');
  for (i = 0; i < placeholders.length; i++) {
    el = placeholders[i];
    key = el.getAttribute('data-i18n-placeholder');
    if (key) el.placeholder = t(key);
  }
}

(function() {
  try {
    I18N_LANG = detectCPALanguage();
    if (typeof document !== 'undefined' && document.body) {
      applyI18N();
    } else if (typeof document !== 'undefined' && document.addEventListener) {
      document.addEventListener('DOMContentLoaded', function() { applyI18N(); });
    }
    if (typeof window.addEventListener === 'function') {
      window.addEventListener('storage', function(e) {
        if (e.key !== 'cli-proxy-language') return;
        var newLang = detectCPALanguage();
        if (newLang !== I18N_LANG) {
          I18N_LANG = newLang;
          applyI18N();
          if (typeof rerender === 'function') {
            rerender({ refreshEvents: false, refreshApiDetail: false });
          }
        }
      });
    }
    var pollParentInterval = setInterval(function() {
      var current = detectCPALanguage();
      if (current !== I18N_LANG) {
        I18N_LANG = current;
        applyI18N();
        if (typeof rerender === 'function') {
          rerender({ refreshEvents: false, refreshApiDetail: false });
        }
      }
    }, 2000);
    if (typeof window.addEventListener === 'function') {
      window.addEventListener('beforeunload', function() { clearInterval(pollParentInterval); });
    }
  } catch (e) { /* i18n failure should not break dashboard */ }
})();

if (typeof module !== 'undefined' && module.exports) {
  module.exports = { I18N_MAP: I18N_MAP };
}
