// Amount formatting.
//
// The backend sends every amount as a decimal string in the token's raw base
// units. At 18 decimals those integers run past 2^53, where float64 stops
// being exact, so Number(), parseFloat() and arithmetic on a coerced number are
// all off limits. Everything here works on BigInt and on strings.

/** Thrown when a string that should be a base-unit integer is not one. */
export class AmountError extends Error {}

const DIGITS = /^\d+$/

/** Parses a raw base-unit decimal string into a BigInt. */
export function toBigInt(raw: string): bigint {
  const trimmed = raw.trim()
  const negative = trimmed.startsWith('-')
  const body = negative ? trimmed.slice(1) : trimmed
  if (body === '' || !DIGITS.test(body)) {
    throw new AmountError(`not a base-unit integer: ${raw}`)
  }
  const value = BigInt(body)
  return negative ? -value : value
}

/**
 * Splits a raw base-unit amount into its whole and fractional halves.
 *
 * The fraction is returned zero-padded to `decimals` characters so that
 * "1" at 18 decimals reads as 0.000000000000000001 and not as 0.1.
 */
export function splitUnits(
  raw: string,
  decimals: number,
): { whole: string; fraction: string; negative: boolean } {
  const value = toBigInt(raw)
  const negative = value < 0n
  const magnitude = negative ? -value : value

  if (decimals <= 0) {
    return { whole: magnitude.toString(), fraction: '', negative }
  }

  const scale = 10n ** BigInt(decimals)
  const whole = (magnitude / scale).toString()
  const fraction = (magnitude % scale).toString().padStart(decimals, '0')
  return { whole, fraction, negative }
}

