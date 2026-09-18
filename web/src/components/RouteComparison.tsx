import { formatPpm, formatUnits, relativePpm, toBigInt } from '../api/format'
import type { Quote } from '../api/types'
import type { RouteOption } from '../hooks/useQuote'

interface Props {
  options: RouteOption[]
  /** Decimals of the output token, for formatting the amounts. */
  decimalsOut: number
  outSymbol: string
  stale: boolean
}

/** Token symbols along a route, in order. */
function pathOf(quote: Quote): string[] {
  const first = quote.route[0]
  if (first === undefined) return [quote.tokenInSymbol, quote.tokenOutSymbol]
  return [first.tokenInSymbol, ...quote.route.map((hop) => hop.tokenOutSymbol)]
}

/**
 * The best output, and which row first reached it.
 *
 * A tie goes to the lower hop count, because that is the row that actually
 * earned the number. Tagging both means the table stops saying anything about
 * whether the extra hop was worth taking.
 */
function bestOf(options: RouteOption[]): { amount: string | null; index: number } {
  let best: bigint | null = null
  let amount: string | null = null
  let index = -1

  options.forEach((option, position) => {
    if (option.quote === null) return
    try {
      const value = toBigInt(option.quote.amountOut)
      if (best === null || value > best) {
        best = value
        amount = option.quote.amountOut
        index = position
      }
    } catch {
      // A malformed amount simply does not compete.
    }
  })

  return { amount, index }
}

/**
 * What each hop limit was able to return, side by side.
 *
 * This is the table that makes the page read as an aggregator rather than a
 * swap box: it shows multi-hop routing earning its keep, in numbers, with
 * nothing to read.
 */
export function RouteComparison({ options, decimalsOut, outSymbol, stale }: Props) {
  const { amount: best, index: bestIndex } = bestOf(options)

  return (
    <table className={stale ? 'compare is-stale' : 'compare'}>
      <caption className="visually-hidden">
        Output by hop limit, against the best route found.
      </caption>
      <thead>
        <tr>
          <th scope="col">Hops</th>
          <th scope="col">Path</th>
          <th scope="col" className="num">
            You get {outSymbol}
          </th>
          <th scope="col" className="num">
            vs best
          </th>
        </tr>
      </thead>
      <tbody>
        {options.map((option, position) => {
          const quote = option.quote
          const isBest = quote !== null && position === bestIndex
          const ppm = quote !== null && best !== null ? relativePpm(quote.amountOut, best) : null

          return (
            <tr key={option.hops} className={isBest ? 'is-best' : undefined}>
              <th scope="row">{option.hops}</th>

              {quote === null ? (
                <>
                  <td className="is-empty">--</td>
                  <td className="num is-empty">--</td>
                  <td className="num is-empty">--</td>
                </>
              ) : (
                <>
                  <td className="compare-path">
                    {pathOf(quote).map((symbol, index) => (
                      <span key={`${symbol}-${index}`}>
                        {index > 0 && <i aria-hidden="true">&gt;</i>}
                        {symbol}
                      </span>
                    ))}
                  </td>
                  <td className="num">{formatUnits(quote.amountOut, decimalsOut, { minDecimals: 2 })}</td>
                  <td className="num">
                    {isBest ? (
                      <span className="best-tag">BEST</span>
                    ) : ppm === null ? (
                      '--'
                    ) : (
                      formatPpm(ppm)
                    )}
                  </td>
                </>
              )}
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
