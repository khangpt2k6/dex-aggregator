import { useEffect, useRef, useState } from 'react'

import { priceStreamURL } from '../api/client'
import { toChartNumber } from '../api/format'
import type { PriceTick } from '../api/types'

export type StreamStatus = 'connecting' | 'open' | 'retrying' | 'closed'

export interface PricePoint {
  /** Unix seconds, matching what lightweight-charts wants. */
  time: number
  value: number
  /** False for the locally seeded warm-up section of the line. */
  live: boolean
}

export interface PriceStreamState {
  /** Points per pair, oldest first, keyed as BASE/QUOTE. */
  series: Map<string, PricePoint[]>
  status: StreamStatus
  /** Pairs the stream has pushed at least once. */
  pairs: string[]
}

/** How many points to keep per pair before dropping from the front. */
const MAX_POINTS = 600

/** How many seeded points sit behind the first live tick. */
const SEED_POINTS = 96

/** Spacing of the seeded points, in seconds. */
const SEED_INTERVAL = 4

const RETRY_BASE_MS = 500
const RETRY_MAX_MS = 15_000

/**
 * A deterministic walk used to draw the section of the line that predates the
 * first tick the browser saw.
 *
 * The backend keeps no price history, so there is nothing real to fetch for
 * the period before the page opened. Seeding it keeps the chart from being a
 * single dot, and the chart labels the seeded region rather than passing it
 * off as observed data.
 */
function seedHistory(pair: string, price: number, endTime: number): PricePoint[] {
  let hash = 2166136261
  for (let i = 0; i < pair.length; i += 1) {
    hash ^= pair.charCodeAt(i)
    hash = Math.imul(hash, 16777619)
  }

  const points: PricePoint[] = []
  let value = price
  for (let i = 0; i < SEED_POINTS; i += 1) {
    hash = Math.imul(hash ^ (hash >>> 15), 2246822507)
    const unit = ((hash >>> 8) & 0xffff) / 0xffff - 0.5
    value = value * (1 + unit * 0.0035)
    points.push({
      time: endTime - (SEED_POINTS - i) * SEED_INTERVAL,
      value,
      live: false,
    })
  }

  // Walk the tail back onto the real price so the seam is not a visible step.
  const drift = price - value
  return points.map((point, index) => ({
    ...point,
    value: point.value + (drift * index) / (SEED_POINTS - 1),
  }))
}

/**
 * Subscribes to /ws/prices and accumulates a series per pair.
 *
 * Reconnects with exponential backoff, because the aggregator restarting
 * during development should not leave a dead chart until the page is
 * reloaded.
 */
export function usePriceStream(quoteDecimals: Map<string, number>): PriceStreamState {
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const [series, setSeries] = useState<Map<string, PricePoint[]>>(() => new Map())
  const [pairs, setPairs] = useState<string[]>([])

  const decimals = useRef(quoteDecimals)
  decimals.current = quoteDecimals

  useEffect(() => {
    let socket: WebSocket | null = null
    let retryTimer = 0
    let attempt = 0
    let disposed = false

    const ingest = (tick: PriceTick) => {
      const quoteSymbol = tick.pair.split('/')[1] ?? ''
      const scale = decimals.current.get(quoteSymbol)
      if (scale === undefined) return

      let value: number
      try {
        value = toChartNumber(tick.price, scale)
      } catch {
        return
      }
      if (!Number.isFinite(value) || value <= 0) return

      setSeries((previous) => {
        const next = new Map(previous)
        const existing = next.get(tick.pair)

        if (existing === undefined) {
          next.set(tick.pair, [...seedHistory(tick.pair, value, tick.time), { time: tick.time, value, live: true }])
          return next
        }

        const last = existing[existing.length - 1]
        if (last !== undefined && tick.time <= last.time) {
          // The stream ticks faster than its one-second timestamps resolve.
          // Replace in place rather than feeding the chart a backwards series.
          const replaced = existing.slice(0, -1)
          replaced.push({ time: last.time, value, live: true })
          next.set(tick.pair, replaced)
          return next
        }

        const appended = [...existing, { time: tick.time, value, live: true }]
        next.set(tick.pair, appended.length > MAX_POINTS ? appended.slice(-MAX_POINTS) : appended)
        return next
      })

      setPairs((previous) => (previous.includes(tick.pair) ? previous : [...previous, tick.pair].sort()))
    }

    const connect = () => {
      if (disposed) return
      setStatus(attempt === 0 ? 'connecting' : 'retrying')

      socket = new WebSocket(priceStreamURL())

      socket.onopen = () => {
        attempt = 0
        setStatus('open')
      }

      socket.onmessage = (event) => {
        if (typeof event.data !== 'string') return
        try {
          ingest(JSON.parse(event.data) as PriceTick)
        } catch {
          // A malformed frame is not worth tearing the connection down for.
        }
      }

      socket.onerror = () => {
        socket?.close()
      }

      socket.onclose = () => {
        if (disposed) return
        setStatus('retrying')
        const wait = Math.min(RETRY_BASE_MS * 2 ** attempt, RETRY_MAX_MS)
        attempt += 1
        retryTimer = window.setTimeout(connect, wait)
      }
    }

    connect()

    return () => {
      disposed = true
      window.clearTimeout(retryTimer)
      if (socket !== null) {
        socket.onclose = null
        socket.close()
      }
      setStatus('closed')
    }
  }, [])

  return { series, status, pairs }
}
