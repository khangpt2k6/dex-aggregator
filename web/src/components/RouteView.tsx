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

const HOP_LIMITS = [1, 2, 3, 4] as const

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
  /** Text for the strip when there is nothing to draw. */
  idleMessage: string
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
 * a direct swap looks deliberate rather than broken.
 */
export function RouteView({
  quote,
  stale,
  decimals,
  indexedPools,
  indexedProtocols,
  maxHops,
  onMaxHops,
  idleMessage,
}: Props) {
  const level = quote === null ? 'normal' : impactLevel(quote.priceImpactBps)

  const protocolsUsed =
    quote === null ? 0 : new Set(quote.route.map((hop) => hop.protocol)).size

  const searchedPools = indexedPools ?? 0
  const searchedProtocols = indexedProtocols ?? protocolsUsed

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
            Hop limit
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

        {quote !== null && (
          <span className={`impact-pill ${level === 'normal' ? '' : `is-${level}`}`}>
            <span className="impact-key">Price impact</span>
            <span className={level === 'normal' ? '' : `is-${level}`}>
              {formatBps(quote.priceImpactBps)}
            </span>
          </span>
        )}
      </div>

      <div className="panel-body">
        <p className="route-summary" aria-live="polite">
          {quote === null ? (
            <span>
              Searching <strong>{searchedPools || '--'}</strong> indexed pools across{' '}
              <strong>{searchedProtocols || '--'}</strong> protocols, up to{' '}
              <strong>{maxHops}</strong> {maxHops === 1 ? 'hop' : 'hops'}.
            </span>
          ) : (
            <span>
              Best of <strong>{searchedPools || '--'}</strong> pools across{' '}
              <strong>{searchedProtocols}</strong> protocols:{' '}
              <strong>
                {quote.hops} {quote.hops === 1 ? 'hop' : 'hops'}
              </strong>{' '}
              through{' '}
              <strong>
                {quote.route.map((hop) => protocolLabel(hop.protocol)).join(' then ')}
              </strong>
              , found in <strong>{formatMicros(quote.routerMicros)}</strong>.
            </span>
          )}
        </p>

        {quote === null || quote.route.length === 0 ? (
          <p className="route-idle">{idleMessage}</p>
        ) : (
          <ol className={stale ? 'pipeline is-stale' : 'pipeline'}>
            <li className="node is-terminal">
              <span className="node-role">In</span>
              <span className="node-symbol">{quote.route[0]?.tokenInSymbol ?? quote.tokenInSymbol}</span>
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
                    <span className={protocolClass(hop.protocol)}>{protocolLabel(hop.protocol)}</span>
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
                      {protocolLabel(hop.protocol)} pool at {hop.poolAddress}, fee {hop.feeBps} basis
                      points.
                    </span>
                  </li>

                  <li className={terminal ? 'node is-terminal' : 'node'}>
                    <span className="node-role">{terminal ? 'Out' : `Via ${index + 1}`}</span>
                    <span className="node-symbol">{hop.tokenOutSymbol}</span>
                    <span className="node-amount">
                      {amountFor(hop.amountOut, hop.tokenOutSymbol)}
                    </span>
                  </li>
                </Fragment>
              )
            })}
          </ol>
        )}
      </div>
    </section>
  )
}
