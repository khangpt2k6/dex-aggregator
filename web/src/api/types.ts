// Wire types for the Go aggregator.
//
// Every amount is a decimal string in the token's raw base units. An 18 decimal
// amount is far past 2^53, so these must never be handed to Number(),
// parseFloat(), or any arithmetic operator. Use the BigInt helpers in
// ./format.ts instead.

export type Protocol = 'uniswap-v3' | 'sushiswap-v2'

export interface Token {
  address: string
  symbol: string
  decimals: number
}

export interface TokensResponse {
  tokens: Token[]
  count: number
}

export interface Hop {
  poolAddress: string
  protocol: Protocol
  feeBps: number
  tokenIn: string
  tokenOut: string
  tokenInSymbol: string
  tokenOutSymbol: string
  /** Raw base units. */
  amountIn: string
  /** Raw base units. */
  amountOut: string
}

export interface Quote {
  tokenIn: string
  tokenOut: string
  tokenInSymbol: string
  tokenOutSymbol: string
  /** Raw base units. */
  amountIn: string
  /** Raw base units. */
  amountOut: string
  /** Raw base units. */
  minReceived: string
  slippageBps: number
  priceImpactBps: number
  route: Hop[]
  hops: number
  snapshotAgeMs: number
  routerMicros: number
  simulated: boolean
}

export interface Pool {
  address: string
  protocol: Protocol
  token0: string
  token1: string
  feeBps: number
  simulated: boolean
  /** Raw base units. Constant product pools only. */
  reserve0?: string
  /** Raw base units. Constant product pools only. */
  reserve1?: string
  /** Raw units. Concentrated liquidity pools only. */
  liquidity?: string
  tick?: number
}

export interface PoolsResponse {
  pools: Pool[]
  count: number
  snapshotAgeMs: number
}

export interface IndexerStatus {
  refreshes: number
  pools: number
  tokens: number
  edges: number
  healthySources: number
  totalSources: number
  sourceNames: string[]
  lastDurationMs: number
  lastError?: string
}

export interface Status {
  indexer: IndexerStatus
  snapshotAgeMs: number
  refreshEveryMs: number
  maxHops: number
  mode: 'live' | 'simulated' | string
}

export interface PriceTick {
  /** BASE/QUOTE, for example WETH/USDC. */
  pair: string
  /** Raw base units of the quote token. */
  price: string
  /** Unix seconds. */
  time: number
}

export interface QuoteRequest {
  tokenIn: string
  tokenOut: string
  /** Whole units with a decimal point, exactly as the user typed it. */
  amountIn: string
  slippageBps: number
  maxHops?: number
}

/** An error the API returned as JSON, carrying its own wording. */
export class ApiError extends Error {
  readonly status: number
  readonly detail: string

  constructor(status: number, message: string, detail: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }

  /** True when the router simply could not find a path. Not a failure. */
  get isNoRoute(): boolean {
    return this.status === 404
  }

  /** True when the backend has not built its first snapshot yet. */
  get isWarmingUp(): boolean {
    return this.status === 503
  }
}
