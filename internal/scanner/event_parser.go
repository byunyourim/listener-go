package scanner

// symbolForChain KCP 체인의 W 접두사 토큰은 접두사 제거 (WETH→ETH, WBTC→BTC 등)
func symbolForChain(rawSymbol string, chainID, kcpChainID int64, hasKcp bool) string {
	if hasKcp && chainID == kcpChainID && len(rawSymbol) > 1 && rawSymbol[0] == 'W' {
		return rawSymbol[1:]
	}
	return rawSymbol
}
