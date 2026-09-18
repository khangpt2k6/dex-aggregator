import {
  formatAge,
  formatBps,
  formatMicros,
  formatRate,
  formatUnits,
  impactLevel,
  isPartialDecimal,
  parseUnits,
} from '../api/format'
import type { Token } from '../api/types'
import type { QuoteState } from '../hooks/useQuote'
import { SlippageControl } from './SlippageControl'
import { TokenSelect } from './TokenSelect'

interface Props {
  tokens: Token[]
  tokenIn: string
  tokenOut: string
  amount: string
  slippageBps: number
  quoteState: QuoteState
  onTokenIn: (symbol: string) => void
  onTokenOut: (symbol: string) => void
  onAmount: (value: string) => void
  onSlippage: (bps: number) => void
  onFlip: () => void
}

interface RowProps {
  name: string
  children: React.ReactNode
  tone?: string | undefined
}

function Row({ name, children, tone }: RowProps) {
  return (
    <div className="summary-row">
      <span className="summary-key">{name}</span>
      <span className={tone === undefined ? 'summary-value' : `summary-value ${tone}`}>{children}</span>
    </div>
  )
}

const PENDING = <span className="summary-value is-pending">--</span>

/**
 * Order entry.
 *
 * Read-only by design: there is no wallet here and nothing is ever signed or
 * submitted. The primary button says so instead of looking like it will trade.
 */
export function SwapPanel({
  tokens,
  tokenIn,
  tokenOut,
  amount,
  slippageBps,
  quoteState,
  onTokenIn,
  onTokenOut,
  onAmount,
  onSlippage,
  onFlip,
}: Props) {
  const { quote, loading, error, stale } = quoteState

  const decimalsOf = (symbol: string): number =>
    tokens.find((token) => token.symbol === symbol)?.decimals ?? 18

  const decimalsIn = decimalsOf(tokenIn)
  const decimalsOut = decimalsOf(tokenOut)

  const typedRaw = parseUnits(amount, decimalsIn)
  const outputText =
    quote !== null ? formatUnits(quote.amountOut, decimalsOut) : loading ? 'routing' : '0.00'

  const rate =
    quote === null ? null : formatRate(quote.amountIn, decimalsIn, quote.amountOut, decimalsOut)

  const level = quote === null ? 'normal' : impactLevel(quote.priceImpactBps)
  const impactTone = level === 'normal' ? undefined : `is-${level}`

  return (
    <section className="panel swap area-swap" aria-label="Swap">
      <div className="panel-head">
        <h2 className="label label-lead">Order entry</h2>
        <span className="spacer" />
        <span className="label">Read only</span>
      </div>

      <div className="panel-body" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div>
          <div className="field">
            <div className="field-head">
              <label className="label" htmlFor="amount-in">
                You pay
              </label>
            </div>
            <div className="field-row">
              <input
                id="amount-in"
                className="amount-input"
                type="text"
                inputMode="decimal"
                autoComplete="off"
                spellCheck={false}
                placeholder="0.0"
                value={amount}
                aria-describedby="amount-in-raw"
                onChange={(event) => {
                  const next = event.target.value
                  // Reject anything that is not a partial decimal, and do it
                  // without rewriting what is already in the field. Fighting
                  // someone mid-keystroke is worse than a moment of "0.".
                  if (!isPartialDecimal(next)) return
                  onAmount(next)
                }}
              />
              <TokenSelect
                id="token-in"
                ariaLabel="Token to pay"
                value={tokenIn}
                tokens={tokens}
                onChange={onTokenIn}
              />
            </div>
            <div className="field-foot" id="amount-in-raw">
              {typedRaw === null
                ? `base units at ${decimalsIn} decimals`
                : `${typedRaw.toString()} base units`}
            </div>
          </div>

          <div className="flip-rail">
            <button type="button" className="flip" onClick={onFlip} aria-label="Swap the two tokens">
              <svg width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
                <path d="M4.5 1.5v10M4.5 12.5L2 10M4.5 12.5L7 10" stroke="currentColor" strokeWidth="1.3" />
                <path d="M10.5 13.5v-10M10.5 2.5L8 5M10.5 2.5L13 5" stroke="currentColor" strokeWidth="1.3" />
              </svg>
            </button>
          </div>

          <div className="field">
            <div className="field-head">
              <span className="label" id="amount-out-label">
                You receive
              </span>
            </div>
            <div className="field-row">
              <output
                className={[
                  'amount-out',
                  quote === null ? 'is-empty' : '',
                  stale ? 'is-stale' : '',
                  loading && quote === null ? 'pulse' : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
                aria-labelledby="amount-out-label"
                aria-live="polite"
              >
                {outputText}
              </output>
              <TokenSelect
                id="token-out"
                ariaLabel="Token to receive"
                value={tokenOut}
                tokens={tokens}
                onChange={onTokenOut}
              />
            </div>
            <div className="field-foot">
              {quote === null
                ? `base units at ${decimalsOut} decimals`
                : `${quote.amountOut} base units`}
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
          <span className="label" id="slippage-label">
            Slippage tolerance
          </span>
          <SlippageControl valueBps={slippageBps} onChange={onSlippage} />
        </div>

        <div className="summary">
          <Row name="Rate">
            {rate === null ? (
              PENDING
            ) : (
              <>
                {rate} {tokenOut} / {tokenIn}
              </>
            )}
          </Row>
          <Row name="Price impact" tone={impactTone}>
            {quote === null ? PENDING : formatBps(quote.priceImpactBps)}
          </Row>
          <Row name="Minimum received">
            {quote === null ? (
              PENDING
            ) : (
              <>
                {formatUnits(quote.minReceived, decimalsOut)} {tokenOut}
              </>
            )}
          </Row>
          <Row name="Quote source">
            {quote === null ? PENDING : quote.simulated ? 'simulated pools' : 'ethereum mainnet'}
          </Row>
          <Row name="Snapshot age">{quote === null ? PENDING : formatAge(quote.snapshotAgeMs)}</Row>
          <Row name="Router time">{quote === null ? PENDING : formatMicros(quote.routerMicros)}</Row>
        </div>

        {error !== null && (
          <div className={error.ordinary ? 'notice reveal' : 'notice is-fault reveal'} role="status">
            <p className="notice-title">{error.title}</p>
            <p className="notice-detail">{error.detail}</p>
          </div>
        )}

        <div className="swap-action">
          <button
            type="button"
            className="action"
            aria-disabled="true"
            aria-describedby="action-note"
            onClick={(event) => event.preventDefault()}
          >
            Quote only
          </button>
          <p className="action-note" id="action-note">
            Read-only. No wallet, no execution.
          </p>
        </div>
      </div>
    </section>
  )
}
