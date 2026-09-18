import { useCallback, useEffect, useMemo, useState } from 'react'

import { fetchProtocolCount, fetchTokens } from './api/client'
import { ApiError } from './api/types'
import type { Token } from './api/types'
import { PriceChart } from './components/PriceChart'
import { RouteView } from './components/RouteView'
import { SimulatedBanner } from './components/SimulatedBanner'
import { StatusBar } from './components/StatusBar'
import { SwapPanel } from './components/SwapPanel'
import { usePriceStream } from './hooks/usePriceStream'
import { useQuote } from './hooks/useQuote'
import { useStatus } from './hooks/useStatus'

/** The pair the chart opens on, and the fallback when a token has no stream. */
const DEFAULT_PAIR = 'WETH/USDC'

export default function App() {
  const [tokens, setTokens] = useState<Token[]>([])
  const [bootError, setBootError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  const [tokenIn, setTokenIn] = useState('WETH')
  const [tokenOut, setTokenOut] = useState('USDC')
  // Opening with an amount already in the field means the route strip is
  // populated the moment the page loads, which is the whole point of it.
  const [amount, setAmount] = useState('1.0')
  const [slippageBps, setSlippageBps] = useState(50)
  const [maxHops, setMaxHops] = useState(3)
  const [pair, setPair] = useState(DEFAULT_PAIR)
  const [pairPinned, setPairPinned] = useState(false)
  const [protocolCount, setProtocolCount] = useState<number | null>(null)

  useEffect(() => {
    const controller = new AbortController()

    fetchTokens(controller.signal)
      .then((list) => {
        setTokens(list)
        setBootError(null)
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return
        setBootError(
          cause instanceof ApiError
            ? cause.detail || cause.message
            : 'The token registry could not be read.',
        )
      })

    fetchProtocolCount(controller.signal)
      .then(setProtocolCount)
      .catch(() => {
        // The route strip falls back to counting the protocols in the route.
      })

    return () => controller.abort()
  }, [attempt])

  const decimals = useMemo(() => {
    const map = new Map<string, number>()
    for (const token of tokens) map.set(token.symbol, token.decimals)
    return map
  }, [tokens])

  const { status, error: statusError } = useStatus()
  const { series, status: streamStatus, pairs } = usePriceStream(decimals)
  const quoteState = useQuote({ tokenIn, tokenOut, amount, slippageBps, maxHops })

  // Follow the token being sold unless the chart pair was chosen by hand.
  useEffect(() => {
    if (pairPinned) return
    const candidate = `${tokenIn}/USDC`
    if (pairs.includes(candidate)) {
      setPair(candidate)
      return
    }
    const outward = `${tokenOut}/USDC`
    if (pairs.includes(outward)) setPair(outward)
  }, [tokenIn, tokenOut, pairs, pairPinned])

  const onFlip = useCallback(() => {
    setTokenIn(tokenOut)
    setTokenOut(tokenIn)
  }, [tokenIn, tokenOut])

  const onTokenIn = useCallback(
    (symbol: string) => {
      setTokenIn(symbol)
      if (symbol === tokenOut) setTokenOut(tokenIn)
    },
    [tokenIn, tokenOut],
  )

  const onTokenOut = useCallback(
    (symbol: string) => {
      setTokenOut(symbol)
      if (symbol === tokenIn) setTokenIn(tokenOut)
    },
    [tokenIn, tokenOut],
  )

  const simulated = status?.mode === 'simulated' || quoteState.quote?.simulated === true

  const idleMessage = quoteState.error
    ? `${quoteState.error.title.toUpperCase()} / NOTHING TO DRAW`
    : amount.trim() === '' || /^0*\.?0*$/.test(amount.trim())
      ? 'ENTER AN AMOUNT TO SEE THE PATH IT TAKES'
      : 'ROUTING'

  if (bootError !== null && tokens.length === 0) {
    return (
      <div className="shell">
        <StatusBar status={status} error={statusError} streamStatus={streamStatus} routerMicros={null} />
        <div className="panel" style={{ padding: 28, gap: 14 }}>
          <div className="notice is-fault" style={{ maxWidth: 560 }}>
            <p className="notice-title">Aggregator unreachable</p>
            <p className="notice-detail">{bootError}</p>
            <p className="notice-detail" style={{ marginTop: 10 }}>
              Start the backend with <span className="mono">go run ./cmd/aggregator</span> from the
              repository root. It listens on port 8080 and needs no configuration.
            </p>
          </div>
          <button
            type="button"
            className="preset"
            style={{ alignSelf: 'flex-start', marginTop: 14 }}
            onClick={() => setAttempt((n) => n + 1)}
          >
            Retry
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="shell">
      <StatusBar
        status={status}
        error={statusError}
        streamStatus={streamStatus}
        routerMicros={quoteState.quote?.routerMicros ?? null}
      />

      {simulated && <SimulatedBanner />}

      <div className="grid">
        <PriceChart
          pairs={pairs}
          selected={pair}
          onSelect={(next) => {
            setPair(next)
            setPairPinned(true)
          }}
          points={series.get(pair) ?? []}
          status={streamStatus}
        />

        <SwapPanel
          tokens={tokens}
          tokenIn={tokenIn}
          tokenOut={tokenOut}
          amount={amount}
          slippageBps={slippageBps}
          quoteState={quoteState}
          onTokenIn={onTokenIn}
          onTokenOut={onTokenOut}
          onAmount={setAmount}
          onSlippage={setSlippageBps}
          onFlip={onFlip}
        />

        <RouteView
          quote={quoteState.quote}
          stale={quoteState.stale}
          decimals={decimals}
          indexedPools={status?.indexer.pools ?? null}
          indexedProtocols={protocolCount}
          maxHops={maxHops}
          onMaxHops={setMaxHops}
          idleMessage={idleMessage}
        />

        <footer className="panel colophon area-foot">
          <p>
            Routing across Uniswap V3 concentrated liquidity and Sushiswap V2 constant product
            pools. Every amount on this page is carried as a base-unit integer and formatted with
            BigInt, never through a float.
          </p>
          <p>
            Backend: Go, {status?.indexer.sourceNames.join(', ') ?? 'indexing'} source, refreshing
            every {status ? Math.round(status.refreshEveryMs / 1000) : '--'}s.
          </p>
        </footer>
      </div>
    </div>
  )
}
