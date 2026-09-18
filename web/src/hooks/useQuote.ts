import { useEffect, useRef, useState } from 'react'

import { fetchQuote } from '../api/client'
import { isPartialDecimal } from '../api/format'
import { ApiError } from '../api/types'
import type { Quote } from '../api/types'

/** Hop limits quoted on every request, for the panel and the comparison. */
export const HOP_LIMITS = [1, 2, 3, 4] as const

export interface QuoteError {
  title: string
  detail: string
  /** A 404 is an ordinary outcome, not a fault. The panel says so. */
  ordinary: boolean
}

/** One row of the route comparison: what this hop limit was able to return. */
export interface RouteOption {
  hops: number
  quote: Quote | null
  /** True when the router found nothing at this limit, which is not a fault. */
  noRoute: boolean
}

export interface QuoteState {
  quote: Quote | null
  loading: boolean
  error: QuoteError | null
  /** True while a request is in flight over a quote that is already displayed. */
  stale: boolean
  /** One entry per hop limit, in HOP_LIMITS order. */
  matrix: RouteOption[]
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

const EMPTY_MATRIX: RouteOption[] = HOP_LIMITS.map((hops) => ({
  hops,
  quote: null,
  noRoute: false,
}))

/**
 * Quotes every hop limit at once whenever the inputs settle.
 *
 * One pass covers both jobs: the panel shows the entry for the selected limit,
 * and the comparison table shows all four side by side. Quoting the selected
 * limit separately would duplicate a request the table has already made.
 *
 * The previous result is held through a refresh rather than blanked, so the
 * route visualiser dims instead of disappearing and the layout never jumps.
 */
export function useQuote(inputs: QuoteInputs): QuoteState {
  const { tokenIn, tokenOut, amount, slippageBps, maxHops } = inputs

  const [state, setState] = useState<QuoteState>({
    quote: null,
    loading: false,
    error: null,
    stale: false,
    matrix: EMPTY_MATRIX,
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
        matrix: EMPTY_MATRIX,
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
      matrix: previous.matrix,
      loading: true,
      stale: previous.quote !== null,
    }))

    const controller = new AbortController()
    const id = ++requestId.current
    const delay = amountChanged ? AMOUNT_DEBOUNCE_MS : CONTROL_DEBOUNCE_MS

    const timer = window.setTimeout(() => {
      const requests = HOP_LIMITS.map((hops) =>
        fetchQuote({ tokenIn, tokenOut, amountIn: parsed, slippageBps, maxHops: hops }, controller.signal),
      )

      void Promise.allSettled(requests).then((settled) => {
        if (id !== requestId.current || controller.signal.aborted) return

        const matrix: RouteOption[] = settled.map((result, index) => ({
          hops: HOP_LIMITS[index] as number,
          quote: result.status === 'fulfilled' ? result.value : null,
          noRoute: result.status === 'rejected' && result.reason instanceof ApiError && result.reason.isNoRoute,
        }))

        const selectedIndex = HOP_LIMITS.indexOf(maxHops as (typeof HOP_LIMITS)[number])
        const selected = settled[selectedIndex === -1 ? 0 : selectedIndex]

        if (selected !== undefined && selected.status === 'fulfilled') {
          setState({ quote: selected.value, loading: false, error: null, stale: false, matrix })
          return
        }

        setState({
          quote: null,
          loading: false,
          stale: false,
          matrix,
          error: describe(selected?.status === 'rejected' ? selected.reason : undefined),
        })
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
      return { title: 'Aggregator unreachable', detail: cause.detail, ordinary: false }
    }
    return { title: cause.message, detail: cause.detail, ordinary: false }
  }
  return {
    title: 'Quote failed',
    detail: cause instanceof Error ? cause.message : 'An unexpected error occurred.',
    ordinary: false,
  }
}
