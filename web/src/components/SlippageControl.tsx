import { useEffect, useState } from 'react'

const PRESETS = [10, 50, 100] as const

const MAX_BPS = 10_000

interface Props {
  valueBps: number
  onChange: (bps: number) => void
}

function toPercentText(bps: number): string {
  return (bps / 100).toFixed(2).replace(/\.?0+$/, '')
}

/**
 * Slippage tolerance, in basis points behind a percentage face.
 *
 * The custom field is kept as text while it is being edited so that typing
 * "0." does not snap back to "0" mid-keystroke.
 */
export function SlippageControl({ valueBps, onChange }: Props) {
  const isPreset = (PRESETS as readonly number[]).includes(valueBps)
  const [custom, setCustom] = useState(() => (isPreset ? '' : toPercentText(valueBps)))

  useEffect(() => {
    if ((PRESETS as readonly number[]).includes(valueBps)) setCustom('')
  }, [valueBps])

  const commit = (text: string) => {
    setCustom(text)
    if (text.trim() === '') return

    // Percent to basis points without floating point: shift the decimal two
    // places by hand so 0.35 becomes exactly 35.
    const match = /^(\d*)(?:\.(\d{0,4}))?$/.exec(text.trim())
    if (match === null) return
    const whole = match[1] ?? ''
    const fraction = (match[2] ?? '').slice(0, 2).padEnd(2, '0')
    const bps = Number(`${whole === '' ? '0' : whole}${fraction}`)
    if (!Number.isFinite(bps) || bps < 0 || bps > MAX_BPS) return
    onChange(bps)
  }

  return (
    <div className="slippage" role="group" aria-label="Slippage tolerance">
      {PRESETS.map((preset) => (
        <button
          key={preset}
          type="button"
          className="preset"
          aria-pressed={valueBps === preset}
          onClick={() => {
            setCustom('')
            onChange(preset)
          }}
        >
          {toPercentText(preset)}%
        </button>
      ))}

      <div className="slippage-custom">
        <input
          type="text"
          inputMode="decimal"
          aria-label="Custom slippage tolerance in percent"
          placeholder={isPreset ? 'custom' : ''}
          value={custom}
          onChange={(event) => {
            const next = event.target.value
            if (!/^\d*\.?\d*$/.test(next)) return
            commit(next)
          }}
        />
        <span aria-hidden="true">%</span>
      </div>
    </div>
  )
}
