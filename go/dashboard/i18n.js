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
    avg_latency: '平均延迟',
    total_tokens: '总 token 数',
    cached_tokens: '缓存 token',
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
    cache_price: '缓存价格',
    cache_placeholder: '默认同输入',
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
    no_api_data: '暂无 API key 请求数据',

    // ---- upstream API ----
    upstream_title: '上游接口统计',
    upstream_subtle: '按上游提供商和来源聚合',
    upstream_select_none: '暂无上游接口',
    no_upstream_data: '暂无接口数据',

    // ---- upstream detail ----
    upstream_detail_title: '上游接口详情',
    upstream_detail_select_hint: '选择一个上游接口查看模型、来源、错误和最近请求。',
    export_api_csv: '导出当前接口表格',
    export_api_json: '导出当前接口明细',

    // ---- model stats ----
    model_stats_title: '模型统计',
    model_stats_subtle: '请求数、token、平均延迟、成功率和估算花费',

    // ---- events ----
    events_title: '请求事件明细',
    events_subtle: '可按模型、来源、凭证筛选并导出。列表保持滚动查看。',
    clear_filters: '清除筛选',
    export_csv: '导出表格',
    export_json: '导出明细',
    events_latency_unit: '延迟单位：毫秒',

    // ---- table headers ----
    col_time: '时间',
    col_model: '模型',
    col_source: '来源',
    col_credential: '凭证',
    col_result: '结果',
    col_latency: '延迟',
    col_input: '输入',
    col_output: '输出',
    col_thinking: '思考',
    col_cache: '缓存',
    col_total: '总计',
    col_api: '接口',
    col_requests: '请求',
    col_success_rate: '成功率',
    col_tokens: 'token',
    col_avg_latency: '平均延迟',
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
    storage_pending_snapshot: '条记录待写入快照',
    storage_pending_flush: '条记录待刷新到文件',
    storage_pending_sync: '条记录待同步到磁盘',
    storage_pending_queue: '条记录等待后台写入，队列容量 {0}',

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
    avg_latency: '平均延遲',
    total_tokens: '總 token 數',
    cached_tokens: '快取 token',
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
    cache_price: '快取價格',
    cache_placeholder: '預設同輸入',
    save: '儲存',
    edit: '編輯',
    delete: '刪除',
    no_prices: '暫無價格設定，設定後會顯示估算花費。',

    api_stats_title: 'API 詳細統計',
    api_stats_subtle: '按呼叫 CPA 服務的 API key 聚合。',
    sort_requests: '請求次數',
    sort_tokens: 'Token數量',
    sort_cost: '總花費',
    no_api_data: '暫無 API key 請求資料',

    upstream_title: '上游介面統計',
    upstream_subtle: '按上游提供商和來源聚合',
    upstream_select_none: '暫無上游介面',
    no_upstream_data: '暫無介面資料',

    upstream_detail_title: '上游介面詳情',
    upstream_detail_select_hint: '選擇一個上游介面檢視模型、來源、錯誤和最近請求。',
    export_api_csv: '匯出當前介面表格',
    export_api_json: '匯出當前介面明細',

    model_stats_title: '模型統計',
    model_stats_subtle: '請求數、token、平均延遲、成功率和估算花費',

    events_title: '請求事件明細',
    events_subtle: '可按模型、來源、憑證篩選並匯出。列表保持捲動檢視。',
    clear_filters: '清除篩選',
    export_csv: '匯出表格',
    export_json: '匯出明細',
    events_latency_unit: '延遲單位：毫秒',

    col_time: '時間',
    col_model: '模型',
    col_source: '來源',
    col_credential: '憑證',
    col_result: '結果',
    col_latency: '延遲',
    col_input: '輸入',
    col_output: '輸出',
    col_thinking: '思考',
    col_cache: '快取',
    col_total: '總計',
    col_api: '介面',
    col_requests: '請求',
    col_success_rate: '成功率',
    col_tokens: 'token',
    col_avg_latency: '平均延遲',
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
    no_304_cache: '伺服器回傳 304，但本地沒有可複用快取',
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
    storage_pending_snapshot: '條記錄待寫入快照',
    storage_pending_flush: '條記錄待刷新到檔案',
    storage_pending_sync: '條記錄待同步到磁碟',
    storage_pending_queue: '條記錄等待背景寫入，佇列容量 {0}',

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
    model_distribution: '模型分佈',
    source_distribution: '來源分佈',
    error_stats: '錯誤統計',
    recent_requests: '最近請求',
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
    success_requests: 'Success',
    failure_requests: 'Failure',
    avg_latency: 'Avg Latency',
    total_tokens: 'Total Tokens',
    cached_tokens: 'Cached Tokens',
    reasoning_tokens: 'Reasoning Tokens',
    rpm: 'Requests/min',
    rpm_meta: 'Last 30 min',
    total_cost: 'Total Cost',
    cost_meta: 'Estimated using model prices',

    health_title: 'Service Health',
    health_subtle: 'Last 7 days, 15-minute slots. Green = high success rate, red = high failure rate.',
    success_rate: 'Success Rate',
    success: 'Success',
    failure: 'Failure',
    legend_less: 'Low',
    legend_more: 'High',
    legend_empty: 'Gray = no requests',

    price_title: 'Model Pricing',
    price_unit: 'Unit: US$/M token',
    model_label: 'Model',
    input_price: 'Input Price',
    output_price: 'Output Price',
    cache_price: 'Cache Price',
    cache_placeholder: 'Same as input',
    save: 'Save',
    edit: 'Edit',
    delete: 'Delete',
    no_prices: 'No prices set. Set prices to see cost estimates.',

    api_stats_title: 'API Key Stats',
    api_stats_subtle: 'Grouped by API key calling CPA.',
    sort_requests: 'Requests',
    sort_tokens: 'Tokens',
    sort_cost: 'Cost',
    no_api_data: 'No API key request data',

    upstream_title: 'Upstream API Stats',
    upstream_subtle: 'Grouped by upstream provider and source',
    upstream_select_none: 'No upstream API',
    no_upstream_data: 'No upstream API data',

    upstream_detail_title: 'Upstream API Detail',
    upstream_detail_select_hint: 'Select an upstream API to view models, sources, errors, and recent requests.',
    export_api_csv: 'Export API CSV',
    export_api_json: 'Export API JSON',

    model_stats_title: 'Model Stats',
    model_stats_subtle: 'Requests, tokens, avg latency, success rate, and estimated cost',

    events_title: 'Request Events',
    events_subtle: 'Filter by model, source, or credential and export. Scroll to browse.',
    clear_filters: 'Clear Filters',
    export_csv: 'Export CSV',
    export_json: 'Export JSON',
    events_latency_unit: 'Latency unit: ms',

    col_time: 'Time',
    col_model: 'Model',
    col_source: 'Source',
    col_credential: 'Credential',
    col_result: 'Result',
    col_latency: 'Latency',
    col_input: 'Input',
    col_output: 'Output',
    col_thinking: 'Thinking',
    col_cache: 'Cache',
    col_total: 'Total',
    col_api: 'API',
    col_requests: 'Requests',
    col_success_rate: 'Success Rate',
    col_tokens: 'Tokens',
    col_avg_latency: 'Avg Latency',
    col_models: 'Models',
    col_cost: 'Cost',
    col_status: 'Status',

    filter_all_models: 'All Models',
    filter_all_sources: 'All Sources',
    filter_all_credentials: 'All Credentials',

    updated_at: 'Updated at',
    loading_api_detail: 'Loading API detail...',
    loading_source_data: 'Loading source data...',
    compat_mode: 'compat mode',
    no_events: 'No request events',
    no_model_data: 'No model data',
    no_source_data: 'No source data',
    no_failures: 'No failed requests',
    no_detail: 'No request detail',
    no_detail_data: 'No API detail',
    detail_load_failed: 'Failed to load detail',
    detail_load_failed_msg: 'Failed to load detail: ',

    storage_enabled: 'Storage enabled',
    storage_disabled: 'Storage disabled',
    storage_error: 'Storage error',
    write_queue_full: 'Queue full',
    write_queue_backlog: 'Queue backlog',
    write_queued: 'Queued',
    write_slow: 'Write slow',
    write_normal: 'Normal',
    write_pressure: 'Write pressure',

    events_count: '{0} total, showing {1}',
    export_truncated: 'Export truncated: {0} total, {1} exported',
    export_failed: 'Export failed',
    export_failed_msg: 'Export failed: ',
    export_job_timeout: 'Export job timed out',
    export_job_failed: 'Export job failed',
    export_no_id: 'Export job returned no ID',
    import_complete: 'Import complete: {0} added, {1} skipped, {2} expired ignored',
    import_failed: 'Import failed: ',
    import_no_key: 'Management key not found. Please return to the management center, log in, and enable "Remember login".',

    load_usage_failed: 'Failed to load usage statistics',
    response_not_json: 'Response is not valid JSON',
    empty_response: 'Empty response',
    request_failed: 'Request failed',
    request_failed_colon: 'Request failed: ',
    unknown_error: 'Unknown error',
    no_304_cache: 'Server returned 304 but no local cache available',
    unknown_api: 'Unknown API',
    unknown_interface: 'Unknown interface',
    unknown_source: 'Unknown source',
    credential: 'Credential',
    no_body_returned: 'No error body returned',

    storage_batch_title: 'Last batch write: {0} records',
    storage_batch_duration: 'took',
    storage_batch_avg: 'avg',
    storage_batch_p95: 'p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: 'max queue wait',
    storage_batch_avg_wait: 'avg queue wait',
    storage_batch_wait_p95: 'queue p95',
    storage_pending_snapshot: ' records pending snapshot',
    storage_pending_flush: ' records pending flush',
    storage_pending_sync: ' records pending sync',
    storage_pending_queue: ' records pending write, queue capacity {0}',

    price_save_failed: 'Failed to save price: ',
    price_delete_failed: 'Failed to delete price: ',

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
  },

  'ru': {
    page_title: 'Статистика использования',
    dashboard_heading: 'Статистика использования',
    time_range: 'Период',
    range_7h: 'Последние 7 ч.',
    range_24h: 'Последние 24 ч.',
    range_7d: 'Последние 7 дн.',
    range_all: 'Всё',
    export_data: 'Экспорт',
    import_data: 'Импорт',
    refresh_btn: 'Обновить',
    loading: 'Загрузка...',

    total_requests: 'Всего запросов',
    success_requests: 'Успешно',
    failure_requests: 'Ошибок',
    avg_latency: 'Средняя задержка',
    total_tokens: 'Всего токенов',
    cached_tokens: 'Кэшировано',
    reasoning_tokens: 'Токенов размышления',
    rpm: 'Запросов/мин',
    rpm_meta: 'Последние 30 мин',
    total_cost: 'Общая стоимость',
    cost_meta: 'Оценка по ценам моделей',

    health_title: 'Мониторинг сервиса',
    health_subtle: 'Последние 7 дней, интервалы 15 мин. Зелёный = высокая успешность, красный = много ошибок.',
    success_rate: 'Успешность',
    success: 'Успешно',
    failure: 'Ошибок',
    legend_less: 'Мало',
    legend_more: 'Много',
    legend_empty: 'Серый = нет запросов',

    price_title: 'Цены моделей',
    price_unit: 'Единица: US$/M токенов',
    model_label: 'Модель',
    input_price: 'Цена ввода',
    output_price: 'Цена вывода',
    cache_price: 'Цена кэша',
    cache_placeholder: 'Как ввод',
    save: 'Сохранить',
    edit: 'Изменить',
    delete: 'Удалить',
    no_prices: 'Цены не заданы. Установите цены для оценки стоимости.',

    api_stats_title: 'Статистика API ключей',
    api_stats_subtle: 'Сгруппировано по API ключу, вызывающему CPA.',
    sort_requests: 'Запросы',
    sort_tokens: 'Токены',
    sort_cost: 'Стоимость',
    no_api_data: 'Нет данных API ключей',

    upstream_title: 'Статистика API',
    upstream_subtle: 'Сгруппировано по провайдеру и источнику',
    upstream_select_none: 'Нет API',
    no_upstream_data: 'Нет данных API',

    upstream_detail_title: 'Детали API',
    upstream_detail_select_hint: 'Выберите API для просмотра моделей, источников, ошибок и последних запросов.',
    export_api_csv: 'Экспорт CSV',
    export_api_json: 'Экспорт JSON',

    model_stats_title: 'Статистика моделей',
    model_stats_subtle: 'Запросы, токены, средняя задержка, успешность и оценка стоимости',

    events_title: 'События запросов',
    events_subtle: 'Фильтр по модели, источнику или учётным данным. Прокрутка для просмотра.',
    clear_filters: 'Сброс фильтров',
    export_csv: 'Экспорт CSV',
    export_json: 'Экспорт JSON',
    events_latency_unit: 'Единица задержки: мс',

    col_time: 'Время',
    col_model: 'Модель',
    col_source: 'Источник',
    col_credential: 'Учётные данные',
    col_result: 'Результат',
    col_latency: 'Задержка',
    col_input: 'Ввод',
    col_output: 'Вывод',
    col_thinking: 'Размышление',
    col_cache: 'Кэш',
    col_total: 'Всего',
    col_api: 'API',
    col_requests: 'Запросы',
    col_success_rate: 'Успешность',
    col_tokens: 'Токены',
    col_avg_latency: 'Ср. задержка',
    col_models: 'Модели',
    col_cost: 'Стоимость',
    col_status: 'Статус',

    filter_all_models: 'Все модели',
    filter_all_sources: 'Все источники',
    filter_all_credentials: 'Все учётные данные',

    updated_at: 'Обновлено',
    loading_api_detail: 'Загрузка деталей API...',
    loading_source_data: 'Загрузка источников...',
    compat_mode: 'режим совместимости',
    no_events: 'Нет событий запросов',
    no_model_data: 'Нет данных моделей',
    no_source_data: 'Нет данных источников',
    no_failures: 'Нет ошибок',
    no_detail: 'Нет деталей запросов',
    no_detail_data: 'Нет деталей API',
    detail_load_failed: 'Ошибка загрузки деталей',
    detail_load_failed_msg: 'Ошибка загрузки деталей: ',

    storage_enabled: 'Хранилище включено',
    storage_disabled: 'Хранилище отключено',
    storage_error: 'Ошибка хранилища',
    write_queue_full: 'Очередь заполнена',
    write_queue_backlog: 'Задержка очереди',
    write_queued: 'В очереди',
    write_slow: 'Медленная запись',
    write_normal: 'Нормально',
    write_pressure: 'Нагрузка записи',

    events_count: 'Всего {0}, показано {1}',
    export_truncated: 'Экспорт усечён: всего {0}, экспортировано {1}',
    export_failed: 'Ошибка экспорта',
    export_failed_msg: 'Ошибка экспорта: ',
    export_job_timeout: 'Тайм-аут задачи экспорта',
    export_job_failed: 'Задача экспорта не удалась',
    export_no_id: 'Задача экспорта не вернула ID',
    import_complete: 'Импорт завершён: добавлено {0}, пропущено {1}, устарело {2}',
    import_failed: 'Ошибка импорта: ',
    import_no_key: 'Ключ управления не найден. Вернитесь в центр управления, войдите и включите «Запомнить вход».',

    load_usage_failed: 'Не удалось загрузить статистику',
    response_not_json: 'Ответ не является валидным JSON',
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
    storage_batch_duration: 'заняло',
    storage_batch_avg: 'среднее',
    storage_batch_p95: 'p95',
    storage_batch_p99: 'p99',
    storage_batch_wait: 'макс. ожидание',
    storage_batch_avg_wait: 'среднее ожидание',
    storage_batch_wait_p95: 'ожидание p95',
    storage_pending_snapshot: ' записей ожидают снимка',
    storage_pending_flush: ' записей ожидают сброса',
    storage_pending_sync: ' записей ожидают синхронизации',
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
  }
};

