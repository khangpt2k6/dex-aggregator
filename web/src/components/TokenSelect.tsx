import type { Token } from '../api/types'

interface Props {
  id: string
  /** Accessible name, since the visible label belongs to the field as a whole. */
  ariaLabel: string
  value: string
  tokens: Token[]
  onChange: (symbol: string) => void
}

/**
 * Token picker.
 *
 * A native select rather than a custom listbox: it is keyboard and screen
 * reader correct on every platform without any work, and the eight tokens this
 * aggregator indexes do not need search or filtering.
 */
export function TokenSelect({ id, ariaLabel, value, tokens, onChange }: Props) {
  return (
    <div className="token-select">
      <select
        id={id}
        aria-label={ariaLabel}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {tokens.map((token) => (
          <option key={token.address} value={token.symbol}>
            {token.symbol}
          </option>
        ))}
      </select>
    </div>
  )
}