/** Inserts thousands separators into a run of digits. */
function group(whole: string): string {
  return whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

export interface FormatOptions {
  /** Hard cap on decimal places. Defaults to a significant-digit rule. */
  maxDecimals?: number
  /** Floor on decimal places once trailing zeros are trimmed. */
  minDecimals?: number
  /** Thousands separators in the whole part. On by default. */
  grouping?: boolean
}

/**
 * Renders a raw base-unit amount for display.
 *
 * The default decimal count follows significant digits rather than a fixed
 * width: 4,494.2683 for a four-figure balance, 0.05020412 for a fraction of a
 * WBTC. A swap interface that prints 0.05 for both is useless.
 */
export function formatUnits(raw: string, decimals: number, options: FormatOptions = {}): string {
  const { whole, fraction, negative } = splitUnits(raw, decimals)
  const grouping = options.grouping ?? true
  const minDecimals = options.minDecimals ?? 0

  let places: number
  if (options.maxDecimals !== undefined) {
    places = options.maxDecimals
  } else if (whole !== '0') {
    // Aim for roughly eight significant digits overall.
    places = Math.max(2, Math.min(6, 9 - whole.length))
  } else {
    // Below one, keep six digits past the leading zeros so a small balance
    // does not collapse to 0.00.
    const firstSignificant = fraction.search(/[1-9]/)
    places = firstSignificant === -1 ? minDecimals : firstSignificant + 6
  }

  places = Math.max(minDecimals, Math.min(places, fraction.length))

  // Truncate rather than round. Overstating an output by a rounded-up digit is
  // the wrong direction to be wrong in.
  let shown = fraction.slice(0, places)
  while (shown.length > minDecimals && shown.endsWith('0')) {
    shown = shown.slice(0, -1)
  }

  const head = grouping ? group(whole) : whole
  const sign = negative ? '-' : ''
  return shown === '' ? `${sign}${head}` : `${sign}${head}.${shown}`
}

/**
 * Converts a whole-unit string the user typed into raw base units.
 *
 * Returns null for anything that is not a non-negative decimal number, so the
 * caller can tell "still typing" apart from "reject".
 */
export function parseUnits(input: string, decimals: number): bigint | null {
  const value = input.trim()
  if (value === '' || value === '.' || !/^\d*\.?\d*$/.test(value)) return null

  const parts = value.split('.')
  const wholePart = parts[0] ?? ''
  const fractionPart = parts[1] ?? ''

  // Digits past the token's precision are dropped, never rounded.
  const scaledFraction = fractionPart.slice(0, decimals).padEnd(decimals, '0')

  const wholeUnits = BigInt(wholePart === '' ? '0' : wholePart) * 10n ** BigInt(decimals)
  const fractionUnits = scaledFraction === '' ? 0n : BigInt(scaledFraction)
  return wholeUnits + fractionUnits
}

/** True when the string is a non-negative decimal the amount field accepts. */
export function isPartialDecimal(input: string): boolean {
  return /^\d*\.?\d*$/.test(input)
}

/** True when the raw amount is greater than zero. */
export function isPositive(raw: string): boolean {
  try {
    return toBigInt(raw) > 0n
  } catch {
    return false
  }
}

const RATE_PRECISION = 12

/**
 * Computes out-per-in as a display string, entirely in BigInt.
 *
 * rate = (outRaw * 10^decIn) / (inRaw * 10^decOut), carried at twelve extra
 * digits of precision and then formatted like any other fixed-point value.
 */
export function formatRate(
  amountIn: string,
  decimalsIn: number,
  amountOut: string,
  decimalsOut: number,
): string | null {
  let inRaw: bigint
  let outRaw: bigint
  try {
    inRaw = toBigInt(amountIn)
    outRaw = toBigInt(amountOut)
  } catch {
    return null
  }
  if (inRaw <= 0n) return null

  const numerator = outRaw * 10n ** BigInt(decimalsIn + RATE_PRECISION)
  const denominator = inRaw * 10n ** BigInt(decimalsOut)
  return formatUnits((numerator / denominator).toString(), RATE_PRECISION)
}

/**
 * Converts a raw amount to a JavaScript number, for charting only.
 *
 * This is the one place a base-unit amount becomes a float, because the chart
 * library takes numbers. It is safe here because a price drawn to ten decimal
 * places needs less range than float64 has, and nothing downstream of the
 * pixel it plots depends on the exact digits.
 */
export function toChartNumber(raw: string, decimals: number): number {
  const { whole, fraction, negative } = splitUnits(raw, decimals)
  const tail = fraction.slice(0, 10)
  const value = Number(tail === '' ? whole : `${whole}.${tail}`)
  return negative ? -value : value
}

/** Renders basis points as a percentage, for example 23 as "0.23%". */
export function formatBps(bps: number): string {
  return `${(bps / 100).toFixed(2)}%`
}

/** Renders router time. Sub-millisecond readings stay in microseconds. */
export function formatMicros(micros: number): string {
  // The Go timer on Windows has coarse resolution, so a fast search can come
  // back as a flat zero. Printing "0 us" reads as broken instrumentation.
  if (micros <= 0) return '< 1 us'
  if (micros < 1000) return `${micros} us`
  return `${(micros / 1000).toFixed(micros < 10_000 ? 2 : 1)} ms`
}

/** Renders snapshot age. */
export function formatAge(ms: number): string {
  if (ms < 1000) return `${ms} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.floor(ms / 60_000)}m ${Math.floor((ms % 60_000) / 1000)}s`
}

/** Shortens a contract address to a head and a tail. */
export function shortAddress(address: string, head = 6, tail = 4): string {
  if (address.length <= head + tail + 1) return address
  return `${address.slice(0, head)}…${address.slice(-tail)}`
}

/** Display label for a protocol identifier. */
export function protocolLabel(protocol: string): string {
  switch (protocol) {
    case 'uniswap-v3':
      return 'UNI V3'
    case 'sushiswap-v2':
      return 'SUSHI V2'
    default:
      return protocol.toUpperCase()
  }
}

export type ImpactLevel = 'normal' | 'elevated' | 'severe'

/** Price impact turns amber at 100 bps and red at 500 bps. */
export function impactLevel(bps: number): ImpactLevel {
  if (bps >= 500) return 'severe'
  if (bps >= 100) return 'elevated'
  return 'normal'
}