// ---- i18n runtime ----

var I18N_LANG = 'zh-CN';

function detectCPALanguage() {
  try {
    // Strategy 1: parent iframe (CPA management panel)
    if (window.parent && window.parent !== window) {
      try {
        var parentLang = window.parent.localStorage.getItem('cli-proxy-language');
        if (parentLang && I18N_MAP[parentLang]) return parentLang;
      } catch (e) { /* cross-origin */ }
    }
    // Strategy 2: shared localStorage
    var stored = localStorage.getItem('cli-proxy-language');
    if (stored && I18N_MAP[stored]) return stored;
    // Strategy 3: browser language
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
  if (!template) {
    // Fallback to zh-CN
    template = I18N_MAP['zh-CN'][key];
  }
  if (!template) return key;
  if (!args.length) return template;
  return template.replace(/\{(\d+)\}/g, function(match, idx) {
    var val = args[Number(idx)];
    return val != null ? String(val) : match;
  });
}

// Apply i18n to all [data-i18n] elements
function applyI18N() {
  var elements = document.querySelectorAll('[data-i18n]');
  for (var i = 0; i < elements.length; i++) {
    var el = elements[i];
    var key = el.getAttribute('data-i18n');
    if (key) el.textContent = t(key);
  }
  // Also handle elements with data-i18n-placeholder for inputs
  var placeholders = document.querySelectorAll('[data-i18n-placeholder]');
  for (var i = 0; i < placeholders.length; i++) {
    var el = placeholders[i];
    var key = el.getAttribute('data-i18n-placeholder');
    if (key) el.placeholder = t(key);
  }
}

// Initialize i18n
(function() {
  try {
    I18N_LANG = detectCPALanguage();

    // Listen for language changes via storage event
    if (typeof window.addEventListener === 'function') {
      window.addEventListener('storage', function(e) {
        if (e.key === 'cli-proxy-language' && e.newValue && I18N_MAP[e.newValue]) {
          I18N_LANG = e.newValue;
          applyI18N();
          // Trigger re-render for dynamic content
          if (typeof rerender === 'function') {
            rerender({ refreshEvents: false, refreshApiDetail: false });
          }
        }
      });
    }

    // Observe parent document language changes
    if (typeof MutationObserver !== 'undefined') {
      try {
        if (window.parent && window.parent !== window && window.parent.document) {
          var langObserver = new MutationObserver(function() {
            var newLang = detectCPALanguage();
            if (newLang !== I18N_LANG) {
              I18N_LANG = newLang;
              applyI18N();
              if (typeof rerender === 'function') {
                rerender({ refreshEvents: false, refreshApiDetail: false });
              }
            }
          });
          langObserver.observe(window.parent.document.documentElement, {
            attributes: true,
            attributeFilter: ['lang']
          });
        }
      } catch (e) { /* cross-origin */ }
    }
  } catch (e) { /* i18n failure should not break dashboard */ }
})();

// Node.js test compatibility — only I18N_MAP is global-scoped; t()/applyI18N are inside the IIFE below
if (typeof module !== 'undefined' && module.exports) {
  module.exports = { I18N_MAP: I18N_MAP };
}
