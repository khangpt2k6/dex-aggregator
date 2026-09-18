import { Fragment } from 'react'
import type { CSSProperties } from 'react'

import {
  formatBps,
  formatMicros,
  formatUnits,
  impactLevel,
  protocolLabel,
  shortAddress,
} from '../api/format'
import type { Quote } from '../api/types'
import type { RouteOption } from '../hooks/useQuote'
import { HOP_LIMITS } from '../hooks/useQuote'
import { RouteComparison } from './RouteComparison'

interface Props {
  quote: Quote | null
  stale: boolean
  /** Decimals per token symbol, for rendering the amount held at each node. */
  decimals: Map<string, number>
  /** Pools currently indexed, from /api/v1/status. */
  indexedPools: number | null
  /** Distinct protocols currently indexed. */
  indexedProtocols: number | null
  maxHops: number
  onMaxHops: (hops: number) => void
  /** One entry per hop limit, for the comparison table. */
  matrix: RouteOption[]
  outSymbol: string
  /** Text for the strip when there is nothing to draw. */
  idleMessage: string
}

interface ChipProps {
  name: string
  value: string
  tone?: string | undefined
}

function Chip({ name, value, tone }: ChipProps) {
  return (
    <div className="chip">
      <span className="chip-key">{name}</span>
      <span className={tone === undefined ? 'chip-val' : `chip-val ${tone}`}>{value}</span>
    </div>
  )
}

function protocolClass(protocol: string): string {
  return protocol === 'uniswap-v3' ? 'hop-protocol is-univ3' : 'hop-protocol is-sushi'
}

/**
 * The route visualiser.
 *
 * This is the point of the whole interface, so it is a full-width strip that is
 * always on screen: no accordion, no modal, no hover reveal. A single-hop route
 * uses exactly the same node-connector-node construction as a four-hop one, so
 * a direct swap looks deliberate rather than broken. Everything the router did
 * is stated as a labelled figure rather than a sentence.
 */
export function RouteView({
  quote,
  stale,
  decimals,
  indexedPools,
  indexedProtocols,
  maxHops,
  onMaxHops,
  matrix,
  outSymbol,
  idleMessage,
}: Props) {
  const level = quote === null ? 'normal' : impactLevel(quote.priceImpactBps)
  const protocolsUsed = quote === null ? 0 : new Set(quote.route.map((hop) => hop.protocol)).size

  // minDecimals keeps a whole amount reading as 1.00 rather than 1, which
  // matters when the column is meant to scan as a run of figures.
  const amountFor = (raw: string, symbol: string): string =>
    formatUnits(raw, decimals.get(symbol) ?? 18, { minDecimals: 2 })

  return (
    <section className="panel area-route" aria-label="Route">
      <div className="panel-head">
        <h2 className="label label-lead">Route</h2>

        <div className="hops-control">
          <span className="label" id="hop-limit-label">
            Limit
          </span>
          <div className="hops-buttons" role="group" aria-labelledby="hop-limit-label">
            {HOP_LIMITS.map((limit) => (
              <button
                key={limit}
                type="button"
                aria-pressed={limit === maxHops}
                onClick={() => onMaxHops(limit)}
              >
                {limit}
              </button>
            ))}
          </div>
        </div>

        <span className="spacer" />

        <div className="chips" aria-live="polite">
          <Chip name="Hops" value={quote === null ? '--' : String(quote.hops)} />
          <Chip name="Pools" value={indexedPools === null ? '--' : String(indexedPools)} />
          <Chip name="Protocols" value={String(indexedProtocols ?? (protocolsUsed || '--'))} />
          <Chip name="Router" value={quote === null ? '--' : formatMicros(quote.routerMicros)} />
          <Chip
            name="Impact"
            value={quote === null ? '--' : formatBps(quote.priceImpactBps)}
            tone={level === 'normal' ? undefined : `is-${level}`}
          />
        </div>
      </div>

      <div className="route-body">
        <div className="route-flow">
          {quote === null || quote.route.length === 0 ? (
            <p className="route-idle">{idleMessage}</p>
          ) : (
            <ol className={stale ? 'pipeline is-stale' : 'pipeline'}>
              <li className="node is-terminal">
                <span className="node-role">In</span>
                <span className="node-symbol">
                  {quote.route[0]?.tokenInSymbol ?? quote.tokenInSymbol}
                </span>
                <span className="node-amount">
                  {amountFor(quote.amountIn, quote.route[0]?.tokenInSymbol ?? quote.tokenInSymbol)}
                </span>
              </li>

              {quote.route.map((hop, index) => {
                const terminal = index === quote.route.length - 1
                return (
                  <Fragment key={`${hop.poolAddress}-${index}`}>
                    <li
                      className="hop"
                      style={{ '--flow-delay': `${index * 0.35}s` } as CSSProperties}
                    >
                      <span className={protocolClass(hop.protocol)}>
                        {protocolLabel(hop.protocol)}
                      </span>
                      <span className="hop-wire" aria-hidden="true">
                        <span className="hop-line" />
                        <span className="hop-head" />
                      </span>
                      <span className="hop-meta">
                        <span className="hop-fee">{hop.feeBps} bps</span>
                        <span>{shortAddress(hop.poolAddress, 8, 4)}</span>
                      </span>
                      <span className="visually-hidden">
                        Hop {index + 1}: {hop.tokenInSymbol} to {hop.tokenOutSymbol} through the{' '}
                        {protocolLabel(hop.protocol)} pool at {hop.poolAddress}, fee {hop.feeBps}{' '}
                        basis points.
                      </span>
                    </li>

                    <li className={terminal ? 'node is-terminal' : 'node'}>
                      <span className="node-role">{terminal ? 'Out' : `Via ${index + 1}`}</span>
                      <span className="node-symbol">{hop.tokenOutSymbol}</span>
                      <span className="node-amount">{amountFor(hop.amountOut, hop.tokenOutSymbol)}</span>
                    </li>
                  </Fragment>
                )
              })}
            </ol>
          )}
        </div>

        <div className="route-compare">
          <RouteComparison
            options={matrix}
            decimalsOut={decimals.get(outSymbol) ?? 18}
            outSymbol={outSymbol}
            stale={stale}
          />
        </div>
      </div>
    </section>
  )
}
