import { useEffect, useRef, useState } from 'react'

import { fetchQuote } from '../api/client'
import { isPartialDecimal } from '../api/format'
import { ApiError } from '../api/types'
import type { Quote } from '../api/types'

export interface QuoteError {
  title: string
  detail: string
  /** A 404 is an ordinary outcome, not a fault. The panel says so. */
  ordinary: boolean
}

export interface QuoteState {
  quote: Quote | null
  loading: boolean
  error: QuoteError | null
  /** True while a request is in flight over a quote that is already displayed. */
  stale: boolean
}

export interface QuoteInputs {
  tokenIn: string
  tokenOut: string
  /** Whole units as typed. Empty or zero means no request is made. */
  amount: string
  slippageBps: number
  maxHops: number
}

/** Amount edits wait for typing to stop. Everything else feels like a click. */
const AMOUNT_DEBOUNCE_MS = 300
const CONTROL_DEBOUNCE_MS = 60

/**
 * Requests a route whenever the inputs settle.
 *
 * The previous quote is held through a refresh rather than blanked, so the
 * route visualiser dims instead of disappearing and the layout never jumps.
 */
export function useQuote(inputs: QuoteInputs): QuoteState {
  const { tokenIn, tokenOut, amount, slippageBps, maxHops } = inputs

  const [state, setState] = useState<QuoteState>({
    quote: null,
    loading: false,
    error: null,
    stale: false,
  })

  const previousAmount = useRef(amount)
  const requestId = useRef(0)

  useEffect(() => {
    const amountChanged = previousAmount.current !== amount
    previousAmount.current = amount

    // String tests only. Nothing in this file coerces an amount to a number.
    const parsed = amount.trim()
    const empty = !isPartialDecimal(parsed) || parsed === '' || /^0*\.?0*$/.test(parsed)

    if (empty || tokenIn === tokenOut) {
      setState({
        quote: null,
        loading: false,
        stale: false,
        error:
          tokenIn === tokenOut && !empty
            ? {
                title: 'Same token on both sides',
                detail: 'Pick a different token to route into.',
                ordinary: true,
              }
            : null,
      })
      return
    }

    setState((previous) => ({
      quote: previous.quote,
      error: previous.error,
      loading: true,
      stale: previous.quote !== null,
    }))

    const controller = new AbortController()
    const id = ++requestId.current
    const delay = amountChanged ? AMOUNT_DEBOUNCE_MS : CONTROL_DEBOUNCE_MS

    const timer = window.setTimeout(() => {
      fetchQuote({ tokenIn, tokenOut, amountIn: parsed, slippageBps, maxHops }, controller.signal)
        .then((quote) => {
          if (id !== requestId.current) return
          setState({ quote, loading: false, error: null, stale: false })
        })
        .catch((cause: unknown) => {
          if (id !== requestId.current) return
          if (cause instanceof DOMException && cause.name === 'AbortError') return
          setState({ quote: null, loading: false, stale: false, error: describe(cause) })
        })
    }, delay)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [tokenIn, tokenOut, amount, slippageBps, maxHops])

  return state
}

function describe(cause: unknown): QuoteError {
  if (cause instanceof ApiError) {
    if (cause.isNoRoute) {
      return {
        title: 'No route found',
        detail: cause.detail || 'These two tokens are not connected by indexed liquidity within the hop limit.',
        ordinary: true,
      }
    }
    if (cause.isWarmingUp) {
      return {
        title: 'Aggregator is warming up',
        detail: cause.detail || 'The indexer has not published its first pool snapshot yet. This clears on its own.',
        ordinary: true,
      }
    }
    if (cause.status === 0) {
      return {
        title: 'Aggregator unreachable',
        detail: cause.detail,
        ordinary: false,
      }
    }
    return { title: cause.message, detail: cause.detail, ordinary: false }
  }
  return {
    title: 'Quote failed',
    detail: cause instanceof Error ? cause.message : 'An unexpected error occurred.',
    ordinary: false,
  }
}
