package retry

// JSON-RPC 에러 코드 (go-ethereum rpc/errors.go v1.17.3에서 검증).
// 값 범위는 JSON-RPC 2.0 스펙(-32000~-32099 = 구현 정의 서버 에러).
const (
	rpcCodeInternalError      = -32603 // panic/marshal — 서버 내부 문제 (재시도)
	rpcCodeNotificationsUnsup = -32001 // notifications unsupported (재시도 X)
	rpcCodeResponseTooLarge   = -32003 // 응답 too large — 블록 범위 축소가 해법 (재시도 X)
	rpcCodeServerErrorMax     = -32000 // 서버 에러 범위 상한 (덜 음수)
	rpcCodeServerErrorMin     = -32099 // 서버 에러 범위 하한 (더 음수)
)

// HTTP 재시도 대상 상태 코드
const (
	httpStatusTooManyRequests     = 429
	httpStatusInternalServerError = 500
	httpStatusBadGateway          = 502
	httpStatusServiceUnavailable  = 503
	httpStatusGatewayTimeout      = 504
)
