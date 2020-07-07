package metrics

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/argoproj/argo/pkg/apis/workflow/v1alpha1"
)

const (
	argoNamespace            = "argo"
	workflowsSubsystem       = "workflows"
	DefaultMetricsServerPort = "9090"
	DefaultMetricsServerPath = "/metrics"
)

type ServerConfig struct {
	Enabled      bool
	Path         string
	Port         string
	TTL          time.Duration
	IgnoreErrors bool
}

func (s ServerConfig) SameServerAs(other ServerConfig) bool {
	return s.Port == other.Port && s.Path == other.Path && s.Enabled && other.Enabled
}

type metric struct {
	metric      prometheus.Metric
	lastUpdated time.Time
}

type Metrics struct {
	// Ensures mutual exclusion in workflows map
	workflowsMutex  sync.Mutex
	metricsConfig   ServerConfig
	telemetryConfig ServerConfig

	workflowsProcessed prometheus.Counter
	workflowsByPhase   map[v1alpha1.NodePhase]prometheus.Gauge
	workflows          map[string]bool
	operationDurations prometheus.Histogram
	errors             map[ErrorCause]prometheus.Counter
	customMetrics      map[string]metric

	// Used to quickly check if a metric desc is already used by the system
	defaultMetricDescs map[string]bool
}

var _ prometheus.Collector = &Metrics{}

func New(metricsConfig, telemetryConfig ServerConfig) *Metrics {
	metrics := &Metrics{
		metricsConfig:      metricsConfig,
		telemetryConfig:    telemetryConfig,
		workflowsProcessed: newCounter("workflows_processed_count", "Number of workflow updates processed", nil),
		workflowsByPhase:   getWorkflowPhaseGauges(),
		workflows:          make(map[string]bool),
		operationDurations: newHistogram("operation_duration_seconds", "Histogram of durations of operations", nil, []float64{0.1, 0.25, 0.5, 0.75, 1.0, 1.25, 1.5, 1.75, 2.0, 2.5, 3.0}),
		errors:             getErrorCounters(),
		customMetrics:      make(map[string]metric),
		defaultMetricDescs: make(map[string]bool),
	}

	for _, metric := range metrics.allMetrics() {
		metrics.defaultMetricDescs[metric.Desc().String()] = true
	}

	return metrics
}

func (m *Metrics) allMetrics() []prometheus.Metric {
	allMetrics := []prometheus.Metric{
		m.workflowsProcessed,
		m.operationDurations,
	}
	for _, metric := range m.workflowsByPhase {
		allMetrics = append(allMetrics, metric)
	}
	for _, metric := range m.errors {
		allMetrics = append(allMetrics, metric)
	}
	for _, metric := range m.customMetrics {
		allMetrics = append(allMetrics, metric.metric)
	}

	return allMetrics
}

func (m *Metrics) WorkflowAdded(key string, phase v1alpha1.NodePhase) {
	m.workflowsMutex.Lock()
	defer m.workflowsMutex.Unlock()

	if m.workflows[key] {
		return
	}
	m.workflows[key] = true
	if _, ok := m.workflowsByPhase[phase]; ok {
		m.workflowsByPhase[phase].Inc()
	}
}

func (m *Metrics) WorkflowUpdated(key string, fromPhase, toPhase v1alpha1.NodePhase) {
	m.workflowsMutex.Lock()
	hasKey := m.workflows[key]
	m.workflowsMutex.Unlock()
	if fromPhase == toPhase || !hasKey {
		return
	}
	m.WorkflowDeleted(key, fromPhase)
	m.WorkflowAdded(key, toPhase)
}

func (m *Metrics) WorkflowDeleted(key string, phase v1alpha1.NodePhase) {
	m.workflowsMutex.Lock()
	defer m.workflowsMutex.Unlock()

	if !m.workflows[key] {
		return
	}
	delete(m.workflows, key)
	if _, ok := m.workflowsByPhase[phase]; ok {
		m.workflowsByPhase[phase].Dec()
	}
}

func (m *Metrics) OperationCompleted(durationSeconds float64) {
	m.operationDurations.Observe(durationSeconds)
}

func (m *Metrics) GetCustomMetric(key string) prometheus.Metric {
	// It's okay to return nil metrics in this function
	return m.customMetrics[key].metric
}

func (m *Metrics) UpsertCustomMetric(key string, newMetric prometheus.Metric) error {
	if _, inUse := m.defaultMetricDescs[newMetric.Desc().String()]; inUse {
		return fmt.Errorf("metric '%s' is already in use by the system, please use a different name", newMetric.Desc())
	}
	m.customMetrics[key] = metric{metric: newMetric, lastUpdated: time.Now()}
	return nil
}

type ErrorCause string

const (
	ErrorCauseOperationPanic              ErrorCause = "OperationPanic"
	ErrorCauseCronWorkflowSubmissionError ErrorCause = "CronWorkflowSubmissionError"
)

func (m *Metrics) OperationPanic() {
	m.errors[ErrorCauseOperationPanic].Inc()
}

func (m *Metrics) CronWorkflowSubmissionError() {
	m.errors[ErrorCauseCronWorkflowSubmissionError].Inc()
}