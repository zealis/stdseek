// Package metrics 暴露 Prometheus 指标（规划书：爬虫吞吐、索引延迟、QPS）。
// 指标命名统一以 voidxo_ 前缀，便于 Grafana 面板按前缀筛选。
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPQPS / 延迟：route 使用 chi 的路由模板（如 /api/search），避免 URL 高基数
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "voidxo_http_requests_total",
		Help: "HTTP 请求总数（按方法/路由/状态码）",
	}, []string{"method", "route", "status"})

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "voidxo_http_request_duration_seconds",
		Help:    "HTTP 请求耗时分布",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"route"})

	SearchTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "voidxo_search_total",
		Help: "检索请求总数",
	})

	SearchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "voidxo_search_duration_seconds",
		Help:    "检索耗时（Bleve 召回 + 重排序）",
		Buckets: []float64{0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
	})

	CrawlPages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "voidxo_crawl_pages_total",
		Help: "爬虫页面处理结果（ok/error/skipped）",
	}, []string{"status"})

	IndexedDocs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "voidxo_index_documents_total",
		Help: "写入索引的文档总数",
	})

	DocumentsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "voidxo_documents_total",
		Help: "索引中的文档总数（启动时统计）",
	})

	ProsperityKeys = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "voidxo_prosperity_keys",
		Help: "最近一次繁荣度统计的节点数",
	})

	ProsperityPairs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "voidxo_prosperity_pairs",
		Help: "最近一次繁荣度统计的出链条数",
	})
)

// Handler 暴露 /metrics。
func Handler() http.Handler { return promhttp.Handler() }

// Middleware 记录 HTTP 请求数与耗时。
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ObserveSearch 记录一次检索耗时。
func ObserveSearch(d time.Duration) {
	SearchTotal.Inc()
	SearchDuration.Observe(d.Seconds())
}
