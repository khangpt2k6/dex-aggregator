// HTTP client for the aggregator.
//
// Responses are read as text and handed to JSON.parse unchanged. The amounts
// inside arrive as strings and stay strings; nothing here coerces them.

import { ApiError } from './types'
import type { PoolsResponse, Quote, QuoteRequest, Status, TokensResponse, Token } from './types'

const BASE = '/api/v1'

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      signal: signal ?? null,
      headers: { Accept: 'application/json' },
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError(0, 'unreachable', 'The aggregator did not answer. Check that it is running on :8080.')
  }

  const body = await response.text()

  if (!response.ok) {
    let message = `request failed with ${response.status}`
    let detail = body.slice(0, 400)
    try {
      const parsed = JSON.parse(body) as { error?: string; detail?: string }
      if (parsed.error) message = parsed.error
      if (parsed.detail) detail = parsed.detail
    } catch {
      // A non-JSON error body is still worth showing verbatim.
    }
    throw new ApiError(response.status, message, detail)
  }

  try {
    return JSON.parse(body) as T
  } catch {
    throw new ApiError(response.status, 'bad response', 'The aggregator returned something that is not JSON.')
  }
}

export async function fetchTokens(signal?: AbortSignal): Promise<Token[]> {
  const data = await getJSON<TokensResponse>(`${BASE}/tokens`, signal)
  return data.tokens
}

export async function fetchStatus(signal?: AbortSignal): Promise<Status> {
  return getJSON<Status>(`${BASE}/status`, signal)
}

/**
 * Counts the distinct protocols in the index.
 *
 * The status endpoint reports how many pools are indexed but not what they
 * are, and the route strip claims a number of protocols searched, so that
 * claim is read from the pool list rather than hardcoded.
 */
export async function fetchProtocolCount(signal?: AbortSignal): Promise<number> {
  const data = await getJSON<PoolsResponse>(`${BASE}/pools`, signal)
  return new Set(data.pools.map((pool) => pool.protocol)).size
}

export async function fetchQuote(request: QuoteRequest, signal?: AbortSignal): Promise<Quote> {
  const params = new URLSearchParams({
    tokenIn: request.tokenIn,
    tokenOut: request.tokenOut,
    // Sent with a decimal point on purpose: the backend reads that as whole
    // units, which is what the user typed, and does the base-unit scaling with
    // the token's real decimals rather than a guess made here.
    amountIn: request.amountIn.includes('.') ? request.amountIn : `${request.amountIn}.0`,
    slippageBps: String(request.slippageBps),
  })
  if (request.maxHops !== undefined) params.set('maxHops', String(request.maxHops))

  return getJSON<Quote>(`${BASE}/quote?${params.toString()}`, signal)
}

/** Absolute websocket URL for the price stream, same origin as the page. */
export function priceStreamURL(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws/prices`
}
