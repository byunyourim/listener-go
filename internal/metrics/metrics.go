// Package metrics prometheus 메트릭 전역 등록 + 헬퍼.
// 모든 모듈은 이 패키지의 변수를 직접 참조해 카운터/게이지를 갱신한다.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "listener"

// Scanner 진행 상황 (입금 누락 감지의 핵심 지표)
var (
	// ScannerCursorBlock 체인+스캐너별 마지막 처리(커서) 블록.
	ScannerCursorBlock = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "cursor_block",
		Help: "Last processed block (cursor) per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})

	// ScannerLatestBlock 체인의 최신 블록 (RPC 조회값).
	ScannerLatestBlock = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "latest_block",
		Help: "Latest block number from RPC per chain_id.",
	}, []string{"chain_id"})

	// ScannerLagBlocks latest - cursor (지연 블록 수).
	ScannerLagBlocks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "lag_blocks",
		Help: "How many blocks behind latest per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})

	// ScannerBlocksProcessed 처리한 블록 누적.
	ScannerBlocksProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "blocks_processed_total",
		Help: "Cumulative blocks processed per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})

	// ScannerDepositsFound 발견한 입금 이벤트 누적.
	ScannerDepositsFound = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "deposits_found_total",
		Help: "Cumulative deposit events found per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})

	// ScannerErrors 사이클 단위 에러 누적.
	ScannerErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scanner", Name: "errors_total",
		Help: "Cumulative scanner errors per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})
)

// Buffer 적체 지표 (입금 안전 보장의 핵심)
var (
	// BufferPendingTotal 현재 미전송 deposit_buffer row 수 (전 체인 합).
	BufferPendingTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "buffer", Name: "pending_total",
		Help: "Current number of unsent rows in deposit_buffer.",
	})

	// BufferOldestAgeSeconds deposit_buffer의 가장 오래된 row의 age(초). 너무 크면 publisher 정체 신호.
	BufferOldestAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "buffer", Name: "oldest_age_seconds",
		Help: "Age of oldest unsent row in seconds.",
	})
)

// Publisher 전송 지표
var (
	// PublisherSent 전송 성공 누적.
	PublisherSent = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "publisher", Name: "sent_total",
		Help: "Cumulative deposits sent over WS.",
	})

	// PublisherSendErrors 송신 실패 누적 (연결 끊김 등).
	PublisherSendErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "publisher", Name: "send_errors_total",
		Help: "Cumulative WS send errors.",
	})

	// PublisherConnected 현재 WS 연결 상태 (0/1).
	PublisherConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "publisher", Name: "connected",
		Help: "1 if currently connected to Adapter WS, 0 otherwise.",
	})

	// PublisherReconnects 재연결 시도 누적.
	PublisherReconnects = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "publisher", Name: "reconnects_total",
		Help: "Cumulative reconnect attempts.",
	})
)

// Supervisor 라이프사이클 지표
var (
	// SupervisorChainsRunning 현재 활성 체인 수.
	SupervisorChainsRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "supervisor", Name: "chains_running",
		Help: "Number of currently running chains.",
	})

	// SupervisorReconciles reconcile 사이클 결과 누적.
	SupervisorReconciles = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "supervisor", Name: "reconciles_total",
		Help: "Cumulative reconcile cycles by result.",
	}, []string{"result"}) // success | error

	// SupervisorPanics 체인 goroutine panic 누적 (반드시 0이어야 함).
	SupervisorPanics = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "supervisor", Name: "panics_total",
		Help: "Cumulative panics recovered per (chain_id, scanner).",
	}, []string{"chain_id", "scanner"})
)
